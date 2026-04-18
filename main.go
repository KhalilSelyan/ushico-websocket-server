package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)


func logRealtime(event string, fields map[string]interface{}) {
	payload := map[string]interface{}{"event": event}
	for key, value := range fields {
		payload[key] = value
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[realtime] %s %+v", event, fields)
		return
	}
	log.Printf("[realtime] %s", encoded)
}

// Configure the WebSocket upgrader with buffer sizes, CORS policy, and compression.
var upgrader = websocket.Upgrader{
	CheckOrigin:       func(r *http.Request) bool { return true }, // Allow all origins
	ReadBufferSize:    4096,                                       // Increased from 1024
	WriteBufferSize:   4096,                                       // Increased from 1024
	EnableCompression: true,                                       // Enable permessage-deflate compression
}

// Message represents the structure of messages sent over WebSocket.
type Message struct {
	Channel string          `json:"channel"` // The channel identifier.
	Event   string          `json:"event"`   // The event type (e.g., "sync", "subscribe").
	Data    json.RawMessage `json:"data"`    // The raw message data.
}

// SyncData represents the synchronization data for video playback.
type SyncData struct {
	Timestamp float64 `json:"timestamp"` // The current time of the video.
	URL       string  `json:"url"`       // The video URL.
	RoomID    string  `json:"roomId"`    // The room identifier (updated from ChatID).
	State     string  `json:"state"`     // The state of the video (playing/paused).
	VideoID   string  `json:"videoId"`   // UUID generated when URL changes for sync safety.
	SentAt    int64   `json:"sentAt"`
	Reason    string  `json:"reason"`
}

// ErrorResponse for client errors.
type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Code    string `json:"code"`
}

// BinaryBroadcast holds a binary message to broadcast to a room
type BinaryBroadcast struct {
	RoomID   string
	SenderID string
	Data     []byte
}


// Global variables for managing clients and messages.
var (
	clients         = make(map[*Client]bool)            // Map of connected clients.
	broadcast       = make(chan Message, 256)           // Buffered channel for broadcasting messages.
	binaryBroadcast = make(chan BinaryBroadcast, 256)   // Buffered channel for binary broadcasts.
	register        = make(chan *Client)                // Channel for registering new clients.
	unregister      = make(chan *Client)                // Channel for unregistering clients.
	mutex           = &sync.RWMutex{}                   // Read-write mutex to protect the clients map.
	rooms           = make(map[string]*Room)            // Active rooms.
	roomMutex       = &sync.RWMutex{}                   // Protect rooms map.
	channelSubs     = make(map[string]map[*Client]bool) // Channel -> subscribed clients (for O(1) lookup).
	channelMutex    = &sync.RWMutex{}                   // Protect channelSubs map.
)

// handleBinaryMessage processes incoming binary WebSocket messages
func handleBinaryMessage(client *Client, data []byte) {
	if len(data) < 1 {
		return
	}

	msgType := data[0]
	switch msgType {
	case BinaryMsgAvatarUpdate:
		roomID, err := decodeBinaryAvatarUpdate(data)
		if err != nil {
			log.Printf("Error decoding binary avatar update: %v", err)
			return
		}

		// Store avatar state in room (decode to JSON for storage)
		roomMutex.Lock()
		if room, exists := rooms[roomID]; exists {
			if room.CinemaAvatars == nil {
				room.CinemaAvatars = make(map[string]json.RawMessage)
			}
			avatarJSON := binaryAvatarToJSON(data)
			if avatarJSON != nil {
				room.CinemaAvatars[client.userID] = avatarJSON
			}
		}
		roomMutex.Unlock()

		binaryBroadcast <- BinaryBroadcast{RoomID: roomID, SenderID: client.userID, Data: data}
		logRealtime("cinema_avatar_update_binary", map[string]interface{}{"roomId": roomID, "userId": client.userID, "bytes": len(data)})

	case BinaryMsgHostSync:
		roomID, err := decodeBinaryHostSync(data)
		if err != nil {
			log.Printf("Error decoding binary host sync: %v", err)
			return
		}

		// Store current video state (decode to JSON)
		roomMutex.Lock()
		if room, exists := rooms[roomID]; exists {
			syncData := binaryHostSyncToSyncData(data)
			if syncData != nil {
				room.CurrentVideo = *syncData
			}
		}
		roomMutex.Unlock()

		binaryBroadcast <- BinaryBroadcast{RoomID: roomID, SenderID: client.userID, Data: data}
		logRealtime("host_sync_binary", map[string]interface{}{"roomId": roomID, "userId": client.userID, "bytes": len(data)})

	case BinaryMsgCinemaAnimation:
		roomID, err := decodeBinaryCinemaAnimation(data)
		if err != nil {
			log.Printf("Error decoding binary cinema animation: %v", err)
			return
		}
		binaryBroadcast <- BinaryBroadcast{RoomID: roomID, SenderID: client.userID, Data: data}
		logRealtime("cinema_animation_binary", map[string]interface{}{"roomId": roomID, "userId": client.userID, "bytes": len(data)})

	case BinaryMsgVideoReaction:
		roomID, err := decodeBinaryVideoReaction(data)
		if err != nil {
			log.Printf("Error decoding binary video reaction: %v", err)
			return
		}
		binaryBroadcast <- BinaryBroadcast{RoomID: roomID, SenderID: client.userID, Data: data}
		logRealtime("video_reaction_binary", map[string]interface{}{"roomId": roomID, "userId": client.userID, "bytes": len(data)})

	default:
		log.Printf("Unknown binary message type: %d", msgType)
	}
}

// validateChatID checks if the chat ID is in the expected format (e.g., "user1--user2").
func validateChatID(chatID string) bool {
	parts := strings.Split(chatID, "--")
	return len(parts) == 2 && len(parts[0]) > 0 && len(parts[1]) > 0
}

// sendErrorResponse sends an error message back to the client.
func sendErrorResponse(client *Client, errorCode, message string) {
	errorResp := ErrorResponse{
		Error:   errorCode,
		Message: message,
		Code:    errorCode,
	}

	errorData, _ := json.Marshal(errorResp)
	errorMessage := Message{
		Channel: "error",
		Event:   "error_response",
		Data:    errorData,
	}

	if err := client.safeWriteJSON(errorMessage); err != nil {
		log.Printf("Error sending error response to client: %v", err)
	}
}

// validateHostPermission checks if user has playback control permission.
func validateHostPermission(roomID, userID string, action string) error {
	if !isEffectiveHost(roomID, userID) {
		return fmt.Errorf("permission denied: only session host can %s", action)
	}
	return nil
}

// handleUserTyping broadcasts typing indicator to room participants
func handleUserTyping(client *Client, message Message) {
	var typingData struct {
		RoomID   string `json:"roomId"`
		UserID   string `json:"userId"`
		UserName string `json:"userName"`
	}

	if err := json.Unmarshal(message.Data, &typingData); err != nil {
		log.Printf("Error parsing typing data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid typing data format")
		return
	}

	// Validate user is in the room
	if !client.isInRoom(typingData.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room to send typing indicators")
		return
	}

	// Broadcast typing indicator to all room participants except sender
	broadcastToRoomExceptSender(typingData.RoomID, client.userID, "user_typing", typingData)
}

// handleUserStoppedTyping broadcasts stop typing indicator to room participants
func handleUserStoppedTyping(client *Client, message Message) {
	var typingData struct {
		RoomID   string `json:"roomId"`
		UserID   string `json:"userId"`
		UserName string `json:"userName"`
	}

	if err := json.Unmarshal(message.Data, &typingData); err != nil {
		log.Printf("Error parsing stop typing data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid stop typing data format")
		return
	}

	// Validate user is in the room
	if !client.isInRoom(typingData.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room to send typing indicators")
		return
	}

	// Broadcast stop typing indicator to all room participants except sender
	broadcastToRoomExceptSender(typingData.RoomID, client.userID, "user_stopped_typing", typingData)
}

// handleVideoReaction broadcasts emoji reactions with timestamp sync
func handleVideoReaction(client *Client, message Message) {
	var reactionData struct {
		RoomID         string  `json:"roomId"`
		UserID         string  `json:"userId"`
		UserName       string  `json:"userName"`
		Emoji          string  `json:"emoji"`
		VideoTimestamp float64 `json:"videoTimestamp"`
		Timestamp      string  `json:"timestamp"`
		ReactionID     string  `json:"reactionId"`
	}

	if err := json.Unmarshal(message.Data, &reactionData); err != nil {
		log.Printf("Error parsing reaction data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid reaction data format")
		return
	}

	// Validate user is in the room
	if !client.isInRoom(reactionData.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room to send reactions")
		return
	}

	// Validate emoji (basic check for common reactions)
	validEmojis := map[string]bool{
		"😂": true, "❤️": true, "😮": true, "👏": true, "😢": true,
		"🔥": true, "💯": true, "👍": true, "👎": true, "😍": true,
	}

	if !validEmojis[reactionData.Emoji] {
		sendErrorResponse(client, "INVALID_EMOJI", "Invalid emoji for reactions")
		return
	}

	// Broadcast reaction to all room participants including sender (they want to see their own reaction)
	broadcastToRoom(reactionData.RoomID, "video_reaction", reactionData)
}

// handleRoomAnnouncement broadcasts system messages to room participants
func handleRoomAnnouncement(client *Client, message Message) {
	var announcementData struct {
		RoomID         string          `json:"roomId"`
		Type           string          `json:"type"` // "user_joined", "user_left", "video_changed", "host_paused", etc.
		UserName       string          `json:"userName"`
		Message        string          `json:"message"` // Pre-formatted message text
		Timestamp      string          `json:"timestamp"`
		AnnouncementID string          `json:"announcementId"`
		Metadata       json.RawMessage `json:"metadata,omitempty"`
	}

	if err := json.Unmarshal(message.Data, &announcementData); err != nil {
		log.Printf("Error parsing announcement data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid announcement data format")
		return
	}

	// Validate announcement type
	validTypes := map[string]bool{
		"user_joined":            true,
		"user_left":              true,
		"video_changed":          true,
		"host_paused":            true,
		"host_resumed":           true,
		"host_transferred":       true,
		"room_created":           true,
		"video_seeked":           true,
		"host_started_streaming": true,
		"host_stopped_streaming": true,
		"queue_add":              true,
		"queue_remove":           true,
		"bookmark_added":         true,
		"poll_started":           true,
		"poll_voted":             true,
		"poll_closed":            true,
	}

	if !validTypes[announcementData.Type] {
		sendErrorResponse(client, "INVALID_ANNOUNCEMENT_TYPE", "Invalid announcement type")
		return
	}

	// Broadcast announcement to all room participants
	broadcastToRoom(announcementData.RoomID, "room_announcement", announcementData)
}

// handleUserPresenceUpdate updates and broadcasts user presence status
func handleUserPresenceUpdate(client *Client, message Message) {
	var presenceData PresenceUpdateData

	if err := json.Unmarshal(message.Data, &presenceData); err != nil {
		log.Printf("Error parsing presence data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid presence data format")
		return
	}

	// Validate presence state
	validStates := map[string]bool{
		"active":  true,
		"away":    true,
		"offline": true,
	}

	if !validStates[presenceData.PresenceState] {
		sendErrorResponse(client, "INVALID_PRESENCE_STATE", "Invalid presence state")
		return
	}

	// Validate user is in the room
	if !client.isInRoom(presenceData.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room to update presence")
		return
	}

	// Store userName on client for disconnect broadcasts
	if presenceData.UserName != "" && client.userName == "" {
		client.userName = presenceData.UserName
	}

	// Update presence in room state
	roomMutex.Lock()
	if room, exists := rooms[presenceData.RoomID]; exists {
		if room.Presence == nil {
			room.Presence = make(map[string]string)
		}
		room.Presence[presenceData.UserID] = presenceData.PresenceState
	}
	roomMutex.Unlock()

	// Broadcast presence update to all room participants
	broadcastToRoom(presenceData.RoomID, "user_presence_updated", presenceData)
}

// handleGetRoomPresence sends current presence status of all room participants
func handleGetRoomPresence(client *Client, message Message) {
	var requestData struct {
		RoomID string `json:"roomId"`
	}

	if err := json.Unmarshal(message.Data, &requestData); err != nil {
		log.Printf("Error parsing room presence request: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid presence request format")
		return
	}

	// Validate user is in the room
	if !client.isInRoom(requestData.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room to get presence")
		return
	}

	// Get current presence state - validate against actually connected users
	roomMutex.RLock()
	var participants []RoomPresenceParticipant
	if room, exists := rooms[requestData.RoomID]; exists {
		participants = make([]RoomPresenceParticipant, 0, len(room.Presence))

		// Get list of currently connected userIDs
		mutex.RLock()
		connectedUsers := make(map[string]bool)
		for client := range clients {
			if client.userID != "" {
				connectedUsers[client.userID] = true
			}
		}
		mutex.RUnlock()

		for userID, state := range room.Presence {
			// If user is not connected, force them to offline regardless of stored state
			if !connectedUsers[userID] {
				state = "offline"
				// Also update the stored state to prevent future stale data
				room.Presence[userID] = "offline"
			}

			participants = append(participants, RoomPresenceParticipant{
				UserID:        userID,
				UserName:      userID,
				PresenceState: state,
			})
		}
	}
	roomMutex.RUnlock()

	// Send presence data back to requesting client
	responseData, _ := json.Marshal(RoomPresenceResponse{RoomID: requestData.RoomID, Participants: participants})

	responseMessage := Message{
		Channel: fmt.Sprintf("user-%s", client.userID),
		Event:   "room_presence_status",
		Data:    responseData,
	}

	if err := client.safeWriteJSON(responseMessage); err != nil {
		log.Printf("Error sending presence status: %v", err)
	}
}

// handleStreamModeChanged broadcasts when someone starts/stops streaming
func handleStreamModeChanged(client *Client, message Message) {
	var modeData struct {
		RoomID    string `json:"roomId"`
		UserID    string `json:"userId"`
		Mode      string `json:"mode"` // "none", "screen", "camera", "file", "url"
		Timestamp string `json:"timestamp"`
	}

	if err := json.Unmarshal(message.Data, &modeData); err != nil {
		log.Printf("Error parsing stream mode data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid stream mode data format")
		return
	}

	// Validate mode
	validModes := map[string]bool{"none": true, "screen": true, "camera": true, "file": true, "url": true}
	if !validModes[modeData.Mode] {
		sendErrorResponse(client, "INVALID_MODE", "Mode must be 'none', 'screen', 'camera', 'file', or 'url'")
		return
	}

	// Validate user is in the room
	if !client.isInRoom(modeData.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room to change stream mode")
		return
	}

	roomMutex.Lock()
	room, exists := rooms[modeData.RoomID]
	if !exists {
		roomMutex.Unlock()
		sendErrorResponse(client, "ROOM_NOT_FOUND", "Room not found")
		return
	}

	// Check if someone else is already streaming (FIFO lock)
	if modeData.Mode != "none" && room.CurrentStreamerID != "" && room.CurrentStreamerID != client.userID {
		roomMutex.Unlock()
		sendErrorResponse(client, "STREAM_LOCKED", "Someone else is currently streaming")
		return
	}

	// Update room streaming state
	if modeData.Mode == "none" {
		// Only the current streamer can stop streaming
		if room.CurrentStreamerID == client.userID {
			room.CurrentStreamerID = ""
			room.CurrentStreamMode = ""
		}
	} else {
		// Start streaming - lock to this user
		room.CurrentStreamerID = client.userID
		room.CurrentStreamMode = modeData.Mode
	}
	roomMutex.Unlock()

	// Broadcast to all room participants (including sender for confirmation)
	broadcastToRoom(modeData.RoomID, "stream_mode_changed", modeData)
	log.Printf("Stream mode changed to %s in room %s by %s", modeData.Mode, modeData.RoomID, client.userID)
}

// handleGetStreamStatus returns current streaming state for late joiners
func handleGetStreamStatus(client *Client, message Message) {
	var requestData struct {
		RoomID string `json:"roomId"`
	}

	if err := json.Unmarshal(message.Data, &requestData); err != nil {
		log.Printf("Error parsing stream status request: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid stream status request format")
		return
	}

	// Validate user is in the room
	if !client.isInRoom(requestData.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room to get stream status")
		return
	}

	roomMutex.RLock()
	room, exists := rooms[requestData.RoomID]
	var response StreamStatusResponse
	if exists {
		response = StreamStatusResponse{
			RoomID:            requestData.RoomID,
			CurrentStreamerID: room.CurrentStreamerID,
			CurrentStreamMode: room.CurrentStreamMode,
		}
	} else {
		response = StreamStatusResponse{
			RoomID: requestData.RoomID,
		}
	}
	roomMutex.RUnlock()

	responseData, _ := json.Marshal(response)
	responseMessage := Message{
		Channel: fmt.Sprintf("user-%s", client.userID),
		Event:   "stream_status",
		Data:    responseData,
	}

	if err := client.safeWriteJSON(responseMessage); err != nil {
		log.Printf("Error sending stream status: %v", err)
	}
}

// handleWebcamJoin broadcasts when a user joins the webcam session
func handleWebcamJoin(client *Client, message Message) {
	var webcamData WebcamJoinData

	if err := json.Unmarshal(message.Data, &webcamData); err != nil {
		log.Printf("Error parsing webcam join data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid webcam join data format")
		return
	}

	// Validate user is in the room
	if !client.isInRoom(webcamData.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room to join webcam session")
		return
	}

	// Store userName on client for disconnect broadcasts
	if webcamData.UserName != "" && client.userName == "" {
		client.userName = webcamData.UserName
	}

	roomMutex.Lock()
	if room, exists := rooms[webcamData.RoomID]; exists {
		if room.WebcamParticipants == nil {
			room.WebcamParticipants = make(map[string]WebcamStateParticipant)
		}
		room.WebcamParticipants[webcamData.UserID] = WebcamStateParticipant{
			UserID:       webcamData.UserID,
			UserName:     webcamData.UserName,
			UserImage:    webcamData.UserImage,
			VideoEnabled: webcamData.VideoEnabled,
			AudioEnabled: webcamData.AudioEnabled,
		}
	}
	roomMutex.Unlock()
	logRealtime("webcam_join", map[string]interface{}{
		"roomId":       webcamData.RoomID,
		"userId":       webcamData.UserID,
		"audioEnabled": webcamData.AudioEnabled,
		"videoEnabled": webcamData.VideoEnabled,
	})

	// Broadcast to all room participants except sender
	broadcastToRoomExceptSender(webcamData.RoomID, client.userID, "webcam_join", webcamData)
	log.Printf("User %s joined webcam in room %s", webcamData.UserName, webcamData.RoomID)
}

// handleWebcamLeave broadcasts when a user leaves the webcam session
func handleWebcamLeave(client *Client, message Message) {
	var webcamData WebcamLeaveData

	if err := json.Unmarshal(message.Data, &webcamData); err != nil {
		log.Printf("Error parsing webcam leave data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid webcam leave data format")
		return
	}

	roomMutex.Lock()
	if room, exists := rooms[webcamData.RoomID]; exists && room.WebcamParticipants != nil {
		delete(room.WebcamParticipants, webcamData.UserID)
	}
	roomMutex.Unlock()
	logRealtime("webcam_leave", map[string]interface{}{
		"roomId": webcamData.RoomID,
		"userId": webcamData.UserID,
	})

	// Broadcast to all room participants except sender
	broadcastToRoomExceptSender(webcamData.RoomID, client.userID, "webcam_leave", webcamData)
	log.Printf("User %s left webcam in room %s", webcamData.UserID, webcamData.RoomID)
}

// handleWebcamToggle broadcasts when a user toggles audio/video
func handleWebcamToggle(client *Client, message Message) {
	var webcamData WebcamToggleData

	if err := json.Unmarshal(message.Data, &webcamData); err != nil {
		log.Printf("Error parsing webcam toggle data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid webcam toggle data format")
		return
	}

	// Validate user is in the room
	if !client.isInRoom(webcamData.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room to toggle webcam")
		return
	}

	if webcamData.Type != "audio" && webcamData.Type != "video" {
		sendErrorResponse(client, "INVALID_DATA", "Invalid webcam toggle type")
		return
	}

	roomMutex.Lock()
	if room, exists := rooms[webcamData.RoomID]; exists && room.WebcamParticipants != nil {
		participant := room.WebcamParticipants[webcamData.UserID]
		if webcamData.Type == "audio" {
			participant.AudioEnabled = webcamData.Enabled
		} else {
			participant.VideoEnabled = webcamData.Enabled
		}
		participant.UserID = webcamData.UserID
		room.WebcamParticipants[webcamData.UserID] = participant
	}
	roomMutex.Unlock()
	logRealtime("webcam_toggle", map[string]interface{}{
		"roomId":  webcamData.RoomID,
		"userId":  webcamData.UserID,
		"type":    webcamData.Type,
		"enabled": webcamData.Enabled,
	})

	// Broadcast to all room participants except sender
	broadcastToRoomExceptSender(webcamData.RoomID, client.userID, "webcam_toggle", webcamData)
}

func handleGetWebcamState(client *Client, message Message) {
	var req struct {
		RoomID string `json:"roomId"`
	}

	if err := json.Unmarshal(message.Data, &req); err != nil {
		sendErrorResponse(client, "INVALID_DATA", "Invalid webcam state request")
		return
	}

	if !client.isInRoom(req.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room to request webcam state")
		return
	}

	roomMutex.RLock()
	participants := make([]WebcamStateParticipant, 0)
	if room, exists := rooms[req.RoomID]; exists && room.WebcamParticipants != nil {
		participants = make([]WebcamStateParticipant, 0, len(room.WebcamParticipants))
		for _, participant := range room.WebcamParticipants {
			participants = append(participants, participant)
		}
	}
	roomMutex.RUnlock()

	broadcastToSpecificUser(client.userID, "webcam_state", WebcamStateResponse{RoomID: req.RoomID, Participants: participants})
	logRealtime("webcam_state_snapshot", map[string]interface{}{
		"roomId":           req.RoomID,
		"userId":           client.userID,
		"participantCount": len(participants),
	})
}

// handleWebcamHubChange broadcasts when the webcam hub changes
func handleWebcamHubChange(client *Client, message Message) {
	var webcamData struct {
		RoomID       string  `json:"roomId"`
		NewHubUserId *string `json:"newHubUserId"`
		Timestamp    string  `json:"timestamp"`
	}

	if err := json.Unmarshal(message.Data, &webcamData); err != nil {
		log.Printf("Error parsing webcam hub change data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid webcam hub change data format")
		return
	}

	// Broadcast to all room participants
	broadcastToRoom(webcamData.RoomID, "webcam_hub_change", webcamData)
	log.Printf("Webcam hub changed in room %s to user %v", webcamData.RoomID, webcamData.NewHubUserId)
}

// handleCinemaAvatarUpdate broadcasts avatar position to room and stores for late joiners
func handleCinemaAvatarUpdate(client *Client, message Message) {
	var avatarData CinemaAvatarData

	if err := json.Unmarshal(message.Data, &avatarData); err != nil {
		return
	}

	if !client.isInRoom(avatarData.RoomID) {
		return
	}

	// Store last known state for late joiners
	roomMutex.Lock()
	if room, exists := rooms[avatarData.RoomID]; exists {
		if room.CinemaAvatars == nil {
			room.CinemaAvatars = make(map[string]json.RawMessage)
		}
		room.CinemaAvatars[client.userID] = message.Data
	}
	roomMutex.Unlock()

	broadcastToRoomExceptSender(avatarData.RoomID, client.userID, "cinema_avatar_update", message.Data)
	logRealtime("cinema_avatar_update", map[string]interface{}{
		"roomId": avatarData.RoomID,
		"userId": avatarData.UserID,
		"anim":   avatarData.Anim,
	})
}

// handleCinemaAnimation broadcasts animation emotes from a user to other room members
func handleCinemaAnimation(client *Client, message Message) {
	var animData CinemaAnimationData
	if err := json.Unmarshal(message.Data, &animData); err != nil {
		return
	}

	broadcastToRoomExceptSender(animData.RoomID, client.userID, "cinema_animation", message.Data)
}

// handleCinemaMoodChanged broadcasts mood lighting changes to the room
func handleCinemaMoodChanged(client *Client, message Message) {
	var moodData CinemaMoodChangedData
	if err := json.Unmarshal(message.Data, &moodData); err != nil {
		return
	}

	broadcastToRoomExceptSender(moodData.RoomID, client.userID, "cinema_mood_changed", message.Data)
}

// handleCinemaRoomThemeChanged broadcasts room theme preset changes to the room
func handleCinemaRoomThemeChanged(client *Client, message Message) {
	var themeData CinemaRoomThemeChangedData
	if err := json.Unmarshal(message.Data, &themeData); err != nil {
		return
	}

	broadcastToRoomExceptSender(themeData.RoomID, client.userID, "cinema_room_theme_changed", message.Data)
}


// handleFaceModeToggle handles when a user toggles face mode (webcam on avatar face)
func handleFaceModeToggle(client *Client, message Message) {
	var faceData FaceModeData

	if err := json.Unmarshal(message.Data, &faceData); err != nil {
		log.Printf("Error parsing face mode data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid face mode data format")
		return
	}

	// Validate user is in the room
	if !client.isInRoom(faceData.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room to toggle face mode")
		return
	}

	// Update room state
	roomMutex.Lock()
	if room, exists := rooms[faceData.RoomID]; exists {
		if room.FaceModeParticipants == nil {
			room.FaceModeParticipants = make(map[string]bool)
		}
		if faceData.Enabled {
			room.FaceModeParticipants[faceData.UserID] = true
		} else {
			delete(room.FaceModeParticipants, faceData.UserID)
		}
	}
	roomMutex.Unlock()

	logRealtime("face_mode_toggle", map[string]interface{}{
		"roomId":  faceData.RoomID,
		"userId":  faceData.UserID,
		"enabled": faceData.Enabled,
	})

	// Broadcast to all room participants except sender
	broadcastToRoomExceptSender(faceData.RoomID, client.userID, "face_mode_toggle", faceData)
}

// handleGetFaceModeState sends face mode state to late joiners
func handleGetFaceModeState(client *Client, message Message) {
	var req struct {
		RoomID string `json:"roomId"`
	}

	if err := json.Unmarshal(message.Data, &req); err != nil {
		sendErrorResponse(client, "INVALID_DATA", "Invalid face mode state request")
		return
	}

	roomMutex.RLock()
	participants := make([]FaceModeParticipant, 0)
	if room, exists := rooms[req.RoomID]; exists && room.FaceModeParticipants != nil {
		for userID, enabled := range room.FaceModeParticipants {
			if enabled {
				participants = append(participants, FaceModeParticipant{
					UserID:  userID,
					Enabled: true,
				})
			}
		}
	}
	roomMutex.RUnlock()

	response := FaceModeStateResponse{
		RoomID:       req.RoomID,
		Participants: participants,
	}

	broadcastToSpecificUser(client.userID, "face_mode_state", response)
}

// ============================================================================
// QUEUE MANAGEMENT HANDLERS
// ============================================================================

// handleQueueAdd adds a video to the room's queue
func handleQueueAdd(client *Client, message Message) {
	var data QueueAddData
	if err := json.Unmarshal(message.Data, &data); err != nil {
		log.Printf("Error parsing queue add data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid queue add data")
		return
	}

	if !client.isInRoom(data.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room to add to queue")
		return
	}

	// Create the queue item
	item := QueueItem{
		ID:         fmt.Sprintf("qi_%d", time.Now().UnixNano()),
		VideoURL:   data.VideoURL,
		VideoTitle: data.VideoTitle,
		AddedBy:    data.AddedBy,
		AddedAt:    time.Now().UnixMilli(),
	}

	// Add to room queue
	roomMutex.Lock()
	room, exists := rooms[data.RoomID]
	if !exists {
		roomMutex.Unlock()
		sendErrorResponse(client, "ROOM_NOT_FOUND", "Room not found")
		return
	}
	if room.Queue == nil {
		room.Queue = make([]QueueItem, 0)
	}
	room.Queue = append(room.Queue, item)
	queueCopy := make([]QueueItem, len(room.Queue))
	copy(queueCopy, room.Queue)
	roomMutex.Unlock()

	// Broadcast updated queue to all participants
	broadcastToRoom(data.RoomID, "queue_updated", QueueUpdatedData{
		RoomID: data.RoomID,
		Queue:  queueCopy,
	})

	logRealtime("queue_add", map[string]interface{}{
		"roomId":  data.RoomID,
		"userId":  client.userID,
		"itemId":  item.ID,
		"videoUrl": data.VideoURL,
	})
}

// handleQueueRemove removes a video from the room's queue
func handleQueueRemove(client *Client, message Message) {
	var data QueueRemoveData
	if err := json.Unmarshal(message.Data, &data); err != nil {
		log.Printf("Error parsing queue remove data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid queue remove data")
		return
	}

	if !client.isInRoom(data.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room to remove from queue")
		return
	}

	roomMutex.Lock()
	room, exists := rooms[data.RoomID]
	if !exists {
		roomMutex.Unlock()
		sendErrorResponse(client, "ROOM_NOT_FOUND", "Room not found")
		return
	}

	// Find and remove the item
	found := false
	for i, item := range room.Queue {
		if item.ID == data.QueueItemID {
			room.Queue = append(room.Queue[:i], room.Queue[i+1:]...)
			found = true
			break
		}
	}
	queueCopy := make([]QueueItem, len(room.Queue))
	copy(queueCopy, room.Queue)
	roomMutex.Unlock()

	if !found {
		sendErrorResponse(client, "ITEM_NOT_FOUND", "Queue item not found")
		return
	}

	// Broadcast updated queue
	broadcastToRoom(data.RoomID, "queue_updated", QueueUpdatedData{
		RoomID: data.RoomID,
		Queue:  queueCopy,
	})

	logRealtime("queue_remove", map[string]interface{}{
		"roomId": data.RoomID,
		"userId": client.userID,
		"itemId": data.QueueItemID,
	})
}

// handleQueueReorder reorders a video in the queue
func handleQueueReorder(client *Client, message Message) {
	var data QueueReorderData
	if err := json.Unmarshal(message.Data, &data); err != nil {
		log.Printf("Error parsing queue reorder data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid queue reorder data")
		return
	}

	if !client.isInRoom(data.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room to reorder queue")
		return
	}

	// Validate host permission for reordering
	if err := validateHostPermission(data.RoomID, client.userID, "reorder queue"); err != nil {
		sendErrorResponse(client, "PERMISSION_DENIED", "Only host can reorder queue")
		return
	}

	roomMutex.Lock()
	room, exists := rooms[data.RoomID]
	if !exists {
		roomMutex.Unlock()
		sendErrorResponse(client, "ROOM_NOT_FOUND", "Room not found")
		return
	}

	// Find the item
	var itemIndex int = -1
	var item QueueItem
	for i, qi := range room.Queue {
		if qi.ID == data.QueueItemID {
			itemIndex = i
			item = qi
			break
		}
	}

	if itemIndex == -1 {
		roomMutex.Unlock()
		sendErrorResponse(client, "ITEM_NOT_FOUND", "Queue item not found")
		return
	}

	// Remove from old position
	room.Queue = append(room.Queue[:itemIndex], room.Queue[itemIndex+1:]...)

	// Insert at new position
	newIndex := data.NewIndex
	if newIndex < 0 {
		newIndex = 0
	}
	if newIndex > len(room.Queue) {
		newIndex = len(room.Queue)
	}

	// Insert at new position
	room.Queue = append(room.Queue[:newIndex], append([]QueueItem{item}, room.Queue[newIndex:]...)...)

	queueCopy := make([]QueueItem, len(room.Queue))
	copy(queueCopy, room.Queue)
	roomMutex.Unlock()

	// Broadcast updated queue
	broadcastToRoom(data.RoomID, "queue_updated", QueueUpdatedData{
		RoomID: data.RoomID,
		Queue:  queueCopy,
	})

	logRealtime("queue_reorder", map[string]interface{}{
		"roomId":   data.RoomID,
		"userId":   client.userID,
		"itemId":   data.QueueItemID,
		"newIndex": data.NewIndex,
	})
}

// handleQueueNext advances to the next video in the queue
func handleQueueNext(client *Client, message Message) {
	var data QueueNextData
	if err := json.Unmarshal(message.Data, &data); err != nil {
		log.Printf("Error parsing queue next data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid queue next data")
		return
	}

	if !client.isInRoom(data.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room to skip to next")
		return
	}

	// Validate host permission
	if err := validateHostPermission(data.RoomID, client.userID, "skip to next in queue"); err != nil {
		sendErrorResponse(client, "PERMISSION_DENIED", "Only host can skip to next video")
		return
	}

	roomMutex.Lock()
	room, exists := rooms[data.RoomID]
	if !exists || len(room.Queue) == 0 {
		roomMutex.Unlock()
		sendErrorResponse(client, "QUEUE_EMPTY", "Queue is empty")
		return
	}

	// Pop the first item
	nextItem := room.Queue[0]
	room.Queue = room.Queue[1:]
	queueCopy := make([]QueueItem, len(room.Queue))
	copy(queueCopy, room.Queue)
	roomMutex.Unlock()

	// Broadcast autoplay event for the next item
	broadcastToRoom(data.RoomID, "queue_autoplay", QueueAutoplayData{
		RoomID: data.RoomID,
		Item:   nextItem,
	})

	// Broadcast updated queue
	broadcastToRoom(data.RoomID, "queue_updated", QueueUpdatedData{
		RoomID: data.RoomID,
		Queue:  queueCopy,
	})

	logRealtime("queue_next", map[string]interface{}{
		"roomId": data.RoomID,
		"userId": client.userID,
		"itemId": nextItem.ID,
	})
}

// handleQueueCountdown broadcasts a countdown before the next video plays
func handleQueueCountdown(client *Client, message Message) {
	var data QueueCountdownData
	if err := json.Unmarshal(message.Data, &data); err != nil {
		log.Printf("Error parsing queue countdown data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid queue countdown data")
		return
	}

	if !client.isInRoom(data.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room")
		return
	}

	// Validate host permission
	if err := validateHostPermission(data.RoomID, client.userID, "start countdown"); err != nil {
		sendErrorResponse(client, "PERMISSION_DENIED", "Only host can start countdown")
		return
	}

	// Broadcast countdown to all participants
	broadcastToRoom(data.RoomID, "queue_countdown", data)

	logRealtime("queue_countdown", map[string]interface{}{
		"roomId":           data.RoomID,
		"userId":           client.userID,
		"secondsRemaining": data.SecondsRemaining,
	})
}

// handleQueueAutoplay broadcasts when a queued video starts playing automatically
func handleQueueAutoplay(client *Client, message Message) {
	var data QueueAutoplayData
	if err := json.Unmarshal(message.Data, &data); err != nil {
		log.Printf("Error parsing queue autoplay data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid queue autoplay data")
		return
	}

	if !client.isInRoom(data.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room")
		return
	}

	// Validate host permission
	if err := validateHostPermission(data.RoomID, client.userID, "autoplay"); err != nil {
		sendErrorResponse(client, "PERMISSION_DENIED", "Only host can trigger autoplay")
		return
	}

	// Broadcast to room
	broadcastToRoom(data.RoomID, "queue_autoplay", data)

	logRealtime("queue_autoplay", map[string]interface{}{
		"roomId": data.RoomID,
		"userId": client.userID,
		"itemId": data.Item.ID,
	})
}

// ============================================================================
// MOVIE PROPOSAL HANDLERS
// ============================================================================

// handleMoviePropose creates a new movie proposal for the room
func handleMoviePropose(client *Client, message Message) {
	var data MovieProposeData
	if err := json.Unmarshal(message.Data, &data); err != nil {
		log.Printf("Error parsing movie propose data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid movie propose data")
		return
	}

	if !client.isInRoom(data.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room to propose a movie")
		return
	}

	roomMutex.Lock()
	room, exists := rooms[data.RoomID]
	if !exists {
		roomMutex.Unlock()
		sendErrorResponse(client, "ROOM_NOT_FOUND", "Room not found")
		return
	}

	// Check if there's already an active proposal
	if room.ActiveProposal != nil && room.ActiveProposal.Status == "pending" {
		roomMutex.Unlock()
		sendErrorResponse(client, "PROPOSAL_EXISTS", "A proposal is already in progress")
		return
	}

	// Create the proposal
	proposalID := fmt.Sprintf("mp_%d", time.Now().UnixNano())
	expiresAt := time.Now().Add(60 * time.Second)

	proposal := &MovieProposal{
		ID:     proposalID,
		RoomID: data.RoomID,
		Movie: MovieProposalMovie{
			ID:         data.MovieID,
			TmdbID:     data.MovieID, // Client sends movieId which is the DB id
			Title:      data.MovieTitle,
			PosterPath: data.PosterPath,
		},
		Proposer:  data.Proposer,
		VotesUp:   []MovieProposalUser{},
		VotesDown: []MovieProposalUser{},
		Status:    "pending",
		ExpiresAt: expiresAt.Format(time.RFC3339),
		CreatedAt: time.Now().Format(time.RFC3339),
	}

	room.ActiveProposal = proposal
	roomMutex.Unlock()

	// Broadcast to all room participants
	broadcastToRoom(data.RoomID, "movie_proposed", MovieProposedData{
		RoomID:     data.RoomID,
		ProposalID: proposalID,
		Movie:      proposal.Movie,
		Proposer:   proposal.Proposer,
		ExpiresAt:  proposal.ExpiresAt,
	})

	// Schedule expiration check
	go func() {
		time.Sleep(60 * time.Second)
		handleProposalExpiration(data.RoomID, proposalID)
	}()

	logRealtime("movie_propose", map[string]interface{}{
		"roomId":     data.RoomID,
		"proposalId": proposalID,
		"movieTitle": data.MovieTitle,
		"proposerId": data.Proposer.ID,
	})
}

// handleProposalExpiration checks and expires a proposal after timeout
func handleProposalExpiration(roomID, proposalID string) {
	roomMutex.Lock()
	room, exists := rooms[roomID]
	if !exists {
		roomMutex.Unlock()
		return
	}

	proposal := room.ActiveProposal
	if proposal == nil || proposal.ID != proposalID || proposal.Status != "pending" {
		roomMutex.Unlock()
		return
	}

	// Check if majority voted down
	totalVotes := len(proposal.VotesUp) + len(proposal.VotesDown)
	reason := "timeout"
	if totalVotes > 0 && len(proposal.VotesDown) > len(proposal.VotesUp) {
		reason = "majority_down"
	}

	proposal.Status = "rejected"
	roomMutex.Unlock()

	// Broadcast rejection
	broadcastToRoom(roomID, "movie_rejected", MovieRejectedData{
		RoomID:     roomID,
		ProposalID: proposalID,
		Reason:     reason,
	})

	logRealtime("movie_expired", map[string]interface{}{
		"roomId":     roomID,
		"proposalId": proposalID,
		"reason":     reason,
	})
}

// handleMovieVote records a vote on an active movie proposal
func handleMovieVote(client *Client, message Message) {
	var data MovieVoteData
	if err := json.Unmarshal(message.Data, &data); err != nil {
		log.Printf("Error parsing movie vote data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid movie vote data")
		return
	}

	if !client.isInRoom(data.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room to vote")
		return
	}

	roomMutex.Lock()
	room, exists := rooms[data.RoomID]
	if !exists {
		roomMutex.Unlock()
		sendErrorResponse(client, "ROOM_NOT_FOUND", "Room not found")
		return
	}

	proposal := room.ActiveProposal
	if proposal == nil || proposal.ID != data.ProposalID {
		roomMutex.Unlock()
		sendErrorResponse(client, "NO_PROPOSAL", "No active proposal with that ID")
		return
	}

	if proposal.Status != "pending" {
		roomMutex.Unlock()
		sendErrorResponse(client, "PROPOSAL_CLOSED", "Proposal is no longer active")
		return
	}

	// Remove any existing vote from this user
	voterID := data.Voter.ID
	newVotesUp := make([]MovieProposalUser, 0, len(proposal.VotesUp))
	for _, v := range proposal.VotesUp {
		if v.ID != voterID {
			newVotesUp = append(newVotesUp, v)
		}
	}
	newVotesDown := make([]MovieProposalUser, 0, len(proposal.VotesDown))
	for _, v := range proposal.VotesDown {
		if v.ID != voterID {
			newVotesDown = append(newVotesDown, v)
		}
	}

	// Add the new vote
	if data.Vote == "up" {
		newVotesUp = append(newVotesUp, data.Voter)
	} else {
		newVotesDown = append(newVotesDown, data.Voter)
	}

	proposal.VotesUp = newVotesUp
	proposal.VotesDown = newVotesDown

	// Make copies for broadcast
	votesUpCopy := make([]MovieProposalUser, len(proposal.VotesUp))
	copy(votesUpCopy, proposal.VotesUp)
	votesDownCopy := make([]MovieProposalUser, len(proposal.VotesDown))
	copy(votesDownCopy, proposal.VotesDown)
	roomMutex.Unlock()

	// Broadcast vote update
	broadcastToRoom(data.RoomID, "movie_vote_update", MovieVoteUpdateData{
		RoomID:     data.RoomID,
		ProposalID: data.ProposalID,
		Votes: struct {
			Up   []MovieProposalUser `json:"up"`
			Down []MovieProposalUser `json:"down"`
		}{
			Up:   votesUpCopy,
			Down: votesDownCopy,
		},
	})

	logRealtime("movie_vote", map[string]interface{}{
		"roomId":     data.RoomID,
		"proposalId": data.ProposalID,
		"voterId":    voterID,
		"vote":       data.Vote,
	})
}

// handleMovieApprove approves a movie proposal (host only)
func handleMovieApprove(client *Client, message Message) {
	var data MovieApproveData
	if err := json.Unmarshal(message.Data, &data); err != nil {
		log.Printf("Error parsing movie approve data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid movie approve data")
		return
	}

	if !client.isInRoom(data.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room")
		return
	}

	// Validate host permission
	if err := validateHostPermission(data.RoomID, client.userID, "approve movie"); err != nil {
		sendErrorResponse(client, "PERMISSION_DENIED", "Only host can approve movies")
		return
	}

	roomMutex.Lock()
	room, exists := rooms[data.RoomID]
	if !exists {
		roomMutex.Unlock()
		sendErrorResponse(client, "ROOM_NOT_FOUND", "Room not found")
		return
	}

	proposal := room.ActiveProposal
	if proposal == nil || proposal.ID != data.ProposalID {
		roomMutex.Unlock()
		sendErrorResponse(client, "NO_PROPOSAL", "No active proposal with that ID")
		return
	}

	if proposal.Status != "pending" {
		roomMutex.Unlock()
		sendErrorResponse(client, "PROPOSAL_CLOSED", "Proposal is no longer active")
		return
	}

	proposal.Status = "approved"
	movieCopy := proposal.Movie
	roomMutex.Unlock()

	// Broadcast approval
	broadcastToRoom(data.RoomID, "movie_approved", MovieApprovedData{
		RoomID:     data.RoomID,
		ProposalID: data.ProposalID,
		Movie:      movieCopy,
		ApprovedBy: data.Approver,
	})

	logRealtime("movie_approved", map[string]interface{}{
		"roomId":     data.RoomID,
		"proposalId": data.ProposalID,
		"movieTitle": movieCopy.Title,
		"approvedBy": data.Approver.ID,
	})
}

// handleMovieReject rejects a movie proposal (host only)
func handleMovieReject(client *Client, message Message) {
	var data MovieRejectData
	if err := json.Unmarshal(message.Data, &data); err != nil {
		log.Printf("Error parsing movie reject data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid movie reject data")
		return
	}

	if !client.isInRoom(data.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room")
		return
	}

	// Validate host permission
	if err := validateHostPermission(data.RoomID, client.userID, "reject movie"); err != nil {
		sendErrorResponse(client, "PERMISSION_DENIED", "Only host can reject movies")
		return
	}

	roomMutex.Lock()
	room, exists := rooms[data.RoomID]
	if !exists {
		roomMutex.Unlock()
		sendErrorResponse(client, "ROOM_NOT_FOUND", "Room not found")
		return
	}

	proposal := room.ActiveProposal
	if proposal == nil || proposal.ID != data.ProposalID {
		roomMutex.Unlock()
		sendErrorResponse(client, "NO_PROPOSAL", "No active proposal with that ID")
		return
	}

	if proposal.Status != "pending" {
		roomMutex.Unlock()
		sendErrorResponse(client, "PROPOSAL_CLOSED", "Proposal is no longer active")
		return
	}

	proposal.Status = "rejected"
	roomMutex.Unlock()

	// Broadcast rejection
	broadcastToRoom(data.RoomID, "movie_rejected", MovieRejectedData{
		RoomID:     data.RoomID,
		ProposalID: data.ProposalID,
		Reason:     "host",
	})

	logRealtime("movie_rejected", map[string]interface{}{
		"roomId":     data.RoomID,
		"proposalId": data.ProposalID,
		"reason":     "host",
	})
}

// ============================================================================
// ROLE AND MODERATION HANDLERS
// ============================================================================

// handleRoleChanged broadcasts when a user's role changes
func handleRoleChanged(client *Client, message Message) {
	var data RoleChangedData
	if err := json.Unmarshal(message.Data, &data); err != nil {
		log.Printf("Error parsing role changed data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid role changed data")
		return
	}

	if !client.isInRoom(data.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room")
		return
	}

	// Validate host permission
	if err := validateHostPermission(data.RoomID, client.userID, "change roles"); err != nil {
		sendErrorResponse(client, "PERMISSION_DENIED", "Only host can change roles")
		return
	}

	// Update room state
	roomMutex.Lock()
	if room, exists := rooms[data.RoomID]; exists {
		room.Participants[data.UserID] = data.NewRole
	}
	roomMutex.Unlock()

	// Broadcast role change to all participants
	broadcastToRoom(data.RoomID, "role_changed", data)

	logRealtime("role_changed", map[string]interface{}{
		"roomId":    data.RoomID,
		"userId":    data.UserID,
		"newRole":   data.NewRole,
		"changedBy": data.ChangedBy,
	})
}

// handleChatMute mutes a user in the room chat
func handleChatMute(client *Client, message Message) {
	var data ChatMuteData
	if err := json.Unmarshal(message.Data, &data); err != nil {
		log.Printf("Error parsing chat mute data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid chat mute data")
		return
	}

	if !client.isInRoom(data.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room")
		return
	}

	// Validate host permission
	if err := validateHostPermission(data.RoomID, client.userID, "mute users"); err != nil {
		sendErrorResponse(client, "PERMISSION_DENIED", "Only host can mute users")
		return
	}

	// Calculate expiration time
	expiresAt := time.Now().Add(time.Duration(data.DurationMinutes) * time.Minute)

	// Store mute state
	roomMutex.Lock()
	room, exists := rooms[data.RoomID]
	if !exists {
		roomMutex.Unlock()
		sendErrorResponse(client, "ROOM_NOT_FOUND", "Room not found")
		return
	}
	if room.MutedUsers == nil {
		room.MutedUsers = make(map[string]MuteInfo)
	}
	room.MutedUsers[data.TargetUserID] = MuteInfo{
		ExpiresAt: expiresAt,
		MutedBy:   client.userID,
		Reason:    data.Reason,
	}
	roomMutex.Unlock()

	// Broadcast mute event to room
	broadcastToRoom(data.RoomID, "user_muted", UserMutedData{
		RoomID:    data.RoomID,
		UserID:    data.TargetUserID,
		ExpiresAt: expiresAt.Format(time.RFC3339),
		MutedBy:   client.userID,
		Reason:    data.Reason,
	})

	// Send mute status to the muted user
	broadcastToSpecificUser(data.TargetUserID, "mute_status", MuteStatusData{
		RoomID:    data.RoomID,
		IsMuted:   true,
		ExpiresAt: expiresAt.Format(time.RFC3339),
		Reason:    data.Reason,
	})

	logRealtime("chat_mute", map[string]interface{}{
		"roomId":          data.RoomID,
		"targetUserId":    data.TargetUserID,
		"mutedBy":         client.userID,
		"durationMinutes": data.DurationMinutes,
	})
}

// handleChatUnmute unmutes a user in the room chat
func handleChatUnmute(client *Client, message Message) {
	var data ChatUnmuteData
	if err := json.Unmarshal(message.Data, &data); err != nil {
		log.Printf("Error parsing chat unmute data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid chat unmute data")
		return
	}

	if !client.isInRoom(data.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room")
		return
	}

	// Validate host permission
	if err := validateHostPermission(data.RoomID, client.userID, "unmute users"); err != nil {
		sendErrorResponse(client, "PERMISSION_DENIED", "Only host can unmute users")
		return
	}

	// Remove mute state
	roomMutex.Lock()
	room, exists := rooms[data.RoomID]
	if exists && room.MutedUsers != nil {
		delete(room.MutedUsers, data.TargetUserID)
	}
	roomMutex.Unlock()

	// Broadcast unmute event to room
	broadcastToRoom(data.RoomID, "user_unmuted", UserUnmutedData{
		RoomID: data.RoomID,
		UserID: data.TargetUserID,
	})

	// Send mute status to the unmuted user
	broadcastToSpecificUser(data.TargetUserID, "mute_status", MuteStatusData{
		RoomID:  data.RoomID,
		IsMuted: false,
	})

	logRealtime("chat_unmute", map[string]interface{}{
		"roomId":       data.RoomID,
		"targetUserId": data.TargetUserID,
		"unmutedBy":    client.userID,
	})
}

// handleDeleteMessage handles message deletion by moderators
func handleDeleteMessage(client *Client, message Message) {
	var data DeleteMessageData
	if err := json.Unmarshal(message.Data, &data); err != nil {
		log.Printf("Error parsing delete message data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid delete message data")
		return
	}

	if !client.isInRoom(data.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room")
		return
	}

	// Validate host permission
	if err := validateHostPermission(data.RoomID, client.userID, "delete messages"); err != nil {
		sendErrorResponse(client, "PERMISSION_DENIED", "Only host can delete messages")
		return
	}

	// Broadcast message deletion to room
	broadcastToRoom(data.RoomID, "message_deleted", MessageDeletedData{
		RoomID:    data.RoomID,
		MessageID: data.MessageID,
		DeletedBy: client.userID,
	})

	logRealtime("delete_message", map[string]interface{}{
		"roomId":    data.RoomID,
		"messageId": data.MessageID,
		"deletedBy": client.userID,
	})
}

// handleGetCinemaAvatars sends all current avatar states to the requester (late joiner)
func handleGetCinemaAvatars(client *Client, message Message) {
	var req struct {
		RoomID string `json:"roomId"`
	}

	if err := json.Unmarshal(message.Data, &req); err != nil {
		return
	}

	roomMutex.RLock()
	room, exists := rooms[req.RoomID]
	var avatars []json.RawMessage
	if exists && room.CinemaAvatars != nil {
		for uid, state := range room.CinemaAvatars {
			if uid != client.userID {
				avatars = append(avatars, state)
			}
		}
	}
	roomMutex.RUnlock()

	if avatars == nil {
		avatars = []json.RawMessage{}
	}

	responseData, _ := json.Marshal(CinemaAvatarStateResponse{RoomID: req.RoomID, Avatars: decodeCinemaAvatarStates(avatars)})

	responseMsg := Message{
		Channel: fmt.Sprintf("user-%s", client.userID),
		Event:   "cinema_avatar_state",
		Data:    responseData,
	}

	select {
	case client.send <- responseMsg:
	default:
	}
}

// handleClient manages the connection for an individual client.
func handleClient(client *Client) {
	defer func() {
		log.Printf("Client disconnecting, cleaning up...")
		unregister <- client
		client.conn.Close()
	}()

	// Set connection parameters.
	client.conn.SetReadLimit(512 * 1024)                          // 512KB max message size.
	client.conn.SetReadDeadline(time.Now().Add(60 * time.Second)) // Set initial read deadline.

	// Setup pong handler to reset read deadline on receiving pong messages.
	client.conn.SetPongHandler(func(string) error {
		client.lastPongTime = time.Now()
		client.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	// Send binary_supported announcement
	binarySupportedData, _ := json.Marshal(map[string]interface{}{})
	binarySupportedMsg := Message{Channel: "system", Event: "binary_supported", Data: binarySupportedData}
	select {
	case client.send <- binarySupportedMsg:
	default:
	}

	// Main message handling loop.
	for {
		// Read message from client (supports both text and binary).
		messageType, rawData, err := client.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("Error reading message: %v", err)
			}
			break
		}

		// Handle binary messages
		if messageType == websocket.BinaryMessage {
			handleBinaryMessage(client, rawData)
			continue
		}

		// Parse JSON message
		var message Message
		if err := json.Unmarshal(rawData, &message); err != nil {
			log.Printf("Error parsing JSON message: %v", err)
			continue
		}

		// Handle messages based on the Event field.
		switch message.Event {
		case "subscribe":
			client.subscribe(message.Channel)
		case "unsubscribe":
			client.unsubscribe(message.Channel)
		// NEW ROOM EVENTS
		case "create_room":
			handleCreateRoom(client, message)
		case "join_room":
			handleJoinRoom(client, message)
		case "leave_room":
			handleLeaveRoom(client, message)
		case "host_sync":
			handleHostSync(client, message)
		case "get_current_video_state":
			handleGetCurrentVideoState(client, message)
		case "transfer_host":
			handleTransferHost(client, message)
		case "transfer_session_control":
			handleTransferSessionControl(client, message)
		case "reclaim_session_control":
			handleReclaimSessionControl(client, message)
		case "room_message":
			handleRoomMessage(client, message)
		case "sync_room_state":
			handleSyncRoomState(client, message)
		// NOTIFICATION EVENTS
		case "room_join_request":
			handleRoomJoinRequest(client, message)
		case "join_request_approved":
			handleJoinRequestApproved(client, message)
		case "join_request_denied":
			handleJoinRequestDenied(client, message)
		case "room_invitation":
			handleRoomInvitation(client, message)
		case "room_deactivated":
			handleRoomDeactivated(client, message)
		case "participant_kicked":
			handleParticipantKicked(client, message)
		// TYPING INDICATORS
		case "user_typing":
			handleUserTyping(client, message)
		case "user_stopped_typing":
			handleUserStoppedTyping(client, message)
		// VIDEO REACTIONS
		case "video_reaction":
			handleVideoReaction(client, message)
		// ACTIVITY ANNOUNCEMENTS
		case "room_announcement":
			handleRoomAnnouncement(client, message)
		// USER PRESENCE
		case "user_presence_update":
			handleUserPresenceUpdate(client, message)
		case "get_room_presence":
			handleGetRoomPresence(client, message)
		// WEBRTC STREAMING
		case "stream_mode_changed":
			handleStreamModeChanged(client, message)
		case "get_stream_status":
			handleGetStreamStatus(client, message)
		// PARTICIPANT WEBCAMS
		case "webcam_join":
			handleWebcamJoin(client, message)
		case "webcam_leave":
			handleWebcamLeave(client, message)
		case "webcam_toggle":
			handleWebcamToggle(client, message)
		case "get_webcam_state":
			handleGetWebcamState(client, message)
		case "webcam_hub_change":
			handleWebcamHubChange(client, message)
		// CINEMA AVATARS
		case "cinema_avatar_update":
			handleCinemaAvatarUpdate(client, message)
		case "get_cinema_avatars":
			handleGetCinemaAvatars(client, message)
		case "cinema_animation":
			handleCinemaAnimation(client, message)
		case "cinema_mood_changed":
			handleCinemaMoodChanged(client, message)
		case "cinema_room_theme_changed":
			handleCinemaRoomThemeChanged(client, message)

		// FACE MODE - webcam mapped onto avatar face
		case "face_mode_toggle":
			handleFaceModeToggle(client, message)
		case "get_face_mode_state":
			handleGetFaceModeState(client, message)
		// QUEUE MANAGEMENT
		case "queue_add":
			handleQueueAdd(client, message)
		case "queue_remove":
			handleQueueRemove(client, message)
		case "queue_reorder":
			handleQueueReorder(client, message)
		case "queue_next":
			handleQueueNext(client, message)
		case "queue_countdown":
			handleQueueCountdown(client, message)
		case "queue_autoplay":
			handleQueueAutoplay(client, message)
		// MOVIE PROPOSALS
		case "movie_propose":
			handleMoviePropose(client, message)
		case "movie_vote":
			handleMovieVote(client, message)
		case "movie_approve":
			handleMovieApprove(client, message)
		case "movie_reject":
			handleMovieReject(client, message)
		// ROLE MANAGEMENT
		case "role_changed":
			handleRoleChanged(client, message)
		// CHAT MODERATION
		case "chat_mute":
			handleChatMute(client, message)
		case "chat_unmute":
			handleChatUnmute(client, message)
		case "delete_message":
			handleDeleteMessage(client, message)
		// ROOM LOCK & BAN
		case "lock_changed":
			handleLockChanged(client, message)
		case "participant_banned":
			handleParticipantBanned(client, message)
		case "participant_unbanned":
			handleParticipantUnbanned(client, message)
		// APPLICATION-LEVEL HEARTBEAT
		// Browser WebSocket API can't send protocol-level pings,
		// so clients send JSON ping events to detect zombie connections.
		case "ping":
			pongData, _ := json.Marshal(map[string]interface{}{})
			pongMsg := Message{Channel: "system", Event: "pong", Data: pongData}
			select {
			case client.send <- pongMsg:
			default:
			}
		// LEGACY SUPPORT
		case "sync":
			handleLegacySync(client, message)
		case "incoming_message", "new_message", "new_friend", "incoming_friend_request", "friend_request_denied", "friend_request_accepted", "friend_removed":
			// Broadcast the message to clients subscribed to the channel
			broadcast <- message
		default:
			log.Printf("Unknown event type: %s", message.Event)
		}
	}
}

// handleConnections upgrades the HTTP connection to a WebSocket and registers the client.
func handleConnections(w http.ResponseWriter, r *http.Request) {
	// Extract userID from query parameters
	userID := r.URL.Query().Get("userID")
	if userID == "" {
		http.Error(w, "UserID required in query parameters", http.StatusBadRequest)
		return
	}

	// Upgrade the HTTP connection to a WebSocket connection.
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Error upgrading connection: %v", err)
		return
	}

	// Create a new client with buffered send channels.
	client := &Client{
		conn:         conn,
		channels:     make(map[string]bool),
		userID:       userID,
		rooms:        make(map[string]string),
		roomChannels: make(map[string]string),
		lastPongTime: time.Now(),              // Initialize to now, will be updated on pong
		send:         make(chan Message, 256), // Buffered channel for outgoing JSON messages
		sendBinary:   make(chan []byte, 256),  // Buffered channel for outgoing binary messages
	}

	log.Printf("New client connected from: %s with userID: %s", conn.RemoteAddr(), userID)

	// Register the client.
	register <- client

	// Start the write pump in its own goroutine.
	go client.writePump()

	// Start handling messages from the client (blocks until client disconnects).
	handleClient(client)
}

// handleMessages processes incoming messages and broadcasts them to subscribed clients.
func handleMessages() {
	// Setup a ticker to send ping messages for keep-alive.
	ticker := time.NewTicker(54 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case client := <-register:
			// Register a new client.
			mutex.Lock()
			clients[client] = true
			mutex.Unlock()

		case client := <-unregister:
			// Unregister a client.
			log.Printf("Client unregistered from: %s with userID: %s", client.conn.RemoteAddr(), client.userID)

			// Remove client from all rooms and update presence
			for roomID := range client.rooms {
				// Mark user as offline before leaving
				roomMutex.Lock()
				if room, exists := rooms[roomID]; exists && room.Presence != nil {
					room.Presence[client.userID] = "offline"
					// Clean up cinema avatar state
					if room.CinemaAvatars != nil {
						delete(room.CinemaAvatars, client.userID)
					}
					if room.WebcamParticipants != nil {
						delete(room.WebcamParticipants, client.userID)
					}
					logRealtime("client_disconnect_cleanup", map[string]interface{}{
						"roomId": roomID,
						"userId": client.userID,
					})
					// Broadcast offline status using channelSubs for O(1) lookup
					offlineData, _ := json.Marshal(map[string]interface{}{
						"roomId":        roomID,
						"userId":        client.userID,
						"userName":      client.userName,
						"presenceState": "offline",
						"timestamp":     time.Now().Format(time.RFC3339),
					})

					offlineMessage := Message{
						Channel: fmt.Sprintf("room-%s", roomID),
						Event:   "user_presence_updated",
						Data:    offlineData,
					}

					// Send to other room participants using channelSubs
					channel := fmt.Sprintf("room-%s", roomID)
					channelMutex.RLock()
					for otherClient := range channelSubs[channel] {
						if otherClient != client {
							select {
							case otherClient.send <- offlineMessage:
							default:
								log.Printf("Client %s send buffer full during presence update", otherClient.userID)
							}
						}
					}
					channelMutex.RUnlock()
				}
				roomMutex.Unlock()

				if err := leaveRoom(roomID, client.userID); err != nil {
					log.Printf("Error removing user %s from room %s during disconnect: %v", client.userID, roomID, err)
				}
			}

			// Clean up empty rooms
			cleanupEmptyRooms()

			// Remove from channelSubs map
			client.unsubscribeAll()

			mutex.Lock()
			if _, ok := clients[client]; ok {
				delete(clients, client)
				// Close send channels to stop writePump goroutine
				close(client.send)
				close(client.sendBinary)
			}
			mutex.Unlock()

		case message := <-broadcast:
			// Broadcast a message to all subscribed clients using O(1) channel lookup.
			channelMutex.RLock()
			subscribers := channelSubs[message.Channel]
			for client := range subscribers {
				// Non-blocking send to client's buffered channel
				select {
				case client.send <- message:
					// Message queued successfully
				default:
					// Client's send buffer is full, log and skip
					log.Printf("Client %s send buffer full, dropping message", client.userID)
				}
			}
			channelMutex.RUnlock()

		case binMsg := <-binaryBroadcast:
			// Broadcast binary message to room subscribers (excluding sender)
			channel := fmt.Sprintf("room-%s", binMsg.RoomID)
			channelMutex.RLock()
			for client := range channelSubs[channel] {
				if client.userID != binMsg.SenderID {
					// Non-blocking send to client's binary channel (no goroutine spawn)
					select {
					case client.sendBinary <- binMsg.Data:
						// Queued successfully
					default:
						log.Printf("Client %s binary buffer full, dropping message", client.userID)
					}
				}
			}
			channelMutex.RUnlock()

		case <-ticker.C:
			// Send ping messages to all clients for keep-alive.
			// Also check for stale connections that haven't responded to pongs.
			mutex.RLock()
			var clientsToRemove []*Client
			pongTimeout := 2 * time.Minute // Consider client dead if no pong in 2 minutes

			for client := range clients {
				// Check if client hasn't responded to pongs recently
				if time.Since(client.lastPongTime) > pongTimeout {
					log.Printf("Client %s (user %s) timed out - no pong in %v",
						client.conn.RemoteAddr(), client.userID, time.Since(client.lastPongTime))
					client.conn.Close()
					clientsToRemove = append(clientsToRemove, client)
					continue
				}

				// Send ping
				err := client.safeWriteControl(websocket.PingMessage, []byte{}, time.Now().Add(10*time.Second))
				if err != nil {
					log.Printf("Ping failed for client %s: %v", client.conn.RemoteAddr(), err)
					client.conn.Close()
					clientsToRemove = append(clientsToRemove, client)
				}
			}
			mutex.RUnlock()

			// Remove clients that failed to respond to ping.
			if len(clientsToRemove) > 0 {
				mutex.Lock()
				for _, client := range clientsToRemove {
					delete(clients, client)
				}
				mutex.Unlock()
			}
		}
	}
}

// startReminderCron starts a background goroutine that calls the SvelteKit
// reminder processing endpoint every minute.
func startReminderCron() {
	sveltekitURL := os.Getenv("SVELTEKIT_URL")
	if sveltekitURL == "" {
		log.Println("[reminders] SVELTEKIT_URL not set, reminder cron disabled")
		return
	}

	apiSecret := os.Getenv("INTERNAL_API_SECRET")
	endpoint := strings.TrimSuffix(sveltekitURL, "/") + "/api/internal/process-reminders"

	ticker := time.NewTicker(1 * time.Minute)

	go func() {
		// Run once immediately on startup
		processReminders(endpoint, apiSecret)

		for range ticker.C {
			processReminders(endpoint, apiSecret)
		}
	}()

	log.Printf("[reminders] Reminder cron started, calling %s every minute\n", endpoint)
}

// processReminders calls the SvelteKit reminder endpoint.
func processReminders(endpoint, apiSecret string) {
	client := &http.Client{Timeout: 30 * time.Second}

	req, err := http.NewRequest("POST", endpoint, nil)
	if err != nil {
		log.Printf("[reminders] Failed to create request: %v\n", err)
		return
	}

	if apiSecret != "" {
		req.Header.Set("Authorization", "Bearer "+apiSecret)
	}

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[reminders] Failed to call endpoint: %v\n", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("[reminders] Endpoint returned status %d\n", resp.StatusCode)
		return
	}

	log.Println("[reminders] Successfully processed reminders")
}

func main() {
	// Configure logging to include date, time, and file line numbers.
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	// Set up the WebSocket endpoint.
	http.HandleFunc("/ws", handleConnections)

	// Start the message handling goroutine.
	go handleMessages()

	// Start the reminder processing cron.
	startReminderCron()

	// Read the port from the environment variable, default to 8080 if not set.
	port := os.Getenv("PORT")
	if port == "" {
		port = "8085"
	}
	addr := ":" + port

	// Start the HTTP server.
	fmt.Printf("WebSocket server starting on %s\n", addr)
	err := http.ListenAndServe(addr, nil)
	if err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}
