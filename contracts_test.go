package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestCriticalContractsFileVersionAndEvents(t *testing.T) {
	data, err := os.ReadFile("contracts/websocket-critical-events.json")
	if err != nil {
		t.Fatalf("read contracts file: %v", err)
	}

	var parsed struct {
		Version int                        `json:"version"`
		Events  map[string]json.RawMessage `json:"events"`
	}

	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse contracts file: %v", err)
	}

	if parsed.Version != WebSocketContractVersion {
		t.Fatalf("contract version mismatch: got %d want %d", parsed.Version, WebSocketContractVersion)
	}

	required := []string{
		"host_sync",
		"room_video_state",
		"user_presence_updated",
		"room_presence_status",
		"webcam_join",
		"webcam_leave",
		"webcam_toggle",
		"webcam_state",
		"cinema_avatar_update",
		"cinema_avatar_state",
	}

	for _, event := range required {
		if _, ok := parsed.Events[event]; !ok {
			t.Fatalf("missing required contract event %q", event)
		}
	}
}
