package main

import (
	"encoding/json"
	"log"
)

// handleCinemaAvatarUpdate broadcasts avatar position to room and stores for late joiners
func handleCinemaAvatarUpdate(client *Client, message Message) {
	var avatarData CinemaAvatarData

	if err := json.Unmarshal(message.Data, &avatarData); err != nil {
		return
	}

	if !client.isInRoom(avatarData.RoomID) {
		return
	}
	avatarData.UserID = client.userID
	message.Data, _ = json.Marshal(avatarData)

	// Store last known state for late joiners
	roomMutex.Lock()
	if room, exists := rooms[avatarData.RoomID]; exists {
		if room.CinemaAvatars == nil {
			room.CinemaAvatars = make(map[string]json.RawMessage)
		}
		room.CinemaAvatars[client.userID] = message.Data
	}
	roomMutex.Unlock()

	broadcastToRoomExceptSender(avatarData.RoomID, client.userID, "cinema_avatar_update", message.Data)
	logRealtime("cinema_avatar_update", map[string]interface{}{
		"roomId": avatarData.RoomID,
		"userId": avatarData.UserID,
		"anim":   avatarData.Anim,
	})
}

// handleCinemaAnimation broadcasts animation emotes from a user to other room members
func handleCinemaAnimation(client *Client, message Message) {
	var animData CinemaAnimationData
	if err := json.Unmarshal(message.Data, &animData); err != nil {
		return
	}
	if !client.isInRoom(animData.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room to send cinema animations")
		return
	}
	animData.UserID = client.userID

	broadcastToRoomExceptSender(animData.RoomID, client.userID, "cinema_animation", animData)
}

// handleCinemaMoodChanged broadcasts mood lighting changes to the room
func handleCinemaMoodChanged(client *Client, message Message) {
	var moodData CinemaMoodChangedData
	if err := json.Unmarshal(message.Data, &moodData); err != nil {
		return
	}
	if !client.isInRoom(moodData.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room to change cinema mood")
		return
	}
	moodData.UserID = client.userID

	broadcastToRoomExceptSender(moodData.RoomID, client.userID, "cinema_mood_changed", moodData)
}

// handleCinemaRoomThemeChanged broadcasts room theme preset changes to the room
func handleCinemaRoomThemeChanged(client *Client, message Message) {
	var themeData CinemaRoomThemeChangedData
	if err := json.Unmarshal(message.Data, &themeData); err != nil {
		return
	}
	if !client.isInRoom(themeData.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room to change cinema theme")
		return
	}
	themeData.UserID = client.userID

	broadcastToRoomExceptSender(themeData.RoomID, client.userID, "cinema_room_theme_changed", themeData)
}

// handleFaceModeToggle handles when a user toggles face mode (webcam on avatar face)
func handleFaceModeToggle(client *Client, message Message) {
	var faceData FaceModeData

	if err := json.Unmarshal(message.Data, &faceData); err != nil {
		log.Printf("Error parsing face mode data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid face mode data format")
		return
	}

	// Validate user is in the room
	if !client.isInRoom(faceData.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room to toggle face mode")
		return
	}

	faceData.UserID = client.userID
	// Update room state
	roomMutex.Lock()
	if room, exists := rooms[faceData.RoomID]; exists {
		if room.FaceModeParticipants == nil {
			room.FaceModeParticipants = make(map[string]bool)
		}
		if faceData.Enabled {
			room.FaceModeParticipants[client.userID] = true
		} else {
			delete(room.FaceModeParticipants, client.userID)
		}
	}
	roomMutex.Unlock()

	logRealtime("face_mode_toggle", map[string]interface{}{
		"roomId":  faceData.RoomID,
		"userId":  client.userID,
		"enabled": faceData.Enabled,
	})

	// Broadcast to all room participants except sender
	broadcastToRoomExceptSender(faceData.RoomID, client.userID, "face_mode_toggle", faceData)
}

// handleGetFaceModeState sends face mode state to late joiners
func handleGetFaceModeState(client *Client, message Message) {
	var req struct {
		RoomID string `json:"roomId"`
	}

	if err := json.Unmarshal(message.Data, &req); err != nil {
		sendErrorResponse(client, "INVALID_DATA", "Invalid face mode state request")
		return
	}
	if !client.isInRoom(req.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room to request face mode state")
		return
	}

	roomMutex.RLock()
	participants := make([]FaceModeParticipant, 0)
	if room, exists := rooms[req.RoomID]; exists && room.FaceModeParticipants != nil {
		for userID, enabled := range room.FaceModeParticipants {
			if enabled {
				participants = append(participants, FaceModeParticipant{
					UserID:  userID,
					Enabled: true,
				})
			}
		}
	}
	roomMutex.RUnlock()

	response := FaceModeStateResponse{
		RoomID:       req.RoomID,
		Participants: participants,
	}

	broadcastToSpecificUser(client.userID, "face_mode_state", response)
}

// handleGetCinemaAvatars sends all current cinema avatar states to a client
func handleGetCinemaAvatars(client *Client, message Message) {
	var req struct {
		RoomID string `json:"roomId"`
	}

	if err := json.Unmarshal(message.Data, &req); err != nil {
		sendErrorResponse(client, "INVALID_DATA", "Invalid cinema avatars request")
		return
	}

	if !client.isInRoom(req.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room to request cinema avatar state")
		return
	}

	roomMutex.RLock()
	var avatars []json.RawMessage
	if room, exists := rooms[req.RoomID]; exists && room.CinemaAvatars != nil {
		for uid, state := range room.CinemaAvatars {
			if uid != client.userID {
				avatars = append(avatars, state)
			}
		}
	}
	roomMutex.RUnlock()

	if avatars == nil {
		avatars = []json.RawMessage{}
	}

	broadcastToSpecificUser(client.userID, "cinema_avatar_state", CinemaAvatarStateResponse{
		RoomID:  req.RoomID,
		Avatars: decodeCinemaAvatarStates(avatars),
	})
	logRealtime("cinema_avatar_state_snapshot", map[string]interface{}{
		"roomId":      req.RoomID,
		"userId":      client.userID,
		"avatarCount": len(avatars),
	})
}
