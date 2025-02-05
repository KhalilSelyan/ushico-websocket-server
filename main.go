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

type IdentifyData struct {
	UserID string `json:"userId"`
}

// Client represents a connected WebSocket client.
type Client struct {
	conn     *websocket.Conn            // The WebSocket connection.
	channels map[string]map[string]bool // Map of channels to events.
	mu       sync.Mutex                 // Mutex to protect the channels map.
	userID   string                     // Add userID to identify the client
}

// Message represents the structure of messages sent over WebSocket.
type Message struct {
	Channel string          `json:"channel"` // The channel identifier.
	Event   string          `json:"event"`   // The event type (e.g., "sync", "subscribe").
	Data    json.RawMessage `json:"data"`    // The raw message data.
	UserID  string          `json:"userId"`
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

// Add a debug log function
func debugLog(format string, v ...interface{}) {
	log.Printf("[DEBUG] "+format, v...)
}

func (c *Client) subscribe(channel string, event string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.channels[channel] == nil {
		c.channels[channel] = make(map[string]bool)
	}
	c.channels[channel][event] = true
	debugLog("Client %s subscribed to channel: %s, event: %s", c.userID, channel, event)
}

func (c *Client) unsubscribe(channel string, event string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.channels[channel] != nil {
		delete(c.channels[channel], event)
		if len(c.channels[channel]) == 0 {
			delete(c.channels, channel)
		}
		debugLog("Client %s unsubscribed from channel: %s, event: %s", c.userID, channel, event)
	}
}

func (c *Client) isSubscribed(channel string, event string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	debugLog("Checking subscription for client %s: channel=%s, event=%s",
		c.userID, channel, event)

	if events, ok := c.channels[channel]; ok {
		if strings.HasPrefix(channel, "sync-") {
			isSubscribed := events[event]
			debugLog("Client %s subscription to %s:%s = %v",
				c.userID, channel, event, isSubscribed)
			return isSubscribed
		}
		debugLog("Client %s has events for channel %s: %v",
			c.userID, channel, events)
		return true
	}
	debugLog("Client %s not subscribed to channel %s", c.userID, channel)
	return false
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

		// Handle system messages first
		if message.Channel == "system" && message.Event == "identify" {
			var identifyData IdentifyData
			if err := json.Unmarshal(message.Data, &identifyData); err != nil {
				debugLog("Error parsing identify data: %v", err)
				continue
			}
			client.userID = identifyData.UserID
			debugLog("Client identified with userID: %s", client.userID)
			continue
		}

		// Ensure userId is set from message if not already set
		if client.userID == "" && message.UserID != "" {
			client.userID = message.UserID
			debugLog("Set client userID from message: %s", client.userID)
		}

		debugLog("Received message from %s: Channel=%s, Event=%s",
			client.userID, message.Channel, message.Event)

		// Handle messages based on the Event field.
		switch message.Event {
		case "subscribe":
			var data struct {
				Event string `json:"event"`
			}
			if err := json.Unmarshal(message.Data, &data); err != nil {
				debugLog("Error parsing subscribe data: %v", err)
				continue
			}
			client.subscribe(message.Channel, data.Event)
			debugLog("Client %s subscribed to %s:%s", client.userID, message.Channel, data.Event)

		case "sync":
			if !strings.HasPrefix(message.Channel, "sync-") {
				debugLog("Invalid sync channel format: %s", message.Channel)
				continue
			}
			debugLog("Broadcasting sync from %s on channel %s", client.userID, message.Channel)
			broadcast <- message

		default:
			debugLog("Broadcasting message from %s: %s:%s", client.userID, message.Channel, message.Event)
			broadcast <- message
		}
	}
}

// handleConnections upgrades the HTTP connection to a WebSocket and registers the client.
func handleConnections(w http.ResponseWriter, r *http.Request) {
	// Upgrade the HTTP connection to a WebSocket connection.
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Error upgrading connection: %v", err)
		return
	}

	// Create a new client.
	client := &Client{
		conn:     conn,
		channels: make(map[string]map[string]bool), // Updated to match the expected type
	}

	log.Printf("New client connected from: %s", conn.RemoteAddr())

	// Register the client.
	register <- client

	// Start handling messages from the client.
	handleClient(client)
}

// handleMessages processes incoming messages and broadcasts them to subscribed clients.
func handleMessages() {
	ticker := time.NewTicker(54 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case client := <-register:
			mutex.Lock()
			clients[client] = true
			debugLog("New client registered: %s", client.userID)
			mutex.Unlock()

		case client := <-unregister:
			mutex.Lock()
			if _, ok := clients[client]; ok {
				delete(clients, client)
				debugLog("Client unregistered: %s", client.userID)
			}
			mutex.Unlock()

		case message := <-broadcast:
			debugLog("Broadcasting message: Channel=%s, Event=%s, UserID=%s",
				message.Channel, message.Event, message.UserID)

			mutex.RLock()
			for client := range clients {
				if client.isSubscribed(message.Channel, message.Event) {
					// if client.userID == message.UserID {
					// 	debugLog("Skipping message back to sender: %s", client.userID)
					// 	continue
					// }

					debugLog("Sending message to client: %s", client.userID)
					err := client.conn.WriteJSON(message)
					if err != nil {
						debugLog("Error sending message to client %s: %v", client.userID, err)
						client.conn.Close()
						delete(clients, client)
					}
				}
			}
			mutex.RUnlock()

		case <-ticker.C:
			mutex.RLock()
			for client := range clients {
				err := client.conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(10*time.Second))
				if err != nil {
					debugLog("Ping failed for client %s: %v", client.userID, err)
					client.conn.Close()
					delete(clients, client)
				}
			}
			mutex.RUnlock()
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
