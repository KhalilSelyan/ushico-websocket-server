package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
)

// MuteInfo tracks mute state for a user
type MuteInfo struct {
	ExpiresAt time.Time `json:"expiresAt"`
	MutedBy   string    `json:"mutedBy"`
	Reason    string    `json:"reason,omitempty"`
}

// Room represents a watch party room state (in-memory only).
type Room struct {
	ID                    string                            `json:"id"`
	HostID                string                            `json:"hostId"`
	SessionHostID         string                            `json:"sessionHostId,omitempty"` // Temporary playback controller (falls back to HostID if empty)
	Name                  string                            `json:"name"`
	Participants          map[string]string                 `json:"participants"`  // userID -> role mapping
	Presence              map[string]string                 `json:"presence"`      // userID -> presence state (active, away, offline)
	CinemaAvatars         map[string]json.RawMessage        `json:"cinemaAvatars"` // userID -> last avatar state
	WebcamParticipants    map[string]WebcamStateParticipant `json:"webcamParticipants"`
	FaceModeParticipants  map[string]bool                   `json:"faceModeParticipants"` // userID -> face mode enabled
	Queue                 []QueueItem                       `json:"queue"`                // Video queue
	MutedUsers            map[string]MuteInfo               `json:"mutedUsers"`           // userID -> mute info
	ActiveProposal        *MovieProposal                    `json:"activeProposal"`       // Active movie proposal (nil if none)
	IsActive              bool                              `json:"isActive"`
	CreatedAt             time.Time                         `json:"createdAt"`
	CurrentVideo          SyncData                          `json:"currentVideo"`
	CurrentStreamerID     string                            `json:"currentStreamerId,omitempty"`     // User currently streaming (empty = nobody streaming)
	CurrentStreamMode     string                            `json:"currentStreamMode,omitempty"`     // Current stream mode (screen, camera, file, none)
	CurrentStreamerPeerID string                            `json:"currentStreamerPeerId,omitempty"` // PeerJS id viewers connect to for the current stream
}

func clearRoomStreamer(room *Room) {
	room.CurrentStreamerID = ""
	room.CurrentStreamMode = ""
	room.CurrentStreamerPeerID = ""
}

// RoomData for room creation/management events.
type RoomData struct {
	RoomID   string   `json:"roomId"`
	RoomName string   `json:"roomName,omitempty"`
	UserID   string   `json:"userId"`
	UserIDs  []string `json:"userIds,omitempty"` // for bulk operations
	Role     string   `json:"role,omitempty"`    // "host" or "viewer" — sent by client on join
}

// validateRoomID checks if room ID format is valid.
func validateRoomID(roomID string) bool {
	return len(roomID) > 0 && !strings.Contains(roomID, "--")
}

// createRoom creates a new watch party room.
func createRoom(hostID, roomName string) *Room {
	roomMutex.Lock()
	defer roomMutex.Unlock()

	roomID := fmt.Sprintf("room_%d", time.Now().UnixNano())
	room := &Room{
		ID:                   roomID,
		HostID:               hostID,
		Name:                 roomName,
		Participants:         make(map[string]string),
		Presence:             make(map[string]string),
		WebcamParticipants:   make(map[string]WebcamStateParticipant),
		FaceModeParticipants: make(map[string]bool),
		Queue:                make([]QueueItem, 0),
		MutedUsers:           make(map[string]MuteInfo),
		IsActive:             true,
		CreatedAt:            time.Now(),
		CurrentVideo:         SyncData{},
	}
	room.Participants[hostID] = "host"
	room.Presence[hostID] = "active"
	rooms[roomID] = room

	return room
}

// joinRoom adds a user to an existing room.
func joinRoom(roomID, userID, requestedRole string) (string, bool, error) {
	roomMutex.Lock()
	defer roomMutex.Unlock()

	room, exists := rooms[roomID]
	if !exists {
		// Auto-create room in server memory if it doesn't exist
		// (room was created via DB, not via WebSocket create_room)
		room = &Room{
			ID:                   roomID,
			Participants:         make(map[string]string),
			Presence:             make(map[string]string),
			CinemaAvatars:        make(map[string]json.RawMessage),
			WebcamParticipants:   make(map[string]WebcamStateParticipant),
			FaceModeParticipants: make(map[string]bool),
			Queue:                make([]QueueItem, 0),
			MutedUsers:           make(map[string]MuteInfo),
			IsActive:             true,
			CreatedAt:            time.Now(),
		}
		rooms[roomID] = room
		log.Printf("Auto-created room %s in server memory for user %s", roomID, userID)
	}
	if !room.IsActive {
		return "", false, fmt.Errorf("room is not active")
	}

	if existingRole, exists := room.Participants[userID]; exists {
		if requestedRole == "host" && room.HostID == userID {
			room.Participants[userID] = "host"
			existingRole = "host"
		}
		room.Presence[userID] = "active"
		return existingRole, false, nil
	}

	role := "viewer"
	if requestedRole == "host" && room.HostID == userID {
		role = "host"
	}

	room.Participants[userID] = role
	room.Presence[userID] = "active"
	return role, true, nil
}

// leaveRoom removes a user from a room.
func leaveRoom(roomID, userID string) error {
	roomMutex.Lock()
	defer roomMutex.Unlock()

	room, exists := rooms[roomID]
	if !exists {
		return fmt.Errorf("room not found")
	}

	delete(room.Participants, userID)
	delete(room.Presence, userID) // Remove presence when leaving
	if room.WebcamParticipants != nil {
		delete(room.WebcamParticipants, userID)
	}
	if room.FaceModeParticipants != nil {
		delete(room.FaceModeParticipants, userID)
	}

	// If session host leaves, clear session host (control reverts to owner)
	if room.SessionHostID == userID {
		room.SessionHostID = ""
	}

	// If current streamer leaves, clear streaming state and notify room
	if room.CurrentStreamerID == userID {
		clearRoomStreamer(room)
		// Broadcast streamer_stopped to room (will be sent after mutex unlocks)
		go func() {
			streamerStoppedData := map[string]interface{}{
				"roomId": roomID,
				"userId": userID,
				"mode":   "none",
				"reason": "disconnect",
			}
			broadcastToRoom(roomID, "stream_mode_changed", streamerStoppedData)
		}()
	}

	// If no participants left, deactivate room
	if len(room.Participants) == 0 {
		room.IsActive = false
		log.Printf("Room %s deactivated (no participants)", roomID)
	} else if room.HostID == userID {
		// Transfer host to first available participant
		for participantID := range room.Participants {
			room.HostID = participantID
			room.Participants[participantID] = "host"
			break
		}
	}

	return nil
}

// isRoomHost checks if user is the host of the room.
func isRoomHost(roomID, userID string) bool {
	roomMutex.RLock()
	defer roomMutex.RUnlock()

	room, exists := rooms[roomID]
	if !exists {
		return false
	}
	return room.HostID == userID
}

// getEffectiveHost returns the user who currently controls playback.
// Returns SessionHostID if set, otherwise falls back to HostID.
func getEffectiveHost(roomID string) string {
	roomMutex.RLock()
	defer roomMutex.RUnlock()

	room, exists := rooms[roomID]
	if !exists {
		return ""
	}
	if room.SessionHostID != "" {
		return room.SessionHostID
	}
	return room.HostID
}

// isEffectiveHost checks if user currently controls playback.
func isEffectiveHost(roomID, userID string) bool {
	return getEffectiveHost(roomID) == userID
}

// isRoomOwner checks if user is the permanent room owner (can reclaim control).
func isRoomOwner(roomID, userID string) bool {
	roomMutex.RLock()
	defer roomMutex.RUnlock()

	room, exists := rooms[roomID]
	if !exists {
		return false
	}
	return room.HostID == userID
}

// transferHost transfers host privileges to another user.
func transferHost(roomID, newHostID string) error {
	roomMutex.Lock()
	defer roomMutex.Unlock()

	room, exists := rooms[roomID]
	if !exists {
		return fmt.Errorf("room not found")
	}

	// Check if new host is in the room
	if _, isParticipant := room.Participants[newHostID]; !isParticipant {
		return fmt.Errorf("user is not a participant in this room")
	}

	// Update roles
	oldHostID := room.HostID
	room.Participants[oldHostID] = "viewer"
	room.Participants[newHostID] = "host"
	room.HostID = newHostID

	return nil
}

// getRoomParticipants returns all participants in a room.
func getRoomParticipants(roomID string) []string {
	roomMutex.RLock()
	defer roomMutex.RUnlock()

	room, exists := rooms[roomID]
	if !exists {
		return []string{}
	}

	participants := make([]string, 0, len(room.Participants))
	for userID := range room.Participants {
		participants = append(participants, userID)
	}
	return participants
}

// cleanupEmptyRooms removes rooms with no participants.
func cleanupEmptyRooms() {
	roomMutex.Lock()
	defer roomMutex.Unlock()

	for roomID, room := range rooms {
		if len(room.Participants) == 0 || !room.IsActive {
			delete(rooms, roomID)
		}
	}
}
