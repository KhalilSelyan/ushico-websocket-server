# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Architecture

This is a Go-based WebSocket server designed for real-time communication, particularly for video synchronization and messaging. The server uses a publish-subscribe pattern where clients subscribe to channels and receive broadcasted messages.

### Key Components

- **Client Management**: Each WebSocket connection is represented by a `Client` struct that maintains channel subscriptions and connection state
- **Channel-based Communication**: Clients subscribe to channels (e.g., "sync-{chatId}" for video sync, message channels for chat)
- **Message Broadcasting**: Central message handler broadcasts messages to all clients subscribed to specific channels
- **Connection Management**: Automatic cleanup of disconnected clients with ping/pong keep-alive mechanism

### Message Types

The server handles these event types:
- `subscribe`/`unsubscribe`: Channel subscription management
- `sync`: Video playback synchronization with timestamp and state data
- `incoming_message`, `new_message`: Chat messaging
- `new_friend`, `incoming_friend_request`, `friend_request_denied`, `friend_request_accepted`, `friend_removed`: Friend system events

### Chat ID Format

Chat IDs follow the pattern "user1--user2" (validated by `validateChatID` function). Video sync channels use the format "sync-{chatId}".

## Development Commands

```bash
# Run the server locally
go run main.go

# Build the application
go build -o ushico-websocket-server .

# Format code
go fmt ./...

# Run tests (if any exist)
go test ./...

# Build Docker image
docker build -t ushico-websocket-server .

# Run with Docker
docker run -p 8085:8085 ushico-websocket-server
```

## Configuration

- Default port: 8085 (configurable via PORT environment variable)
- WebSocket endpoint: `/ws`
- CORS: Allows all origins
- Connection limits: 512KB max message size, 60-second read deadline