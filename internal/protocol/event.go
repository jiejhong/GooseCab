package protocol

import "time"

type Actor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type Event struct {
	EventID       string    `json:"event_id"`
	RoomID        string    `json:"room_id"`
	Seq           int64     `json:"seq"`
	Type          string    `json:"type"`
	Actor         Actor     `json:"actor"`
	TaskID        string    `json:"task_id,omitempty"`
	CapabilityID  string    `json:"capability_id,omitempty"`
	SessionID     string    `json:"session_id,omitempty"`
	CorrelationID string    `json:"correlation_id,omitempty"`
	CausationID   string    `json:"causation_id,omitempty"`
	TS            time.Time `json:"ts"`
	Payload       any       `json:"payload,omitempty"`
}
