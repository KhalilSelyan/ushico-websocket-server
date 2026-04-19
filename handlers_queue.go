package main

import (
	"encoding/json"
	"fmt"
	"log"
	"time"
)

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

	item := QueueItem{
		ID:         fmt.Sprintf("qi_%d", time.Now().UnixNano()),
		VideoURL:   data.VideoURL,
		VideoTitle: data.VideoTitle,
		AddedBy:    data.AddedBy,
		AddedAt:    time.Now().UnixMilli(),
	}

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

	broadcastToRoom(data.RoomID, "queue_updated", QueueUpdatedData{
		RoomID: data.RoomID,
		Queue:  queueCopy,
	})

	logRealtime("queue_add", map[string]interface{}{
		"roomId":   data.RoomID,
		"userId":   client.userID,
		"itemId":   item.ID,
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

	room.Queue = append(room.Queue[:itemIndex], room.Queue[itemIndex+1:]...)

	newIndex := data.NewIndex
	if newIndex < 0 {
		newIndex = 0
	}
	if newIndex > len(room.Queue) {
		newIndex = len(room.Queue)
	}

	room.Queue = append(room.Queue[:newIndex], append([]QueueItem{item}, room.Queue[newIndex:]...)...)

	queueCopy := make([]QueueItem, len(room.Queue))
	copy(queueCopy, room.Queue)
	roomMutex.Unlock()

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

	nextItem := room.Queue[0]
	room.Queue = room.Queue[1:]
	queueCopy := make([]QueueItem, len(room.Queue))
	copy(queueCopy, room.Queue)
	roomMutex.Unlock()

	broadcastToRoom(data.RoomID, "queue_autoplay", QueueAutoplayData{
		RoomID: data.RoomID,
		Item:   nextItem,
	})

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

	if err := validateHostPermission(data.RoomID, client.userID, "start countdown"); err != nil {
		sendErrorResponse(client, "PERMISSION_DENIED", "Only host can start countdown")
		return
	}

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

	if err := validateHostPermission(data.RoomID, client.userID, "autoplay"); err != nil {
		sendErrorResponse(client, "PERMISSION_DENIED", "Only host can trigger autoplay")
		return
	}

	broadcastToRoom(data.RoomID, "queue_autoplay", data)

	logRealtime("queue_autoplay", map[string]interface{}{
		"roomId": data.RoomID,
		"userId": client.userID,
		"itemId": data.Item.ID,
	})
}
