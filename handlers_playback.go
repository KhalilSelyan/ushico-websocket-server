package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
)

func handleHostSync(client *Client, message Message) {
	roomID := strings.TrimPrefix(message.Channel, "room-")
	if !validateRoomID(roomID) {
		log.Printf("Invalid room ID format: %s", roomID)
		sendErrorResponse(client, "INVALID_ROOM_ID", "Invalid room ID format")
		return
	}
	if err := validateRoomMembership(client, roomID); err != nil {
		sendErrorResponse(client, "permission_denied", err.Error())
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
	if !client.isInRoom(data.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room to delegate playback control")
		return
	}

	roomMutex.Lock()
	room, exists := rooms[data.RoomID]
	if !exists {
		roomMutex.Unlock()
		sendErrorResponse(client, "ROOM_NOT_FOUND", "Room not found")
		return
	}
	if _, ok := room.Participants[data.NewHostID]; !ok {
		roomMutex.Unlock()
		sendErrorResponse(client, "INVALID_TARGET", "New session host must be in the room")
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
	if !client.isInRoom(data.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room to reclaim playback control")
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
