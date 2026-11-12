package cascade

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiejhong/GooseCab/internal/agent"
	"github.com/jiejhong/GooseCab/internal/eventbus"
	"github.com/jiejhong/GooseCab/internal/gateway"
	"github.com/jiejhong/GooseCab/internal/protocol"
	"github.com/jiejhong/GooseCab/internal/store"
)

func server(t *testing.T) (string, *store.Store) {
	t.Helper()
	s, e := store.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if e != nil {
		t.Fatal(e)
	}
	srv := httptest.NewServer((&gateway.WS{Store: s, Bus: eventbus.New()}).Handler(http.NotFoundHandler()))
	t.Cleanup(func() { srv.Close(); s.Close() })
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/agent/connect", s
}
func connect(t *testing.T, url, room, id string) *agent.Client {
	t.Helper()
	a, e := agent.Dial(url, room, id, nil)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { a.Close() })
	return a
}
func runBridge(t *testing.T, b *Bridge) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()
	t.Cleanup(cancel)
	return cancel, done
}
func awaitHello(t *testing.T, a *agent.Client, id string) {
	t.Helper()
	end := time.Now().Add(5 * time.Second)
	for {
		m, e := a.ReadWithTimeout(time.Until(end))
		if e != nil {
			t.Fatal(e)
		}
		actor, _ := m["actor"].(map[string]any)
		if m["type"] == "agent.hello" && actor["id"] == id {
			return
		}
	}
}
func waitResult(t *testing.T, a *agent.Client, id string) protocol.CapabilityResult {
	t.Helper()
	end := time.Now().Add(5 * time.Second)
	for {
		m, e := a.ReadWithTimeout(time.Until(end))
		if e != nil {
			t.Fatal(e)
		}
		if m["type"] == "error" {
			t.Fatalf("error frame: %v", m)
		}
		if m["type"] == "capability.result" && m["correlation_id"] == id {
			var r protocol.CapabilityResult
			if e = decode(m["payload"], &r); e != nil {
				t.Fatal(e)
			}
			return r
		}
	}
}
func cfg(u, d, mode string) Config {
	return Config{ID: "edge", Mode: mode, Upstream: Endpoint{URL: u, Room: "up"}, Downstream: Endpoint{URL: d, Room: "down"}, Routes: []Route{{Worker: "worker", Capability: agent.TextCapability}}, MaxHops: 4, Timeout: time.Second}
}
func TestLiveGatewayLeaderAndFlat(t *testing.T) {
	for _, mode := range []string{"leader", "flat"} {
		t.Run(mode, func(t *testing.T) {
			u, us := server(t)
			d, ds := server(t)
			c := cfg(u, d, mode)
			worker := connect(t, d, "down", "worker")
			executor, e := agent.OpenExecutor(filepath.Join(t.TempDir(), "worker.db"))
			if e != nil {
				t.Fatal(e)
			}
			defer executor.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go agent.RunExecutor(ctx, worker, executor, nil)
			caller := connect(t, u, "up", "caller")
			path := filepath.Join(t.TempDir(), "bridge.db")
			b, e := Open(c, path)
			if e != nil {
				t.Fatal(e)
			}
			stop, done := runBridge(t, b)
			awaitHello(t, caller, c.Identity("worker"))
			if e = caller.Invoke("real", agent.TextCapability, c.Identity("worker"), map[string]any{"text": "two gateways"}); e != nil {
				t.Fatal(e)
			}
			r := waitResult(t, caller, "real")
			if r.Status != "completed" || r.Output == nil || r.Output.Text != "TWO GATEWAYS" {
				t.Fatalf("%+v", r)
			}
			stop()
			if e = <-done; e != nil {
				t.Fatal(e)
			}
			b.Close()
			// Reconnect both bridge endpoints with the SAME durable mapping: no second
			// downstream invoke, but cached results remain available upstream.
			b, e = Open(c, path)
			if e != nil {
				t.Fatal(e)
			}
			defer b.Close()
			stop, done = runBridge(t, b)
			awaitHello(t, caller, c.Identity("worker"))
			if e = caller.Invoke("real", agent.TextCapability, c.Identity("worker"), map[string]any{"text": "two gateways"}); e != nil {
				t.Fatal(e)
			}
			r = waitResult(t, caller, "real")
			if r.Status != "completed" {
				t.Fatal(r)
			}
			stop()
			if e = <-done; e != nil {
				t.Fatal(e)
			}
			events, e := ds.EventsAfter(context.Background(), "down", 0)
			if e != nil {
				t.Fatal(e)
			}
			count := 0
			for _, ev := range events {
				if ev.Type == "capability.invoke" {
					count++
					var inv protocol.CapabilityInvoke
					decode(ev.Payload, &inv)
					if ev.Actor.ID != "edge/bridge" || inv.TargetAgentID != "worker" || len(inv.Route) != 1 || inv.Route[0] != "edge" {
						t.Fatalf("lost provenance: %+v %+v", ev, inv)
					}
				}
			}
			if count != 1 {
				t.Fatalf("executed/re-submitted %d times", count)
			}
			events, e = us.EventsAfter(context.Background(), "up", 0)
			if e != nil {
				t.Fatal(e)
			}
			trace, progress := false, false
			for _, ev := range events {
				if ev.Type == "cascade.forwarded" {
					trace = true
				}
				if ev.Type == "capability.progress" && ev.Actor.ID == c.Identity("worker") {
					progress = true
				}
				if ev.Type == "capability.result" && ev.Actor.ID != c.Identity("worker") {
					t.Fatal("forged downstream identity upstream")
				}
			}
			if !trace || !progress {
				t.Fatal("missing route trace or progress")
			}
		})
	}
}
func TestLoopAllowlistAndOfflineFailClosed(t *testing.T) {
	u, _ := server(t)
	d, ds := server(t)
	c := cfg(u, d, "leader")
	c.Timeout = 100 * time.Millisecond
	caller := connect(t, u, "up", "caller")
	b, e := Open(c, filepath.Join(t.TempDir(), "bridge.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	stop, done := runBridge(t, b)
	defer func() { stop(); <-done }()
	awaitHello(t, caller, "edge")
	cases := []struct {
		id, cap string
		route   []string
		status  string
	}{{"loop", agent.TextCapability, []string{"edge"}, "failed"}, {"hops", agent.TextCapability, []string{"a", "b", "c", "d"}, "failed"}, {"blocked", "cap.shell", nil, "failed"}, {"offline", agent.TextCapability, nil, "unknown"}}
	for _, tt := range cases {
		if e = caller.Write(struct {
			Type string `json:"type"`
			protocol.CapabilityInvoke
		}{"capability.invoke", protocol.CapabilityInvoke{RequestID: tt.id, CapabilityID: tt.cap, TargetAgentID: "edge", Payload: map[string]any{"text": "hi"}, Route: tt.route}}); e != nil {
			t.Fatal(e)
		}
		if r := waitResult(t, caller, tt.id); r.Status != tt.status || r.ResultAvailable {
			t.Fatalf("%s: %+v", tt.id, r)
		}
	}
	events, e := ds.EventsAfter(context.Background(), "down", 0)
	if e != nil {
		t.Fatal(e)
	}
	n := 0
	for _, ev := range events {
		if ev.Type == "capability.invoke" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("nonallowlisted/loop calls escaped: %d", n)
	}
}
func TestJournalBindsRoutes(t *testing.T) {
	c := cfg("ws://up/ws", "ws://down/ws", "leader")
	path := filepath.Join(t.TempDir(), "map.db")
	b, e := Open(c, path)
	if e != nil {
		t.Fatal(e)
	}
	b.Close()
	c.Routes[0].Worker = "different"
	if b, e = Open(c, path); e == nil {
		b.Close()
		t.Fatal("rerouted existing journal")
	}
}
func TestRoomAndEventIsolation(t *testing.T) {
	u, _ := server(t)
	d, ds := server(t)
	c := cfg(u, d, "flat")
	caller := connect(t, u, "up", "caller")
	other := connect(t, u, "other", "caller")
	b, e := Open(c, filepath.Join(t.TempDir(), "bridge.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	stop, done := runBridge(t, b)
	awaitHello(t, caller, "edge/worker")
	other.Invoke("wrong-room", agent.TextCapability, "edge/worker", map[string]any{"text": "secret"})
	caller.SendChat("not replicated")
	caller.SendEvent("transfer.request", map[string]any{"file": "not replicated"})
	caller.Invoke("file-denied", agent.TextCapability, "edge/worker", map[string]any{"text": "x", "file_ref": "private"})
	// Strict namespace validation now rejects malformed text at the upstream
	// Gateway, before it can be persisted or reach the bridge at all.
	for {
		frame, readErr := caller.ReadWithTimeout(3 * time.Second)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if frame["type"] == "error" && frame["request_id"] == "file-denied" {
			if frame["code"] != "invalid_execution" {
				t.Fatal(frame)
			}
			break
		}
	}
	stop()
	<-done
	events, e := ds.EventsAfter(context.Background(), "down", 0)
	if e != nil {
		t.Fatal(e)
	}
	for _, ev := range events {
		if ev.Type != "agent.hello" {
			raw, _ := json.Marshal(ev)
			t.Fatalf("unexpected downstream event: %s", raw)
		}
	}
}

func TestLateWorkerReconcilesUnknownAfterBridgeRestart(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprint(restart), func(t *testing.T) {
			u, _ := server(t)
			d, _ := server(t)
			c := cfg(u, d, "leader")
			c.Timeout = 100 * time.Millisecond
			caller := connect(t, u, "up", "caller")
			path := filepath.Join(t.TempDir(), "bridge.db")
			b, e := Open(c, path)
			if e != nil {
				t.Fatal(e)
			}
			stop, done := runBridge(t, b)
			awaitHello(t, caller, "edge")
			if e = caller.Invoke("late", agent.TextCapability, "edge", map[string]any{"text": "late worker"}); e != nil {
				t.Fatal(e)
			}
			if r := waitResult(t, caller, "late"); r.Status != "unknown" {
				t.Fatal(r)
			}
			if restart {
				stop()
				if e = <-done; e != nil {
					t.Fatal(e)
				}
				b.Close()
				b, e = Open(c, path)
				if e != nil {
					t.Fatal(e)
				}
				stop, done = runBridge(t, b)
				awaitHello(t, caller, "edge")
			}
			defer b.Close()
			defer func() {
				stop()
				if e := <-done; e != nil {
					t.Error(e)
				}
			}()
			worker := connect(t, d, "down", "worker")
			executor, e := agent.OpenExecutor(filepath.Join(t.TempDir(), "worker.db"))
			if e != nil {
				t.Fatal(e)
			}
			defer executor.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			workerDone := make(chan error, 1)
			go func() { workerDone <- agent.RunExecutor(ctx, worker, executor, nil) }()
			defer func() { cancel(); <-workerDone }()
			for {
				r := waitResult(t, caller, "late")
				if r.Status == "unknown" {
					continue
				}
				if r.Status != "completed" || r.Output == nil || r.Output.Text != "LATE WORKER" {
					t.Fatal(r)
				}
				break
			}
			if e = caller.Invoke("healthy", agent.TextCapability, "edge", map[string]any{"text": "still running"}); e != nil {
				t.Fatal(e)
			}
			if r := waitResult(t, caller, "healthy"); r.Status != "completed" {
				t.Fatal(r)
			}
		})
	}
}

func TestCapabilityNamespaceAndImmutableRoutes(t *testing.T) {
	c := cfg("ws://up/ws", "ws://down/ws", "leader")
	c.Routes[0].Capability = "cap.text.arbitrary"
	if err := c.validate(); err == nil {
		t.Fatal("accepted unsupported legacy capability")
	}
	c.Routes[0].Capability = "cap.learned.text.example"
	b, err := Open(c, filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	c.Routes[0].Worker = "changed"
	if b.config.Routes[0].Worker != "worker" {
		t.Fatal("caller mutated configured routes")
	}
}
