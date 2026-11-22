package main

import (
	"context"
	"embed"
	"encoding/json"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jiejhong/GooseCab/internal/eventbus"
	"github.com/jiejhong/GooseCab/internal/protocol"
)

type roomServer struct {
	host      string
	policy    *roomPolicy
	db        *roomDB
	bus       *eventbus.Bus
	mu        sync.Mutex
	lastWrite map[protocol.Actor]time.Time
}

func newRoomServer(host string, policy *roomPolicy, db *roomDB) *roomServer {
	return &roomServer{host: host, policy: policy, db: db, bus: eventbus.New(), lastWrite: make(map[protocol.Actor]time.Time)}
}

func (s *roomServer) sameOrigin(r *http.Request) bool {
	values := r.Header.Values("Origin")
	return len(values) == 1 && values[0] == "http://"+s.host
}

//go:embed web/index.html web/observer.js web/observer.css
var observerFiles embed.FS

func (s *roomServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	if r.Host != s.host || r.URL.IsAbs() {
		http.Error(w, "invalid local host", http.StatusForbidden)
		return
	}
	queryAllowed := r.URL.Path == "/api/mentions" || r.URL.Path == "/api/events" || r.URL.Path == agentConnectPath
	if (r.URL.RawQuery != "" && !queryAllowed) ||
		r.URL.ForceQuery || r.URL.RawPath != "" || r.URL.EscapedPath() != r.URL.Path {
		http.NotFound(w, r)
		return
	}
	var method string
	switch r.URL.Path {
	case "/", "/observer.js", "/observer.css", "/api/events", "/api/mentions", "/ws", agentConnectPath:
		method = http.MethodGet
	case "/api/messages", "/api/mentions/read":
		method = http.MethodPost
	default:
		http.NotFound(w, r)
		return
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	switch r.URL.Path {
	case "/":
		serveObserverFile(w, "index.html", "text/html; charset=utf-8")
	case "/observer.js":
		serveObserverFile(w, "observer.js", "text/javascript; charset=utf-8")
	case "/observer.css":
		serveObserverFile(w, "observer.css", "text/css; charset=utf-8")
	case agentConnectPath:
		s.serveAgent(w, r)
	case "/api/messages":
		s.postMessage(w, r)
	case "/api/mentions/read":
		s.acknowledgeMentions(w, r)
	default:
		s.serveRead(w, r)
	}
}

func serveObserverFile(w http.ResponseWriter, filename, contentType string) {
	data, err := observerFiles.ReadFile("web/" + filename)
	if err != nil {
		http.Error(w, "observer unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(data)
}

func denyRead(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", localScheme)
	http.Error(w, "room read denied", http.StatusUnauthorized)
}

func (s *roomServer) serveRead(w http.ResponseWriter, r *http.Request) {
	if len(r.Header.Values("Origin")) != 0 && !s.sameOrigin(r) {
		http.Error(w, "invalid origin", http.StatusForbidden)
		return
	}
	auth := requestAuthorization(r)
	read := s.policy.readHuman
	if r.URL.Path != "/api/events" {
		read = s.policy.readStream
	}
	actor, lease, ok := read(auth)
	if !ok {
		denyRead(w)
		return
	}
	ctx, cancel := lease.Context(r.Context())
	defer cancel()
	if ctx.Err() != nil {
		denyRead(w)
		return
	}
	if r.URL.Path == "/ws" {
		s.stream(ctx, w, r)
		return
	}
	if r.URL.Path == "/api/mentions" {
		s.serveMentions(ctx, w, r, actor)
		return
	}
	_, canWrite := s.policy.writer(auth)
	s.serveHistory(ctx, w, r, actor, canWrite)
}

func writeLocalJSON(ctx context.Context, w http.ResponseWriter, status int, data any) {
	if ctx.Err() != nil {
		denyRead(w)
		return
	}
	deadline, _ := ctx.Deadline()
	_ = http.NewResponseController(w).SetWriteDeadline(minTime(deadline, time.Now().Add(5*time.Second)))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func (s *roomServer) stream(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{CheckOrigin: s.sameOrigin, HandshakeTimeout: 3 * time.Second}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	// Snapshot and subscription share the commit lock, so an event appears in
	// exactly one of the replay and live paths.
	s.mu.Lock()
	if s.db.failed || ctx.Err() != nil {
		s.mu.Unlock()
		return
	}
	sub := s.bus.SubscribeRoom(s.policy.room, maxEvents)
	replay := slices.Clone(s.db.state.Events)
	s.mu.Unlock()
	defer s.bus.Unsubscribe(sub)

	conn.SetReadLimit(4096)
	received := make(chan struct{})
	go func() {
		defer close(received)
		// The socket only delivers events. An application write ends the
		// subscription. WebSocket control frames do not renew the lease.
		_, _, _ = conn.ReadMessage()
		_ = conn.Close()
	}()
	defer func() {
		_ = conn.Close()
		<-received
	}()
	expiry, _ := ctx.Deadline()
	send := func(event protocol.Event) bool {
		return ctx.Err() == nil && time.Now().Before(expiry) &&
			conn.SetWriteDeadline(minTime(expiry, time.Now().Add(5*time.Second))) == nil &&
			conn.WriteJSON(event) == nil
	}
	for _, event := range replay {
		if !send(event) {
			return
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-received:
			return
		case event, ok := <-sub:
			if !ok || !send(event) {
				return
			}
		}
	}
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
