package main

import (
	"encoding/json"
	"log"
	"time"
)

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
	if !client.isInRoom(announcementData.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room to send announcements")
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
	data.ChangedBy = client.userID

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
