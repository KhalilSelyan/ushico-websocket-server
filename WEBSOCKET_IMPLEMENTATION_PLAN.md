# WebSocket Server - Watch Party Implementation Plan

## Overview
Extend the existing Go WebSocket server to support multi-person watch parties with room-based communication, coordinated with the Next.js frontend.

## Phase 1: Data Structures & Types (30 minutes)

### New Structs
```go
// Room represents a watch party room state (in-memory only)
type Room struct {
    ID           string            `json:"id"`
    HostID       string            `json:"hostId"`
    Name         string            `json:"name"`
    Participants map[string]string `json:"participants"` // userID -> role mapping
    IsActive     bool              `json:"isActive"`
    CreatedAt    time.Time         `json:"createdAt"`
    CurrentVideo SyncData          `json:"currentVideo"`
}

// RoomData for room creation/management events
type RoomData struct {
    RoomID   string   `json:"roomId"`
    RoomName string   `json:"roomName,omitempty"`
    UserID   string   `json:"userId"`
    UserIDs  []string `json:"userIds,omitempty"` // for bulk operations
}

// ErrorResponse for client errors
type ErrorResponse struct {
    Error   string `json:"error"`
    Message string `json:"message"`
    Code    string `json:"code"`
}
```

### Update Existing Structs
```go
// Update Client struct
type Client struct {
    conn     *websocket.Conn
    channels map[string]bool
    mu       sync.Mutex
    userID   string // User identifier
    rooms    map[string]string // roomID -> role mapping
}

// Update SyncData to use RoomID
type SyncData struct {
    Timestamp float64 `json:"timestamp"`
    URL       string  `json:"url"`
    RoomID    string  `json:"roomId"` // Changed from ChatID
    State     string  `json:"state"`
}
```

### Global Variables Addition
```go
var (
    // Existing globals...
    rooms     = make(map[string]*Room) // Active rooms
    roomMutex = &sync.RWMutex{}        // Protect rooms map
)
```

## Phase 2: Room Management Functions (1.5 hours)

### Core Room Functions
```go
// createRoom creates a new watch party room
func createRoom(hostID, roomName string) *Room

// joinRoom adds a user to an existing room
func joinRoom(roomID, userID string) error

// leaveRoom removes a user from a room
func leaveRoom(roomID, userID string) error

// validateRoomID checks if room ID format is valid
func validateRoomID(roomID string) bool

// isRoomHost checks if user is the host of the room
func isRoomHost(roomID, userID string) bool

// transferHost transfers host privileges to another user
func transferHost(roomID, newHostID string) error

// getRoomParticipants returns all participants in a room
func getRoomParticipants(roomID string) []string

// cleanupEmptyRooms removes rooms with no participants
func cleanupEmptyRooms()

// handleHostDisconnection manages host leaving scenarios
func handleHostDisconnection(roomID, hostID string)
```

### Client Room Management
```go
// Add room tracking to Client
func (c *Client) joinRoom(roomID, role string)
func (c *Client) leaveRoom(roomID string)
func (c *Client) isInRoom(roomID string) bool
func (c *Client) getRoleInRoom(roomID string) string
```

## Phase 3: Message Handler Updates (1 hour)

### New Event Types
```go
switch message.Event {
// Existing events...
case "create_room":
    handleCreateRoom(client, message)
case "join_room":
    handleJoinRoom(client, message)
case "leave_room":
    handleLeaveRoom(client, message)
case "host_sync":
    handleHostSync(client, message) // Replace "sync"
case "transfer_host":
    handleTransferHost(client, message)
case "room_message":
    handleRoomMessage(client, message)
// Keep "sync" for backward compatibility (deprecated)
case "sync":
    handleLegacySync(client, message)
}
```

### Event Handler Functions
```go
func handleCreateRoom(client *Client, message Message)
func handleJoinRoom(client *Client, message Message)
func handleLeaveRoom(client *Client, message Message)
func handleHostSync(client *Client, message Message)
func handleTransferHost(client *Client, message Message)
func handleRoomMessage(client *Client, message Message)
func handleLegacySync(client *Client, message Message) // Backward compatibility
```

## Phase 4: Channel Format Updates (30 minutes)

### Channel Naming Convention
- **Room Sync**: `room-{roomId}` (replaces `sync-{chatId}`)
- **Room Messages**: `room-{roomId}` (same channel for all room communication)
- **Legacy Support**: Continue supporting `sync-user1--user2` format

### Channel Validation
```go
func validateChannelFormat(channel string) (channelType string, identifier string, isValid bool) {
    // Returns: "room", "legacy", or "invalid"
}
```

## Phase 5: Permission System (30 minutes)

### Host-Only Actions
- Only hosts can send `host_sync` events
- Only hosts can `transfer_host`
- Only hosts can control video playback

### Permission Validation
```go
func validateHostPermission(roomID, userID string, action string) error
func sendErrorResponse(client *Client, errorCode, message string)
```

## Phase 6: Connection Management Updates (45 minutes)

### Enhanced Client Tracking
```go
// Update handleClient to support userID
func handleClient(client *Client, userID string)

// Update connection handler
func handleConnections(w http.ResponseWriter, r *http.Request) {
    userID := r.URL.Query().Get("userID") // Now required
    if userID == "" {
        http.Error(w, "UserID required", http.StatusBadRequest)
        return
    }
    // ... rest of connection logic
}
```

### Cleanup Logic Updates
```go
// Update unregister logic to handle rooms
case client := <-unregister:
    // Remove client from all rooms
    for roomID := range client.rooms {
        leaveRoom(roomID, client.userID)
    }
    // Existing cleanup logic...
```

## Phase 7: Backward Compatibility (30 minutes)

### Legacy Support
```go
// Keep validateChatID for old format
func validateChatID(chatID string) bool // Existing function

// Handle legacy sync events
func handleLegacySync(client *Client, message Message) {
    // Convert chatId to temporary room or maintain old behavior
    log.Printf("Legacy sync event - consider upgrading to room format")
    // Forward to existing sync logic
}
```

### Migration Helpers
```go
// Convert legacy chatId to room format
func migrateLegacySession(chatID string) string // Returns roomID

// Support both channel formats during transition
func normalizeChannel(channel string) string
```

## Phase 8: WebSocket Message Protocol (Coordination with Frontend)

### Outgoing Messages (Server → Client)
```go
// Room events
{
    "channel": "room-{roomId}",
    "event": "participant_joined",
    "data": {
        "userId": "user123",
        "userName": "John Doe",
        "role": "viewer"
    }
}

{
    "channel": "room-{roomId}",
    "event": "participant_left",
    "data": {
        "userId": "user123",
        "userName": "John Doe"
    }
}

{
    "channel": "room-{roomId}",
    "event": "host_transferred",
    "data": {
        "oldHostId": "user123",
        "newHostId": "user456",
        "newHostName": "Jane Doe"
    }
}

// Error responses
{
    "channel": "error",
    "event": "error_response",
    "data": {
        "error": "PERMISSION_DENIED",
        "message": "Only host can control video playback",
        "code": "HOST_ONLY"
    }
}
```

### Incoming Messages (Client → Server)
```go
// Room management
{
    "channel": "room-{roomId}",
    "event": "create_room",
    "data": {
        "roomName": "Movie Night",
        "userIds": ["user2", "user3"] // Optional initial invites
    }
}

{
    "channel": "room-{roomId}",
    "event": "host_sync",
    "data": {
        "timestamp": 120.5,
        "url": "video-url",
        "roomId": "room123",
        "state": "playing"
    }
}
```

## Implementation Order

1. **Data Structures** (30 min)
   - Add Room, RoomData, ErrorResponse structs
   - Update Client and SyncData structs
   - Add global room management variables

2. **Room Management Functions** (1.5 hours)
   - Implement core room CRUD operations
   - Add client room tracking methods
   - Add cleanup and host management functions

3. **Message Handlers** (1 hour)
   - Add new event type handlers
   - Implement permission validation
   - Add error response system

4. **Channel Updates** (30 min)
   - Update channel naming and validation
   - Add legacy format support

5. **Connection Management** (45 min)
   - Require userID in connections
   - Update cleanup logic for rooms
   - Handle disconnection scenarios

6. **Testing** (30 min)
   - Test multi-client room scenarios
   - Validate host permissions
   - Test cleanup and error handling

## Key Integration Points with Frontend

### WebSocket Connection
- **Frontend must provide userID** in connection query params
- **Frontend subscribes to room channels**: `room-{roomId}`
- **Frontend handles new event types**: `participant_joined`, `host_transferred`, etc.

### Data Flow
1. **Frontend creates room** via API → **WebSocket notifies participants**
2. **Frontend joins room** via API → **WebSocket manages real-time state**
3. **Frontend sends host_sync** → **WebSocket broadcasts to room participants**
4. **Frontend handles errors** → **WebSocket sends structured error responses**

### Testing Coordination
- **WebSocket server** handles room state and real-time communication
- **Frontend** handles room persistence, user management, and UI
- **Both coordinate** on message formats and error handling

## Estimated Timeline
- **Total Implementation**: 4.5 hours
- **Testing & Integration**: 1 hour
- **Documentation**: 30 minutes
- **Total**: 6 hours

This plan coordinates with the frontend to ensure seamless integration while maintaining backward compatibility and proper separation of concerns.