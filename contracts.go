package main

import "encoding/json"

const WebSocketContractVersion = 2

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

type CinemaAnimationData struct {
	RoomID    string `json:"roomId"`
	UserID    string `json:"userId"`
	UserName  string `json:"userName"`
	Animation string `json:"animation"`
}

type CinemaMoodChangedData struct {
	RoomID     string  `json:"roomId"`
	UserID     string  `json:"userId"`
	UserName   string  `json:"userName"`
	Color      string  `json:"color"`
	Brightness float64 `json:"brightness"`
	Timestamp  string  `json:"timestamp,omitempty"`
}

type CinemaRoomThemeChangedData struct {
	RoomID    string `json:"roomId"`
	UserID    string `json:"userId"`
	UserName  string `json:"userName"`
	ThemeID   string `json:"themeId"`
	Timestamp string `json:"timestamp,omitempty"`
}

// Face mode - webcam mapped onto avatar face
type FaceModeData struct {
	RoomID  string `json:"roomId"`
	UserID  string `json:"userId"`
	Enabled bool   `json:"enabled"`
}

type FaceModeParticipant struct {
	UserID  string `json:"userId"`
	Enabled bool   `json:"enabled"`
}

type FaceModeStateResponse struct {
	RoomID       string                `json:"roomId"`
	Participants []FaceModeParticipant `json:"participants"`
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

// Queue types for video playlist management

type QueueItemAddedBy struct {
	UserID   string `json:"userId"`
	UserName string `json:"userName"`
}

type QueueItem struct {
	ID         string           `json:"id"`
	VideoURL   string           `json:"videoUrl"`
	VideoTitle string           `json:"videoTitle,omitempty"`
	AddedBy    QueueItemAddedBy `json:"addedBy"`
	AddedAt    int64            `json:"addedAt"`
}

type QueueAddData struct {
	RoomID     string           `json:"roomId"`
	VideoURL   string           `json:"videoUrl"`
	VideoTitle string           `json:"videoTitle,omitempty"`
	AddedBy    QueueItemAddedBy `json:"addedBy"`
}

type QueueRemoveData struct {
	RoomID      string `json:"roomId"`
	QueueItemID string `json:"queueItemId"`
}

type QueueReorderData struct {
	RoomID      string `json:"roomId"`
	QueueItemID string `json:"queueItemId"`
	NewIndex    int    `json:"newIndex"`
}

type QueueUpdatedData struct {
	RoomID string      `json:"roomId"`
	Queue  []QueueItem `json:"queue"`
}

type QueueNextData struct {
	RoomID string `json:"roomId"`
}

type QueueCountdownData struct {
	RoomID           string    `json:"roomId"`
	SecondsRemaining int       `json:"secondsRemaining"`
	NextItem         QueueItem `json:"nextItem"`
}

type QueueAutoplayData struct {
	RoomID string    `json:"roomId"`
	Item   QueueItem `json:"item"`
}

// Moderation types for role changes and chat control

type RoleChangedData struct {
	RoomID    string `json:"roomId"`
	UserID    string `json:"userId"`
	NewRole   string `json:"newRole"`
	ChangedBy string `json:"changedBy"`
}

type ChatMuteData struct {
	RoomID          string `json:"roomId"`
	TargetUserID    string `json:"targetUserId"`
	DurationMinutes int    `json:"durationMinutes"`
	Reason          string `json:"reason,omitempty"`
}

type ChatUnmuteData struct {
	RoomID       string `json:"roomId"`
	TargetUserID string `json:"targetUserId"`
}

type UserMutedData struct {
	RoomID    string `json:"roomId"`
	UserID    string `json:"userId"`
	ExpiresAt string `json:"expiresAt"`
	MutedBy   string `json:"mutedBy"`
	Reason    string `json:"reason,omitempty"`
}

type UserUnmutedData struct {
	RoomID string `json:"roomId"`
	UserID string `json:"userId"`
}

type MuteStatusData struct {
	RoomID    string `json:"roomId"`
	IsMuted   bool   `json:"isMuted"`
	ExpiresAt string `json:"expiresAt,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type DeleteMessageData struct {
	RoomID    string `json:"roomId"`
	MessageID string `json:"messageId"`
}

type MessageDeletedData struct {
	RoomID    string `json:"roomId"`
	MessageID string `json:"messageId"`
	DeletedBy string `json:"deletedBy"`
}

// Movie proposal types for collaborative movie selection

type MovieProposalUser struct {
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	Image *string `json:"image"`
}

type MovieProposalMovie struct {
	ID          string  `json:"id"`
	TmdbID      string  `json:"tmdbId"`
	Title       string  `json:"title"`
	PosterPath  *string `json:"posterPath"`
	ReleaseDate *string `json:"releaseDate"`
	VoteAverage *string `json:"voteAverage"`
}

type MovieProposal struct {
	ID        string                 `json:"id"`
	RoomID    string                 `json:"roomId"`
	Movie     MovieProposalMovie     `json:"movie"`
	Proposer  MovieProposalUser      `json:"proposer"`
	VotesUp   []MovieProposalUser    `json:"votesUp"`
	VotesDown []MovieProposalUser    `json:"votesDown"`
	Status    string                 `json:"status"` // pending, approved, rejected
	ExpiresAt string                 `json:"expiresAt"`
	CreatedAt string                 `json:"createdAt"`
}

// Client → Server

type MovieProposeData struct {
	RoomID     string            `json:"roomId"`
	MovieID    string            `json:"movieId"`
	MovieTitle string            `json:"movieTitle"`
	PosterPath *string           `json:"posterPath"`
	Proposer   MovieProposalUser `json:"proposer"`
}

type MovieVoteData struct {
	RoomID     string            `json:"roomId"`
	ProposalID string            `json:"proposalId"`
	Vote       string            `json:"vote"` // "up" or "down"
	Voter      MovieProposalUser `json:"voter"`
}

type MovieApproveData struct {
	RoomID     string            `json:"roomId"`
	ProposalID string            `json:"proposalId"`
	Approver   MovieProposalUser `json:"approver"`
}

type MovieRejectData struct {
	RoomID     string `json:"roomId"`
	ProposalID string `json:"proposalId"`
}

// Server → Clients

type MovieProposedData struct {
	RoomID     string             `json:"roomId"`
	ProposalID string             `json:"proposalId"`
	Movie      MovieProposalMovie `json:"movie"`
	Proposer   MovieProposalUser  `json:"proposer"`
	ExpiresAt  string             `json:"expiresAt"`
}

type MovieVoteUpdateData struct {
	RoomID     string `json:"roomId"`
	ProposalID string `json:"proposalId"`
	Votes      struct {
		Up   []MovieProposalUser `json:"up"`
		Down []MovieProposalUser `json:"down"`
	} `json:"votes"`
}

type MovieApprovedData struct {
	RoomID     string             `json:"roomId"`
	ProposalID string             `json:"proposalId"`
	Movie      MovieProposalMovie `json:"movie"`
	ApprovedBy MovieProposalUser  `json:"approvedBy"`
}

type MovieRejectedData struct {
	RoomID     string `json:"roomId"`
	ProposalID string `json:"proposalId"`
	Reason     string `json:"reason"` // "host", "timeout", "majority_down"
}

// Stream mode types for FIFO streaming (first-to-stream gets exclusive access)

type StreamModeChangedData struct {
	RoomID string `json:"roomId"`
	UserID string `json:"userId"`
	Mode   string `json:"mode"` // "none", "screen", "camera", "file", "url"
	Reason string `json:"reason,omitempty"` // "disconnect" when streamer leaves unexpectedly
}

type StreamStatusResponse struct {
	RoomID            string `json:"roomId"`
	CurrentStreamerID string `json:"currentStreamerId,omitempty"`
	CurrentStreamMode string `json:"currentStreamMode,omitempty"`
}

// Session control types for temporary playback delegation

type TransferSessionControlData struct {
	RoomID      string `json:"roomId"`
	NewHostID   string `json:"newHostId"`
	NewHostName string `json:"newHostName"`
}

type ReclaimSessionControlData struct {
	RoomID string `json:"roomId"`
}

type SessionControlChangedData struct {
	RoomID          string `json:"roomId"`
	SessionHostID   string `json:"sessionHostId"`   // "" means owner has control
	SessionHostName string `json:"sessionHostName"` // "" when owner has control
	OwnerID         string `json:"ownerId"`         // permanent room owner
}

// Room lock types for access control

type LockChangedData struct {
	RoomID    string `json:"roomId"`
	LockState string `json:"lockState"` // "open", "invite-only", "locked"
	ChangedBy string `json:"changedBy"`
}

// Ban types for user moderation

type BanUserData struct {
	RoomID          string `json:"roomId"`
	TargetUserID    string `json:"targetUserId"`
	TargetUserName  string `json:"targetUserName"`
	BanType         string `json:"banType"` // "session", "timed", "permanent"
	DurationMinutes int    `json:"durationMinutes,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

type UnbanUserData struct {
	RoomID       string `json:"roomId"`
	TargetUserID string `json:"targetUserId"`
}

type ParticipantBannedData struct {
	RoomID   string `json:"roomId"`
	UserID   string `json:"userId"`
	UserName string `json:"userName"`
	BanType  string `json:"banType"`
	Reason   string `json:"reason,omitempty"`
	BannedBy string `json:"bannedBy"`
}

type ParticipantUnbannedData struct {
	RoomID     string `json:"roomId"`
	UserID     string `json:"userId"`
	UnbannedBy string `json:"unbannedBy"`
}

type YouBannedData struct {
	RoomID    string `json:"roomId"`
	RoomName  string `json:"roomName"`
	BanType   string `json:"banType"`
	Reason    string `json:"reason,omitempty"`
	ExpiresAt string `json:"expiresAt,omitempty"`
}
