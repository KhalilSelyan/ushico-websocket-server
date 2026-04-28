package main

import (
	"encoding/json"
	"fmt"
	"log"
)

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
		PeerID    string `json:"peerId,omitempty"`
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
			room.CurrentStreamerPeerID = ""
		}
	} else {
		// Start streaming - lock to this user
		room.CurrentStreamerID = client.userID
		room.CurrentStreamMode = modeData.Mode
		room.CurrentStreamerPeerID = modeData.PeerID
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
			RoomID:                requestData.RoomID,
			CurrentStreamerID:     room.CurrentStreamerID,
			CurrentStreamMode:     room.CurrentStreamMode,
			CurrentStreamerPeerID: room.CurrentStreamerPeerID,
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

	if !client.isInRoom(reactionData.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room to send reactions")
		return
	}

	validEmojis := map[string]bool{
		"😂": true, "❤️": true, "😮": true, "👏": true, "😢": true,
		"🔥": true, "💯": true, "👍": true, "👎": true, "😍": true,
	}

	if !validEmojis[reactionData.Emoji] {
		sendErrorResponse(client, "INVALID_EMOJI", "Invalid emoji for reactions")
		return
	}

	broadcastToRoom(reactionData.RoomID, "video_reaction", reactionData)
}
