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
