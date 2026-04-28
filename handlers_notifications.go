package main

import (
	"encoding/json"
	"log"
	"time"
)

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
	requestData.RequesterID = client.userID

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
	if err := validateHostPermission(approvalData.RoomID, client.userID, "approve join requests"); err != nil {
		sendErrorResponse(client, "permission_denied", err.Error())
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
	if err := validateHostPermission(denialData.RoomID, client.userID, "deny join requests"); err != nil {
		sendErrorResponse(client, "permission_denied", err.Error())
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
	if inviteData.InviterID != client.userID {
		sendErrorResponse(client, "permission_denied", "Cannot send invitations as another user")
		return
	}
	if err := validateRoomModerationPermission(inviteData.RoomID, client.userID, "invite users"); err != nil {
		sendErrorResponse(client, "permission_denied", err.Error())
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
	if err := validateHostPermission(deactivationData.RoomID, client.userID, "deactivate room"); err != nil {
		sendErrorResponse(client, "permission_denied", err.Error())
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
	if err := validateRoomModerationPermission(kickData.RoomID, client.userID, "kick participants"); err != nil {
		sendErrorResponse(client, "permission_denied", err.Error())
		return
	}
	if kickData.KickedID == client.userID {
		sendErrorResponse(client, "INVALID_TARGET", "Cannot kick yourself")
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

	if err := validateRoomModerationPermission(data.RoomID, client.userID, "change lock state"); err != nil {
		sendErrorResponse(client, "permission_denied", err.Error())
		return
	}
	data.ChangedBy = client.userID

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
	if err := validateRoomModerationPermission(data.RoomID, client.userID, "ban participants"); err != nil {
		sendErrorResponse(client, "permission_denied", err.Error())
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
	if !isRoomOwner(data.RoomID, client.userID) && getRoomParticipantRole(data.RoomID, client.userID) != "co-host" {
		sendErrorResponse(client, "permission_denied", "Only hosts can unban participants")
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
