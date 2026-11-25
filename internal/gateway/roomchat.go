package gateway

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jiejhong/GooseCab/internal/protocol"
)

const (
	roomChatAgentPath  = "/api/room-chat/agent"
	roomChatExportPath = "/api/room-chat/export.html"
)

//go:embed web/room-chat.html web/room-chat.js
var roomChatAssets embed.FS

type RoomChat struct {
	ws     *WS
	policy *roomChatPolicy
	legacy string
	mu     sync.Mutex
	writes map[roomChatWriter]time.Time
}

type roomChatWriter struct {
	room  string
	actor protocol.Actor
}

func NewRoomChat(ctx context.Context, ws *WS, config RoomChatConfig, legacy string) (*RoomChat, error) {
	policy, err := newRoomChatPolicy(config, legacy)
	if err != nil {
		return nil, err
	}
	if ws == nil || ws.Store == nil || ws.Bus == nil {
		return nil, errors.New("room chat requires the Gateway store and event bus")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := ws.Store.EnableRoomChat(ctx); err != nil {
		return nil, errors.New("could not initialize the room chat unread table")
	}
	rooms := make([]string, 0, len(policy.rooms))
	for room, grants := range policy.rooms {
		if !grants.legacyAudience {
			rooms = append(rooms, room)
		}
	}
	slices.Sort(rooms)
	if len(rooms) == 0 {
		err = ws.Store.CheckRoomChatProtection(ctx, nil)
	} else {
		err = ws.Store.ProtectRoomChat(ctx, rooms, config.RequireUnusedRooms)
	}
	if err != nil {
		return nil, err
	}
	return &RoomChat{ws: ws, policy: policy, legacy: legacy, writes: make(map[roomChatWriter]time.Time)}, nil
}

func (c *RoomChat) ProtectedRoom(room string) bool {
	if c == nil {
		return false
	}
	grants, ok := c.policy.rooms[room]
	return ok && !grants.legacyAudience
}

func (c *RoomChat) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		scopedLegacy := false
		switch path {
		case "/api/events", "/api/events/export.html", "/api/rooms/presence", "/ws", "/api/v1/agent/connect":
			q, err := url.ParseQuery(r.URL.RawQuery)
			if err != nil {
				http.Error(w, "invalid room query", http.StatusBadRequest)
				return
			}
			for _, room := range q["room_id"] {
				scopedLegacy = scopedLegacy || c.ProtectedRoom(room)
			}
		}
		if !scopedLegacy && path != "/room-chat" && path != "/room-chat.js" && !strings.HasPrefix(path, "/api/room-chat/") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; connect-src 'self'; script-src 'self'; style-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		if r.URL.RawPath != "" || !c.policy.protectedTransport(r) {
			http.Error(w, "room chat requires its configured protected origin", http.StatusForbidden)
			return
		}
		method := http.MethodGet
		switch path {
		case "/room-chat", "/room-chat.js", "/api/room-chat/session", "/api/room-chat/history", "/api/room-chat/unread", roomChatAgentPath, roomChatExportPath:
		case "/api/room-chat/messages", "/api/room-chat/unread/ack":
			method = http.MethodPost
		default:
			if !scopedLegacy {
				http.NotFound(w, r)
				return
			}
		}
		if r.Method != method {
			w.Header().Set("Allow", method)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		origin := r.Header.Values("Origin")
		native := path == roomChatAgentPath || path == "/api/v1/agent/connect"
		if (native && len(origin) != 0) ||
			((method == http.MethodPost || len(origin) != 0) && (len(origin) != 1 || origin[0] != c.policy.origin.String())) {
			http.Error(w, "room chat requires its same origin", http.StatusForbidden)
			return
		}
		if path == "/room-chat" || path == "/room-chat.js" {
			name, typ := "room-chat.html", "text/html; charset=utf-8"
			if path == "/room-chat.js" {
				name, typ = "room-chat.js", "text/javascript; charset=utf-8"
			}
			if r.URL.RawQuery != "" {
				http.NotFound(w, r)
				return
			}
			data, err := roomChatAssets.ReadFile("web/" + name)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", typ)
			_, _ = w.Write(data)
			return
		}
		q, err := url.ParseQuery(r.URL.RawQuery)
		history := path == "/api/room-chat/history" || path == roomChatExportPath || path == "/api/events" || path == "/api/events/export.html"
		stream := path == "/ws" || path == "/api/v1/agent/connect"
		if err != nil || len(q["room_id"]) != 1 || (!history && !stream && len(q) != 1) {
			http.Error(w, "invalid room chat query", http.StatusBadRequest)
			return
		}
		room := q.Get("room_id")
		settings, enabled := c.policy.rooms[room]
		scheme, role, authorizer := "GooseCabRoom", "human", settings.humans
		if path == "/api/v1/agent/connect" || (path == "/api/events" && strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")) {
			scheme, role, authorizer = "Bearer", "agent", settings.agents
		} else if path == roomChatAgentPath {
			role, authorizer = "agent", settings.agents
		}
		token, auth := roomChatAuthorization(r, scheme)
		lease, ok := authorizer.AuthorizeRead(auth, room)
		if !enabled || !ok {
			http.Error(w, "room chat access denied", http.StatusUnauthorized)
			return
		}
		ctx, cancel := lease.Context(r.Context())
		defer cancel()
		actor := protocol.Actor{Kind: role, ID: lease.Principal().HumanID}
		if path == roomChatAgentPath {
			c.serveAgentRead(ctx, w, r, room, actor, token, settings)
			return
		}
		member := settings.members[actor]
		if stream {
			access := roomWebSocketAccess{agentID: actor.ID, write: role == "agent" && member.Write && !settings.readOnly,
				redact: newHistoryRedactor(c.legacy, token)}
			c.ws.serveRoomSocket(w, r.WithContext(ctx), &access)
			return
		}
		if method == http.MethodPost && settings.readOnly {
			http.Error(w, "room chat is read-only", http.StatusForbidden)
			return
		}
		ctx, cancelRequest := context.WithTimeout(ctx, 5*time.Second)
		defer cancelRequest()
		r = r.WithContext(ctx)
		switch path {
		case "/api/room-chat/session":
			redact := newHistoryRedactor(c.legacy, token)
			writeRoomChatJSON(w, http.StatusOK, map[string]any{"actor": protocol.Actor{Kind: "human", ID: redact.text(actor.ID)},
				"display_name": redact.text(member.DisplayName), "write": member.Write && !settings.readOnly, "broadcast": member.Broadcast && !settings.readOnly, "ack": !settings.readOnly, "legacy_audience": settings.legacyAudience})
		case "/api/room-chat/history", roomChatExportPath, "/api/events", "/api/events/export.html":
			(&HistoryAPI{Store: c.ws.Store}).serve(w, r, c.legacy, token)
		case "/api/rooms/presence":
			c.ws.roomPresence.ServeHTTP(w, r.WithContext(ctx))
		case "/api/room-chat/messages":
			c.post(w, r, room, actor, settings, token)
		case "/api/room-chat/unread":
			c.unread(w, r, room, actor, token)
		case "/api/room-chat/unread/ack":
			c.ack(w, r, room, actor)
		}
	})
}

type roomChatMessage struct {
	Text       string          `json:"text"`
	Mention    *protocol.Actor `json:"mention,omitempty"`
	MentionAll bool            `json:"mention_all,omitempty"`
}

func (c *RoomChat) post(w http.ResponseWriter, r *http.Request, room string, actor protocol.Actor, settings roomChatGrants, token string) {
	member := settings.members[actor]
	if !member.Write {
		http.Error(w, "room chat write denied", http.StatusForbidden)
		return
	}
	var input roomChatMessage
	if !readRoomChatBody(w, r, 32<<10, &input) {
		return
	}
	if !validRoomChatText(input.Text, c.legacy, token) || (input.Mention != nil && input.MentionAll) {
		http.Error(w, "invalid room chat message", http.StatusBadRequest)
		return
	}
	now := time.Now()
	var recipients []protocol.Actor
	switch {
	case input.MentionAll:
		if settings.legacyAudience || !member.Broadcast {
			http.Error(w, "room chat broadcast denied", http.StatusForbidden)
			return
		}
		for target, grant := range settings.members {
			if target != actor && now.Before(grant.ExpiresAt) {
				recipients = append(recipients, target)
			}
		}
	case input.Mention != nil:
		target := *input.Mention
		if target.Kind == "user" {
			target.Kind = "human"
		}
		if grant, ok := settings.members[target]; !ok || !now.Before(grant.ExpiresAt) {
			http.Error(w, "invalid room chat mention", http.StatusBadRequest)
			return
		}
		input.Mention = &target
		recipients = append(recipients, target)
	}
	// The stored event and broadcast are shared with the ordinary room.
	// Recipient rows are committed with it and read only by identity.
	if settings.legacyAudience {
		input.Mention = nil
	}
	event := protocol.Event{RoomID: room, Actor: actor, Type: "chat.message", Payload: struct {
		roomChatMessage
		AuthorDisplayName string `json:"author_display_name,omitempty"`
	}{input, member.DisplayName}}
	writer := roomChatWriter{room, actor}
	c.mu.Lock()
	if now.Sub(c.writes[writer]) < 500*time.Millisecond {
		c.mu.Unlock()
		w.Header().Set("Retry-After", "1")
		http.Error(w, "room chat write rate exceeded", http.StatusTooManyRequests)
		return
	}
	err := c.ws.appendAndPublish(r.Context(), &event, recipients, nil)
	if err == nil {
		c.writes[writer] = now
	}
	c.mu.Unlock()
	if err != nil {
		http.Error(w, "room chat commit not confirmed", http.StatusServiceUnavailable)
		return
	}
	writeRoomChatJSON(w, http.StatusCreated, map[string]any{"room_id": room, "event_id": event.EventID, "seq": event.Seq, "actor": actor})
}

func (c *RoomChat) unread(w http.ResponseWriter, r *http.Request, room string, actor protocol.Actor, token string) {
	events, count, err := c.ws.Store.RoomChatUnread(r.Context(), room, actor, 20)
	if err != nil {
		http.Error(w, "could not read room chat mentions", http.StatusServiceUnavailable)
		return
	}
	redact := newHistoryRedactor(c.legacy, token)
	for i, event := range events {
		events[i], err = redact.event(event)
		if err != nil {
			http.Error(w, "could not prepare room chat mentions", http.StatusInternalServerError)
			return
		}
	}
	writeRoomChatJSON(w, http.StatusOK, map[string]any{"events": events, "unread_count": count})
}

func (c *RoomChat) ack(w http.ResponseWriter, r *http.Request, room string, actor protocol.Actor) {
	var input struct {
		EventIDs []string `json:"event_ids"`
	}
	if !readRoomChatBody(w, r, 8<<10, &input) {
		return
	}
	if len(input.EventIDs) == 0 || len(input.EventIDs) > 20 {
		http.Error(w, "invalid acknowledgement list", http.StatusBadRequest)
		return
	}
	for _, id := range input.EventIDs {
		if id == "" || len(id) > 256 {
			http.Error(w, "invalid event ID", http.StatusBadRequest)
			return
		}
	}
	if err := c.ws.Store.AcknowledgeRoomChat(r.Context(), room, actor, input.EventIDs); err != nil {
		http.Error(w, "room chat acknowledgement not confirmed", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func validRoomChatText(text string, reserved ...string) bool {
	if strings.TrimSpace(text) == "" || len(text) > 4096 || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return false
	}
	for _, token := range reserved {
		if token != "" && strings.Contains(text, token) {
			return false
		}
	}
	return true
}

func readRoomChatBody(w http.ResponseWriter, r *http.Request, limit int64, target any) bool {
	typ, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || typ != "application/json" {
		http.Error(w, "room chat requires JSON", http.StatusUnsupportedMediaType)
		return false
	}
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := decodeRoomChatJSON(http.MaxBytesReader(w, r.Body, limit), target); err != nil {
		http.Error(w, "invalid room chat request", http.StatusBadRequest)
		return false
	}
	return true
}

func writeRoomChatJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
