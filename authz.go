package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

type RealtimeParticipant struct {
	UserID string `json:"userId"`
	Role   string `json:"role"`
}

type RealtimeRoomAccess struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
	Role    string `json:"role,omitempty"`
	Room    struct {
		ID           string                `json:"id"`
		Name         string                `json:"name"`
		HostID       string                `json:"hostId"`
		Participants []RealtimeParticipant `json:"participants"`
	} `json:"room"`
}

var authorizeRoomAccess = authorizeRoomAccessHTTP

func authorizeRoomAccessHTTP(roomID, userID string) (*RealtimeRoomAccess, error) {
	sveltekitURL := os.Getenv("SVELTEKIT_URL")
	apiSecret := os.Getenv("INTERNAL_API_SECRET")
	if sveltekitURL == "" || apiSecret == "" {
		return nil, fmt.Errorf("SVELTEKIT_URL and INTERNAL_API_SECRET are required for room authorization")
	}

	body, err := json.Marshal(map[string]string{"roomId": roomID, "userId": userID})
	if err != nil {
		return nil, err
	}

	endpoint := strings.TrimSuffix(sveltekitURL, "/") + "/api/internal/realtime/room-access"
	req, err := http.NewRequest("POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiSecret)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var access RealtimeRoomAccess
	if err := json.NewDecoder(resp.Body).Decode(&access); err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK || !access.Allowed {
		if access.Reason == "" {
			access.Reason = fmt.Sprintf("authorization failed with status %d", resp.StatusCode)
		}
		return &access, errors.New(access.Reason)
	}

	return &access, nil
}

func upsertAuthorizedRoom(access *RealtimeRoomAccess) {
	roomMutex.Lock()
	defer roomMutex.Unlock()

	existingStreamerID := ""
	existingStreamMode := ""
	existingStreamerPeerID := ""
	existingStreamMetadata := StreamMetadata{}
	currentVideo := SyncData{}
	queue := make([]QueueItem, 0)
	mutedUsers := make(map[string]MuteInfo)
	var activeProposal *MovieProposal
	if existingRoom, exists := rooms[access.Room.ID]; exists {
		existingStreamerID = existingRoom.CurrentStreamerID
		existingStreamMode = existingRoom.CurrentStreamMode
		existingStreamerPeerID = existingRoom.CurrentStreamerPeerID
		existingStreamMetadata = existingRoom.StreamMetadata
		currentVideo = existingRoom.CurrentVideo
		queue = existingRoom.Queue
		mutedUsers = existingRoom.MutedUsers
		activeProposal = existingRoom.ActiveProposal
	}

	room := &Room{
		ID:                    access.Room.ID,
		HostID:                access.Room.HostID,
		Name:                  access.Room.Name,
		Participants:          make(map[string]string),
		Presence:              make(map[string]string),
		CinemaAvatars:         make(map[string]json.RawMessage),
		WebcamParticipants:    make(map[string]WebcamStateParticipant),
		FaceModeParticipants:  make(map[string]bool),
		Queue:                 queue,
		MutedUsers:            mutedUsers,
		ActiveProposal:        activeProposal,
		IsActive:              true,
		CreatedAt:             time.Now(),
		CurrentVideo:          currentVideo,
		CurrentStreamerID:     existingStreamerID,
		CurrentStreamMode:     existingStreamMode,
		CurrentStreamerPeerID: existingStreamerPeerID,
		StreamMetadata:        existingStreamMetadata,
	}

	for _, participant := range access.Room.Participants {
		room.Participants[participant.UserID] = participant.Role
		room.Presence[participant.UserID] = "active"
	}

	rooms[access.Room.ID] = room
}
