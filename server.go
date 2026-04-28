package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// handleConnections upgrades the HTTP connection to a WebSocket and registers the client.
func handleConnections(w http.ResponseWriter, r *http.Request) {
	userID, err := verifyRealtimeToken(r.URL.Query().Get("token"))
	if err != nil {
		http.Error(w, "valid realtime token required", http.StatusUnauthorized)
		return
	}

	// Upgrade the HTTP connection to a WebSocket connection.
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Error upgrading connection: %v", err)
		return
	}

	// Create a new client with buffered send channels.
	client := &Client{
		conn:         conn,
		channels:     make(map[string]bool),
		userID:       userID,
		rooms:        make(map[string]string),
		roomChannels: make(map[string]string),
		lastPongTime: time.Now(),
		send:         make(chan Message, 256),
		sendBinary:   make(chan []byte, 256),
	}

	log.Printf("New client connected from: %s with userID: %s", conn.RemoteAddr(), userID)

	// Register the client.
	register <- client

	// Start the write pump in its own goroutine.
	go client.writePump()

	// Start handling messages from the client (blocks until client disconnects).
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
			cleanupDisconnectedClient(client)

		case message := <-broadcast:
			// Broadcast a message to all subscribed clients using O(1) channel lookup.
			channelMutex.RLock()
			subscribers := channelSubs[message.Channel]
			for client := range subscribers {
				select {
				case client.send <- message:
				default:
					log.Printf("Client %s send buffer full, dropping message", client.userID)
				}
			}
			channelMutex.RUnlock()

		case binMsg := <-binaryBroadcast:
			// Broadcast binary message to room subscribers (excluding sender)
			channel := fmt.Sprintf("room-%s", binMsg.RoomID)
			channelMutex.RLock()
			for client := range channelSubs[channel] {
				if client.userID != binMsg.SenderID {
					select {
					case client.sendBinary <- binMsg.Data:
					default:
						log.Printf("Client %s binary buffer full, dropping message", client.userID)
					}
				}
			}
			channelMutex.RUnlock()

		case <-ticker.C:
			// Send ping messages to all clients for keep-alive.
			mutex.RLock()
			var clientsToRemove []*Client
			pongTimeout := 2 * time.Minute

			for client := range clients {
				lastPong := client.getLastPong()
				if time.Since(lastPong) > pongTimeout {
					log.Printf("Client %s (user %s) timed out - no pong in %v",
						client.conn.RemoteAddr(), client.userID, time.Since(lastPong))
					client.conn.Close()
					clientsToRemove = append(clientsToRemove, client)
					continue
				}

				err := client.safeWriteControl(websocket.PingMessage, []byte{}, time.Now().Add(10*time.Second))
				if err != nil {
					log.Printf("Ping failed for client %s: %v", client.conn.RemoteAddr(), err)
					client.conn.Close()
					clientsToRemove = append(clientsToRemove, client)
				}
			}
			mutex.RUnlock()

			for _, client := range clientsToRemove {
				cleanupDisconnectedClient(client)
			}
		}
	}
}

func cleanupDisconnectedClient(client *Client) {
	remoteAddr := "unknown"
	if client.conn != nil {
		remoteAddr = client.conn.RemoteAddr().String()
	}
	log.Printf("Client unregistered from: %s with userID: %s", remoteAddr, client.userID)

	mutex.Lock()
	if _, ok := clients[client]; !ok {
		mutex.Unlock()
		return
	}
	delete(clients, client)
	mutex.Unlock()

	client.mu.Lock()
	roomIDs := make([]string, 0, len(client.rooms))
	for roomID := range client.rooms {
		roomIDs = append(roomIDs, roomID)
	}
	client.mu.Unlock()

	for _, roomID := range roomIDs {
		roomMutex.Lock()
		if room, exists := rooms[roomID]; exists && room.Presence != nil {
			room.Presence[client.userID] = "offline"
			if room.CinemaAvatars != nil {
				delete(room.CinemaAvatars, client.userID)
			}
			if room.WebcamParticipants != nil {
				delete(room.WebcamParticipants, client.userID)
			}
			logRealtime("client_disconnect_cleanup", map[string]interface{}{
				"roomId": roomID,
				"userId": client.userID,
			})
			offlineData, _ := json.Marshal(map[string]interface{}{
				"roomId":        roomID,
				"userId":        client.userID,
				"userName":      client.userName,
				"presenceState": "offline",
				"timestamp":     time.Now().Format(time.RFC3339),
			})

			offlineMessage := Message{
				Channel: fmt.Sprintf("room-%s", roomID),
				Event:   "user_presence_updated",
				Data:    offlineData,
			}

			channel := fmt.Sprintf("room-%s", roomID)
			channelMutex.RLock()
			for otherClient := range channelSubs[channel] {
				if otherClient != client {
					select {
					case otherClient.send <- offlineMessage:
					default:
						log.Printf("Client %s send buffer full during presence update", otherClient.userID)
					}
				}
			}
			channelMutex.RUnlock()
		}
		roomMutex.Unlock()

		if err := leaveRoom(roomID, client.userID); err != nil {
			log.Printf("Error removing user %s from room %s during disconnect: %v", client.userID, roomID, err)
		}
	}

	cleanupEmptyRooms()
	client.unsubscribeAll()

	close(client.send)
	close(client.sendBinary)
}

// startReminderCron starts a background goroutine that calls the SvelteKit
// reminder processing endpoint every minute.
func startReminderCron() {
	sveltekitURL := os.Getenv("SVELTEKIT_URL")
	if sveltekitURL == "" {
		log.Println("[reminders] SVELTEKIT_URL not set, reminder cron disabled")
		return
	}

	apiSecret := os.Getenv("INTERNAL_API_SECRET")
	endpoint := strings.TrimSuffix(sveltekitURL, "/") + "/api/internal/process-reminders"

	ticker := time.NewTicker(1 * time.Minute)

	go func() {
		// Run once immediately on startup
		processReminders(endpoint, apiSecret)

		for range ticker.C {
			processReminders(endpoint, apiSecret)
		}
	}()

	log.Printf("[reminders] Reminder cron started, calling %s every minute\n", endpoint)
}

// processReminders calls the SvelteKit reminder endpoint.
func processReminders(endpoint, apiSecret string) {
	client := &http.Client{Timeout: 30 * time.Second}

	req, err := http.NewRequest("POST", endpoint, nil)
	if err != nil {
		log.Printf("[reminders] Failed to create request: %v\n", err)
		return
	}

	if apiSecret != "" {
		req.Header.Set("Authorization", "Bearer "+apiSecret)
	}

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[reminders] Failed to call endpoint: %v\n", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("[reminders] Endpoint returned status %d\n", resp.StatusCode)
		return
	}

	log.Println("[reminders] Successfully processed reminders")
}
