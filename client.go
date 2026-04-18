package main

import (
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Client represents a connected WebSocket client.
type Client struct {
	conn         *websocket.Conn   // The WebSocket connection.
	channels     map[string]bool   // The channels the client is subscribed to.
	mu           sync.Mutex        // Mutex to protect the channels map.
	writeMu      sync.Mutex        // Mutex to protect WebSocket writes.
	userID       string            // User identifier for the client
	userName     string            // User display name (captured from presence updates)
	rooms        map[string]string // roomID -> role mapping
	roomChannels map[string]string // roomID -> pre-formatted channel name (e.g., "room-xyz")
	lastPongTime time.Time         // Last time a pong was received from this client
	send         chan Message      // Buffered channel for outgoing JSON messages
	sendBinary   chan []byte       // Buffered channel for outgoing binary messages
}

// Subscribe adds the client to the specified channel.
func (c *Client) subscribe(channel string) {
	c.mu.Lock()
	c.channels[channel] = true
	c.mu.Unlock()

	// Update global channel subscriptions map for O(1) lookups
	channelMutex.Lock()
	if channelSubs[channel] == nil {
		channelSubs[channel] = make(map[*Client]bool)
	}
	channelSubs[channel][c] = true
	channelMutex.Unlock()
}

// Unsubscribe removes the client from the specified channel.
func (c *Client) unsubscribe(channel string) {
	c.mu.Lock()
	delete(c.channels, channel)
	c.mu.Unlock()

	// Update global channel subscriptions map
	channelMutex.Lock()
	if channelSubs[channel] != nil {
		delete(channelSubs[channel], c)
		// Clean up empty channel maps
		if len(channelSubs[channel]) == 0 {
			delete(channelSubs, channel)
		}
	}
	channelMutex.Unlock()
}

// isSubscribed checks if the client is subscribed to the specified channel.
func (c *Client) isSubscribed(channel string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.channels[channel]
}

// unsubscribeAll removes the client from all channels (used on disconnect).
func (c *Client) unsubscribeAll() {
	c.mu.Lock()
	channels := make([]string, 0, len(c.channels))
	for channel := range c.channels {
		channels = append(channels, channel)
	}
	c.mu.Unlock()

	// Remove from global channel subscriptions
	channelMutex.Lock()
	for _, channel := range channels {
		if channelSubs[channel] != nil {
			delete(channelSubs[channel], c)
			if len(channelSubs[channel]) == 0 {
				delete(channelSubs, channel)
			}
		}
	}
	channelMutex.Unlock()
}

// writePump pumps messages from both JSON and binary channels to the WebSocket connection.
// Runs in its own goroutine per client.
func (c *Client) writePump() {
	defer func() {
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			if !ok {
				// Channel closed, exit
				return
			}
			if err := c.safeWriteJSON(message); err != nil {
				log.Printf("Error sending JSON to client %s: %v", c.userID, err)
				return
			}
		case data, ok := <-c.sendBinary:
			if !ok {
				// Channel closed, exit
				return
			}
			if err := c.safeWriteBinary(data); err != nil {
				log.Printf("Error sending binary to client %s: %v", c.userID, err)
				return
			}
		}
	}
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

// safeWriteBinary safely writes binary data to the WebSocket connection.
func (c *Client) safeWriteBinary(data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.WriteMessage(websocket.BinaryMessage, data)
}

// joinRoomAsClient adds the client to a room with the specified role.
func (c *Client) joinRoomAsClient(roomID, role string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rooms == nil {
		c.rooms = make(map[string]string)
	}
	if c.roomChannels == nil {
		c.roomChannels = make(map[string]string)
	}
	c.rooms[roomID] = role
	c.roomChannels[roomID] = "room-" + roomID // Pre-format channel name to avoid repeated allocations
}

// leaveRoomAsClient removes the client from a room.
func (c *Client) leaveRoomAsClient(roomID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.rooms, roomID)
	delete(c.roomChannels, roomID)
}

// getRoomChannel returns the pre-formatted channel name for a room.
func (c *Client) getRoomChannel(roomID string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ch, ok := c.roomChannels[roomID]; ok {
		return ch
	}
	return "room-" + roomID // Fallback
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
