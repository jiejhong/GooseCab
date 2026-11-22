package protocol

const (
	LocalRoomPullMode  = "pull"
	LocalRoomPageBytes = 256 << 10
	LocalRoomPageLimit = 50
)

type LocalRoomReadQuery struct {
	AfterSeq    int64  `json:"after_seq,omitempty"`
	AfterCursor string `json:"after_cursor,omitempty"`
	Limit       int    `json:"limit"`
}

type LocalRoomPage struct {
	RoomID     string  `json:"room_id"`
	AgentID    string  `json:"agent_id"`
	Events     []Event `json:"events"`
	NextSeq    int64   `json:"next_seq"`
	NextCursor string  `json:"next_cursor,omitempty"`
	HasMore    bool    `json:"has_more"`
}

// LocalRoomPageReceipt is private to the authenticated WebSocket and page.
type LocalRoomPageReceipt struct {
	DeliveryID string   `json:"delivery_id"`
	EventIDs   []string `json:"event_ids"`
}

type LocalRoomReadReply struct {
	Type    string                `json:"type"`
	Page    *LocalRoomPage        `json:"page,omitempty"`
	Receipt *LocalRoomPageReceipt `json:"receipt,omitempty"`
}
