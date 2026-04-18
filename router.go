package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

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

		// Route message to appropriate handler
		routeMessage(client, message)
	}
}

// routeMessage dispatches a message to the appropriate handler based on event type.
func routeMessage(client *Client, message Message) {
	switch message.Event {
	case "subscribe":
		client.subscribe(message.Channel)
	case "unsubscribe":
		client.unsubscribe(message.Channel)
	// ROOM EVENTS
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
	// FACE MODE
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
		broadcast <- message
	default:
		log.Printf("Unknown event type: %s", message.Event)
	}
}
