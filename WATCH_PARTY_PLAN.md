# Go WebSocket Server - Watch Party Implementation Plan

## Overview
Convert the existing 1-on-1 WebSocket server to support multi-person watch parties with host/viewer roles and room-based communication.

## Phase 1: Data Structures & Types (30 minutes)

### New Structs
```go
// Room represents a watch party room
type Room struct {
    ID           string    `json:"id"`
    HostID       string    `json:"hostId"`
    Name         string    `json:"name"`
    Participants []string  `json:"participants"`
    IsActive     bool      `json:"isActive"`
    CreatedAt    time.Time `json:"createdAt"`
    CurrentVideo SyncData  `json:"currentVideo"`
}

// RoomData for room creation/management events
type RoomData struct {
    RoomID   string `json:"roomId"`
    RoomName string `json:"roomName,omitempty"`
    UserID   string `json:"userId"`
}
```

### Update Existing Structs
- Add `UserID` and `Role` fields to `Client` struct
- Update `SyncData` to use `RoomID` instead of `ChatID`
- Add global `rooms` map for room state management

## Phase 2: Room Management System (2 hours)

### New Functions
- `createRoom(hostID, roomName string) *Room`
- `joinRoom(roomID, userID string) error`
- `leaveRoom(roomID, userID string) error`
- `validateRoomID(roomID string) bool`
- `isRoomHost(roomID, userID string) bool`
- `transferHost(roomID, newHostID string) error`

### Room State Management
- Track active rooms in memory
- Handle host disconnection scenarios
- Clean up empty rooms

## Phase 3: Message Handler Updates (1 hour)

### New Event Types
```go
case "create_room":
    // Create new watch party room
case "join_room":
    // Add user to existing room
case "leave_room":
    // Remove user from room
case "host_sync":
    // Video sync from host only (replace current "sync")
case "transfer_host":
    // Transfer host privileges
```

### Channel Format Changes
- From: `sync-user1--user2`
- To: `room-{roomId}` for both sync and chat
- Update all channel validation logic

## Phase 4: Permission System (30 minutes)

### Host-Only Events
- Only hosts can send `host_sync` events
- Only hosts can `transfer_host`
- Add role validation before processing sync messages

### Error Responses
- Send error messages back to clients for unauthorized actions
- Add proper logging for permission violations

## Phase 5: Connection Management Updates (30 minutes)

### Enhanced Client Tracking
- Track which rooms each client is in
- Update unregister logic to remove from all rooms
- Handle host disconnection gracefully

### Cleanup Logic
- Remove clients from rooms on disconnect
- Delete empty rooms
- Transfer host if needed

## Phase 6: Backward Compatibility (30 minutes)

### Legacy Support
- Keep existing `validateChatID` for transition period
- Support both old and new channel formats
- Add deprecation warnings

## Implementation Order

1. **Start with data structures** - Define Room, update Client
2. **Add room management functions** - Core CRUD operations
3. **Update message handlers** - New event types and validation
4. **Implement permission system** - Host-only controls
5. **Update connection management** - Enhanced cleanup
6. **Add backward compatibility** - Smooth transition

## Testing Strategy

1. **Unit tests** for room management functions
2. **WebSocket testing** with multiple clients
3. **Host disconnection scenarios**
4. **Permission validation testing**
5. **Load testing** with multiple rooms

## Key Files to Modify

- `main.go` - All changes will be in this single file
- No new dependencies needed
- Estimated total: ~400-500 lines of new/modified code

## Deployment Considerations

- Zero downtime deployment possible
- Backward compatible during transition
- Environment variables for room limits (optional)
- Memory usage will increase with active rooms

## Frontend Integration Points

### New WebSocket Events (to match frontend expectations)
- `create_room` - Host creates a watch party room
- `join_room` - Viewers join existing room
- `leave_room` - Leave the room
- `host_sync` - Only from host (replaces current `sync`)
- `transfer_host` - Transfer host privileges

### Channel Format
- Room channels: `room-{roomId}`
- Compatible with frontend's room-based routing: `/watch/room/[roomId]`

### Data Structures Alignment
- Room IDs will be UUIDs (compatible with frontend database)
- User IDs will match frontend user system
- Sync data format remains the same (just roomId instead of chatId)

## Error Handling

### Client Error Responses
```go
type ErrorResponse struct {
    Error   string `json:"error"`
    Message string `json:"message"`
    Code    string `json:"code"`
}
```

### Error Scenarios
- Room not found
- User not authorized (non-host trying to sync)
- Room full (if we add capacity limits)
- Invalid room ID format