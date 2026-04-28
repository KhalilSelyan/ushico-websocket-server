package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Configure the WebSocket upgrader with buffer sizes, CORS policy, and compression.
var upgrader = websocket.Upgrader{
	CheckOrigin:       checkAllowedOrigin,
	ReadBufferSize:    4096,
	WriteBufferSize:   4096,
	EnableCompression: true,
}

func checkAllowedOrigin(r *http.Request) bool {
	allowed := strings.Split(os.Getenv("ALLOWED_ORIGINS"), ",")
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	for _, value := range allowed {
		if strings.TrimSpace(value) == origin {
			return true
		}
	}
	return false
}

func verifyRealtimeToken(token string) (string, error) {
	secret := os.Getenv("WEBSOCKET_AUTH_SECRET")
	if secret == "" {
		return "", fmt.Errorf("WEBSOCKET_AUTH_SECRET is required")
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", fmt.Errorf("invalid token")
	}

	expiresAt, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() > expiresAt {
		return "", fmt.Errorf("expired token")
	}

	payload := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(parts[2])) {
		return "", fmt.Errorf("invalid token signature")
	}

	return parts[0], nil
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
