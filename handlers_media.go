package main

import (
	"encoding/json"
	"fmt"
	"log"
	"time"
)

// handleVideoReaction broadcasts emoji reactions with timestamp sync
func handleVideoReaction(client *Client, message Message) {
	var reactionData struct {
		RoomID         string  `json:"roomId"`
		UserID         string  `json:"userId"`
		UserName       string  `json:"userName"`
		Emoji          string  `json:"emoji"`
		VideoTimestamp float64 `json:"videoTimestamp"`
		Timestamp      string  `json:"timestamp"`
		ReactionID     string  `json:"reactionId"`
	}

	if err := json.Unmarshal(message.Data, &reactionData); err != nil {
		log.Printf("Error parsing reaction data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid reaction data format")
		return
	}

	// Validate user is in the room
	if !client.isInRoom(reactionData.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room to send reactions")
		return
	}

	// Validate emoji (basic check for common reactions)
	validEmojis := map[string]bool{
		"😂": true, "❤️": true, "😮": true, "👏": true, "😢": true,
		"🔥": true, "💯": true, "👍": true, "👎": true, "😍": true,
	}

	if !validEmojis[reactionData.Emoji] {
		sendErrorResponse(client, "INVALID_EMOJI", "Invalid emoji for reactions")
		return
	}

	// Broadcast reaction to all room participants including sender (they want to see their own reaction)
	broadcastToRoom(reactionData.RoomID, "video_reaction", reactionData)
}

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

	// Create the queue item
	item := QueueItem{
		ID:         fmt.Sprintf("qi_%d", time.Now().UnixNano()),
		VideoURL:   data.VideoURL,
		VideoTitle: data.VideoTitle,
		AddedBy:    data.AddedBy,
		AddedAt:    time.Now().UnixMilli(),
	}

	// Add to room queue
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

	// Broadcast updated queue to all participants
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

	// Find and remove the item
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

	// Broadcast updated queue
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

	// Validate host permission for reordering
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

	// Find the item
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

	// Remove from old position
	room.Queue = append(room.Queue[:itemIndex], room.Queue[itemIndex+1:]...)

	// Insert at new position
	newIndex := data.NewIndex
	if newIndex < 0 {
		newIndex = 0
	}
	if newIndex > len(room.Queue) {
		newIndex = len(room.Queue)
	}

	// Insert at new position
	room.Queue = append(room.Queue[:newIndex], append([]QueueItem{item}, room.Queue[newIndex:]...)...)

	queueCopy := make([]QueueItem, len(room.Queue))
	copy(queueCopy, room.Queue)
	roomMutex.Unlock()

	// Broadcast updated queue
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

	// Validate host permission
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

	// Pop the first item
	nextItem := room.Queue[0]
	room.Queue = room.Queue[1:]
	queueCopy := make([]QueueItem, len(room.Queue))
	copy(queueCopy, room.Queue)
	roomMutex.Unlock()

	// Broadcast autoplay event for the next item
	broadcastToRoom(data.RoomID, "queue_autoplay", QueueAutoplayData{
		RoomID: data.RoomID,
		Item:   nextItem,
	})

	// Broadcast updated queue
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

	// Validate host permission
	if err := validateHostPermission(data.RoomID, client.userID, "start countdown"); err != nil {
		sendErrorResponse(client, "PERMISSION_DENIED", "Only host can start countdown")
		return
	}

	// Broadcast countdown to all participants
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

	// Validate host permission
	if err := validateHostPermission(data.RoomID, client.userID, "autoplay"); err != nil {
		sendErrorResponse(client, "PERMISSION_DENIED", "Only host can trigger autoplay")
		return
	}

	// Broadcast to room
	broadcastToRoom(data.RoomID, "queue_autoplay", data)

	logRealtime("queue_autoplay", map[string]interface{}{
		"roomId": data.RoomID,
		"userId": client.userID,
		"itemId": data.Item.ID,
	})
}

// handleMoviePropose creates a new movie proposal for the room
func handleMoviePropose(client *Client, message Message) {
	var data MovieProposeData
	if err := json.Unmarshal(message.Data, &data); err != nil {
		log.Printf("Error parsing movie propose data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid movie propose data")
		return
	}

	if !client.isInRoom(data.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room to propose a movie")
		return
	}

	roomMutex.Lock()
	room, exists := rooms[data.RoomID]
	if !exists {
		roomMutex.Unlock()
		sendErrorResponse(client, "ROOM_NOT_FOUND", "Room not found")
		return
	}

	// Check if there's already an active proposal
	if room.ActiveProposal != nil && room.ActiveProposal.Status == "pending" {
		roomMutex.Unlock()
		sendErrorResponse(client, "PROPOSAL_EXISTS", "A proposal is already in progress")
		return
	}

	// Create the proposal
	proposalID := fmt.Sprintf("mp_%d", time.Now().UnixNano())
	expiresAt := time.Now().Add(60 * time.Second)

	proposal := &MovieProposal{
		ID:     proposalID,
		RoomID: data.RoomID,
		Movie: MovieProposalMovie{
			ID:         data.MovieID,
			TmdbID:     data.MovieID, // Client sends movieId which is the DB id
			Title:      data.MovieTitle,
			PosterPath: data.PosterPath,
		},
		Proposer:  data.Proposer,
		VotesUp:   []MovieProposalUser{},
		VotesDown: []MovieProposalUser{},
		Status:    "pending",
		ExpiresAt: expiresAt.Format(time.RFC3339),
		CreatedAt: time.Now().Format(time.RFC3339),
	}

	room.ActiveProposal = proposal
	roomMutex.Unlock()

	// Broadcast to all room participants
	broadcastToRoom(data.RoomID, "movie_proposed", MovieProposedData{
		RoomID:     data.RoomID,
		ProposalID: proposalID,
		Movie:      proposal.Movie,
		Proposer:   proposal.Proposer,
		ExpiresAt:  proposal.ExpiresAt,
	})

	// Schedule expiration check
	go func() {
		time.Sleep(60 * time.Second)
		handleProposalExpiration(data.RoomID, proposalID)
	}()

	logRealtime("movie_propose", map[string]interface{}{
		"roomId":     data.RoomID,
		"proposalId": proposalID,
		"movieTitle": data.MovieTitle,
		"proposerId": data.Proposer.ID,
	})
}

// handleProposalExpiration checks and expires a proposal after timeout
func handleProposalExpiration(roomID, proposalID string) {
	roomMutex.Lock()
	room, exists := rooms[roomID]
	if !exists {
		roomMutex.Unlock()
		return
	}

	proposal := room.ActiveProposal
	if proposal == nil || proposal.ID != proposalID || proposal.Status != "pending" {
		roomMutex.Unlock()
		return
	}

	// Check if majority voted down
	totalVotes := len(proposal.VotesUp) + len(proposal.VotesDown)
	reason := "timeout"
	if totalVotes > 0 && len(proposal.VotesDown) > len(proposal.VotesUp) {
		reason = "majority_down"
	}

	proposal.Status = "rejected"
	roomMutex.Unlock()

	// Broadcast rejection
	broadcastToRoom(roomID, "movie_rejected", MovieRejectedData{
		RoomID:     roomID,
		ProposalID: proposalID,
		Reason:     reason,
	})

	logRealtime("movie_expired", map[string]interface{}{
		"roomId":     roomID,
		"proposalId": proposalID,
		"reason":     reason,
	})
}

// handleMovieVote records a vote on an active movie proposal
func handleMovieVote(client *Client, message Message) {
	var data MovieVoteData
	if err := json.Unmarshal(message.Data, &data); err != nil {
		log.Printf("Error parsing movie vote data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid movie vote data")
		return
	}

	if !client.isInRoom(data.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room to vote")
		return
	}

	roomMutex.Lock()
	room, exists := rooms[data.RoomID]
	if !exists {
		roomMutex.Unlock()
		sendErrorResponse(client, "ROOM_NOT_FOUND", "Room not found")
		return
	}

	proposal := room.ActiveProposal
	if proposal == nil || proposal.ID != data.ProposalID {
		roomMutex.Unlock()
		sendErrorResponse(client, "NO_PROPOSAL", "No active proposal with that ID")
		return
	}

	if proposal.Status != "pending" {
		roomMutex.Unlock()
		sendErrorResponse(client, "PROPOSAL_CLOSED", "Proposal is no longer active")
		return
	}

	// Remove any existing vote from this user
	voterID := data.Voter.ID
	newVotesUp := make([]MovieProposalUser, 0, len(proposal.VotesUp))
	for _, v := range proposal.VotesUp {
		if v.ID != voterID {
			newVotesUp = append(newVotesUp, v)
		}
	}
	newVotesDown := make([]MovieProposalUser, 0, len(proposal.VotesDown))
	for _, v := range proposal.VotesDown {
		if v.ID != voterID {
			newVotesDown = append(newVotesDown, v)
		}
	}

	// Add the new vote
	if data.Vote == "up" {
		newVotesUp = append(newVotesUp, data.Voter)
	} else {
		newVotesDown = append(newVotesDown, data.Voter)
	}

	proposal.VotesUp = newVotesUp
	proposal.VotesDown = newVotesDown

	// Make copies for broadcast
	votesUpCopy := make([]MovieProposalUser, len(proposal.VotesUp))
	copy(votesUpCopy, proposal.VotesUp)
	votesDownCopy := make([]MovieProposalUser, len(proposal.VotesDown))
	copy(votesDownCopy, proposal.VotesDown)
	roomMutex.Unlock()

	// Broadcast vote update
	broadcastToRoom(data.RoomID, "movie_vote_update", MovieVoteUpdateData{
		RoomID:     data.RoomID,
		ProposalID: data.ProposalID,
		Votes: struct {
			Up   []MovieProposalUser `json:"up"`
			Down []MovieProposalUser `json:"down"`
		}{
			Up:   votesUpCopy,
			Down: votesDownCopy,
		},
	})

	logRealtime("movie_vote", map[string]interface{}{
		"roomId":     data.RoomID,
		"proposalId": data.ProposalID,
		"voterId":    voterID,
		"vote":       data.Vote,
	})
}

// handleMovieApprove approves a movie proposal (host only)
func handleMovieApprove(client *Client, message Message) {
	var data MovieApproveData
	if err := json.Unmarshal(message.Data, &data); err != nil {
		log.Printf("Error parsing movie approve data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid movie approve data")
		return
	}

	if !client.isInRoom(data.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room")
		return
	}

	// Validate host permission
	if err := validateHostPermission(data.RoomID, client.userID, "approve movie"); err != nil {
		sendErrorResponse(client, "PERMISSION_DENIED", "Only host can approve movies")
		return
	}

	roomMutex.Lock()
	room, exists := rooms[data.RoomID]
	if !exists {
		roomMutex.Unlock()
		sendErrorResponse(client, "ROOM_NOT_FOUND", "Room not found")
		return
	}

	proposal := room.ActiveProposal
	if proposal == nil || proposal.ID != data.ProposalID {
		roomMutex.Unlock()
		sendErrorResponse(client, "NO_PROPOSAL", "No active proposal with that ID")
		return
	}

	if proposal.Status != "pending" {
		roomMutex.Unlock()
		sendErrorResponse(client, "PROPOSAL_CLOSED", "Proposal is no longer active")
		return
	}

	proposal.Status = "approved"
	movieCopy := proposal.Movie
	roomMutex.Unlock()

	// Broadcast approval
	broadcastToRoom(data.RoomID, "movie_approved", MovieApprovedData{
		RoomID:     data.RoomID,
		ProposalID: data.ProposalID,
		Movie:      movieCopy,
		ApprovedBy: data.Approver,
	})

	logRealtime("movie_approved", map[string]interface{}{
		"roomId":     data.RoomID,
		"proposalId": data.ProposalID,
		"movieTitle": movieCopy.Title,
		"approvedBy": data.Approver.ID,
	})
}

// handleMovieReject rejects a movie proposal (host only)
func handleMovieReject(client *Client, message Message) {
	var data MovieRejectData
	if err := json.Unmarshal(message.Data, &data); err != nil {
		log.Printf("Error parsing movie reject data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid movie reject data")
		return
	}

	if !client.isInRoom(data.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room")
		return
	}

	// Validate host permission
	if err := validateHostPermission(data.RoomID, client.userID, "reject movie"); err != nil {
		sendErrorResponse(client, "PERMISSION_DENIED", "Only host can reject movies")
		return
	}

	roomMutex.Lock()
	room, exists := rooms[data.RoomID]
	if !exists {
		roomMutex.Unlock()
		sendErrorResponse(client, "ROOM_NOT_FOUND", "Room not found")
		return
	}

	proposal := room.ActiveProposal
	if proposal == nil || proposal.ID != data.ProposalID {
		roomMutex.Unlock()
		sendErrorResponse(client, "NO_PROPOSAL", "No active proposal with that ID")
		return
	}

	if proposal.Status != "pending" {
		roomMutex.Unlock()
		sendErrorResponse(client, "PROPOSAL_CLOSED", "Proposal is no longer active")
		return
	}

	proposal.Status = "rejected"
	roomMutex.Unlock()

	// Broadcast rejection
	broadcastToRoom(data.RoomID, "movie_rejected", MovieRejectedData{
		RoomID:     data.RoomID,
		ProposalID: data.ProposalID,
		Reason:     "host",
	})

	logRealtime("movie_rejected", map[string]interface{}{
		"roomId":     data.RoomID,
		"proposalId": data.ProposalID,
		"reason":     "host",
	})
}
