package gateway

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/jiejhong/GooseCab/internal/eventbus"
	"github.com/jiejhong/GooseCab/internal/protocol"
	"github.com/jiejhong/GooseCab/internal/registry"
	"github.com/jiejhong/GooseCab/internal/store"
)

type WS struct {
	Store    *store.Store
	Bus      *eventbus.Bus
	Registry *registry.Registry
	eventMu  sync.Mutex
}

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	if parsed, err := url.Parse(origin); err == nil && parsed.Host != "" && strings.EqualFold(parsed.Host, r.Host) {
		return true
	}
	return os.Getenv("GOOSECAB_ALLOW_CROSS_ORIGIN") == "1"
}}

func (w *WS) Handler(next http.Handler) http.Handler {
	// Initialize once while assembling the handler, not concurrently on first use.
	if w.Bus == nil {
		w.Bus = eventbus.New()
	}
	if w.Registry == nil {
		w.Registry = registry.New()
	}
	return Authenticate(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agent/connect" && r.URL.Path != "/ws" {
			next.ServeHTTP(rw, r)
			return
		}
		q := r.URL.Query()
		room := q.Get("room_id")
		after := int64(0)
		if strings.TrimSpace(room) == "" || len(room) > 256 || len(q["room_id"]) != 1 {
			http.Error(rw, "room_id is required (maximum 256 bytes)", http.StatusBadRequest)
			return
		}
		if q.Has("resume_seq") {
			var err error
			after, err = strconv.ParseInt(q.Get("resume_seq"), 10, 64)
			if err != nil || after < 0 || len(q["resume_seq"]) != 1 {
				http.Error(rw, "invalid resume_seq", http.StatusBadRequest)
				return
			}
		}
		c, err := upgrader.Upgrade(rw, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		c.SetReadLimit(1 << 20)
		sub := w.Bus.Subscribe(256)
		defer w.Bus.Unsubscribe(sub)
		var writeMu sync.Mutex
		writeJSON := func(v any) error { writeMu.Lock(); defer writeMu.Unlock(); return c.WriteJSON(v) }
		reject := func(code, message string) {
			_ = writeJSON(map[string]any{"type": "error", "code": code, "message": message})
		}
		events, err := w.Store.EventsAfter(r.Context(), room, after)
		if err != nil {
			reject("replay_failed", "unable to read room history")
			return
		}
		// Subscribe before reading the snapshot, then suppress events already replayed.
		// Use IDs, not a moving seq cutoff: concurrent producers can broadcast in a
		// different order from their database commits.
		replayed := make(map[string]struct{}, len(events))
		for _, evt := range events {
			if writeJSON(evt) != nil {
				return
			}
			replayed[evt.EventID] = struct{}{}
		}
		done := make(chan struct{})
		defer close(done)
		go func() {
			for {
				select {
				case evt, ok := <-sub:
					if !ok {
						return
					}
					if evt.RoomID == room && evt.Seq > after {
						if _, seen := replayed[evt.EventID]; seen {
							delete(replayed, evt.EventID)
							continue
						}
						if writeJSON(evt) != nil {
							_ = c.Close()
							return
						}
					}
				case <-done:
					return
				}
			}
		}()
		agentID := ""
		persistWith := func(evt protocol.Event, afterSave func()) bool {
			// Store allocates IDs/sequences atomically across WS and HTTP producers.
			// Serialize WS append+broadcast so their live order follows storage order.
			w.eventMu.Lock()
			evt.RoomID = room
			evt.Actor = protocol.Actor{Kind: "agent", ID: agentID}
			if err := w.Store.AppendNext(r.Context(), &evt); err != nil {
				w.eventMu.Unlock()
				log.Printf("persist websocket event: %v", err)
				reject("persist_failed", "unable to save event")
				return false
			}
			if afterSave != nil {
				afterSave()
			}
			w.Bus.Publish(evt)
			w.eventMu.Unlock()
			return true
		}
		persist := func(evt protocol.Event) bool { return persistWith(evt, nil) }
		publish := func(typ, capID, corr string, payload any) bool {
			return persist(protocol.Event{Type: typ, CapabilityID: capID, CorrelationID: corr, Payload: payload})
		}
		for {
			var msg map[string]json.RawMessage
			if c.ReadJSON(&msg) != nil {
				return
			}
			if r.URL.Path == "/ws" {
				reject("read_only", "observer connections cannot publish")
				continue
			}
			var typ string
			if json.Unmarshal(msg["type"], &typ) != nil {
				reject("invalid_message", "type is required")
				continue
			}
			if agentID == "" && typ != "agent.hello" {
				reject("hello_required", "send agent.hello before publishing")
				continue
			}
			raw, _ := json.Marshal(msg)
			switch typ {
			case "agent.hello":
				if agentID != "" {
					reject("already_initialized", "agent identity is fixed for this connection")
					continue
				}
				var hello protocol.AgentHello
				if json.Unmarshal(raw, &hello) != nil || strings.TrimSpace(hello.AgentID) == "" || len(hello.AgentID) > 256 {
					reject("invalid_hello", "agent_id is required (maximum 256 bytes)")
					continue
				}
				valid := true
				for _, cap := range hello.Capabilities {
					if strings.TrimSpace(cap) == "" {
						valid = false
					}
				}
				if !valid {
					reject("invalid_hello", "capability IDs must not be empty")
					continue
				}
				agentID = hello.AgentID
				if !persistWith(protocol.Event{Type: "agent.hello", Payload: hello}, func() { w.Registry.RegisterHello(hello.AgentID, hello.Name, hello.Role, hello.Capabilities) }) {
					agentID = ""
					continue
				}
				if writeJSON(map[string]any{"type": "agent.welcome", "resume_from": after, "room_id": room}) != nil {
					return
				}
				for _, cap := range hello.Capabilities {
					publish("capability.announce", cap, "", protocol.CapabilityAnnounce{AgentID: agentID, CapabilityID: cap})
				}
			case "capability.announce":
				var v protocol.CapabilityAnnounce
				if json.Unmarshal(raw, &v) != nil || strings.TrimSpace(v.CapabilityID) == "" {
					reject("invalid_announce", "capability_id is required")
					continue
				}
				if v.AgentID != "" && v.AgentID != agentID {
					reject("identity_mismatch", "agent_id must match the connection")
					continue
				}
				v.AgentID = agentID
				persistWith(protocol.Event{Type: typ, CapabilityID: v.CapabilityID, Payload: v}, func() {
					w.Registry.Announce(agentID, registry.Capability{ID: v.CapabilityID, Name: v.Name, Version: v.Version, Manifest: v.Manifest})
				})
			case "capability.invoke":
				var v protocol.CapabilityInvoke
				if json.Unmarshal(raw, &v) != nil || strings.TrimSpace(v.RequestID) == "" || strings.TrimSpace(v.CapabilityID) == "" {
					reject("invalid_invoke", "request_id and capability_id are required")
					continue
				}
				publish(typ, v.CapabilityID, v.RequestID, v)
			case "capability.progress":
				var v protocol.CapabilityProgress
				if json.Unmarshal(raw, &v) != nil || strings.TrimSpace(v.RequestID) == "" {
					reject("invalid_progress", "request_id is required")
					continue
				}
				publish(typ, "", v.RequestID, v)
			case "capability.result":
				var v protocol.CapabilityResult
				if json.Unmarshal(raw, &v) != nil || strings.TrimSpace(v.RequestID) == "" {
					reject("invalid_result", "request_id is required")
					continue
				}
				publish(typ, "", v.RequestID, v)
			case "transfer.request", "transfer.accepted", "transfer.progress", "transfer.completed", "transfer.failed":
				var payload map[string]any
				_ = json.Unmarshal(raw, &payload)
				corr, _ := payload["file_id"].(string)
				if corr == "" {
					corr, _ = payload["transfer_id"].(string)
				}
				publish(typ, "", corr, payload)
			case "event.publish":
				var evt protocol.Event
				if json.Unmarshal(msg["event"], &evt) != nil || strings.TrimSpace(evt.Type) == "" {
					reject("invalid_event", "event with a type is required")
					continue
				}
				if evt.RoomID != "" && evt.RoomID != room {
					reject("room_mismatch", "event room_id must match the connection")
					continue
				}
				if (evt.Actor.ID != "" && evt.Actor.ID != agentID) || (evt.Actor.Kind != "" && evt.Actor.Kind != "agent") {
					reject("identity_mismatch", "event actor must match the connection")
					continue
				}
				if evt.Type == "agent.hello" || evt.Type == "agent.welcome" || evt.Type == "capability.announce" {
					reject("reserved_event", "use the dedicated protocol frame for this event type")
					continue
				}
				persist(evt)
			default:
				reject("unknown_type", "unsupported message type")
			}
		}
	}))
}
func JSON(v any) []byte { b, _ := json.Marshal(v); return b }
