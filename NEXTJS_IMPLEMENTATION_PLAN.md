# Next.js Frontend - Watch Party Implementation Plan

## Overview

Implement multi-person watch parties in the Next.js frontend, coordinated with the WebSocket server implementation. Focus on database schema, API routes, and UI components.

## Phase 1: Database Schema Updates (30 minutes)

### New Tables in `src/db/schema.ts`

```typescript
// Room table
export const room = creator("room", {
  id: text("id").primaryKey(),
  name: text("name").notNull(),
  hostId: text("host_id")
    .notNull()
    .references(() => user.id, { onDelete: "cascade" }),
  isActive: boolean("is_active").notNull().default(true),
  maxParticipants: text("max_participants").default("10"), // Optional limit
  roomCode: text("room_code").unique(), // Optional invite code
  createdAt: timestamp("created_at").notNull().defaultNow(),
  updatedAt: timestamp("updated_at").notNull().defaultNow(),
});

// Room participants
export const roomParticipant = creator("room_participant", {
  id: text("id").primaryKey(),
  roomId: text("room_id")
    .notNull()
    .references(() => room.id, { onDelete: "cascade" }),
  userId: text("user_id")
    .notNull()
    .references(() => user.id, { onDelete: "cascade" }),
  role: text("role").notNull(), // "host" | "viewer"
  joinedAt: timestamp("joined_at").notNull().defaultNow(),
});

// Room messages (separate from direct messages)
export const roomMessage = creator("room_message", {
  id: text("id").primaryKey(),
  roomId: text("room_id")
    .notNull()
    .references(() => room.id, { onDelete: "cascade" }),
  senderId: text("sender_id")
    .notNull()
    .references(() => user.id, { onDelete: "cascade" }),
  text: text("text").notNull(),
  timestamp: timestamp("timestamp").notNull().defaultNow(),
  createdAt: timestamp("created_at").notNull().defaultNow(),
  updatedAt: timestamp("updated_at").notNull().defaultNow(),
});

// Room invitations
export const roomInvitation = creator("room_invitation", {
  id: text("id").primaryKey(),
  roomId: text("room_id")
    .notNull()
    .references(() => room.id, { onDelete: "cascade" }),
  inviterId: text("inviter_id")
    .notNull()
    .references(() => user.id, { onDelete: "cascade" }),
  inviteeId: text("invitee_id")
    .notNull()
    .references(() => user.id, { onDelete: "cascade" }),
  status: text("status").notNull().default("pending"), // pending, accepted, declined
  createdAt: timestamp("created_at").notNull().defaultNow(),
  updatedAt: timestamp("updated_at").notNull().defaultNow(),
});
```

### Relations

```typescript
export const roomRelations = relations(room, ({ one, many }) => ({
  host: one(user, {
    fields: [room.hostId],
    references: [user.id],
  }),
  participants: many(roomParticipant),
  messages: many(roomMessage),
  invitations: many(roomInvitation),
}));

export const roomParticipantRelations = relations(roomParticipant, ({ one }) => ({
  room: one(room, {
    fields: [roomParticipant.roomId],
    references: [room.id],
  }),
  user: one(user, {
    fields: [roomParticipant.userId],
    references: [user.id],
  }),
}));

export const roomMessageRelations = relations(roomMessage, ({ one }) => ({
  room: one(room, {
    fields: [roomMessage.roomId],
    references: [room.id],
  }),
  sender: one(user, {
    fields: [roomMessage.senderId],
    references: [user.id],
  }),
}));

export const roomInvitationRelations = relations(roomInvitation, ({ one }) => ({
  room: one(room, {
    fields: [roomInvitation.roomId],
    references: [room.id],
  }),
  inviter: one(user, {
    fields: [roomInvitation.inviterId],
    references: [user.id],
  }),
  invitee: one(user, {
    fields: [roomInvitation.inviteeId],
    references: [user.id],
  }),
}));

// Update user relations to include rooms
export const userRelations = relations(user, ({ many }) => ({
  // ... existing relations
  hostedRooms: many(room),
  roomParticipations: many(roomParticipant),
  roomMessages: many(roomMessage),
  sentRoomInvitations: many(roomInvitation, { relationName: "sentRoomInvitations" }),
  receivedRoomInvitations: many(roomInvitation, { relationName: "receivedRoomInvitations" }),
}));
```

### Types Export

```typescript
export type Room = typeof room.$inferSelect;
export type NewRoom = typeof room.$inferInsert;
export type RoomParticipant = typeof roomParticipant.$inferSelect;
export type NewRoomParticipant = typeof roomParticipant.$inferInsert;
export type RoomMessage = typeof roomMessage.$inferSelect;
export type NewRoomMessage = typeof roomMessage.$inferInsert;
export type RoomInvitation = typeof roomInvitation.$inferSelect;
export type NewRoomInvitation = typeof roomInvitation.$inferInsert;
```

## Phase 2: Database Queries (1.5 hours)

### New Functions in `src/db/queries.ts`

```typescript
// Room management
export async function createRoom(
  hostId: string,
  name: string,
  initialParticipants: string[] = []
): Promise<Room>

export async function getRoomById(roomId: string): Promise<Room & {
  host: User;
  participants: (RoomParticipant & { user: User })[];
  messages: (RoomMessage & { sender: User })[];
}>

export async function getUserRooms(userId: string): Promise<Room[]>

export async function joinRoom(roomId: string, userId: string): Promise<void>

export async function leaveRoom(roomId: string, userId: string): Promise<void>

export async function transferRoomHost(roomId: string, newHostId: string): Promise<void>

export async function validateRoomAccess(roomId: string, userId: string): Promise<boolean>

export async function isRoomHost(roomId: string, userId: string): Promise<boolean>

// Room messages
export async function sendRoomMessage(
  roomId: string,
  senderId: string,
  text: string
): Promise<RoomMessage>

export async function getRoomMessages(
  roomId: string,
  limit: number = 50
): Promise<(RoomMessage & { sender: User })[]>

// Room invitations
export async function createRoomInvitation(
  roomId: string,
  inviterId: string,
  inviteeId: string
): Promise<RoomInvitation>

export async function respondToRoomInvitation(
  invitationId: string,
  status: "accepted" | "declined"
): Promise<void>

export async function getUserRoomInvitations(userId: string): Promise<(RoomInvitation & {
  room: Room & { host: User };
  inviter: User;
})[]>

// Utility functions
export async function generateRoomCode(): Promise<string>

export async function getRoomByCode(code: string): Promise<Room | null>

export async function deactivateRoom(roomId: string): Promise<void>
```

## Phase 3: API Routes (2 hours)

### Room Management Routes

#### `src/app/api/rooms/create/route.ts`

```typescript
export async function POST(request: Request) {
  // Create new room with host and optional initial participants
  // Body: { name: string, inviteUserIds?: string[] }
  // Response: { room: Room, participants: User[] }
}
```

#### `src/app/api/rooms/[roomId]/route.ts`

```typescript
export async function GET(request: Request, { params }: { params: { roomId: string } }) {
  // Get room details with participants and recent messages
  // Response: { room: Room, participants: User[], messages: RoomMessage[] }
}

export async function DELETE(request: Request, { params }: { params: { roomId: string } }) {
  // Leave room or deactivate if host
  // Response: { success: boolean }
}
```

#### `src/app/api/rooms/[roomId]/join/route.ts`

```typescript
export async function POST(request: Request, { params }: { params: { roomId: string } }) {
  // Join existing room
  // Response: { success: boolean, room: Room }
}
```

#### `src/app/api/rooms/[roomId]/transfer-host/route.ts`

```typescript
export async function POST(request: Request, { params }: { params: { roomId: string } }) {
  // Transfer host to another participant
  // Body: { newHostId: string }
  // Response: { success: boolean }
}
```

#### `src/app/api/rooms/[roomId]/messages/route.ts`

```typescript
export async function GET(request: Request, { params }: { params: { roomId: string } }) {
  // Get room messages
  // Query: ?limit=50
  // Response: { messages: RoomMessage[] }
}

export async function POST(request: Request, { params }: { params: { roomId: string } }) {
  // Send room message
  // Body: { text: string }
  // Response: { message: RoomMessage }
}
```

### Invitation Routes

#### `src/app/api/rooms/[roomId]/invite/route.ts`

```typescript
export async function POST(request: Request, { params }: { params: { roomId: string } }) {
  // Invite users to room
  // Body: { userIds: string[] }
  // Response: { invitations: RoomInvitation[] }
}
```

#### `src/app/api/invitations/[invitationId]/respond/route.ts`

```typescript
export async function POST(request: Request, { params }: { params: { invitationId: string } }) {
  // Respond to room invitation
  // Body: { status: "accepted" | "declined" }
  // Response: { success: boolean, room?: Room }
}
```

#### `src/app/api/users/me/rooms/route.ts`

```typescript
export async function GET(request: Request) {
  // Get user's rooms and pending invitations
  // Response: { rooms: Room[], invitations: RoomInvitation[] }
}
```

## Phase 4: WebSocket Client Updates (1 hour)

### WebSocket Service Updates in `src/lib/websocket.ts`

```typescript
// Add new event types
export type WebSocketEvent =
  | "incoming_message"
  | "new_message"
  | "new_friend"
  | "incoming_friend_request"
  | "friend_request_denied"
  | "friend_request_accepted"
  | "friend_removed"
  | "subscribe"
  | "unsubscribe"
  // NEW ROOM EVENTS
  | "create_room"
  | "join_room"
  | "leave_room"
  | "host_sync"          // Replaces "sync" for rooms
  | "room_message"
  | "participant_joined"
  | "participant_left"
  | "host_transferred"
  | "error_response";

// Room-specific message types
export interface RoomSyncData {
  timestamp: number;
  url: string;
  roomId: string;
  state: "playing" | "paused";
}

export interface RoomParticipantData {
  userId: string;
  userName: string;
  role: "host" | "viewer";
}

export interface HostTransferData {
  oldHostId: string;
  newHostId: string;
  newHostName: string;
}

// WebSocket service functions
export function subscribeToRoom(roomId: string): void {
  sendMessage({
    channel: `room-${roomId}`,
    event: "subscribe",
    data: {}
  });
}

export function sendHostSync(roomId: string, syncData: RoomSyncData): void {
  sendMessage({
    channel: `room-${roomId}`,
    event: "host_sync",
    data: syncData
  });
}

export function sendRoomMessage(roomId: string, text: string): void {
  sendMessage({
    channel: `room-${roomId}`,
    event: "room_message",
    data: { text, roomId }
  });
}

// Connection setup with userID
export function connectWithUserID(userID: string): void {
  // Update connection to include userID in query params
  const wsUrl = `${process.env.NEXT_PUBLIC_WS_URL}/ws?userID=${userID}`;
  // ... connection logic
}
```

## Phase 5: URL Structure & Routing (1 hour)

### New Route Structure

```text
src/app/(watch)/watch/room/[roomId]/
├── page.tsx          # Room watch page
├── layout.tsx        # Room-specific layout
└── loading.tsx       # Loading state
```

### Route Components

#### `src/app/(watch)/watch/room/[roomId]/page.tsx`

```typescript
interface RoomWatchPageProps {
  params: { roomId: string };
}

export default async function RoomWatchPage({ params }: RoomWatchPageProps) {
  // Fetch room data, validate access
  // Render VideoPlayer with room props
  // Handle error states (room not found, no access)
}
```

#### `src/app/(watch)/watch/room/[roomId]/layout.tsx`

```typescript
export default async function RoomLayout({
  children,
  params,
}: {
  children: React.ReactNode;
  params: { roomId: string };
}) {
  // Room-specific layout with participants sidebar
  // Room name in header
  // Leave room button
}
```

### Migration Middleware

#### `src/middleware.ts` (update existing)

```typescript
// Add redirection for old chatId format
export function middleware(request: NextRequest) {
  const { pathname } = request.nextUrl;

  // Redirect old watch URLs to room format
  if (pathname.match(/^\/watch\/[^\/]+--[^\/]+$/)) {
    const chatId = pathname.split('/')[2];
    // Create temporary room or redirect to create room page
    return NextResponse.redirect(new URL(`/dashboard?migrate=${chatId}`, request.url));
  }

  // ... existing middleware logic
}
```

## Phase 6: Component Updates (3 hours)

### 1. VideoPlayer Component Updates

#### `src/components/VideoPlayer.tsx` (major update)

```typescript
interface VideoPlayerProps {
  roomId: string;           // Changed from chatId
  userRole: "host" | "viewer";
  participants: (User & { role: string })[];
  initialUrl?: string;
  className?: string;
}

export default function VideoPlayer({
  roomId,
  userRole,
  participants,
  initialUrl,
  className
}: VideoPlayerProps) {
  // Host-only controls
  // Participant list display
  // Room-based WebSocket communication
  // Permission-based UI rendering
}
```

**Key Changes:**

- Replace `chatId` with `roomId`
- Add `userRole` prop for permission checks
- Show URL input only to host
- Display "Only host can control" message for viewers
- Add participant list with avatars
- Use `host_sync` event instead of `sync`

### 2. New Room Management Components

#### `src/components/CreateRoomModal.tsx`

```typescript
interface CreateRoomModalProps {
  isOpen: boolean;
  onClose: () => void;
  onRoomCreated: (room: Room) => void;
  friends: User[];
}

export default function CreateRoomModal(props: CreateRoomModalProps) {
  // Room name input
  // Friend selection with checkboxes
  // Create room API call
  // Redirect to room on success
}
```

#### `src/components/RoomParticipantsList.tsx`

```typescript
interface RoomParticipantsListProps {
  participants: (User & { role: string })[];
  currentUserId: string;
  isHost: boolean;
  roomId: string;
  onHostTransfer?: (newHostId: string) => void;
  onParticipantRemove?: (userId: string) => void;
}

export default function RoomParticipantsList(props: RoomParticipantsListProps) {
  // Display participant avatars and names
  // Show host badge
  // Transfer host controls (host only)
  // Remove participant button (host only)
}
```

#### `src/components/RoomInviteModal.tsx`

```typescript
interface RoomInviteModalProps {
  room: Room;
  friends: User[];
  isOpen: boolean;
  onClose: () => void;
}

export default function RoomInviteModal(props: RoomInviteModalProps) {
  // Copy room link functionality
  // Send invites to selected friends
  // Room code display
  // Shareable link generation
}
```

### 3. Messages Component Updates

#### `src/components/Messages.tsx` (update existing)

```typescript
interface MessagesProps {
  // Support both chatId (legacy) and roomId
  chatId?: string;
  roomId?: string;
  // ... existing props
}

export default function Messages(props: MessagesProps) {
  // Detect if room or direct messages
  // Handle room message display differently
  // Show participant names for room messages
  // Display join/leave notifications
}
```

### 4. Dashboard Integration Components

#### `src/components/UserRoomsSection.tsx`

```typescript
interface UserRoomsSectionProps {
  rooms: Room[];
  onCreateRoom: () => void;
  onJoinRoom: (roomId: string) => void;
}

export default function UserRoomsSection(props: UserRoomsSectionProps) {
  // Display active rooms user is in
  // "Create Watch Party" button
  // Quick join room functionality
  // Room status indicators
}
```

#### `src/components/RoomInvitationsSection.tsx`

```typescript
interface RoomInvitationsSectionProps {
  invitations: (RoomInvitation & { room: Room; inviter: User })[];
  onRespond: (invitationId: string, status: "accepted" | "declined") => void;
}

export default function RoomInvitationsSection(props: RoomInvitationsSectionProps) {
  // Display pending room invitations
  // Accept/decline buttons
  // Room details preview
  // Inviter information
}
```

## Phase 7: Dashboard Updates (1.5 hours)

### Dashboard Page Updates

#### `src/app/(dashboard)/dashboard/page.tsx`

```typescript
export default async function DashboardPage() {
  // Fetch user's rooms and invitations
  // Add room management section
  // Integrate with existing friends/movies sections
}
```

### Friends Page Updates

#### `src/app/(dashboard)/dashboard/friends/page.tsx`

```typescript
export default async function FriendsPage() {
  // Add "Invite to Watch Party" button for each friend
  // Show friends' active public rooms
  // Integration with room invitation system
}
```

### Movies Page Updates

#### `src/app/(dashboard)/dashboard/movies/page.tsx`

```typescript
export default async function MoviesPage() {
  // Add "Watch Together" button for movies
  // Integration with room creation flow
  // Pre-populate video URL when creating room
}
```

## Phase 8: Migration & Backward Compatibility (1 hour)

### Migration Components

#### `src/components/ChatIdMigrationModal.tsx`

```typescript
interface ChatIdMigrationModalProps {
  chatId: string;
  onMigrate: (roomId: string) => void;
  onCancel: () => void;
}

export default function ChatIdMigrationModal(props: ChatIdMigrationModalProps) {
  // Explain migration to room system
  // Option to create room with existing chat partner
  // Option to continue with direct messaging
}
```

### Legacy Route Handlers

#### `src/app/(watch)/watch/[chatId]/page.tsx` (update existing)

```typescript
export default async function LegacyWatchPage({ params }: { params: { chatId: string } }) {
  // Check if chatId is old format (user1--user2)
  // Show migration modal
  // Redirect to room creation or direct room
}
```

## Implementation Timeline

### Phase 1: Database & Queries (2 hours)

- Database schema updates
- Query functions implementation

### Phase 2: API Routes (2 hours)

- Room management endpoints
- Invitation system endpoints

### Phase 3: WebSocket & Routing (2 hours)

- WebSocket client updates
- URL structure migration
- Middleware updates

### Phase 4: Core Components (3 hours)

- VideoPlayer updates
- Room management components
- Messages component updates

### Phase 5: Dashboard Integration (1.5 hours)

- Dashboard updates
- Friends/Movies page integration

### Phase 6: Migration & Testing (1.5 hours)

- Backward compatibility
- Migration components
- Testing and polish

#### **Total Estimated Time: 12 hours**

## Coordination Points with WebSocket Server

### Data Synchronization

1. **Frontend creates room in database** → **WebSocket manages real-time state**
2. **Frontend provides userID in WebSocket connection**
3. **Frontend handles WebSocket room events** → **Updates local UI state**
4. **Frontend API validates permissions** → **WebSocket enforces real-time permissions**

### Error Handling

- **Frontend shows user-friendly error messages**
- **WebSocket provides structured error responses**
- **Both systems validate user permissions independently**

### Testing Integration

- **Frontend unit tests** for components and API routes
- **WebSocket integration tests** for real-time functionality
- **End-to-end tests** for complete user flows

This plan coordinates seamlessly with the WebSocket server implementation while maintaining clean separation of concerns and providing a great user experience.
