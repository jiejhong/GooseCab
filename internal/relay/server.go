// Package relay provides a bounded, ephemeral data plane. It never persists bytes.
package relay

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const ChunkSize = 32 * 1024
const DefaultMaxSize int64 = 256 << 20

type Ticket struct {
	ID            string `json:"id"`
	Sender        string `json:"sender"`
	Recipient     string `json:"recipient"`
	SendSecret    string `json:"send_secret,omitempty"`
	ReceiveSecret string `json:"receive_secret,omitempty"`
	Size          int64  `json:"size"`
	SHA256        string `json:"sha256"`
}
type Config struct {
	MaxSessions int
	MaxSize     int64
	Timeout     time.Duration
}
type session struct {
	ticket                             Ticket
	send, recv                         *websocket.Conn
	claimedSend, claimedRecv, finished bool
	done                               chan struct{}
	deadline                           time.Time
}
type Server struct {
	mu       sync.Mutex
	cfg      Config
	sessions map[string]*session
	slots    chan struct{}
}

func New(c Config) *Server {
	if c.MaxSessions <= 0 {
		c.MaxSessions = 64
	}
	if c.MaxSize <= 0 {
		c.MaxSize = DefaultMaxSize
	}
	if c.Timeout <= 0 {
		c.Timeout = 2 * time.Minute
	}
	return &Server{cfg: c, sessions: make(map[string]*session), slots: make(chan struct{}, c.MaxSessions*2+8)}
}
func valid(t Ticket, max int64) bool {
	b, e := hex.DecodeString(t.SHA256)
	return e == nil && len(b) == 32 && t.SHA256 == strings.ToLower(t.SHA256) && t.Size >= 0 && t.Size <= max && len(t.Sender) > 0 && len(t.Sender) <= 128 && len(t.Recipient) > 0 && len(t.Recipient) <= 128
}
func random() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		http.Error(w, "relay connection capacity reached", 429)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/api/v1/relay" && r.Method == http.MethodPost {
		s.create(w, r)
		return
	}
	if r.URL.Path == "/api/v1/relay/connect" && r.Method == http.MethodGet {
		s.connect(w, r)
		return
	}
	http.Error(w, "not found", http.StatusNotFound)
}
func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(5 * time.Second))
	var t Ticket
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(&t) != nil || !valid(t, s.cfg.MaxSize) {
		http.Error(w, "invalid transfer metadata", 400)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sessions) >= s.cfg.MaxSessions {
		http.Error(w, "relay capacity reached", 429)
		return
	}
	t.ID = random()
	t.SendSecret = random()
	t.ReceiveSecret = random()
	x := &session{ticket: t, done: make(chan struct{}), deadline: time.Now().Add(s.cfg.Timeout)}
	s.sessions[t.ID] = x
	time.AfterFunc(s.cfg.Timeout, func() { s.mu.Lock(); defer s.mu.Unlock(); s.finish(x); delete(s.sessions, t.ID) })
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(t)
}

// finish must be called with mu held. A consumed session remains until its deadline.
func (s *Server) finish(x *session) {
	if x.finished {
		return
	}
	x.finished = true
	if x.send != nil {
		_ = x.send.Close()
	}
	if x.recv != nil {
		_ = x.recv.Close()
	}
	close(x.done)
}
func (s *Server) connect(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	x := s.sessions[r.Header.Get("X-Relay-ID")]
	role := r.Header.Get("X-Relay-Role")
	if x == nil || x.finished {
		s.mu.Unlock()
		http.Error(w, "unavailable transfer", 404)
		return
	}
	secret, agent, claimed := x.ticket.SendSecret, x.ticket.Sender, x.claimedSend
	if role == "receive" {
		secret, agent, claimed = x.ticket.ReceiveSecret, x.ticket.Recipient, x.claimedRecv
	} else if role != "send" {
		s.mu.Unlock()
		http.Error(w, "invalid role", 400)
		return
	}
	if subtle.ConstantTimeCompare([]byte(secret), []byte(r.Header.Get("X-Relay-Secret"))) != 1 || r.Header.Get("X-Relay-Agent") != agent {
		s.mu.Unlock()
		http.Error(w, "unauthorized transfer", 403)
		return
	}
	if claimed {
		s.mu.Unlock()
		http.Error(w, "role already claimed", 409)
		return
	}
	if role == "send" {
		x.claimedSend = true
	} else {
		x.claimedRecv = true
	}
	s.mu.Unlock()
	u := websocket.Upgrader{ReadBufferSize: ChunkSize, WriteBufferSize: ChunkSize, HandshakeTimeout: 5 * time.Second}
	c, err := u.Upgrade(w, r, nil)
	s.mu.Lock()
	if err != nil {
		s.finish(x)
		s.mu.Unlock()
		return
	}
	if x.finished {
		_ = c.Close()
		s.mu.Unlock()
		return
	}
	c.SetReadLimit(ChunkSize)
	_ = c.SetReadDeadline(x.deadline)
	_ = c.SetWriteDeadline(x.deadline)
	if role == "send" {
		x.send = c
	} else {
		x.recv = c
	}
	if x.send != nil && x.recv != nil {
		go s.pipe(x)
	}
	s.mu.Unlock()
	<-x.done
}
func (s *Server) pipe(x *session) {
	defer func() { s.mu.Lock(); s.finish(x); s.mu.Unlock() }()
	if x.send.WriteMessage(websocket.TextMessage, []byte("ready")) != nil {
		return
	}
	if x.recv.WriteMessage(websocket.TextMessage, []byte("ready")) != nil {
		return
	}
	var n int64
	for n < x.ticket.Size {
		typ, b, e := x.send.ReadMessage()
		if e != nil || typ != websocket.BinaryMessage || len(b) == 0 || int64(len(b)) > x.ticket.Size-n {
			return
		}
		n += int64(len(b))
		if x.recv.WriteMessage(websocket.BinaryMessage, b) != nil {
			return
		}
	}
	if expect(x.send, "done") != nil {
		return
	}
	if x.recv.WriteMessage(websocket.TextMessage, []byte("done")) != nil {
		return
	}
	if expect(x.recv, "ok") != nil {
		return
	}
	_ = x.send.WriteMessage(websocket.TextMessage, []byte("ok"))
}
func expect(c *websocket.Conn, want string) error {
	typ, b, e := c.ReadMessage()
	if e != nil {
		return e
	}
	if typ != websocket.TextMessage || string(b) != want {
		return errors.New("unexpected relay frame")
	}
	return nil
}
