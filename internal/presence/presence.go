// Package presence tracks responsive Agent connections in the current process.
package presence

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	pingInterval    = 20 * time.Second
	pongTimeout     = 60 * time.Second
	pingWriteWindow = 5 * time.Second
	activityWindow  = time.Second
)

// RoomActivity reads a persisted activity time for exactly one room.
type RoomActivity func(context.Context, string) (*time.Time, error)

type Tracker struct {
	mu           sync.Mutex
	rooms        map[string]map[string]*onlineAgent
	pingInterval time.Duration
	pongTimeout  time.Duration
	roomActivity RoomActivity
}

type onlineAgent struct {
	sessions       map[*Session]time.Time
	lastActivityAt time.Time
}

type Session struct {
	tracker *Tracker
	room    string
	agent   string
	stop    context.CancelFunc
}

type agent struct {
	ID             string     `json:"agent_id"`
	Connections    int        `json:"connections"`
	LastActivityAt *time.Time `json:"last_activity_at"`
}

func New(activity RoomActivity) *Tracker {
	return &Tracker{
		rooms:        make(map[string]map[string]*onlineAgent),
		pingInterval: pingInterval,
		pongTimeout:  pongTimeout,
		roomActivity: activity,
	}
}

func (t *Tracker) connect(room, agent string, deadline time.Time) *Session {
	s := &Session{tracker: t, room: room, agent: agent}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.rooms[room] == nil {
		t.rooms[room] = make(map[string]*onlineAgent)
	}
	group := t.rooms[room][agent]
	if group == nil {
		group = &onlineAgent{sessions: make(map[*Session]time.Time)}
		t.rooms[room][agent] = group
	}
	group.sessions[s] = deadline
	return s
}

func (s *Session) renew(deadline time.Time) {
	t := s.tracker
	t.mu.Lock()
	defer t.mu.Unlock()
	if group := t.rooms[s.room][s.agent]; group != nil {
		if _, active := group.sessions[s]; active {
			group.sessions[s] = deadline
		}
	}
}

// RecordActivity records the server time after a room business action succeeds.
func (s *Session) RecordActivity() { s.recordActivity(time.Now()) }

func (s *Session) recordActivity(at time.Time) {
	t := s.tracker
	t.mu.Lock()
	defer t.mu.Unlock()
	if group := t.rooms[s.room][s.agent]; group != nil {
		if deadline, ok := group.sessions[s]; ok && at.Before(deadline) && at.After(group.lastActivityAt) {
			group.lastActivityAt = at
		}
	}
}

func (s *Session) close() {
	t := s.tracker
	t.mu.Lock()
	defer t.mu.Unlock()
	if group := t.rooms[s.room][s.agent]; group != nil {
		delete(group.sessions, s)
		if len(group.sessions) == 0 {
			delete(t.rooms[s.room], s.agent)
		}
	}
	if len(t.rooms[s.room]) == 0 {
		delete(t.rooms, s.room)
	}
}

// Close stops the heartbeat and removes only this connection.
func (s *Session) Close() {
	s.stop()
	s.close()
}

func (t *Tracker) snapshot(room string, now time.Time) []agent {
	t.mu.Lock()
	agents := make([]agent, 0, len(t.rooms[room]))
	for id, group := range t.rooms[room] {
		count := 0
		for _, deadline := range group.sessions {
			if now.Before(deadline) {
				count++
			}
		}
		if count == 0 {
			continue
		}
		view := agent{ID: id, Connections: count}
		if !group.lastActivityAt.IsZero() {
			at := group.lastActivityAt.UTC()
			view.LastActivityAt = &at
		}
		agents = append(agents, view)
	}
	t.mu.Unlock()
	sort.Slice(agents, func(i, j int) bool { return agents[i].ID < agents[j].ID })
	return agents
}

// OnlineIDs lists Agent IDs with a live WebSocket in the given room.
func (t *Tracker) OnlineIDs(room string, now time.Time) []string {
	agents := t.snapshot(room, now)
	ids := make([]string, 0, len(agents))
	for _, agent := range agents {
		ids = append(ids, agent.ID)
	}
	return ids
}

// Watch starts presence after welcome. Call it from the socket's reader, keep
// reading to process Pong frames, and defer closing the returned session.
func (t *Tracker) Watch(conn *websocket.Conn, room, agent string) (*Session, error) {
	deadline := time.Now().Add(t.pongTimeout)
	if err := conn.SetReadDeadline(deadline); err != nil {
		return nil, err
	}
	s := t.connect(room, agent, deadline)
	conn.SetPongHandler(func(string) error {
		deadline := time.Now().Add(t.pongTimeout)
		if err := conn.SetReadDeadline(deadline); err != nil {
			return err
		}
		s.renew(deadline)
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	s.stop = cancel
	go func() {
		ticker := time.NewTicker(t.pingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// WriteControl may run concurrently with the application's writer.
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(pingWriteWindow)); err != nil {
					s.Close()
					_ = conn.Close()
					return
				}
			}
		}
	}()
	return s, nil
}

// ServeHTTP returns an uncached snapshot. The Gateway authenticates this route.
func (t *Tracker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	room := q.Get("room_id")
	if strings.TrimSpace(room) == "" || len(room) > 256 || len(q["room_id"]) != 1 {
		http.Error(w, "room_id is required (maximum 256 bytes)", http.StatusBadRequest)
		return
	}
	var activityAt *time.Time
	var activityAvailable bool
	if t.roomActivity != nil {
		ctx, cancel := context.WithTimeout(r.Context(), activityWindow)
		at, err := t.roomActivity(ctx, room)
		cancel()
		if err == nil {
			activityAt = at
			activityAvailable = true
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		RoomID            string     `json:"room_id"`
		Agents            []agent    `json:"agents"`
		LastActivityAt    *time.Time `json:"last_activity_at"`
		ActivityAvailable bool       `json:"activity_available"`
	}{
		RoomID:            room,
		Agents:            t.snapshot(room, time.Now()),
		LastActivityAt:    activityAt,
		ActivityAvailable: activityAvailable,
	})
}
