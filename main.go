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
	conn     *websocket.Conn // The WebSocket connection.
	channels map[string]bool // The channels the client is subscribed to.
	mu       sync.Mutex      // Mutex to protect the channels map.
	// userID   string          // Add userID to identify the client
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
	ChatID    string  `json:"chatId"`    // The chat room identifier.
	State     string  `json:"state"`     // The state of the video (playing/paused).
}

// Global variables for managing clients and messages.
var (
	clients    = make(map[*Client]bool) // Map of connected clients.
	broadcast  = make(chan Message)     // Channel for broadcasting messages.
	register   = make(chan *Client)     // Channel for registering new clients.
	unregister = make(chan *Client)     // Channel for unregistering clients.
	mutex      = &sync.RWMutex{}        // Read-write mutex to protect the clients map.
)

// Subscribe adds the client to the specified channel.
func (c *Client) subscribe(channel string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.channels[channel] = true
	log.Printf("Client subscribed to channel: %s", channel)
}

// Unsubscribe removes the client from the specified channel.
func (c *Client) unsubscribe(channel string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.channels, channel)
	log.Printf("Client unsubscribed from channel: %s", channel)
}

// isSubscribed checks if the client is subscribed to the specified channel.
func (c *Client) isSubscribed(channel string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.channels[channel]
}

// validateChatID checks if the chat ID is in the expected format (e.g., "user1--user2").
func validateChatID(chatID string) bool {
	parts := strings.Split(chatID, "--")
	return len(parts) == 2 && len(parts[0]) > 0 && len(parts[1]) > 0
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

		log.Printf("Received message: %+v", message)

		// Handle messages based on the Event field.
		switch message.Event {
		case "subscribe":
			log.Printf("Handling subscribe event for channel: %s", message.Channel)
			client.subscribe(message.Channel)
		case "unsubscribe":
			log.Printf("Handling unsubscribe event for channel: %s", message.Channel)
			client.unsubscribe(message.Channel)
		case "sync":
			log.Printf("Handling sync event for channel: %s", message.Channel)
			// Validate chat ID format.
			chatID := strings.TrimPrefix(message.Channel, "sync-")
			if !validateChatID(chatID) {
				log.Printf("Invalid chat ID format: %s", chatID)
				continue
			}
			// Parse sync data.
			var syncData SyncData
			if err := json.Unmarshal(message.Data, &syncData); err != nil {
				log.Printf("Error parsing sync data: %v", err)
				continue
			}
			// Log the sync message details.
			log.Printf("Broadcasting sync message - Time: %.2f, State: %s", syncData.Timestamp, syncData.State)
			// Broadcast the original message.
			broadcast <- message
		case "incoming_message", "new_message", "new_friend", "incoming_friend_request", "friend_request_denied", "friend_request_accepted", "friend_removed":
			log.Printf("Handling %s event for channel: %s", message.Event, message.Channel)
			// Broadcast the message to clients subscribed to the channel
			broadcast <- message
		default:
			log.Printf("Unknown event type: %s", message.Event)
		}
	}
}

// handleConnections upgrades the HTTP connection to a WebSocket and registers the client.
func handleConnections(w http.ResponseWriter, r *http.Request) {
	// Extract userID from query parameters (or headers)
	// userID := r.URL.Query().Get("userID")
	// if userID == "" {
	// 	http.Error(w, "Unauthorized", http.StatusUnauthorized)
	// 	return
	// }

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
		// userID:   userID,
	}

	log.Printf("New client connected from: %s", conn.RemoteAddr())

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
			log.Printf("New client registered from: %s", client.conn.RemoteAddr())
			mutex.Lock()
			clients[client] = true
			mutex.Unlock()

		case client := <-unregister:
			// Unregister a client.
			log.Printf("Client unregistered from: %s", client.conn.RemoteAddr())
			mutex.Lock()
			if _, ok := clients[client]; ok {
				delete(clients, client)
				client.conn.Close()
			}
			mutex.Unlock()

		case message := <-broadcast:
			// Broadcast a message to all subscribed clients.
			log.Printf("Broadcasting message to channel %s subscribers", message.Channel)
			mutex.RLock()
			var clientsToRemove []*Client
			for client := range clients {
				if client.isSubscribed(message.Channel) {
					err := client.conn.WriteJSON(message)
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
				err := client.conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(10*time.Second))
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
