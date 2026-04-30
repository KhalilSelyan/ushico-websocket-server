package main

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"sync"
	"testing"
)

func resetStreamTestGlobals() {
	roomMutex.Lock()
	rooms = make(map[string]*Room)
	roomMutex.Unlock()

	channelMutex.Lock()
	channelSubs = make(map[string]map[*Client]bool)
	channelMutex.Unlock()
}

func TestStreamModeStoresPeerIDAndOverwritesSpoofedUserID(t *testing.T) {
	resetStreamTestGlobals()
	room := &Room{
		ID:           "room-1",
		Participants: map[string]string{"real-user": "viewer"},
		Presence:     map[string]string{},
		IsActive:     true,
	}
	roomMutex.Lock()
	rooms[room.ID] = room
	roomMutex.Unlock()

	client := &Client{
		userID:       "real-user",
		rooms:        map[string]string{"room-1": "viewer"},
		channels:     map[string]bool{},
		roomChannels: map[string]string{"room-1": "room-room-1"},
		send:         make(chan Message, 1),
	}
	channelMutex.Lock()
	channelSubs["room-room-1"] = map[*Client]bool{client: true}
	channelMutex.Unlock()

	payload, _ := json.Marshal(map[string]string{
		"roomId": "room-1",
		"userId": "spoofed-user",
		"mode":   "screen",
		"peerId": "peer-real-user",
	})
	handleStreamModeChanged(client, Message{Data: payload})

	roomMutex.RLock()
	if room.CurrentStreamerID != "real-user" {
		t.Fatalf("streamer id = %q, want real-user", room.CurrentStreamerID)
	}
	if room.CurrentStreamerPeerID != "peer-real-user" {
		t.Fatalf("peer id = %q, want peer-real-user", room.CurrentStreamerPeerID)
	}
	roomMutex.RUnlock()

	msg := <-client.send
	var broadcast StreamModeChangedData
	if err := json.Unmarshal(msg.Data, &broadcast); err != nil {
		t.Fatalf("parse broadcast: %v", err)
	}
	if broadcast.UserID != "real-user" {
		t.Fatalf("broadcast user id = %q, want real-user", broadcast.UserID)
	}
}

func TestStreamModeStoresAndBroadcastsMetadata(t *testing.T) {
	resetStreamTestGlobals()
	room := &Room{
		ID:           "room-1",
		Participants: map[string]string{"streamer": "viewer"},
		Presence:     map[string]string{},
		IsActive:     true,
	}
	roomMutex.Lock()
	rooms[room.ID] = room
	roomMutex.Unlock()

	client := &Client{
		userID:       "streamer",
		rooms:        map[string]string{"room-1": "viewer"},
		channels:     map[string]bool{},
		roomChannels: map[string]string{"room-1": "room-room-1"},
		send:         make(chan Message, 1),
	}
	channelMutex.Lock()
	channelSubs["room-room-1"] = map[*Client]bool{client: true}
	channelMutex.Unlock()

	payload, _ := json.Marshal(map[string]interface{}{
		"roomId": "room-1",
		"mode":   "file",
		"peerId": "peer-streamer",
		"metadata": map[string]interface{}{
			"fileName": "movie.mp4",
			"fileSize": 12345678,
			"duration": 3661.5,
		},
	})
	handleStreamModeChanged(client, Message{Data: payload})

	roomMutex.RLock()
	metadata := room.StreamMetadata
	roomMutex.RUnlock()
	if metadata.FileName != "movie.mp4" || metadata.FileSize != 12345678 || metadata.Duration != 3661.5 {
		t.Fatalf("metadata = %+v", metadata)
	}

	msg := <-client.send
	var broadcast StreamModeChangedData
	if err := json.Unmarshal(msg.Data, &broadcast); err != nil {
		t.Fatalf("parse broadcast: %v", err)
	}
	if broadcast.Metadata.FileName != "movie.mp4" {
		t.Fatalf("broadcast metadata = %+v", broadcast.Metadata)
	}
}

func TestLeaveRoomClearsStreamerPeerID(t *testing.T) {
	resetStreamTestGlobals()
	room := &Room{
		ID:                    "room-1",
		Participants:          map[string]string{"streamer": "viewer", "other": "viewer"},
		Presence:              map[string]string{},
		IsActive:              true,
		CurrentStreamerID:     "streamer",
		CurrentStreamMode:     "camera",
		CurrentStreamerPeerID: "peer-streamer",
	}
	roomMutex.Lock()
	rooms[room.ID] = room
	roomMutex.Unlock()

	if err := leaveRoom("room-1", "streamer"); err != nil {
		t.Fatalf("leaveRoom: %v", err)
	}

	roomMutex.RLock()
	defer roomMutex.RUnlock()
	if room.CurrentStreamerID != "" || room.CurrentStreamMode != "" || room.CurrentStreamerPeerID != "" {
		t.Fatalf("stream state not cleared: id=%q mode=%q peer=%q", room.CurrentStreamerID, room.CurrentStreamMode, room.CurrentStreamerPeerID)
	}
}

func TestStoppingStreamClearsPeerIDAndAddsReason(t *testing.T) {
	resetStreamTestGlobals()
	room := &Room{
		ID:                    "room-1",
		Participants:          map[string]string{"streamer": "viewer"},
		Presence:              map[string]string{},
		IsActive:              true,
		CurrentStreamerID:     "streamer",
		CurrentStreamMode:     "file",
		CurrentStreamerPeerID: "peer-streamer",
		StreamMetadata:        StreamMetadata{FileName: "movie.mp4", FileSize: 100, Duration: 10},
	}
	roomMutex.Lock()
	rooms[room.ID] = room
	roomMutex.Unlock()

	client := &Client{
		userID:       "streamer",
		rooms:        map[string]string{"room-1": "viewer"},
		channels:     map[string]bool{},
		roomChannels: map[string]string{"room-1": "room-room-1"},
		send:         make(chan Message, 1),
	}
	channelMutex.Lock()
	channelSubs["room-room-1"] = map[*Client]bool{client: true}
	channelMutex.Unlock()

	payload, _ := json.Marshal(map[string]string{"roomId": "room-1", "mode": "none"})
	handleStreamModeChanged(client, Message{Data: payload})

	roomMutex.RLock()
	if room.CurrentStreamerID != "" || room.CurrentStreamMode != "" || room.CurrentStreamerPeerID != "" {
		t.Fatalf("stream state not cleared: id=%q mode=%q peer=%q", room.CurrentStreamerID, room.CurrentStreamMode, room.CurrentStreamerPeerID)
	}
	if room.StreamMetadata != (StreamMetadata{}) {
		t.Fatalf("metadata not cleared: %+v", room.StreamMetadata)
	}
	roomMutex.RUnlock()

	msg := <-client.send
	var broadcast StreamModeChangedData
	if err := json.Unmarshal(msg.Data, &broadcast); err != nil {
		t.Fatalf("parse broadcast: %v", err)
	}
	if broadcast.Reason != "stopped" {
		t.Fatalf("reason = %q, want stopped", broadcast.Reason)
	}
}

func TestNonStreamerCannotBroadcastStop(t *testing.T) {
	resetStreamTestGlobals()
	room := &Room{
		ID:                    "room-1",
		Participants:          map[string]string{"streamer": "viewer", "other": "viewer"},
		Presence:              map[string]string{},
		IsActive:              true,
		CurrentStreamerID:     "streamer",
		CurrentStreamMode:     "screen",
		CurrentStreamerPeerID: "peer-streamer",
	}
	roomMutex.Lock()
	rooms[room.ID] = room
	roomMutex.Unlock()

	client := &Client{
		userID:       "other",
		rooms:        map[string]string{"room-1": "viewer"},
		channels:     map[string]bool{},
		roomChannels: map[string]string{"room-1": "room-room-1"},
		send:         make(chan Message, 1),
	}
	channelMutex.Lock()
	channelSubs["room-room-1"] = map[*Client]bool{client: true}
	channelMutex.Unlock()

	payload, _ := json.Marshal(map[string]string{"roomId": "room-1", "mode": "none"})
	handleStreamModeChanged(client, Message{Data: payload})

	roomMutex.RLock()
	if room.CurrentStreamerID != "streamer" || room.CurrentStreamMode != "screen" || room.CurrentStreamerPeerID != "peer-streamer" {
		t.Fatalf("stream state changed unexpectedly: id=%q mode=%q peer=%q", room.CurrentStreamerID, room.CurrentStreamMode, room.CurrentStreamerPeerID)
	}
	roomMutex.RUnlock()

	select {
	case msg := <-client.send:
		t.Fatalf("unexpected broadcast: %s", msg.Event)
	default:
	}
}

func TestSyncRoomStatePreservesActiveStreamer(t *testing.T) {
	resetStreamTestGlobals()
	originalAuthorizer := authorizeRoomAccess
	authorizeRoomAccess = func(roomID, userID string) (*RealtimeRoomAccess, error) {
		access := &RealtimeRoomAccess{Allowed: true, Role: "host"}
		access.Room.ID = roomID
		access.Room.Name = "Room"
		access.Room.HostID = userID
		access.Room.Participants = []RealtimeParticipant{
			{UserID: userID, Role: "host"},
			{UserID: "streamer", Role: "viewer"},
		}
		return access, nil
	}
	defer func() { authorizeRoomAccess = originalAuthorizer }()

	room := &Room{
		ID:                    "room-1",
		HostID:                "host",
		Participants:          map[string]string{"host": "host"},
		Presence:              map[string]string{},
		IsActive:              true,
		CurrentStreamerID:     "streamer",
		CurrentStreamMode:     "camera",
		CurrentStreamerPeerID: "peer-streamer",
		StreamMetadata:        StreamMetadata{FileName: "movie.mp4", FileSize: 100, Duration: 10},
	}
	roomMutex.Lock()
	rooms[room.ID] = room
	roomMutex.Unlock()

	client := &Client{
		userID:       "host",
		rooms:        map[string]string{},
		channels:     map[string]bool{},
		roomChannels: map[string]string{},
		send:         make(chan Message, 1),
	}

	payload, _ := json.Marshal(map[string]interface{}{
		"roomId":   "room-1",
		"hostId":   "host",
		"roomName": "Room",
		"participants": []map[string]string{
			{"userId": "host", "role": "host"},
			{"userId": "streamer", "role": "viewer"},
		},
	})
	handleSyncRoomState(client, Message{Data: payload})

	roomMutex.RLock()
	preserved := rooms["room-1"].CurrentStreamerID == "streamer" && rooms["room-1"].CurrentStreamMode == "camera" && rooms["room-1"].CurrentStreamerPeerID == "peer-streamer" && rooms["room-1"].StreamMetadata.FileName == "movie.mp4"
	roomMutex.RUnlock()
	if !preserved {
		t.Fatal("sync_room_state did not preserve active streamer")
	}
}

func encodeHostSyncTestPayload(source byte) []byte {
	roomID := []byte("room-1")
	url := []byte("https://example.com/video.mp4")
	videoID := []byte("video-1")
	data := make([]byte, 0, 1+8+8+1+1+1+4+len(roomID)+len(url)+len(videoID))
	data = append(data, BinaryMsgHostSync)
	tmp := make([]byte, 8)
	binary.LittleEndian.PutUint64(tmp, math.Float64bits(12.5))
	data = append(data, tmp...)
	binary.LittleEndian.PutUint64(tmp, uint64(12345))
	data = append(data, tmp...)
	data = append(data, 1) // playing
	data = append(data, 3) // seek
	data = append(data, source)
	data = append(data, byte(len(roomID)))
	lenBytes := make([]byte, 2)
	binary.LittleEndian.PutUint16(lenBytes, uint16(len(url)))
	data = append(data, lenBytes...)
	data = append(data, byte(len(videoID)))
	data = append(data, roomID...)
	data = append(data, url...)
	data = append(data, videoID...)
	return data
}

func TestBinaryHostSyncDecodesSourceMetadata(t *testing.T) {
	data := encodeHostSyncTestPayload(3) // file
	roomID, err := decodeBinaryHostSync(data)
	if err != nil {
		t.Fatalf("decode room id: %v", err)
	}
	if roomID != "room-1" {
		t.Fatalf("room id = %q", roomID)
	}

	syncData := binaryHostSyncToSyncData(data)
	if syncData == nil {
		t.Fatal("sync data is nil")
	}
	if syncData.Source != "file" || syncData.State != "playing" || syncData.Reason != "seek" {
		t.Fatalf("sync data = %+v", syncData)
	}
}

func TestDisconnectCleanupIsIdempotent(t *testing.T) {
	resetStreamTestGlobals()
	client := &Client{
		userID:       "user-1",
		rooms:        map[string]string{},
		channels:     map[string]bool{},
		roomChannels: map[string]string{},
		send:         make(chan Message, 1),
		sendBinary:   make(chan []byte, 1),
	}

	mutex.Lock()
	clients[client] = true
	mutex.Unlock()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		cleanupDisconnectedClient(client)
	}()
	go func() {
		defer wg.Done()
		cleanupDisconnectedClient(client)
	}()
	wg.Wait()

	mutex.RLock()
	_, exists := clients[client]
	mutex.RUnlock()
	if exists {
		t.Fatal("client still registered after cleanup")
	}
}
