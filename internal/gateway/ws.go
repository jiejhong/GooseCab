package gateway

import (
    "encoding/json"
    "net/http"
    "os"
    "strconv"
    "time"
    "sync"

    "github.com/gorilla/websocket"
    "github.com/jiejhong/GooseCab/internal/eventbus"
    "github.com/jiejhong/GooseCab/internal/protocol"
    "github.com/jiejhong/GooseCab/internal/store"
)

type WS struct {
    Store *store.Store
    Bus   *eventbus.Bus
}

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool {
    origin := r.Header.Get("Origin")
    return origin == "" || origin == r.Header.Get("Host") || os.Getenv("GOOSECAB_ALLOW_CROSS_ORIGIN") == "1"
}}

// Handler serves the Agent Node outbound connection and the read-only timeline.
// Clients receive persisted events after resume_seq followed by live events.
func (w *WS) Handler(h http.Handler) http.Handler {
    return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/api/v1/agent/connect" && r.URL.Path != "/ws" {
            h.ServeHTTP(rw, r)
            return
        }
        expected := os.Getenv("GOOSECAB_AGENT_TOKEN")
        if expected != "" && r.Header.Get("Authorization") != "Bearer "+expected {
            http.Error(rw, "unauthorized", http.StatusUnauthorized)
            return
        }
        room := r.URL.Query().Get("room_id")
        after, _ := strconv.ParseInt(r.URL.Query().Get("resume_seq"), 10, 64)
        c, err := upgrader.Upgrade(rw, r, nil)
        if err != nil {
            return
        }
        defer c.Close()
        if w.Bus == nil { w.Bus = eventbus.New() }
        var writeMu sync.Mutex
        writeJSON := func(v any) error { writeMu.Lock(); defer writeMu.Unlock(); return c.WriteJSON(v) }
        sub := w.Bus.Subscribe(64)
        defer w.Bus.Unsubscribe(sub)

        // Replay first. Events published concurrently are buffered by the subscription.
        if events, e := w.Store.EventsAfter(r.Context(), room, after); e == nil {
            for _, evt := range events {
                if err := writeJSON(evt); err != nil { return }
            }
        }
        done := make(chan struct{})
        defer close(done)
        go func() {
            for {
                select {
                case evt, ok := <-sub:
                    if !ok { return }
                    if evt.RoomID == room && evt.Seq > after {
                        if writeJSON(evt) != nil { return }
                    }
                case <-done:
                    return
                }
            }
        }()
        for {
            var msg map[string]any
            if err := c.ReadJSON(&msg); err != nil { return }
            switch msg["type"] {
            case "agent.hello":
                _ = writeJSON(map[string]any{"type": "agent.welcome", "resume_from": after, "room_id": room})
            case "event.publish":
                raw, ok := msg["event"]
                if !ok { continue }
                b, _ := json.Marshal(raw)
                var evt protocol.Event
                if json.Unmarshal(b, &evt) != nil || evt.RoomID == "" || evt.Type == "" { continue }
                seq, e := w.Store.NextSeq(r.Context(), evt.RoomID)
                if e != nil { continue }
                evt.Seq = seq
                if evt.EventID == "" { evt.EventID = evt.Type + "-" + strconv.FormatInt(seq, 10) }
                if evt.TS.IsZero() { evt.TS = nowUTC() }
                if w.Store.Append(r.Context(), evt) == nil { w.Bus.Publish(evt) }
            }
        }
    })
}

func nowUTC() time.Time { return time.Now().UTC() }

func JSON(v any) []byte { b, _ := json.Marshal(v); return b }
