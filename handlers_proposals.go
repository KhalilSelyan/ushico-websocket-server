package main

import (
	"encoding/json"
	"fmt"
	"log"
	"time"
)

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

	if room.ActiveProposal != nil && room.ActiveProposal.Status == "pending" {
		roomMutex.Unlock()
		sendErrorResponse(client, "PROPOSAL_EXISTS", "A proposal is already in progress")
		return
	}

	proposalID := fmt.Sprintf("mp_%d", time.Now().UnixNano())
	expiresAt := time.Now().Add(60 * time.Second)

	proposal := &MovieProposal{
		ID:     proposalID,
		RoomID: data.RoomID,
		Movie: MovieProposalMovie{
			ID:         data.MovieID,
			TmdbID:     data.MovieID,
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

	broadcastToRoom(data.RoomID, "movie_proposed", MovieProposedData{
		RoomID:     data.RoomID,
		ProposalID: proposalID,
		Movie:      proposal.Movie,
		Proposer:   proposal.Proposer,
		ExpiresAt:  proposal.ExpiresAt,
	})

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

	totalVotes := len(proposal.VotesUp) + len(proposal.VotesDown)
	reason := "timeout"
	if totalVotes > 0 && len(proposal.VotesDown) > len(proposal.VotesUp) {
		reason = "majority_down"
	}

	proposal.Status = "rejected"
	roomMutex.Unlock()

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

	if data.Vote == "up" {
		newVotesUp = append(newVotesUp, data.Voter)
	} else {
		newVotesDown = append(newVotesDown, data.Voter)
	}

	proposal.VotesUp = newVotesUp
	proposal.VotesDown = newVotesDown

	votesUpCopy := make([]MovieProposalUser, len(proposal.VotesUp))
	copy(votesUpCopy, proposal.VotesUp)
	votesDownCopy := make([]MovieProposalUser, len(proposal.VotesDown))
	copy(votesDownCopy, proposal.VotesDown)
	roomMutex.Unlock()

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
