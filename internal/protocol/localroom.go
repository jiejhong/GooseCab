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
	// PrivateMentionEventIDs identifies this reader's unread mentions in the
	// page when the room's durable events are also available to legacy readers.
	PrivateMentionEventIDs []string `json:"private_mention_event_ids,omitempty"`
	RoomID                 string   `json:"room_id"`
	AgentID                string   `json:"agent_id"`
	Events                 []Event  `json:"events"`
	NextSeq                int64    `json:"next_seq"`
	NextCursor             string   `json:"next_cursor,omitempty"`
	HasMore                bool     `json:"has_more"`
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

type MentionReply struct {
	EventID   string `json:"event_id"`
	MessageID string `json:"message_id"`
	Text      string `json:"text"`
}

// ValidMentionEventID accepts generated UUIDs and stable, agent-prefixed
// message IDs without whitespace or input that has to be escaped in a room URL.
func ValidMentionEventID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '.' && c != '-' && c != '_' && c != ':' {
			return false
		}
	}
	return true
}
