package main

import "encoding/json"

const WebSocketContractVersion = 1

type RoomVideoStateResponse struct {
	RoomID string    `json:"roomId"`
	Sync   *SyncData `json:"sync"`
}

type PresenceUpdateData struct {
	RoomID        string `json:"roomId"`
	UserID        string `json:"userId"`
	UserName      string `json:"userName"`
	PresenceState string `json:"presenceState"`
	Timestamp     string `json:"timestamp"`
}

type RoomPresenceParticipant struct {
	UserID        string `json:"userId"`
	UserName      string `json:"userName"`
	PresenceState string `json:"presenceState"`
}

type RoomPresenceResponse struct {
	RoomID       string                    `json:"roomId"`
	Participants []RoomPresenceParticipant `json:"participants"`
}

type WebcamJoinData struct {
	RoomID       string `json:"roomId"`
	UserID       string `json:"userId"`
	UserName     string `json:"userName"`
	UserImage    string `json:"userImage,omitempty"`
	VideoEnabled bool   `json:"videoEnabled"`
	AudioEnabled bool   `json:"audioEnabled"`
}

type WebcamLeaveData struct {
	RoomID string `json:"roomId"`
	UserID string `json:"userId"`
}

type WebcamToggleData struct {
	RoomID  string `json:"roomId"`
	UserID  string `json:"userId"`
	Type    string `json:"type"`
	Enabled bool   `json:"enabled"`
}

type WebcamStateParticipant struct {
	UserID       string `json:"userId"`
	UserName     string `json:"userName"`
	UserImage    string `json:"userImage,omitempty"`
	VideoEnabled bool   `json:"videoEnabled"`
	AudioEnabled bool   `json:"audioEnabled"`
}

type WebcamStateResponse struct {
	RoomID       string                   `json:"roomId"`
	Participants []WebcamStateParticipant `json:"participants"`
}

type CinemaAvatarData struct {
	RoomID      string  `json:"roomId"`
	UserID      string  `json:"userId"`
	UserName    string  `json:"userName"`
	PX          float64 `json:"px"`
	PY          float64 `json:"py"`
	PZ          float64 `json:"pz"`
	RY          float64 `json:"ry"`
	Anim        string  `json:"anim"`
	AvatarModel string  `json:"avatarModel,omitempty"`
}

type CinemaAvatarStateResponse struct {
	RoomID  string             `json:"roomId"`
	Avatars []CinemaAvatarData `json:"avatars"`
}

func decodeCinemaAvatarStates(raw []json.RawMessage) []CinemaAvatarData {
	avatars := make([]CinemaAvatarData, 0, len(raw))
	for _, state := range raw {
		var avatar CinemaAvatarData
		if err := json.Unmarshal(state, &avatar); err == nil {
			avatars = append(avatars, avatar)
		}
	}
	return avatars
}
