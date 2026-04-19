package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
)

func handleCreateRoom(client *Client, message Message) {
	var roomData RoomData
	if err := json.Unmarshal(message.Data, &roomData); err != nil {
		log.Printf("Error parsing room data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid room data format")
		return
	}

	if client.userID == "" {
		sendErrorResponse(client, "NO_USER_ID", "User ID required to create room")
		return
	}

	room := createRoom(client.userID, roomData.RoomName)
	client.joinRoomAsClient(room.ID, "host")
	client.subscribe(fmt.Sprintf("room-%s", room.ID))

	roomCreatedData, _ := json.Marshal(map[string]interface{}{
		"roomId":   room.ID,
		"roomName": room.Name,
		"hostId":   room.HostID,
	})

	responseMessage := Message{
		Channel: fmt.Sprintf("room-%s", room.ID),
		Event:   "room_created",
		Data:    roomCreatedData,
	}
	broadcast <- responseMessage
}

func handleJoinRoom(client *Client, message Message) {
	var roomData RoomData
	if err := json.Unmarshal(message.Data, &roomData); err != nil {
		log.Printf("Error parsing room data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid room data format")
		return
	}

	if client.userID == "" {
		sendErrorResponse(client, "NO_USER_ID", "User ID required to join room")
		return
	}

	role, isNewJoin, err := joinRoom(roomData.RoomID, client.userID, roomData.Role)
	if err != nil {
		log.Printf("Error joining room: %v", err)
		sendErrorResponse(client, "JOIN_FAILED", err.Error())
		return
	}

	client.joinRoomAsClient(roomData.RoomID, role)
	client.subscribe(fmt.Sprintf("room-%s", roomData.RoomID))
	logRealtime("room_join", map[string]interface{}{
		"roomId":    roomData.RoomID,
		"userId":    client.userID,
		"role":      role,
		"isNewJoin": isNewJoin,
	})

	if !isNewJoin {
		return
	}

	joinedData, _ := json.Marshal(map[string]interface{}{
		"userId": client.userID,
		"role":   role,
	})

	responseMessage := Message{
		Channel: fmt.Sprintf("room-%s", roomData.RoomID),
		Event:   "participant_joined",
		Data:    joinedData,
	}
	broadcast <- responseMessage

	go sendInitialRoomState(client, roomData.RoomID)
}

func sendInitialRoomState(client *Client, roomID string) {
	roomMutex.RLock()
	room, exists := rooms[roomID]
	if !exists {
		roomMutex.RUnlock()
		return
	}

	var avatars []json.RawMessage
	if room.CinemaAvatars != nil {
		for uid, state := range room.CinemaAvatars {
			if uid != client.userID {
				avatars = append(avatars, state)
			}
		}
	}

	var webcamParticipants []WebcamStateParticipant
	if room.WebcamParticipants != nil {
		for _, p := range room.WebcamParticipants {
			webcamParticipants = append(webcamParticipants, p)
		}
	}

	var presenceParticipants []RoomPresenceParticipant
	if room.Presence != nil {
		for userID, state := range room.Presence {
			presenceParticipants = append(presenceParticipants, RoomPresenceParticipant{
				UserID:        userID,
				UserName:      userID,
				PresenceState: state,
			})
		}
	}

	currentVideo := room.CurrentVideo
	roomMutex.RUnlock()

	if avatars == nil {
		avatars = []json.RawMessage{}
	}
	avatarData, _ := json.Marshal(CinemaAvatarStateResponse{
		RoomID:  roomID,
		Avatars: decodeCinemaAvatarStates(avatars),
	})
	client.send <- Message{
		Channel: fmt.Sprintf("user-%s", client.userID),
		Event:   "cinema_avatar_state",
		Data:    avatarData,
	}

	webcamData, _ := json.Marshal(WebcamStateResponse{
		RoomID:       roomID,
		Participants: webcamParticipants,
	})
	client.send <- Message{
		Channel: fmt.Sprintf("user-%s", client.userID),
		Event:   "webcam_state",
		Data:    webcamData,
	}

	presenceData, _ := json.Marshal(RoomPresenceResponse{
		RoomID:       roomID,
		Participants: presenceParticipants,
	})
	client.send <- Message{
		Channel: fmt.Sprintf("user-%s", client.userID),
		Event:   "room_presence_status",
		Data:    presenceData,
	}

	if currentVideo.URL != "" {
		videoData, _ := json.Marshal(map[string]interface{}{
			"roomId": roomID,
			"sync":   currentVideo,
		})
		client.send <- Message{
			Channel: fmt.Sprintf("user-%s", client.userID),
			Event:   "room_video_state",
			Data:    videoData,
		}
	}

	logRealtime("initial_state_pushed", map[string]interface{}{
		"roomId":        roomID,
		"userId":        client.userID,
		"avatarCount":   len(avatars),
		"webcamCount":   len(webcamParticipants),
		"presenceCount": len(presenceParticipants),
		"hasVideo":      currentVideo.URL != "",
	})
}

func handleLeaveRoom(client *Client, message Message) {
	var roomData RoomData
	if err := json.Unmarshal(message.Data, &roomData); err != nil {
		log.Printf("Error parsing room data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid room data format")
		return
	}

	if client.userID == "" {
		sendErrorResponse(client, "NO_USER_ID", "User ID required to leave room")
		return
	}

	wasHost := isRoomHost(roomData.RoomID, client.userID)
	wasSessionHost := false
	ownerID := ""
	roomMutex.RLock()
	if room, exists := rooms[roomData.RoomID]; exists {
		wasSessionHost = room.SessionHostID == client.userID
		ownerID = room.HostID
	}
	roomMutex.RUnlock()

	if err := leaveRoom(roomData.RoomID, client.userID); err != nil {
		log.Printf("Error leaving room: %v", err)
		sendErrorResponse(client, "LEAVE_FAILED", err.Error())
		return
	}

	client.leaveRoomAsClient(roomData.RoomID)
	client.unsubscribe(fmt.Sprintf("room-%s", roomData.RoomID))
	logRealtime("room_leave", map[string]interface{}{
		"roomId":  roomData.RoomID,
		"userId":  client.userID,
		"wasHost": wasHost,
	})

	leftData, _ := json.Marshal(map[string]interface{}{
		"userId":  client.userID,
		"wasHost": wasHost,
	})

	responseMessage := Message{
		Channel: fmt.Sprintf("room-%s", roomData.RoomID),
		Event:   "participant_left",
		Data:    leftData,
	}
	broadcast <- responseMessage

	roomMutex.RLock()
	room, exists := rooms[roomData.RoomID]
	roomMutex.RUnlock()

	if exists && wasHost && len(room.Participants) > 0 {
		transferData, _ := json.Marshal(map[string]interface{}{
			"oldHostId": client.userID,
			"newHostId": room.HostID,
		})

		transferMessage := Message{
			Channel: fmt.Sprintf("room-%s", roomData.RoomID),
			Event:   "host_transferred",
			Data:    transferData,
		}
		broadcast <- transferMessage
	}

	if wasSessionHost {
		sessionData, _ := json.Marshal(SessionControlChangedData{
			RoomID:          roomData.RoomID,
			SessionHostID:   "",
			SessionHostName: "",
			OwnerID:         ownerID,
		})

		sessionMessage := Message{
			Channel: fmt.Sprintf("room-%s", roomData.RoomID),
			Event:   "session_control_changed",
			Data:    sessionData,
		}
		broadcast <- sessionMessage
	}
}

func handleRoomMessage(client *Client, message Message) {
	roomID := strings.TrimPrefix(message.Channel, "room-")
	if !validateRoomID(roomID) {
		log.Printf("Invalid room ID format: %s", roomID)
		sendErrorResponse(client, "INVALID_ROOM_ID", "Invalid room ID format")
		return
	}

	if !client.isInRoom(roomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "You must be in the room to send messages")
		return
	}

	var messageData struct {
		RoomID      string `json:"roomId"`
		Text        string `json:"text"`
		SenderID    string `json:"senderId"`
		SenderName  string `json:"senderName"`
		SenderImage string `json:"senderImage"`
		Timestamp   string `json:"timestamp"`
		MessageID   string `json:"messageId"`
	}

	if err := json.Unmarshal(message.Data, &messageData); err != nil {
		log.Printf("Error parsing room message data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid message data format")
		return
	}

	if messageData.SenderID != client.userID {
		sendErrorResponse(client, "SENDER_MISMATCH", "Sender ID must match authenticated user")
		return
	}

	broadcastToRoomExceptSender(roomID, client.userID, "room_message", messageData)
}

func handleLegacySync(client *Client, message Message) {
	chatID := strings.TrimPrefix(message.Channel, "sync-")
	if !validateChatID(chatID) {
		log.Printf("Invalid chat ID format: %s", chatID)
		return
	}

	var syncData SyncData
	if err := json.Unmarshal(message.Data, &syncData); err != nil {
		log.Printf("Error parsing sync data: %v", err)
		return
	}

	broadcast <- message
}

func handleSyncRoomState(client *Client, message Message) {
	var roomData struct {
		RoomID       string `json:"roomId"`
		HostID       string `json:"hostId"`
		RoomName     string `json:"roomName"`
		Participants []struct {
			UserID string `json:"userId"`
			Role   string `json:"role"`
		} `json:"participants"`
	}

	if err := json.Unmarshal(message.Data, &roomData); err != nil {
		log.Printf("Error parsing room sync data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid room sync data format")
		return
	}

	if client.userID == "" {
		sendErrorResponse(client, "NO_USER_ID", "User ID required to sync room state")
		return
	}

	var clientRole string
	var isParticipant bool
	for _, p := range roomData.Participants {
		if p.UserID == client.userID {
			clientRole = p.Role
			isParticipant = true
			break
		}
	}

	if !isParticipant {
		sendErrorResponse(client, "NOT_PARTICIPANT", "User is not a participant in this room")
		return
	}

	roomMutex.Lock()
	room := &Room{
		ID:                   roomData.RoomID,
		HostID:               roomData.HostID,
		Name:                 roomData.RoomName,
		Participants:         make(map[string]string),
		Presence:             make(map[string]string),
		WebcamParticipants:   make(map[string]WebcamStateParticipant),
		FaceModeParticipants: make(map[string]bool),
		IsActive:             true,
		CreatedAt:            time.Now(),
		CurrentVideo:         SyncData{},
	}

	for _, p := range roomData.Participants {
		room.Participants[p.UserID] = p.Role
		room.Presence[p.UserID] = "active"
	}

	rooms[roomData.RoomID] = room
	roomMutex.Unlock()

	client.joinRoomAsClient(roomData.RoomID, clientRole)
	client.subscribe(fmt.Sprintf("room-%s", roomData.RoomID))

	roomMutex.RLock()
	currentStreamerID := ""
	currentStreamMode := ""
	if r, exists := rooms[roomData.RoomID]; exists {
		currentStreamerID = r.CurrentStreamerID
		currentStreamMode = r.CurrentStreamMode
	}
	roomMutex.RUnlock()

	confirmationData, _ := json.Marshal(map[string]interface{}{
		"roomId":            roomData.RoomID,
		"role":              clientRole,
		"synced":            true,
		"hostId":            roomData.HostID,
		"participants":      len(roomData.Participants),
		"currentStreamerId": currentStreamerID,
		"currentStreamMode": currentStreamMode,
	})

	responseMessage := Message{
		Channel: fmt.Sprintf("room-%s", roomData.RoomID),
		Event:   "room_state_synced",
		Data:    confirmationData,
	}

	if err := client.safeWriteJSON(responseMessage); err != nil {
		log.Printf("Error sending room sync confirmation: %v", err)
	}
}
