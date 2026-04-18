package main

import (
	"encoding/json"
	"fmt"
	"log"
)

// broadcastToRoom broadcasts a message to all room participants including sender
func broadcastToRoom(roomID, eventType string, data interface{}) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		log.Printf("Error marshaling room broadcast data: %v", err)
		return
	}

	broadcastMessage := Message{
		Channel: fmt.Sprintf("room-%s", roomID),
		Event:   eventType,
		Data:    jsonData,
	}

	channel := fmt.Sprintf("room-%s", roomID)
	channelMutex.RLock()
	subscribers := channelSubs[channel]
	for client := range subscribers {
		select {
		case client.send <- broadcastMessage:
		default:
			log.Printf("Client %s send buffer full, dropping message", client.userID)
		}
	}
	channelMutex.RUnlock()
}

// broadcastToRoomExceptSender broadcasts a message to all room participants except the sender
func broadcastToRoomExceptSender(roomID, senderID, eventType string, data interface{}) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		log.Printf("Error marshaling broadcast data: %v", err)
		return
	}

	broadcastMessage := Message{
		Channel: fmt.Sprintf("room-%s", roomID),
		Event:   eventType,
		Data:    jsonData,
	}

	channel := fmt.Sprintf("room-%s", roomID)
	channelMutex.RLock()
	subscribers := channelSubs[channel]
	for client := range subscribers {
		if client.userID != senderID {
			select {
			case client.send <- broadcastMessage:
			default:
				log.Printf("Client %s send buffer full, dropping message", client.userID)
			}
		}
	}
	channelMutex.RUnlock()
}

// broadcastToSpecificUser sends a message to a specific user if they're connected
func broadcastToSpecificUser(userID, eventType string, data interface{}) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		log.Printf("Error marshaling user-specific broadcast data: %v", err)
		return
	}

	userMessage := Message{
		Channel: fmt.Sprintf("user-%s", userID),
		Event:   eventType,
		Data:    jsonData,
	}

	mutex.RLock()
	for client := range clients {
		if client.userID == userID {
			select {
			case client.send <- userMessage:
			default:
				log.Printf("Client %s send buffer full, dropping user-specific message", userID)
			}
			break
		}
	}
	mutex.RUnlock()
}
