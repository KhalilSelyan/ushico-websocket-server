package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

// Configure the WebSocket upgrader with buffer sizes, CORS policy, and compression.
var upgrader = websocket.Upgrader{
	CheckOrigin:       func(r *http.Request) bool { return true }, // Allow all origins
	ReadBufferSize:    4096,
	WriteBufferSize:   4096,
	EnableCompression: true,
}

// Global variables for managing clients and messages.
var (
	clients         = make(map[*Client]bool)            // Map of connected clients.
	broadcast       = make(chan Message, 256)           // Buffered channel for broadcasting messages.
	binaryBroadcast = make(chan BinaryBroadcast, 256)   // Buffered channel for binary broadcasts.
	register        = make(chan *Client)                // Channel for registering new clients.
	unregister      = make(chan *Client)                // Channel for unregistering clients.
	mutex           = &sync.RWMutex{}                   // Read-write mutex to protect the clients map.
	rooms           = make(map[string]*Room)            // Active rooms.
	roomMutex       = &sync.RWMutex{}                   // Protect rooms map.
	channelSubs     = make(map[string]map[*Client]bool) // Channel -> subscribed clients (for O(1) lookup).
	channelMutex    = &sync.RWMutex{}                   // Protect channelSubs map.
)

// logRealtime logs structured events for real-time monitoring
func logRealtime(event string, fields map[string]interface{}) {
	payload := map[string]interface{}{"event": event}
	for key, value := range fields {
		payload[key] = value
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[realtime] %s %+v", event, fields)
		return
	}
	log.Printf("[realtime] %s", encoded)
}
