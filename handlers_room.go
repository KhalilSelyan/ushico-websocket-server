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

func handleHostSync(client *Client, message Message) {
	roomID := strings.TrimPrefix(message.Channel, "room-")
	if !validateRoomID(roomID) {
		log.Printf("Invalid room ID format: %s", roomID)
		sendErrorResponse(client, "INVALID_ROOM_ID", "Invalid room ID format")
		return
	}

	if err := validateHostPermission(roomID, client.userID, "control video playback"); err != nil {
		log.Printf("Permission denied for user %s in room %s: %v", client.userID, roomID, err)
		sendErrorResponse(client, "PERMISSION_DENIED", "Only host can control video playback")
		return
	}

	var syncData SyncData
	if err := json.Unmarshal(message.Data, &syncData); err != nil {
		log.Printf("Error parsing sync data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid sync data format")
		return
	}

	validPlaybackStates := map[string]bool{"playing": true, "paused": true}
	validPlaybackReasons := map[string]bool{
		"tick": true, "play": true, "pause": true, "seek": true,
		"load": true, "resync": true, "ended": true,
	}

	if !validPlaybackStates[syncData.State] || !validPlaybackReasons[syncData.Reason] {
		sendErrorResponse(client, "INVALID_DATA", "Invalid playback sync state")
		return
	}

	if syncData.SentAt == 0 {
		syncData.SentAt = time.Now().UnixMilli()
	}
	logRealtime("playback_sync", map[string]interface{}{
		"roomId":    roomID,
		"userId":    client.userID,
		"state":     syncData.State,
		"reason":    syncData.Reason,
		"timestamp": syncData.Timestamp,
		"videoId":   syncData.VideoID,
	})

	roomMutex.Lock()
	if room, exists := rooms[roomID]; exists {
		room.CurrentVideo = syncData
		log.Printf("Room %s video sync: URL=%s, VideoID=%s, Time=%.2f, State=%s",
			roomID, syncData.URL, syncData.VideoID, syncData.Timestamp, syncData.State)
	}
	roomMutex.Unlock()

	broadcast <- message
}

func handleGetCurrentVideoState(client *Client, message Message) {
	var req struct {
		RoomID string `json:"roomId"`
	}

	if err := json.Unmarshal(message.Data, &req); err != nil {
		log.Printf("Error parsing current video state request: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid current video state request")
		return
	}

	if !client.isInRoom(req.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room to request current video state")
		return
	}

	roomMutex.RLock()
	room, exists := rooms[req.RoomID]
	roomMutex.RUnlock()

	var syncState *SyncData
	if exists && room.CurrentVideo.VideoID != "" {
		current := room.CurrentVideo
		syncState = &current
	}

	broadcastToSpecificUser(client.userID, "room_video_state", RoomVideoStateResponse{RoomID: req.RoomID, Sync: syncState})
	logRealtime("playback_state_snapshot", map[string]interface{}{
		"roomId":       req.RoomID,
		"userId":       client.userID,
		"hasSyncState": syncState != nil,
	})
}

func handleTransferHost(client *Client, message Message) {
	var roomData RoomData
	if err := json.Unmarshal(message.Data, &roomData); err != nil {
		log.Printf("Error parsing room data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid room data format")
		return
	}

	if err := validateHostPermission(roomData.RoomID, client.userID, "transfer host"); err != nil {
		log.Printf("Permission denied for user %s: %v", client.userID, err)
		sendErrorResponse(client, "PERMISSION_DENIED", "Only current host can transfer host privileges")
		return
	}

	if err := transferHost(roomData.RoomID, roomData.UserID); err != nil {
		log.Printf("Error transferring host: %v", err)
		sendErrorResponse(client, "TRANSFER_FAILED", err.Error())
		return
	}

	client.leaveRoomAsClient(roomData.RoomID)
	client.joinRoomAsClient(roomData.RoomID, "viewer")

	transferData, _ := json.Marshal(map[string]interface{}{
		"oldHostId": client.userID,
		"newHostId": roomData.UserID,
	})

	responseMessage := Message{
		Channel: fmt.Sprintf("room-%s", roomData.RoomID),
		Event:   "host_transferred",
		Data:    transferData,
	}
	broadcast <- responseMessage
}

func handleTransferSessionControl(client *Client, message Message) {
	var data TransferSessionControlData
	if err := json.Unmarshal(message.Data, &data); err != nil {
		log.Printf("Error parsing transfer session control data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid transfer session control data")
		return
	}

	if !isRoomOwner(data.RoomID, client.userID) {
		sendErrorResponse(client, "PERMISSION_DENIED", "Only room owner can delegate playback control")
		return
	}

	roomMutex.Lock()
	room, exists := rooms[data.RoomID]
	if !exists {
		roomMutex.Unlock()
		sendErrorResponse(client, "ROOM_NOT_FOUND", "Room not found")
		return
	}
	room.SessionHostID = data.NewHostID
	ownerID := room.HostID
	roomMutex.Unlock()

	responseData, _ := json.Marshal(SessionControlChangedData{
		RoomID:          data.RoomID,
		SessionHostID:   data.NewHostID,
		SessionHostName: data.NewHostName,
		OwnerID:         ownerID,
	})

	broadcast <- Message{
		Channel: fmt.Sprintf("room-%s", data.RoomID),
		Event:   "session_control_changed",
		Data:    responseData,
	}

	logRealtime("session_control_transferred", map[string]interface{}{
		"roomId":        data.RoomID,
		"ownerId":       client.userID,
		"sessionHostId": data.NewHostID,
	})
}

func handleReclaimSessionControl(client *Client, message Message) {
	var data ReclaimSessionControlData
	if err := json.Unmarshal(message.Data, &data); err != nil {
		log.Printf("Error parsing reclaim session control data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid reclaim session control data")
		return
	}

	if !isRoomOwner(data.RoomID, client.userID) {
		sendErrorResponse(client, "PERMISSION_DENIED", "Only room owner can reclaim playback control")
		return
	}

	roomMutex.Lock()
	room, exists := rooms[data.RoomID]
	if !exists {
		roomMutex.Unlock()
		sendErrorResponse(client, "ROOM_NOT_FOUND", "Room not found")
		return
	}
	previousSessionHost := room.SessionHostID
	room.SessionHostID = ""
	ownerID := room.HostID
	roomMutex.Unlock()

	responseData, _ := json.Marshal(SessionControlChangedData{
		RoomID:          data.RoomID,
		SessionHostID:   "",
		SessionHostName: "",
		OwnerID:         ownerID,
	})

	broadcast <- Message{
		Channel: fmt.Sprintf("room-%s", data.RoomID),
		Event:   "session_control_changed",
		Data:    responseData,
	}

	logRealtime("session_control_reclaimed", map[string]interface{}{
		"roomId":              data.RoomID,
		"ownerId":             client.userID,
		"previousSessionHost": previousSessionHost,
	})
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

func handleRoomJoinRequest(client *Client, message Message) {
	var requestData struct {
		RoomID      string `json:"roomId"`
		RequesterID string `json:"requesterId"`
		RequestID   string `json:"requestId"`
		UserName    string `json:"userName"`
		UserImage   string `json:"userImage"`
	}

	if err := json.Unmarshal(message.Data, &requestData); err != nil {
		log.Printf("Error parsing join request data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid join request data format")
		return
	}

	roomMutex.RLock()
	room, exists := rooms[requestData.RoomID]
	roomMutex.RUnlock()

	if !exists {
		sendErrorResponse(client, "ROOM_NOT_FOUND", "Room not found")
		return
	}

	broadcastToSpecificUser(room.HostID, "room_join_request", requestData)
}

func handleJoinRequestApproved(client *Client, message Message) {
	var approvalData struct {
		RequestID   string `json:"requestId"`
		RequesterID string `json:"requesterId"`
		RoomID      string `json:"roomId"`
		RoomName    string `json:"roomName"`
	}

	if err := json.Unmarshal(message.Data, &approvalData); err != nil {
		log.Printf("Error parsing join approval data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid join approval data format")
		return
	}

	broadcastToSpecificUser(approvalData.RequesterID, "join_request_approved", approvalData)
}

func handleJoinRequestDenied(client *Client, message Message) {
	var denialData struct {
		RequestID   string `json:"requestId"`
		RequesterID string `json:"requesterId"`
		RoomID      string `json:"roomId"`
		RoomName    string `json:"roomName"`
		Reason      string `json:"reason,omitempty"`
	}

	if err := json.Unmarshal(message.Data, &denialData); err != nil {
		log.Printf("Error parsing join denial data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid join denial data format")
		return
	}

	broadcastToSpecificUser(denialData.RequesterID, "join_request_denied", denialData)
}

func handleRoomInvitation(client *Client, message Message) {
	var inviteData struct {
		InvitationID string `json:"invitationId"`
		InviteeID    string `json:"inviteeId"`
		InviterID    string `json:"inviterId"`
		InviterName  string `json:"inviterName"`
		RoomID       string `json:"roomId"`
		RoomName     string `json:"roomName"`
	}

	if err := json.Unmarshal(message.Data, &inviteData); err != nil {
		log.Printf("Error parsing room invitation data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid room invitation data format")
		return
	}

	broadcastToSpecificUser(inviteData.InviteeID, "room_invitation", inviteData)
}

func handleRoomDeactivated(client *Client, message Message) {
	var deactivationData struct {
		RoomID   string `json:"roomId"`
		RoomName string `json:"roomName"`
		Reason   string `json:"reason,omitempty"`
	}

	if err := json.Unmarshal(message.Data, &deactivationData); err != nil {
		log.Printf("Error parsing room deactivation data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid room deactivation data format")
		return
	}

	participants := getRoomParticipants(deactivationData.RoomID)

	for _, participantID := range participants {
		broadcastToSpecificUser(participantID, "room_deactivated", deactivationData)
	}

	roomMutex.Lock()
	if room, exists := rooms[deactivationData.RoomID]; exists {
		room.IsActive = false
	}
	roomMutex.Unlock()
}

func handleParticipantKicked(client *Client, message Message) {
	var kickData struct {
		RoomID     string `json:"roomId"`
		RoomName   string `json:"roomName"`
		KickedID   string `json:"kickedId"`
		KickedName string `json:"kickedName"`
		Reason     string `json:"reason,omitempty"`
	}

	if err := json.Unmarshal(message.Data, &kickData); err != nil {
		log.Printf("Error parsing participant kick data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid participant kick data format")
		return
	}

	if err := leaveRoom(kickData.RoomID, kickData.KickedID); err != nil {
		log.Printf("Error removing kicked user from room: %v", err)
	}

	broadcastToSpecificUser(kickData.KickedID, "participant_kicked", kickData)

	participants := getRoomParticipants(kickData.RoomID)
	kickNotification := map[string]interface{}{
		"userId":   kickData.KickedID,
		"userName": kickData.KickedName,
		"reason":   kickData.Reason,
	}

	for _, participantID := range participants {
		if participantID != kickData.KickedID {
			broadcastToSpecificUser(participantID, "participant_left", kickNotification)
		}
	}
}

func handleLockChanged(client *Client, message Message) {
	var data LockChangedData
	if err := json.Unmarshal(message.Data, &data); err != nil {
		log.Printf("Error parsing lock changed data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid lock changed data format")
		return
	}

	if !client.isInRoom(data.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "You are not in this room")
		return
	}

	logRealtime("lock_changed", map[string]interface{}{
		"roomId":    data.RoomID,
		"userId":    client.userID,
		"lockState": data.LockState,
	})

	broadcastToRoom(data.RoomID, "lock_changed", data)
}

func handleParticipantBanned(client *Client, message Message) {
	var data BanUserData
	if err := json.Unmarshal(message.Data, &data); err != nil {
		log.Printf("Error parsing ban data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid ban data format")
		return
	}

	if !client.isInRoom(data.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "You are not in this room")
		return
	}

	if err := leaveRoom(data.RoomID, data.TargetUserID); err != nil {
		log.Printf("Error removing banned user from room: %v", err)
	}

	roomMutex.RLock()
	roomName := ""
	if room, exists := rooms[data.RoomID]; exists {
		roomName = room.Name
	}
	roomMutex.RUnlock()

	expiresAt := ""
	if data.BanType == "timed" && data.DurationMinutes > 0 {
		expiresAt = time.Now().Add(time.Duration(data.DurationMinutes) * time.Minute).Format(time.RFC3339)
	}

	youBanned := YouBannedData{
		RoomID:    data.RoomID,
		RoomName:  roomName,
		BanType:   data.BanType,
		Reason:    data.Reason,
		ExpiresAt: expiresAt,
	}
	broadcastToSpecificUser(data.TargetUserID, "you_banned", youBanned)

	bannedNotification := ParticipantBannedData{
		RoomID:   data.RoomID,
		UserID:   data.TargetUserID,
		UserName: data.TargetUserName,
		BanType:  data.BanType,
		Reason:   data.Reason,
		BannedBy: client.userID,
	}
	broadcastToRoom(data.RoomID, "participant_banned", bannedNotification)

	leftNotification := map[string]interface{}{
		"userId":   data.TargetUserID,
		"userName": data.TargetUserName,
		"reason":   "banned",
	}
	broadcastToRoom(data.RoomID, "participant_left", leftNotification)

	logRealtime("participant_banned", map[string]interface{}{
		"roomId":       data.RoomID,
		"userId":       client.userID,
		"targetUserId": data.TargetUserID,
		"banType":      data.BanType,
	})
}

func handleParticipantUnbanned(client *Client, message Message) {
	var data UnbanUserData
	if err := json.Unmarshal(message.Data, &data); err != nil {
		log.Printf("Error parsing unban data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid unban data format")
		return
	}

	if !client.isInRoom(data.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "You are not in this room")
		return
	}

	unbannedNotification := ParticipantUnbannedData{
		RoomID:     data.RoomID,
		UserID:     data.TargetUserID,
		UnbannedBy: client.userID,
	}
	broadcastToRoom(data.RoomID, "participant_unbanned", unbannedNotification)

	logRealtime("participant_unbanned", map[string]interface{}{
		"roomId":       data.RoomID,
		"userId":       client.userID,
		"targetUserId": data.TargetUserID,
	})
}
