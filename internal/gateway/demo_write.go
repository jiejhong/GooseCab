package gateway

import (
	"context"
	"embed"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jiejhong/GooseCab/internal/protocol"
)

const DemoWriteRoom = "demo"

//go:embed web/demo-write.html web/demo-write.js
var demoWriteAssets embed.FS

// NewDemoWriter serves the write surface of the demo through a dedicated
// Unix socket. Its listener must authenticate the existing ingress process.
func NewDemoWriter(ws *WS, rawOrigin, legacy string) (http.Handler, error) {
	origin, err := url.Parse(rawOrigin)
	if err != nil || origin.Scheme != "https" || origin.Hostname() == "" || origin.User != nil ||
		origin.Path != "" || origin.RawPath != "" || origin.ForceQuery || origin.RawQuery != "" ||
		origin.Fragment != "" || origin.String() != rawOrigin {
		return nil, errors.New("demo writer requires an exact HTTPS origin")
	}
	if port := origin.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, errors.New("demo writer requires a valid HTTPS origin port")
		}
	}
	if ws == nil || ws.Store == nil || ws.Bus == nil {
		return nil, errors.New("demo writer requires the Gateway store and event bus")
	}
	writer := &demoWriter{ws: ws, origin: origin, legacy: legacy}
	return writer, nil
}

type demoWriter struct {
	ws       *WS
	origin   *url.URL
	legacy   string
	mu       sync.Mutex
	lastSend time.Time
}

func (d *demoWriter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Only a real local socket address from net/http is accepted. Forwarded
	// headers have no role in either ingress admission or author attribution.
	if _, ok := r.Context().Value(http.LocalAddrContextKey).(*net.UnixAddr); !ok ||
		r.Host != d.origin.Host || r.URL.IsAbs() || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery {
		http.Error(w, "demo write ingress required", http.StatusForbidden)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; connect-src 'self'; style-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	switch r.URL.Path {
	case "/demo-write", "/demo-write.js":
		if !demoWriteMethod(w, r, http.MethodGet) {
			return
		}
		path, contentType := "web/demo-write.html", "text/html; charset=utf-8"
		if r.URL.Path == "/demo-write.js" {
			path, contentType = "web/demo-write.js", "text/javascript; charset=utf-8"
		}
		data, err := demoWriteAssets.ReadFile(path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(data)
	case "/api/demo-write/messages":
		if !demoWriteMethod(w, r, http.MethodPost) {
			return
		}
		origins, sites := r.Header.Values("Origin"), r.Header.Values("Sec-Fetch-Site")
		if len(origins) != 1 || origins[0] != d.origin.String() ||
			(len(sites) != 0 && (len(sites) != 1 || sites[0] != "same-origin")) {
			http.Error(w, "demo write requires its same origin", http.StatusForbidden)
			return
		}
		d.post(w, r)
	default:
		http.NotFound(w, r)
	}
}

func demoWriteMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	return false
}

func (d *demoWriter) post(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Text      string `json:"text"`
		RequestID string `json:"request_id,omitempty"`
	}
	if !readRoomChatBody(w, r, 32<<10, &input) {
		return
	}
	if !validRoomChatText(input.Text, d.legacy) {
		http.Error(w, "invalid demo message", http.StatusBadRequest)
		return
	}
	event := protocol.Event{
		RoomID: DemoWriteRoom, Type: "chat.message",
		Actor: protocol.Actor{Kind: "guest", ID: "白名单访客"},
		Payload: struct {
			Text string `json:"text"`
		}{Text: input.Text},
	}
	if input.RequestID != "" {
		id, err := uuid.Parse(input.RequestID)
		if err != nil || id.Version() != 4 || id.String() != input.RequestID {
			http.Error(w, "invalid demo message request ID", http.StatusBadRequest)
			return
		}
		event.EventID = "demo-write-" + id.String()
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	d.mu.Lock()
	defer d.mu.Unlock()
	if event.EventID != "" {
		stored, err := d.ws.Store.EventByID(ctx, DemoWriteRoom, event.EventID)
		if err != nil {
			http.Error(w, "demo message commit not confirmed", http.StatusServiceUnavailable)
			return
		}
		if stored != nil {
			payload, _ := stored.Payload.(map[string]any)
			if stored.Actor != event.Actor || stored.Type != event.Type || payload["text"] != input.Text {
				http.Error(w, "demo message request ID already used", http.StatusConflict)
				return
			}
			writeDemoReceipt(w, *stored)
			return
		}
	}
	if time.Since(d.lastSend) < 500*time.Millisecond {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "demo write rate exceeded", http.StatusTooManyRequests)
		return
	}
	err := d.ws.appendAndPublish(ctx, &event, nil, nil)
	if err != nil {
		http.Error(w, "demo message commit not confirmed", http.StatusServiceUnavailable)
		return
	}
	d.lastSend = time.Now()
	writeDemoReceipt(w, event)
}

func writeDemoReceipt(w http.ResponseWriter, event protocol.Event) {
	writeRoomChatJSON(w, http.StatusCreated, map[string]any{
		"room_id": event.RoomID, "event_id": event.EventID, "seq": event.Seq, "actor": event.Actor,
	})
}
