package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"sync"
)

// Binary message types for high-frequency updates
const (
	BinaryMsgAvatarUpdate    = 0x01
	BinaryMsgAvatarBatch     = 0x02
	BinaryMsgPresenceUpdate  = 0x03
	BinaryMsgHostSync        = 0x04
	BinaryMsgCinemaAnimation = 0x05
	BinaryMsgVideoReaction   = 0x06
)

// Buffer pool for binary message encoding (reduces GC pressure)
var binaryBufferPool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, 0, 512) // Pre-allocate common size for avatar updates
		return &buf
	},
}

// getBuffer gets a buffer from the pool
func getBuffer() *[]byte {
	return binaryBufferPool.Get().(*[]byte)
}

// putBuffer returns a buffer to the pool after resetting it
func putBuffer(buf *[]byte) {
	*buf = (*buf)[:0] // Reset length, keep capacity
	binaryBufferPool.Put(buf)
}

// decodeBinaryAvatarUpdate extracts roomID from a binary avatar update message.
// Format: [type:u8][px:f32][py:f32][pz:f32][ry:f32][anim:u8][userIdLen:u8][userNameLen:u8][roomIdLen:u8][avatarModelLen:u8][strings...]
func decodeBinaryAvatarUpdate(data []byte) (roomID string, err error) {
	if len(data) < 22 { // Minimum: 1+16+1+4 = 22 bytes
		return "", fmt.Errorf("binary message too short")
	}
	if data[0] != BinaryMsgAvatarUpdate {
		return "", fmt.Errorf("not an avatar update message")
	}

	// Skip: type(1) + position(12) + rotation(4) + anim(1) = 18 bytes
	offset := 18
	userIdLen := int(data[offset])
	userNameLen := int(data[offset+1])
	roomIdLen := int(data[offset+2])
	// avatarModelLen := int(data[offset+3])
	offset += 4

	// Skip userId and userName to get to roomId
	offset += userIdLen + userNameLen

	if offset+roomIdLen > len(data) {
		return "", fmt.Errorf("invalid string lengths")
	}

	roomID = string(data[offset : offset+roomIdLen])
	return roomID, nil
}

// decodeBinaryHostSync extracts roomID from binary host sync message
// Format: msgType(1) + timestamp(8) + sentAt(8) + state(1) + reason(1) + roomIdLen(1) + urlLen(2) + videoIdLen(1) + roomId + url + videoId
func decodeBinaryHostSync(data []byte) (roomID string, err error) {
	if len(data) < 23 { // Minimum: 1+8+8+1+1+1+2+1 = 23 bytes header
		return "", fmt.Errorf("binary host sync message too short")
	}
	if data[0] != BinaryMsgHostSync {
		return "", fmt.Errorf("not a host sync message")
	}

	// Skip to lengths: type(1) + timestamp(8) + sentAt(8) + state(1) + reason(1) = 19
	offset := 19
	roomIdLen := int(data[offset])
	offset += 1 + 2 + 1 // Skip roomIdLen(1) + urlLen(2) + videoIdLen(1)

	if offset+roomIdLen > len(data) {
		return "", fmt.Errorf("invalid room ID length")
	}

	roomID = string(data[offset : offset+roomIdLen])
	return roomID, nil
}

// binaryHostSyncToSyncData converts binary host sync to SyncData struct for storage
func binaryHostSyncToSyncData(data []byte) *SyncData {
	if len(data) < 23 {
		return nil
	}

	offset := 1 // Skip message type

	timestamp := math.Float64frombits(binary.LittleEndian.Uint64(data[offset:]))
	offset += 8
	sentAt := int64(binary.LittleEndian.Uint64(data[offset:]))
	offset += 8

	stateIdx := data[offset]
	offset++
	reasonIdx := data[offset]
	offset++

	states := []string{"paused", "playing"}
	reasons := []string{"tick", "play", "pause", "seek", "load", "resync", "ended"}

	state := "paused"
	if int(stateIdx) < len(states) {
		state = states[stateIdx]
	}
	reason := "tick"
	if int(reasonIdx) < len(reasons) {
		reason = reasons[reasonIdx]
	}

	roomIdLen := int(data[offset])
	offset++
	urlLen := int(binary.LittleEndian.Uint16(data[offset:]))
	offset += 2
	videoIdLen := int(data[offset])
	offset++

	if offset+roomIdLen+urlLen+videoIdLen > len(data) {
		return nil
	}

	roomID := string(data[offset : offset+roomIdLen])
	offset += roomIdLen
	url := string(data[offset : offset+urlLen])
	offset += urlLen
	videoID := string(data[offset : offset+videoIdLen])

	return &SyncData{
		Timestamp: timestamp,
		URL:       url,
		RoomID:    roomID,
		State:     state,
		VideoID:   videoID,
		SentAt:    sentAt,
		Reason:    reason,
	}
}

// decodeBinaryCinemaAnimation extracts roomID from binary cinema animation message
// Format: msgType(1) + animIndex(1) + roomIdLen(1) + userIdLen(1) + userNameLen(1) + roomId + userId + userName
func decodeBinaryCinemaAnimation(data []byte) (roomID string, err error) {
	if len(data) < 6 { // Minimum: 1+1+1+1+1+1 = 6 bytes header + at least 1 char roomId
		return "", fmt.Errorf("binary cinema animation message too short")
	}
	if data[0] != BinaryMsgCinemaAnimation {
		return "", fmt.Errorf("not a cinema animation message")
	}

	// Skip: type(1) + animIndex(1) = 2
	offset := 2
	roomIdLen := int(data[offset])
	offset += 3 // Skip roomIdLen(1) + userIdLen(1) + userNameLen(1)

	if offset+roomIdLen > len(data) {
		return "", fmt.Errorf("invalid room ID length")
	}

	roomID = string(data[offset : offset+roomIdLen])
	return roomID, nil
}

// decodeBinaryVideoReaction extracts roomID from binary video reaction message
// Format: msgType(1) + videoTimestamp(8) + roomIdLen(1) + userIdLen(1) + userNameLen(1) + emojiLen(1) + reactionIdLen(1) + roomId + ...
func decodeBinaryVideoReaction(data []byte) (roomID string, err error) {
	if len(data) < 15 { // Minimum: 1+8+1+1+1+1+1+1 = 15 bytes header + at least 1 char roomId
		return "", fmt.Errorf("binary video reaction message too short")
	}
	if data[0] != BinaryMsgVideoReaction {
		return "", fmt.Errorf("not a video reaction message")
	}

	// Skip: type(1) + videoTimestamp(8) = 9
	offset := 9
	roomIdLen := int(data[offset])
	offset += 5 // Skip roomIdLen(1) + userIdLen(1) + userNameLen(1) + emojiLen(1) + reactionIdLen(1)

	if offset+roomIdLen > len(data) {
		return "", fmt.Errorf("invalid room ID length")
	}

	roomID = string(data[offset : offset+roomIdLen])
	return roomID, nil
}

// encodeBinaryAvatarFromJSON converts JSON avatar data to binary format for efficient broadcast
func encodeBinaryAvatarFromJSON(data json.RawMessage) ([]byte, error) {
	var avatar struct {
		RoomID      string  `json:"roomId"`
		UserID      string  `json:"userId"`
		UserName    string  `json:"userName"`
		PX          float64 `json:"px"`
		PY          float64 `json:"py"`
		PZ          float64 `json:"pz"`
		RY          float64 `json:"ry"`
		Anim        string  `json:"anim"`
		AvatarModel string  `json:"avatarModel,omitempty"`
	}
	if err := json.Unmarshal(data, &avatar); err != nil {
		return nil, err
	}

	animIndex := getAnimationIndex(avatar.Anim)
	userIdBytes := []byte(avatar.UserID)
	userNameBytes := []byte(avatar.UserName)
	roomIdBytes := []byte(avatar.RoomID)
	avatarModelBytes := []byte(avatar.AvatarModel)

	totalSize := 1 + 16 + 1 + 4 + len(userIdBytes) + len(userNameBytes) + len(roomIdBytes) + len(avatarModelBytes)
	buf := make([]byte, totalSize)
	offset := 0

	buf[offset] = BinaryMsgAvatarUpdate
	offset++

	binary.LittleEndian.PutUint32(buf[offset:], math.Float32bits(float32(avatar.PX)))
	offset += 4
	binary.LittleEndian.PutUint32(buf[offset:], math.Float32bits(float32(avatar.PY)))
	offset += 4
	binary.LittleEndian.PutUint32(buf[offset:], math.Float32bits(float32(avatar.PZ)))
	offset += 4
	binary.LittleEndian.PutUint32(buf[offset:], math.Float32bits(float32(avatar.RY)))
	offset += 4

	buf[offset] = animIndex
	offset++

	buf[offset] = byte(len(userIdBytes))
	buf[offset+1] = byte(len(userNameBytes))
	buf[offset+2] = byte(len(roomIdBytes))
	buf[offset+3] = byte(len(avatarModelBytes))
	offset += 4

	copy(buf[offset:], userIdBytes)
	offset += len(userIdBytes)
	copy(buf[offset:], userNameBytes)
	offset += len(userNameBytes)
	copy(buf[offset:], roomIdBytes)
	offset += len(roomIdBytes)
	copy(buf[offset:], avatarModelBytes)

	return buf, nil
}

// getAnimationIndex converts animation name to index for binary encoding
func getAnimationIndex(anim string) byte {
	animations := map[string]byte{
		"idle": 0, "walk": 1, "sprint": 2, "sit": 3, "jump": 4,
		"fall": 5, "crouch": 6, "die": 7, "emote-yes": 8, "emote-no": 9,
		"interact-right": 10, "interact-left": 11, "attack-kick-right": 12,
		"slap": 13,
	}
	if idx, ok := animations[anim]; ok {
		return idx
	}
	return 0
}

// binaryAvatarToJSON converts binary avatar data to JSON for storage
func binaryAvatarToJSON(data []byte) json.RawMessage {
	if len(data) < 22 {
		return nil
	}

	offset := 1 // Skip message type

	px := math.Float32frombits(binary.LittleEndian.Uint32(data[offset:]))
	offset += 4
	py := math.Float32frombits(binary.LittleEndian.Uint32(data[offset:]))
	offset += 4
	pz := math.Float32frombits(binary.LittleEndian.Uint32(data[offset:]))
	offset += 4
	ry := math.Float32frombits(binary.LittleEndian.Uint32(data[offset:]))
	offset += 4

	animIndex := data[offset]
	offset++

	animations := []string{"idle", "walk", "sprint", "sit", "jump", "fall", "crouch", "die", "emote-yes", "emote-no", "interact-right", "interact-left", "attack-kick-right", "slap"}
	anim := "idle"
	if int(animIndex) < len(animations) {
		anim = animations[animIndex]
	}

	userIdLen := int(data[offset])
	userNameLen := int(data[offset+1])
	roomIdLen := int(data[offset+2])
	avatarModelLen := int(data[offset+3])
	offset += 4

	if offset+userIdLen+userNameLen+roomIdLen+avatarModelLen > len(data) {
		return nil
	}

	userId := string(data[offset : offset+userIdLen])
	offset += userIdLen
	userName := string(data[offset : offset+userNameLen])
	offset += userNameLen
	roomId := string(data[offset : offset+roomIdLen])
	offset += roomIdLen
	avatarModel := ""
	if avatarModelLen > 0 {
		avatarModel = string(data[offset : offset+avatarModelLen])
	}

	jsonData, _ := json.Marshal(map[string]interface{}{
		"roomId":      roomId,
		"userId":      userId,
		"userName":    userName,
		"px":          px,
		"py":          py,
		"pz":          pz,
		"ry":          ry,
		"anim":        anim,
		"avatarModel": avatarModel,
	})

	return jsonData
}
