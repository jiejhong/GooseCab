package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jiejhong/GooseCab/internal/eventbus"
	"github.com/jiejhong/GooseCab/internal/gateway"
	"github.com/jiejhong/GooseCab/internal/protocol"
	"github.com/jiejhong/GooseCab/internal/store"
)

func invocation(id, text string) map[string]any {
	return map[string]any{"type": "capability.invoke", "room_id": "room", "correlation_id": id, "actor": map[string]any{"kind": "agent", "id": "caller"}, "payload": map[string]any{"request_id": id, "capability_id": TextCapability, "target_agent_id": "worker", "payload": map[string]any{"text": text}}}
}
func normalized(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err = json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}
func TestExecutorDurableResultsAndNoRepeat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "executions.db")
	e, err := OpenExecutor(path)
	if err != nil {
		t.Fatal(err)
	}
	var frames []map[string]any
	send := func(v any) error { frames = append(frames, normalized(t, v)); return nil }
	if handled, err := e.Handle(context.Background(), "room", "worker", invocation("r1", "Hello 鸿梦阁"), send); err != nil || !handled {
		t.Fatalf("%v %v", handled, err)
	}
	if len(frames) != 3 || frames[1]["status"] != "completed" || frames[1]["result_available"] != nil {
		t.Fatalf("expected execution completion before result: %#v", frames)
	}
	if frames[2]["status"] != "completed" || frames[2]["error"] != nil || frames[2]["result_available"] != true || frames[2]["output"].(map[string]any)["text"] != "HELLO 鸿梦阁" {
		t.Fatalf("bad result %#v", frames)
	}
	e.Close()
	e, err = OpenExecutor(path)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	frames = nil
	if _, err = e.Handle(context.Background(), "room", "worker", invocation("r1", "Hello 鸿梦阁"), send); err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 || frames[0]["status"] != "completed" || frames[1]["error"] != nil {
		t.Fatalf("duplicate must replay completion/result, never running: %#v", frames)
	}
	if _, err = e.Handle(context.Background(), "room", "worker", invocation("r1", "changed"), send); err == nil {
		t.Fatal("expected ID conflict")
	}
}

func TestExecutorUncertainClaimNeverRunsAgain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "executions.db")
	e, err := OpenExecutor(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Handle(context.Background(), "room", "worker", invocation("r", "hello"), func(any) error { return errors.New("disconnected") }); err == nil {
		t.Fatal("expected send error")
	}
	e.Close()
	e, err = OpenExecutor(path)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	var frames []map[string]any
	if _, err = e.Handle(context.Background(), "room", "worker", invocation("r", "hello"), func(v any) error { frames = append(frames, normalized(t, v)); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(frames) != 1 || frames[0]["status"] != "unknown" || frames[0]["result_available"] != false {
		t.Fatalf("uncertain execution was retried: %#v", frames)
	}
}

func TestExecutorRetainsResultAfterDeliveryFailure(t *testing.T) {
	e, err := OpenExecutor(filepath.Join(t.TempDir(), "executions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	sends := 0
	if _, err = e.Handle(context.Background(), "room", "worker", invocation("r", "hello"), func(any) error {
		sends++
		if sends == 2 {
			return errors.New("lost completion")
		}
		return nil
	}); err == nil {
		t.Fatal("expected error")
	}
	var frames []map[string]any
	if _, err = e.Handle(context.Background(), "room", "worker", invocation("r", "hello"), func(v any) error { frames = append(frames, normalized(t, v)); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 || frames[1]["result_available"] != true {
		t.Fatalf("cached result lost: %#v", frames)
	}
}

func TestExecutorSafetyAndIsolation(t *testing.T) {
	e, err := OpenExecutor(filepath.Join(t.TempDir(), "executions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	for _, change := range []func(map[string]any){
		func(m map[string]any) { m["room_id"] = "other" },
		func(m map[string]any) { m["type"] = "chat.message" },
		func(m map[string]any) { m["payload"].(map[string]any)["target_agent_id"] = "other" },
		func(m map[string]any) { m["payload"].(map[string]any)["target_agent_id"] = "" },
		func(m map[string]any) { m["payload"].(map[string]any)["capability_id"] = "cap.shell" },
	} {
		m := invocation("r", "hello")
		change(m)
		if handled, err := e.Handle(context.Background(), "room", "worker", m, func(any) error { t.Fatal("unexpected send"); return nil }); err != nil || handled {
			t.Fatalf("ignored event handled: %v %v", handled, err)
		}
	}
	for _, payload := range []any{nil, "hi", map[string]any{"text": strings.Repeat("a", 4097)}, map[string]any{"text": "hi", "shell": "whoami"}, map[string]any{"text": 3}} {
		r := executeText(protocol.CapabilityInvoke{RequestID: "r", Payload: payload})
		if r.Status != "failed" || r.ResultAvailable || r.Output != nil {
			t.Fatalf("accepted unsafe payload %#v", payload)
		}
	}
	r := executeText(protocol.CapabilityInvoke{RequestID: "r", Payload: map[string]any{"text": "$(whoami); rm -rf /"}})
	if r.Status != "completed" || r.Output.Text != "$(WHOAMI); RM -RF /" {
		t.Fatalf("text treated as a command: %#v", r)
	}
}

func TestExecutorConcurrentDuplicate(t *testing.T) {
	e, err := OpenExecutor(filepath.Join(t.TempDir(), "executions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	var wg sync.WaitGroup
	var mu sync.Mutex
	running := 0
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.Handle(context.Background(), "room", "worker", invocation("r", "hello"), func(v any) error {
				m := normalized(t, v)
				mu.Lock()
				defer mu.Unlock()
				if m["status"] == "running" {
					running++
				}
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if running != 1 {
		t.Fatalf("executed %d times", running)
	}
}

func TestRealAgentExecutionRoundTripAndReconnect(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.AppendNext(context.Background(), &protocol.Event{RoomID: "room", Type: "error", Actor: protocol.Actor{Kind: "agent", ID: "intruder"}, Payload: map[string]any{"code": "evil", "message": "not a protocol error"}}); err != nil {
		t.Fatal(err)
	}
	h := (&gateway.WS{Store: s, Bus: eventbus.New()}).Handler(http.NotFoundHandler())
	srv := httptest.NewServer(h)
	defer srv.Close()
	url := "ws" + srv.URL[4:] + "/api/v1/agent/connect"
	worker, err := Dial(url, "room", "worker", []string{TextCapability})
	if err != nil {
		t.Fatal(err)
	}
	e, err := OpenExecutor(filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- RunExecutor(ctx, worker, e, nil) }()
	caller, err := Dial(url, "room", "caller", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer caller.Close()
	if err = caller.Invoke("roundtrip", TextCapability, "worker", map[string]any{"text": "real text"}); err != nil {
		t.Fatal(err)
	}
	readResult := func() map[string]any {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for {
			m, err := caller.ReadWithTimeout(time.Until(deadline))
			if err != nil {
				t.Fatal(err)
			}
			if m["type"] == "capability.result" && m["correlation_id"] == "roundtrip" {
				return m["payload"].(map[string]any)
			}
		}
	}
	result := readResult()
	if result["result_available"] != true || result["output"].(map[string]any)["text"] != "REAL TEXT" {
		t.Fatalf("no real result: %#v", result)
	}
	worker.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop on disconnect")
	}
	worker, err = Dial(url, "room", "worker", []string{TextCapability})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	go func() { done <- RunExecutor(ctx, worker, e, nil) }()
	if err = caller.Invoke("roundtrip", TextCapability, "worker", map[string]any{"text": "real text"}); err != nil {
		t.Fatal(err)
	}
	if got := readResult(); got["output"].(map[string]any)["text"] != "REAL TEXT" {
		t.Fatal("replay failed")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not cancel")
	}
	events, err := s.EventsAfter(context.Background(), "room", 0)
	if err != nil {
		t.Fatal(err)
	}
	running := 0
	for _, ev := range events {
		if ev.Type == "capability.progress" && ev.Payload.(map[string]any)["status"] == "running" {
			running++
		}
	}
	if running != 1 {
		t.Fatalf("reconnect reran execution: %d", running)
	}
}

func TestSaturatedOldInvocationDoesNotStopWorker(t *testing.T) {
	for _, status := range []string{"completed", "unknown"} {
		t.Run(status, func(t *testing.T) {
			s, err := store.Open(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			e, err := OpenExecutor(filepath.Join(t.TempDir(), "journal.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			_, err = e.Handle(context.Background(), "room", "worker", invocation("old", "hi"), func(any) error {
				if status == "unknown" {
					return errors.New("interrupted")
				}
				return nil
			})
			if (err != nil) != (status == "unknown") {
				t.Fatal(err)
			}
			old := protocol.CapabilityInvoke{RequestID: "old", CapabilityID: TextCapability, TargetAgentID: "worker", Payload: map[string]any{"text": "hi"}}
			event := protocol.Event{RoomID: "room", Type: "capability.invoke", CorrelationID: "old", Actor: protocol.Actor{Kind: "agent", ID: "caller"}, Payload: old}
			if err = s.AppendNext(context.Background(), &event); err != nil {
				t.Fatal(err)
			}
			result := protocol.CapabilityResult{RequestID: "old", Status: status, Error: "interrupted"}
			if status == "completed" {
				result = protocol.CapabilityResult{RequestID: "old", Status: "completed", ResultAvailable: true, Output: &protocol.TextOutput{Text: "HI", MediaType: "text/plain"}}
			}
			// Simulate a pre-upgrade saturated history. No new append may enlarge it.
			for i := 0; i < store.MaxInvocationEvents; i++ {
				event = protocol.Event{RoomID: "room", Type: "capability.result", CorrelationID: "old", Actor: protocol.Actor{Kind: "agent", ID: "worker"}, Payload: result}
				if err = s.AppendNext(context.Background(), &event); err != nil {
					t.Fatal(err)
				}
			}
			srv := httptest.NewServer((&gateway.WS{Store: s, Bus: eventbus.New()}).Handler(http.NotFoundHandler()))
			defer srv.Close()
			url := "ws" + srv.URL[4:] + "/api/v1/agent/connect"
			worker, err := Dial(url, "room", "worker", []string{TextCapability})
			if err != nil {
				t.Fatal(err)
			}
			defer worker.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- RunExecutor(ctx, worker, e, nil) }()
			caller, err := Dial(url, "room", "caller", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer caller.Close()
			deadline, stop := context.WithTimeout(ctx, 5*time.Second)
			defer stop()
			got, err := caller.InvokeText(deadline, "healthy", "worker", "works", nil)
			if err != nil || got.Status != "completed" || got.Output.Text != "WORKS" {
				t.Fatalf("saturated %s call blocked healthy task: %+v %v", status, got, err)
			}
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("worker failed to stop")
			}
		})
	}
}
