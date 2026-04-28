package main

import (
	"encoding/json"
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
