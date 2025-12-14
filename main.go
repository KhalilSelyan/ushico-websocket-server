package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Configure the WebSocket upgrader with buffer sizes and CORS policy.
var upgrader = websocket.Upgrader{
	CheckOrigin:     func(r *http.Request) bool { return true }, // Allow all origins
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

// Client represents a connected WebSocket client.
type Client struct {
	conn     *websocket.Conn   // The WebSocket connection.
	channels map[string]bool   // The channels the client is subscribed to.
	mu       sync.Mutex        // Mutex to protect the channels map.
	writeMu  sync.Mutex        // Mutex to protect WebSocket writes.
	userID   string            // User identifier for the client
	rooms    map[string]string // roomID -> role mapping
}

// Message represents the structure of messages sent over WebSocket.
type Message struct {
	Channel string          `json:"channel"` // The channel identifier.
	Event   string          `json:"event"`   // The event type (e.g., "sync", "subscribe").
	Data    json.RawMessage `json:"data"`    // The raw message data.
}

// SyncData represents the synchronization data for video playback.
type SyncData struct {
	Timestamp float64 `json:"timestamp"` // The current time of the video.
	URL       string  `json:"url"`       // The video URL.
	RoomID    string  `json:"roomId"`    // The room identifier (updated from ChatID).
	State     string  `json:"state"`     // The state of the video (playing/paused).
	VideoID   string  `json:"videoId"`   // UUID generated when URL changes for sync safety.
}

// Room represents a watch party room state (in-memory only).
type Room struct {
	ID           string            `json:"id"`
	HostID       string            `json:"hostId"`
	Name         string            `json:"name"`
	Participants map[string]string `json:"participants"` // userID -> role mapping
	Presence     map[string]string `json:"presence"`     // userID -> presence state (active, away, offline)
	IsActive     bool              `json:"isActive"`
	CreatedAt    time.Time         `json:"createdAt"`
	CurrentVideo SyncData          `json:"currentVideo"`
}

// RoomData for room creation/management events.
type RoomData struct {
	RoomID   string   `json:"roomId"`
	RoomName string   `json:"roomName,omitempty"`
	UserID   string   `json:"userId"`
	UserIDs  []string `json:"userIds,omitempty"` // for bulk operations
}

// ErrorResponse for client errors.
type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Code    string `json:"code"`
}

// Global variables for managing clients and messages.
var (
	clients    = make(map[*Client]bool) // Map of connected clients.
	broadcast  = make(chan Message)     // Channel for broadcasting messages.
	register   = make(chan *Client)     // Channel for registering new clients.
	unregister = make(chan *Client)     // Channel for unregistering clients.
	mutex      = &sync.RWMutex{}        // Read-write mutex to protect the clients map.
	rooms      = make(map[string]*Room) // Active rooms.
	roomMutex  = &sync.RWMutex{}        // Protect rooms map.
)

// Subscribe adds the client to the specified channel.
func (c *Client) subscribe(channel string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.channels[channel] = true
}

// Unsubscribe removes the client from the specified channel.
func (c *Client) unsubscribe(channel string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.channels, channel)
}

// isSubscribed checks if the client is subscribed to the specified channel.
func (c *Client) isSubscribed(channel string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.channels[channel]
}

// safeWriteJSON safely writes JSON to the WebSocket connection with proper locking.
func (c *Client) safeWriteJSON(v interface{}) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.WriteJSON(v)
}

// safeWriteControl safely writes control messages to the WebSocket connection with proper locking.
func (c *Client) safeWriteControl(messageType int, data []byte, deadline time.Time) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.WriteControl(messageType, data, deadline)
}

// validateChatID checks if the chat ID is in the expected format (e.g., "user1--user2").
func validateChatID(chatID string) bool {
	parts := strings.Split(chatID, "--")
	return len(parts) == 2 && len(parts[0]) > 0 && len(parts[1]) > 0
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
		ID:           roomID,
		HostID:       hostID,
		Name:         roomName,
		Participants: make(map[string]string),
		Presence:     make(map[string]string),
		IsActive:     true,
		CreatedAt:    time.Now(),
		CurrentVideo: SyncData{},
	}
	room.Participants[hostID] = "host"
	room.Presence[hostID] = "active"
	rooms[roomID] = room

	return room
}

// joinRoom adds a user to an existing room.
func joinRoom(roomID, userID string) error {
	roomMutex.Lock()
	defer roomMutex.Unlock()

	room, exists := rooms[roomID]
	if !exists {
		return fmt.Errorf("room not found")
	}
	if !room.IsActive {
		return fmt.Errorf("room is not active")
	}

	room.Participants[userID] = "viewer"
	room.Presence[userID] = "active" // User joins as active
	return nil
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
			// Room cleaned up
		}
	}
}

// Client room management methods
// joinRoom adds the client to a room with the specified role.
func (c *Client) joinRoomAsClient(roomID, role string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rooms == nil {
		c.rooms = make(map[string]string)
	}
	c.rooms[roomID] = role
}

// leaveRoomAsClient removes the client from a room.
func (c *Client) leaveRoomAsClient(roomID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.rooms, roomID)
}

// isInRoom checks if the client is in the specified room.
func (c *Client) isInRoom(roomID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, exists := c.rooms[roomID]
	return exists
}

// getRoleInRoom returns the client's role in the specified room.
func (c *Client) getRoleInRoom(roomID string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rooms[roomID]
}

// sendErrorResponse sends an error message back to the client.
func sendErrorResponse(client *Client, errorCode, message string) {
	errorResp := ErrorResponse{
		Error:   errorCode,
		Message: message,
		Code:    errorCode,
	}

	errorData, _ := json.Marshal(errorResp)
	errorMessage := Message{
		Channel: "error",
		Event:   "error_response",
		Data:    errorData,
	}

	if err := client.safeWriteJSON(errorMessage); err != nil {
		log.Printf("Error sending error response to client: %v", err)
	}
}

// validateHostPermission checks if user has host permission for the action.
func validateHostPermission(roomID, userID string, action string) error {
	if !isRoomHost(roomID, userID) {
		return fmt.Errorf("permission denied: only host can %s", action)
	}
	return nil
}

// Event handler functions
func handleCreateRoom(client *Client, message Message) {
	var roomData RoomData
	if err := json.Unmarshal(message.Data, &roomData); err != nil {
		log.Printf("Error parsing room data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid room data format")
		return
	}

	if client.userID == "" {
		sendErrorResponse(client, "NO_USER_ID", "User ID required to create room")
		return
	}

	room := createRoom(client.userID, roomData.RoomName)
	client.joinRoomAsClient(room.ID, "host")
	client.subscribe(fmt.Sprintf("room-%s", room.ID))

	// Broadcast room creation notification
	roomCreatedData, _ := json.Marshal(map[string]interface{}{
		"roomId":   room.ID,
		"roomName": room.Name,
		"hostId":   room.HostID,
	})

	responseMessage := Message{
		Channel: fmt.Sprintf("room-%s", room.ID),
		Event:   "room_created",
		Data:    roomCreatedData,
	}
	broadcast <- responseMessage

	// Room created successfully
}

func handleJoinRoom(client *Client, message Message) {
	var roomData RoomData
	if err := json.Unmarshal(message.Data, &roomData); err != nil {
		log.Printf("Error parsing room data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid room data format")
		return
	}

	if client.userID == "" {
		sendErrorResponse(client, "NO_USER_ID", "User ID required to join room")
		return
	}

	if err := joinRoom(roomData.RoomID, client.userID); err != nil {
		log.Printf("Error joining room: %v", err)
		sendErrorResponse(client, "JOIN_FAILED", err.Error())
		return
	}

	client.joinRoomAsClient(roomData.RoomID, "viewer")
	client.subscribe(fmt.Sprintf("room-%s", roomData.RoomID))

	// Broadcast participant joined notification
	joinedData, _ := json.Marshal(map[string]interface{}{
		"userId": client.userID,
		"role":   "viewer",
	})

	responseMessage := Message{
		Channel: fmt.Sprintf("room-%s", roomData.RoomID),
		Event:   "participant_joined",
		Data:    joinedData,
	}
	broadcast <- responseMessage

	// User joined room successfully
}

func handleLeaveRoom(client *Client, message Message) {
	var roomData RoomData
	if err := json.Unmarshal(message.Data, &roomData); err != nil {
		log.Printf("Error parsing room data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid room data format")
		return
	}

	if client.userID == "" {
		sendErrorResponse(client, "NO_USER_ID", "User ID required to leave room")
		return
	}

	// Check if user was host before leaving
	wasHost := isRoomHost(roomData.RoomID, client.userID)

	if err := leaveRoom(roomData.RoomID, client.userID); err != nil {
		log.Printf("Error leaving room: %v", err)
		sendErrorResponse(client, "LEAVE_FAILED", err.Error())
		return
	}

	client.leaveRoomAsClient(roomData.RoomID)
	client.unsubscribe(fmt.Sprintf("room-%s", roomData.RoomID))

	// Broadcast participant left notification
	leftData, _ := json.Marshal(map[string]interface{}{
		"userId":  client.userID,
		"wasHost": wasHost,
	})

	responseMessage := Message{
		Channel: fmt.Sprintf("room-%s", roomData.RoomID),
		Event:   "participant_left",
		Data:    leftData,
	}
	broadcast <- responseMessage

	// If host left and room still has participants, broadcast host transfer
	roomMutex.RLock()
	room, exists := rooms[roomData.RoomID]
	roomMutex.RUnlock()

	if exists && wasHost && len(room.Participants) > 0 {
		transferData, _ := json.Marshal(map[string]interface{}{
			"oldHostId": client.userID,
			"newHostId": room.HostID,
		})

		transferMessage := Message{
			Channel: fmt.Sprintf("room-%s", roomData.RoomID),
			Event:   "host_transferred",
			Data:    transferData,
		}
		broadcast <- transferMessage
	}

	// User left room successfully
}

func handleHostSync(client *Client, message Message) {
	// Extract room ID from channel
	roomID := strings.TrimPrefix(message.Channel, "room-")
	if !validateRoomID(roomID) {
		log.Printf("Invalid room ID format: %s", roomID)
		sendErrorResponse(client, "INVALID_ROOM_ID", "Invalid room ID format")
		return
	}

	// Validate host permission
	if err := validateHostPermission(roomID, client.userID, "control video playback"); err != nil {
		log.Printf("Permission denied for user %s in room %s: %v", client.userID, roomID, err)
		sendErrorResponse(client, "PERMISSION_DENIED", "Only host can control video playback")
		return
	}

	// Parse sync data
	var syncData SyncData
	if err := json.Unmarshal(message.Data, &syncData); err != nil {
		log.Printf("Error parsing sync data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid sync data format")
		return
	}

	// Update room's current video state
	roomMutex.Lock()
	if room, exists := rooms[roomID]; exists {
		room.CurrentVideo = syncData
		log.Printf("Room %s video sync: URL=%s, VideoID=%s, Time=%.2f, State=%s",
			roomID, syncData.URL, syncData.VideoID, syncData.Timestamp, syncData.State)
	}
	roomMutex.Unlock()

	// Broadcast the sync message to all room participants
	broadcast <- message
}

func handleTransferHost(client *Client, message Message) {
	var roomData RoomData
	if err := json.Unmarshal(message.Data, &roomData); err != nil {
		log.Printf("Error parsing room data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid room data format")
		return
	}

	// Validate current host permission
	if err := validateHostPermission(roomData.RoomID, client.userID, "transfer host"); err != nil {
		log.Printf("Permission denied for user %s: %v", client.userID, err)
		sendErrorResponse(client, "PERMISSION_DENIED", "Only current host can transfer host privileges")
		return
	}

	// Transfer host
	if err := transferHost(roomData.RoomID, roomData.UserID); err != nil {
		log.Printf("Error transferring host: %v", err)
		sendErrorResponse(client, "TRANSFER_FAILED", err.Error())
		return
	}

	// Update client roles
	client.leaveRoomAsClient(roomData.RoomID)
	client.joinRoomAsClient(roomData.RoomID, "viewer")

	// Broadcast host transfer notification
	transferData, _ := json.Marshal(map[string]interface{}{
		"oldHostId": client.userID,
		"newHostId": roomData.UserID,
	})

	responseMessage := Message{
		Channel: fmt.Sprintf("room-%s", roomData.RoomID),
		Event:   "host_transferred",
		Data:    transferData,
	}
	broadcast <- responseMessage

	// Host transferred successfully
}

func handleRoomMessage(client *Client, message Message) {
	// Extract room ID from channel
	roomID := strings.TrimPrefix(message.Channel, "room-")
	if !validateRoomID(roomID) {
		log.Printf("Invalid room ID format: %s", roomID)
		sendErrorResponse(client, "INVALID_ROOM_ID", "Invalid room ID format")
		return
	}

	// Check if user is in the room
	if !client.isInRoom(roomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "You must be in the room to send messages")
		return
	}

	// Parse the rich message data
	var messageData struct {
		RoomID      string `json:"roomId"`
		Text        string `json:"text"`
		SenderID    string `json:"senderId"`
		SenderName  string `json:"senderName"`
		SenderImage string `json:"senderImage"`
		Timestamp   string `json:"timestamp"`
		MessageID   string `json:"messageId"`
	}

	if err := json.Unmarshal(message.Data, &messageData); err != nil {
		log.Printf("Error parsing room message data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid message data format")
		return
	}

	// Validate sender ID matches the client
	if messageData.SenderID != client.userID {
		sendErrorResponse(client, "SENDER_MISMATCH", "Sender ID must match authenticated user")
		return
	}

	// Broadcast message to room participants

	broadcastToRoomExceptSender(roomID, client.userID, "room_message", messageData)
}

// broadcastToRoomExceptSender broadcasts a message to all room participants except the sender
func broadcastToRoomExceptSender(roomID, senderID, eventType string, data interface{}) {
	// Marshal the data
	jsonData, err := json.Marshal(data)
	if err != nil {
		log.Printf("Error marshaling broadcast data: %v", err)
		return
	}

	// Create the message
	broadcastMessage := Message{
		Channel: fmt.Sprintf("room-%s", roomID),
		Event:   eventType,
		Data:    jsonData,
	}

	// Send to all clients subscribed to this room channel, except the sender
	mutex.RLock()
	var clientsToRemove []*Client
	for client := range clients {
		if client.isSubscribed(fmt.Sprintf("room-%s", roomID)) && client.userID != senderID {
			err := client.safeWriteJSON(broadcastMessage)
			if err != nil {
				log.Printf("Error sending message to client %s: %v", client.userID, err)
				client.conn.Close()
				clientsToRemove = append(clientsToRemove, client)
			}
		}
	}
	mutex.RUnlock()

	// Remove clients that encountered errors
	if len(clientsToRemove) > 0 {
		mutex.Lock()
		for _, client := range clientsToRemove {
			delete(clients, client)
		}
		mutex.Unlock()
	}

	// Message broadcasted successfully
}

func handleLegacySync(client *Client, message Message) {
	// Legacy sync event

	// Extract chat ID and validate
	chatID := strings.TrimPrefix(message.Channel, "sync-")
	if !validateChatID(chatID) {
		log.Printf("Invalid chat ID format: %s", chatID)
		return
	}

	// Parse sync data
	var syncData SyncData
	if err := json.Unmarshal(message.Data, &syncData); err != nil {
		log.Printf("Error parsing sync data: %v", err)
		return
	}

	// Broadcast legacy sync

	// Broadcast the original message (maintain backward compatibility)
	broadcast <- message
}

func handleSyncRoomState(client *Client, message Message) {
	var roomData struct {
		RoomID       string `json:"roomId"`
		HostID       string `json:"hostId"`
		RoomName     string `json:"roomName"`
		Participants []struct {
			UserID string `json:"userId"`
			Role   string `json:"role"`
		} `json:"participants"`
	}

	if err := json.Unmarshal(message.Data, &roomData); err != nil {
		log.Printf("Error parsing room sync data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid room sync data format")
		return
	}

	if client.userID == "" {
		sendErrorResponse(client, "NO_USER_ID", "User ID required to sync room state")
		return
	}

	// Validate that the client is in the participants list
	var clientRole string
	var isParticipant bool
	for _, p := range roomData.Participants {
		if p.UserID == client.userID {
			clientRole = p.Role
			isParticipant = true
			break
		}
	}

	if !isParticipant {
		sendErrorResponse(client, "NOT_PARTICIPANT", "User is not a participant in this room")
		return
	}

	// Create or update room in WebSocket server's memory
	roomMutex.Lock()
	room := &Room{
		ID:           roomData.RoomID,
		HostID:       roomData.HostID,
		Name:         roomData.RoomName,
		Participants: make(map[string]string),
		Presence:     make(map[string]string),
		IsActive:     true,
		CreatedAt:    time.Now(),
		CurrentVideo: SyncData{},
	}

	// Add all participants
	for _, p := range roomData.Participants {
		room.Participants[p.UserID] = p.Role
		room.Presence[p.UserID] = "active" // Initialize as active when syncing
	}

	rooms[roomData.RoomID] = room
	roomMutex.Unlock()

	// Subscribe client to room with their role
	client.joinRoomAsClient(roomData.RoomID, clientRole)
	client.subscribe(fmt.Sprintf("room-%s", roomData.RoomID))

	// Send confirmation back to client
	confirmationData, _ := json.Marshal(map[string]interface{}{
		"roomId":   roomData.RoomID,
		"role":     clientRole,
		"synced":   true,
		"hostId":   roomData.HostID,
		"participants": len(roomData.Participants),
	})

	responseMessage := Message{
		Channel: fmt.Sprintf("room-%s", roomData.RoomID),
		Event:   "room_state_synced",
		Data:    confirmationData,
	}

	if err := client.safeWriteJSON(responseMessage); err != nil {
		log.Printf("Error sending room sync confirmation: %v", err)
	}

	// Room state synchronized
}

// handleRoomJoinRequest notifies the room host of a new join request
func handleRoomJoinRequest(client *Client, message Message) {
	var requestData struct {
		RoomID      string `json:"roomId"`
		RequesterID string `json:"requesterId"`
		RequestID   string `json:"requestId"`
		UserName    string `json:"userName"`
		UserImage   string `json:"userImage"`
	}

	if err := json.Unmarshal(message.Data, &requestData); err != nil {
		log.Printf("Error parsing join request data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid join request data format")
		return
	}

	// Validate that the room exists and get host ID
	roomMutex.RLock()
	room, exists := rooms[requestData.RoomID]
	roomMutex.RUnlock()

	if !exists {
		sendErrorResponse(client, "ROOM_NOT_FOUND", "Room not found")
		return
	}

	// Send notification to room host
	broadcastToSpecificUser(room.HostID, "room_join_request", requestData)
}

// handleJoinRequestApproved notifies the requester that their request was approved
func handleJoinRequestApproved(client *Client, message Message) {
	var approvalData struct {
		RequestID   string `json:"requestId"`
		RequesterID string `json:"requesterId"`
		RoomID      string `json:"roomId"`
		RoomName    string `json:"roomName"`
	}

	if err := json.Unmarshal(message.Data, &approvalData); err != nil {
		log.Printf("Error parsing join approval data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid join approval data format")
		return
	}

	// Send notification to the requester
	broadcastToSpecificUser(approvalData.RequesterID, "join_request_approved", approvalData)
}

// handleJoinRequestDenied notifies the requester that their request was denied
func handleJoinRequestDenied(client *Client, message Message) {
	var denialData struct {
		RequestID   string `json:"requestId"`
		RequesterID string `json:"requesterId"`
		RoomID      string `json:"roomId"`
		RoomName    string `json:"roomName"`
		Reason      string `json:"reason,omitempty"`
	}

	if err := json.Unmarshal(message.Data, &denialData); err != nil {
		log.Printf("Error parsing join denial data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid join denial data format")
		return
	}

	// Send notification to the requester
	broadcastToSpecificUser(denialData.RequesterID, "join_request_denied", denialData)
}

// handleRoomInvitation notifies a user of a room invitation
func handleRoomInvitation(client *Client, message Message) {
	var inviteData struct {
		InvitationID string `json:"invitationId"`
		InviteeID    string `json:"inviteeId"`
		InviterID    string `json:"inviterId"`
		InviterName  string `json:"inviterName"`
		RoomID       string `json:"roomId"`
		RoomName     string `json:"roomName"`
	}

	if err := json.Unmarshal(message.Data, &inviteData); err != nil {
		log.Printf("Error parsing room invitation data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid room invitation data format")
		return
	}

	// Send notification to the invitee
	broadcastToSpecificUser(inviteData.InviteeID, "room_invitation", inviteData)
}

// handleRoomDeactivated notifies all participants that the room has been ended
func handleRoomDeactivated(client *Client, message Message) {
	var deactivationData struct {
		RoomID   string `json:"roomId"`
		RoomName string `json:"roomName"`
		Reason   string `json:"reason,omitempty"`
	}

	if err := json.Unmarshal(message.Data, &deactivationData); err != nil {
		log.Printf("Error parsing room deactivation data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid room deactivation data format")
		return
	}

	// Get all room participants before deactivating
	participants := getRoomParticipants(deactivationData.RoomID)

	// Notify all participants
	for _, participantID := range participants {
		broadcastToSpecificUser(participantID, "room_deactivated", deactivationData)
	}

	// Deactivate the room in our state
	roomMutex.Lock()
	if room, exists := rooms[deactivationData.RoomID]; exists {
		room.IsActive = false
	}
	roomMutex.Unlock()
}

// handleParticipantKicked notifies a user that they were removed from the room
func handleParticipantKicked(client *Client, message Message) {
	var kickData struct {
		RoomID      string `json:"roomId"`
		RoomName    string `json:"roomName"`
		KickedID    string `json:"kickedId"`
		KickedName  string `json:"kickedName"`
		Reason      string `json:"reason,omitempty"`
	}

	if err := json.Unmarshal(message.Data, &kickData); err != nil {
		log.Printf("Error parsing participant kick data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid participant kick data format")
		return
	}

	// Remove user from room state
	if err := leaveRoom(kickData.RoomID, kickData.KickedID); err != nil {
		log.Printf("Error removing kicked user from room: %v", err)
	}

	// Send notification to the kicked user
	broadcastToSpecificUser(kickData.KickedID, "participant_kicked", kickData)

	// Notify other participants about the kick
	participants := getRoomParticipants(kickData.RoomID)
	kickNotification := map[string]interface{}{
		"userId":   kickData.KickedID,
		"userName": kickData.KickedName,
		"reason":   kickData.Reason,
	}

	for _, participantID := range participants {
		if participantID != kickData.KickedID {
			broadcastToSpecificUser(participantID, "participant_left", kickNotification)
		}
	}
}

// broadcastToSpecificUser sends a message to a specific user if they're connected
func broadcastToSpecificUser(userID, eventType string, data interface{}) {
	// Marshal the data
	jsonData, err := json.Marshal(data)
	if err != nil {
		log.Printf("Error marshaling user-specific broadcast data: %v", err)
		return
	}

	// Create the message
	userMessage := Message{
		Channel: fmt.Sprintf("user-%s", userID),
		Event:   eventType,
		Data:    jsonData,
	}

	// Find and send to the specific user
	mutex.RLock()
	for client := range clients {
		if client.userID == userID {
			if err := client.safeWriteJSON(userMessage); err != nil {
				log.Printf("Error sending message to user %s: %v", userID, err)
			}
			break
		}
	}
	mutex.RUnlock()
}

// handleUserTyping broadcasts typing indicator to room participants
func handleUserTyping(client *Client, message Message) {
	var typingData struct {
		RoomID   string `json:"roomId"`
		UserID   string `json:"userId"`
		UserName string `json:"userName"`
	}

	if err := json.Unmarshal(message.Data, &typingData); err != nil {
		log.Printf("Error parsing typing data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid typing data format")
		return
	}

	// Validate user is in the room
	if !client.isInRoom(typingData.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room to send typing indicators")
		return
	}

	// Broadcast typing indicator to all room participants except sender
	broadcastToRoomExceptSender(typingData.RoomID, client.userID, "user_typing", typingData)
}

// handleUserStoppedTyping broadcasts stop typing indicator to room participants
func handleUserStoppedTyping(client *Client, message Message) {
	var typingData struct {
		RoomID   string `json:"roomId"`
		UserID   string `json:"userId"`
		UserName string `json:"userName"`
	}

	if err := json.Unmarshal(message.Data, &typingData); err != nil {
		log.Printf("Error parsing stop typing data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid stop typing data format")
		return
	}

	// Validate user is in the room
	if !client.isInRoom(typingData.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room to send typing indicators")
		return
	}

	// Broadcast stop typing indicator to all room participants except sender
	broadcastToRoomExceptSender(typingData.RoomID, client.userID, "user_stopped_typing", typingData)
}

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

// broadcastToRoom broadcasts a message to all room participants including sender
func broadcastToRoom(roomID, eventType string, data interface{}) {
	// Marshal the data
	jsonData, err := json.Marshal(data)
	if err != nil {
		log.Printf("Error marshaling room broadcast data: %v", err)
		return
	}

	// Create the message
	broadcastMessage := Message{
		Channel: fmt.Sprintf("room-%s", roomID),
		Event:   eventType,
		Data:    jsonData,
	}

	// Send to all clients subscribed to this room channel
	mutex.RLock()
	var clientsToRemove []*Client
	for client := range clients {
		if client.isSubscribed(fmt.Sprintf("room-%s", roomID)) {
			err := client.safeWriteJSON(broadcastMessage)
			if err != nil {
				log.Printf("Error sending message to client %s: %v", client.userID, err)
				client.conn.Close()
				clientsToRemove = append(clientsToRemove, client)
			}
		}
	}
	mutex.RUnlock()

	// Remove clients that encountered errors
	if len(clientsToRemove) > 0 {
		mutex.Lock()
		for _, client := range clientsToRemove {
			delete(clients, client)
		}
		mutex.Unlock()
	}
}

// handleRoomAnnouncement broadcasts system messages to room participants
func handleRoomAnnouncement(client *Client, message Message) {
	var announcementData struct {
		RoomID      string `json:"roomId"`
		Type        string `json:"type"`        // "user_joined", "user_left", "video_changed", "host_paused", etc.
		UserName    string `json:"userName"`
		Message     string `json:"message"`     // Pre-formatted message text
		Timestamp   string `json:"timestamp"`
		AnnouncementID string `json:"announcementId"`
	}

	if err := json.Unmarshal(message.Data, &announcementData); err != nil {
		log.Printf("Error parsing announcement data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid announcement data format")
		return
	}

	// Validate announcement type
	validTypes := map[string]bool{
		"user_joined":           true,
		"user_left":             true,
		"video_changed":         true,
		"host_paused":           true,
		"host_resumed":          true,
		"host_transferred":      true,
		"room_created":          true,
		"video_seeked":          true,
		"host_started_streaming": true,
		"host_stopped_streaming": true,
	}

	if !validTypes[announcementData.Type] {
		sendErrorResponse(client, "INVALID_ANNOUNCEMENT_TYPE", "Invalid announcement type")
		return
	}

	// Broadcast announcement to all room participants
	broadcastToRoom(announcementData.RoomID, "room_announcement", announcementData)
}

// handleUserPresenceUpdate updates and broadcasts user presence status
func handleUserPresenceUpdate(client *Client, message Message) {
	var presenceData struct {
		RoomID       string `json:"roomId"`
		UserID       string `json:"userId"`
		UserName     string `json:"userName"`
		PresenceState string `json:"presenceState"` // "active", "away", "offline"
		Timestamp    string `json:"timestamp"`
	}

	if err := json.Unmarshal(message.Data, &presenceData); err != nil {
		log.Printf("Error parsing presence data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid presence data format")
		return
	}

	// Validate presence state
	validStates := map[string]bool{
		"active":  true,
		"away":    true,
		"offline": true,
	}

	if !validStates[presenceData.PresenceState] {
		sendErrorResponse(client, "INVALID_PRESENCE_STATE", "Invalid presence state")
		return
	}

	// Validate user is in the room
	if !client.isInRoom(presenceData.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room to update presence")
		return
	}

	// Update presence in room state
	roomMutex.Lock()
	if room, exists := rooms[presenceData.RoomID]; exists {
		if room.Presence == nil {
			room.Presence = make(map[string]string)
		}
		room.Presence[presenceData.UserID] = presenceData.PresenceState
	}
	roomMutex.Unlock()

	// Broadcast presence update to all room participants
	broadcastToRoom(presenceData.RoomID, "user_presence_updated", presenceData)
}

// handleGetRoomPresence sends current presence status of all room participants
func handleGetRoomPresence(client *Client, message Message) {
	var requestData struct {
		RoomID string `json:"roomId"`
	}

	if err := json.Unmarshal(message.Data, &requestData); err != nil {
		log.Printf("Error parsing room presence request: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid presence request format")
		return
	}

	// Validate user is in the room
	if !client.isInRoom(requestData.RoomID) {
		sendErrorResponse(client, "NOT_IN_ROOM", "Must be in room to get presence")
		return
	}

	// Get current presence state - validate against actually connected users
	roomMutex.RLock()
	var presenceData map[string]string
	if room, exists := rooms[requestData.RoomID]; exists {
		presenceData = make(map[string]string)

		// Get list of currently connected userIDs
		mutex.RLock()
		connectedUsers := make(map[string]bool)
		for client := range clients {
			if client.userID != "" {
				connectedUsers[client.userID] = true
			}
		}
		mutex.RUnlock()

		for userID, state := range room.Presence {
			// If user is not connected, force them to offline regardless of stored state
			if !connectedUsers[userID] {
				presenceData[userID] = "offline"
				// Also update the stored state to prevent future stale data
				room.Presence[userID] = "offline"
			} else {
				presenceData[userID] = state
			}
		}
	}
	roomMutex.RUnlock()

	// Send presence data back to requesting client
	responseData, _ := json.Marshal(map[string]interface{}{
		"roomId":   requestData.RoomID,
		"presence": presenceData,
	})

	responseMessage := Message{
		Channel: fmt.Sprintf("user-%s", client.userID),
		Event:   "room_presence_status",
		Data:    responseData,
	}

	if err := client.safeWriteJSON(responseMessage); err != nil {
		log.Printf("Error sending presence status: %v", err)
	}
}

// handleStreamModeChanged broadcasts when host switches between URL and WebRTC streaming modes
func handleStreamModeChanged(client *Client, message Message) {
	var modeData struct {
		RoomID    string `json:"roomId"`
		UserID    string `json:"userId"`
		Mode      string `json:"mode"` // "url" or "webrtc"
		Timestamp string `json:"timestamp"`
	}

	if err := json.Unmarshal(message.Data, &modeData); err != nil {
		log.Printf("Error parsing stream mode data: %v", err)
		sendErrorResponse(client, "INVALID_DATA", "Invalid stream mode data format")
		return
	}

	// Validate mode
	if modeData.Mode != "url" && modeData.Mode != "webrtc" {
		sendErrorResponse(client, "INVALID_MODE", "Mode must be 'url' or 'webrtc'")
		return
	}

	// Validate user is host in the room
	if err := validateHostPermission(modeData.RoomID, client.userID, "change stream mode"); err != nil {
		log.Printf("Permission denied for user %s: %v", client.userID, err)
		sendErrorResponse(client, "PERMISSION_DENIED", "Only host can change stream mode")
		return
	}

	// Broadcast to all room participants except sender
	broadcastToRoomExceptSender(modeData.RoomID, client.userID, "stream_mode_changed", modeData)
	log.Printf("Stream mode changed to %s in room %s by %s", modeData.Mode, modeData.RoomID, client.userID)
}

// handleClient manages the connection for an individual client.
func handleClient(client *Client) {
	defer func() {
		log.Printf("Client disconnecting, cleaning up...")
		unregister <- client
		client.conn.Close()
	}()

	// Set connection parameters.
	client.conn.SetReadLimit(512 * 1024)                          // 512KB max message size.
	client.conn.SetReadDeadline(time.Now().Add(60 * time.Second)) // Set initial read deadline.

	// Setup pong handler to reset read deadline on receiving pong messages.
	client.conn.SetPongHandler(func(string) error {
		client.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	// Main message handling loop.
	for {
		var message Message
		// Read message from client.
		err := client.conn.ReadJSON(&message)
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("Error reading message: %v", err)
			}
			break
		}

		// Message received

		// Handle messages based on the Event field.
		switch message.Event {
		case "subscribe":
			client.subscribe(message.Channel)
		case "unsubscribe":
			client.unsubscribe(message.Channel)
		// NEW ROOM EVENTS
		case "create_room":
			handleCreateRoom(client, message)
		case "join_room":
			handleJoinRoom(client, message)
		case "leave_room":
			handleLeaveRoom(client, message)
		case "host_sync":
			handleHostSync(client, message)
		case "transfer_host":
			handleTransferHost(client, message)
		case "room_message":
			handleRoomMessage(client, message)
		case "sync_room_state":
			handleSyncRoomState(client, message)
		// NOTIFICATION EVENTS
		case "room_join_request":
			handleRoomJoinRequest(client, message)
		case "join_request_approved":
			handleJoinRequestApproved(client, message)
		case "join_request_denied":
			handleJoinRequestDenied(client, message)
		case "room_invitation":
			handleRoomInvitation(client, message)
		case "room_deactivated":
			handleRoomDeactivated(client, message)
		case "participant_kicked":
			handleParticipantKicked(client, message)
		// TYPING INDICATORS
		case "user_typing":
			handleUserTyping(client, message)
		case "user_stopped_typing":
			handleUserStoppedTyping(client, message)
		// VIDEO REACTIONS
		case "video_reaction":
			handleVideoReaction(client, message)
		// ACTIVITY ANNOUNCEMENTS
		case "room_announcement":
			handleRoomAnnouncement(client, message)
		// USER PRESENCE
		case "user_presence_update":
			handleUserPresenceUpdate(client, message)
		case "get_room_presence":
			handleGetRoomPresence(client, message)
		// WEBRTC STREAMING
		case "stream_mode_changed":
			handleStreamModeChanged(client, message)
		// LEGACY SUPPORT
		case "sync":
			handleLegacySync(client, message)
		case "incoming_message", "new_message", "new_friend", "incoming_friend_request", "friend_request_denied", "friend_request_accepted", "friend_removed":
			// Broadcast the message to clients subscribed to the channel
			broadcast <- message
		default:
			log.Printf("Unknown event type: %s", message.Event)
		}
	}
}

// handleConnections upgrades the HTTP connection to a WebSocket and registers the client.
func handleConnections(w http.ResponseWriter, r *http.Request) {
	// Extract userID from query parameters
	userID := r.URL.Query().Get("userID")
	if userID == "" {
		http.Error(w, "UserID required in query parameters", http.StatusBadRequest)
		return
	}

	// Upgrade the HTTP connection to a WebSocket connection.
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Error upgrading connection: %v", err)
		return
	}

	// Create a new client.
	client := &Client{
		conn:     conn,
		channels: make(map[string]bool),
		userID:   userID,
		rooms:    make(map[string]string),
	}

	log.Printf("New client connected from: %s with userID: %s", conn.RemoteAddr(), userID)

	// Register the client.
	register <- client

	// Start handling messages from the client.
	handleClient(client)
}

// handleMessages processes incoming messages and broadcasts them to subscribed clients.
func handleMessages() {
	// Setup a ticker to send ping messages for keep-alive.
	ticker := time.NewTicker(54 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case client := <-register:
			// Register a new client.
			mutex.Lock()
			clients[client] = true
			mutex.Unlock()

		case client := <-unregister:
			// Unregister a client.
			log.Printf("Client unregistered from: %s with userID: %s", client.conn.RemoteAddr(), client.userID)

			// Remove client from all rooms and update presence
			for roomID := range client.rooms {
				// Mark user as offline before leaving
				roomMutex.Lock()
				if room, exists := rooms[roomID]; exists && room.Presence != nil {
					room.Presence[client.userID] = "offline"
					// Broadcast offline status
					offlineData, _ := json.Marshal(map[string]interface{}{
						"roomId":        roomID,
						"userId":        client.userID,
						"presenceState": "offline",
						"timestamp":     time.Now().Format(time.RFC3339),
					})

					offlineMessage := Message{
						Channel: fmt.Sprintf("room-%s", roomID),
						Event:   "user_presence_updated",
						Data:    offlineData,
					}

					// Send to other room participants
					for otherClient := range clients {
						if otherClient != client && otherClient.isSubscribed(fmt.Sprintf("room-%s", roomID)) {
							otherClient.safeWriteJSON(offlineMessage)
						}
					}
				}
				roomMutex.Unlock()

				if err := leaveRoom(roomID, client.userID); err != nil {
					log.Printf("Error removing user %s from room %s during disconnect: %v", client.userID, roomID, err)
				}
			}

			// Clean up empty rooms
			cleanupEmptyRooms()

			mutex.Lock()
			if _, ok := clients[client]; ok {
				delete(clients, client)
				client.conn.Close()
			}
			mutex.Unlock()

		case message := <-broadcast:
			// Broadcast a message to all subscribed clients.
			mutex.RLock()
			var clientsToRemove []*Client
			for client := range clients {
				if client.isSubscribed(message.Channel) {
				err := client.safeWriteJSON(message)
					if err != nil {
						log.Printf("Error sending message to client: %v", err)
						client.conn.Close()
						clientsToRemove = append(clientsToRemove, client)
					}
				}
			}
			mutex.RUnlock()

			// Remove clients that encountered errors.
			if len(clientsToRemove) > 0 {
				mutex.Lock()
				for _, client := range clientsToRemove {
					delete(clients, client)
				}
				mutex.Unlock()
			}

		case <-ticker.C:
			// Send ping messages to all clients for keep-alive.
			mutex.RLock()
			var clientsToRemove []*Client
			for client := range clients {
			err := client.safeWriteControl(websocket.PingMessage, []byte{}, time.Now().Add(10*time.Second))
				if err != nil {
					log.Printf("Ping failed for client %s: %v", client.conn.RemoteAddr(), err)
					client.conn.Close()
					clientsToRemove = append(clientsToRemove, client)
				}
			}
			mutex.RUnlock()

			// Remove clients that failed to respond to ping.
			if len(clientsToRemove) > 0 {
				mutex.Lock()
				for _, client := range clientsToRemove {
					delete(clients, client)
				}
				mutex.Unlock()
			}
		}
	}
}

func main() {
	// Configure logging to include date, time, and file line numbers.
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	// Set up the WebSocket endpoint.
	http.HandleFunc("/ws", handleConnections)

	// Start the message handling goroutine.
	go handleMessages()

	// Read the port from the environment variable, default to 8080 if not set.
	port := os.Getenv("PORT")
	if port == "" {
		port = "8085"
	}
	addr := ":" + port

	// Start the HTTP server.
	fmt.Printf("WebSocket server starting on %s\n", addr)
	err := http.ListenAndServe(addr, nil)
	if err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}
