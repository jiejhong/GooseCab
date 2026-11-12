package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jiejhong/GooseCab/internal/agent"
	"github.com/jiejhong/GooseCab/internal/eventbus"
	"github.com/jiejhong/GooseCab/internal/gateway"
	"github.com/jiejhong/GooseCab/internal/skills"
	"github.com/jiejhong/GooseCab/internal/store"
)

func testPlan() Plan {
	return Plan{ID: "p", URL: "ws://localhost/api/v1/agent/connect", Room: "room", Caller: "planner", Concurrency: 2, Separator: "|", Tasks: []Task{{ID: "a", Target: "one", Text: "hello"}, {ID: "b", Target: "two", Text: "world"}, {ID: "join", Target: "one", Text: "result", DependsOn: []string{"a", "b"}}}}
}
func TestValidation(t *testing.T) {
	tests := []func(*Plan){func(p *Plan) { p.Tasks[0].DependsOn = []string{"join"} }, func(p *Plan) { p.Tasks[0].DependsOn = []string{"missing"} }, func(p *Plan) { p.Tasks[0].DependsOn = []string{"b", "b"} }, func(p *Plan) { p.Tasks[0].Target = "" }, func(p *Plan) { p.Tasks[0].Text = strings.Repeat("x", 4097) }, func(p *Plan) { p.Tasks[0].Text = string([]byte{255}) }, func(p *Plan) { p.Tasks[1].ID = "a" }, func(p *Plan) { p.Concurrency = 9 }, func(p *Plan) { p.URL = "ws://user:secret@host/" }}
	if e := testPlan().Validate(); e != nil {
		t.Fatal(e)
	}
	for i, change := range tests {
		p := testPlan()
		change(&p)
		if p.Validate() == nil {
			t.Errorf("invalid plan %d accepted", i)
		}
	}
}
func fixture(t *testing.T) (*store.Store, string) {
	t.Helper()
	s, e := store.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	srv := httptest.NewServer((&gateway.WS{Store: s, Bus: eventbus.New()}).Handler(http.NotFoundHandler()))
	t.Cleanup(srv.Close)
	return s, "ws" + srv.URL[4:] + "/api/v1/agent/connect"
}
func worker(t *testing.T, ctx context.Context, url, id string, hook func(map[string]any) error) {
	t.Helper()
	c, e := agent.Dial(url, "room", id, []string{agent.TextCapability})
	if e != nil {
		t.Fatal(e)
	}
	x, e := agent.OpenExecutor(filepath.Join(t.TempDir(), "worker.db"))
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- agent.RunExecutor(ctx, c, x, hook) }()
	t.Cleanup(func() { c.Close(); <-done; x.Close() })
}
func calls(t *testing.T, s *store.Store) int {
	t.Helper()
	events, e := s.EventsAfter(context.Background(), "room", 0)
	if e != nil {
		t.Fatal(e)
	}
	n := 0
	for _, v := range events {
		if v.Type == "capability.invoke" {
			n++
		}
	}
	return n
}
func TestRealConcurrentDAGAndRestart(t *testing.T) {
	s, url := fixture(t)
	p := testPlan()
	p.URL = url
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Both real workers must observe their own first assignment before either
	// executes. A serial scheduler cannot satisfy this barrier.
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	go func() { <-arrived; <-arrived; close(release) }()
	for _, id := range []string{"one", "two"} {
		id := id
		var once sync.Once
		worker(t, ctx, url, id, func(m map[string]any) error {
			if m["type"] == "capability.invoke" {
				payload, _ := m["payload"].(map[string]any)
				if payload["target_agent_id"] == id {
					once.Do(func() {
						arrived <- struct{}{}
						select {
						case <-release:
						case <-ctx.Done():
						}
					})
				}
			}
			return nil
		})
	}
	path := filepath.Join(t.TempDir(), "plan.db")
	j, e := Open(path, p)
	if e != nil {
		t.Fatal(e)
	}
	if e = j.Run(ctx, Options{Timeout: 3 * time.Second}); e != nil {
		t.Fatal(e)
	}
	got := j.States()
	if got[2].Result.Output.Text != "RESULT|HELLO|WORLD" {
		t.Fatalf("bad fan-in: %+v", got[2])
	}
	ev, e := j.Events()
	if e != nil || len(ev) != 9 {
		t.Fatalf("events %d %v", len(ev), e)
	}
	j.Close()
	j, e = Open(path, p)
	if e != nil {
		t.Fatal(e)
	}
	defer j.Close()
	if e = j.Run(ctx, Options{Timeout: time.Second}); e != nil {
		t.Fatal(e)
	}
	if n := calls(t, s); n != 3 {
		t.Fatalf("restart resubmitted: %d", n)
	}
}
func TestUnknownRestartNeverResubmitsAndReconciles(t *testing.T) {
	s, url := fixture(t)
	p := testPlan()
	p.URL = url
	p.Tasks = p.Tasks[:1]
	p.Tasks[0].Target = "late"
	path := filepath.Join(t.TempDir(), "plan.db")
	j, e := Open(path, p)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if e = j.Run(ctx, Options{Timeout: 80 * time.Millisecond}); e == nil {
		t.Fatal("offline task succeeded")
	}
	if j.States()[0].Status != "unknown" {
		t.Fatal(j.States())
	}
	j.Close()
	j, e = Open(path, p)
	if e != nil {
		t.Fatal(e)
	}
	defer j.Close()
	if e = j.Run(ctx, Options{Timeout: time.Second}); e == nil {
		t.Fatal("unknown task rerun")
	}
	if calls(t, s) != 1 {
		t.Fatal("resubmitted unknown")
	}
	if e = j.Reconcile(ctx, Options{Timeout: time.Second}); e != nil {
		t.Fatal(e)
	}
	if calls(t, s) != 1 || j.States()[0].Status != "unknown" {
		t.Fatal("reconcile submitted missing work")
	}
	worker(t, ctx, url, "late", nil)
	for j.States()[0].Status != "completed" {
		if e = j.Reconcile(ctx, Options{Timeout: time.Second}); e != nil {
			t.Fatal(e)
		}
		if ctx.Err() != nil {
			t.Fatal("late result not recovered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if calls(t, s) != 1 {
		t.Fatal("reconcile resubmitted")
	}
}
func TestInterruptedClaimLockAndPlanConflict(t *testing.T) {
	p := testPlan()
	path := filepath.Join(t.TempDir(), "plan.db")
	j, e := Open(path, p)
	if e != nil {
		t.Fatal(e)
	}
	if x, e := Open(path, p); e == nil {
		x.Close()
		t.Fatal("second owner allowed")
	}
	state := j.States()[0]
	state.Status = "running"
	state.Input = "hello"
	if e = j.save(state); e != nil {
		t.Fatal(e)
	}
	j.Close()
	j, e = Open(path, p)
	if e != nil {
		t.Fatal(e)
	}
	if j.States()[0].Status != "unknown" {
		t.Fatal("interrupted task not unknown")
	}
	j.Close()
	p.Tasks[0].Text = "different"
	if x, e := Open(path, p); e == nil {
		x.Close()
		t.Fatal("changed plan accepted")
	}
}
func TestFanInBoundsAndFailedDependency(t *testing.T) {
	_, url := fixture(t)
	p := testPlan()
	p.URL = url
	p.Tasks[0].Text = strings.Repeat("a", 4096)
	p.Tasks = p.Tasks[:2]
	p.Tasks[1].DependsOn = []string{"a"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	worker(t, ctx, url, "one", nil)
	j, e := Open(filepath.Join(t.TempDir(), "plan.db"), p)
	if e != nil {
		t.Fatal(e)
	}
	defer j.Close()
	if e = j.Run(ctx, Options{Timeout: time.Second}); e == nil {
		t.Fatal("oversized fan-in accepted")
	}
	if j.States()[1].Status != "failed" {
		t.Fatal(j.States())
	}
}

func TestRunAndReconcileBoundStalledHandshake(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer srv.Close()
	p := testPlan()
	p.URL = "ws" + srv.URL[4:] + "/api/v1/agent/connect"
	p.Tasks = p.Tasks[:1]
	j, e := Open(filepath.Join(t.TempDir(), "plan.db"), p)
	if e != nil {
		t.Fatal(e)
	}
	defer j.Close()
	for _, action := range []func(context.Context, Options) error{j.Run, j.Reconcile} {
		start := time.Now()
		if e = action(context.Background(), Options{Timeout: 40 * time.Millisecond}); e == nil {
			t.Fatal("stalled handshake succeeded")
		}
		if time.Since(start) > time.Second {
			t.Fatal("per-task deadline ignored")
		}
	}
	if j.States()[0].Status != "unknown" {
		t.Fatal(j.States())
	}
}

func TestReconcileRejectsDifferentInputWithoutSubmission(t *testing.T) {
	s, url := fixture(t)
	p := testPlan()
	p.URL = url
	p.Tasks = p.Tasks[:1]
	j, e := Open(filepath.Join(t.TempDir(), "plan.db"), p)
	if e != nil {
		t.Fatal(e)
	}
	defer j.Close()
	if e = j.Run(context.Background(), Options{Timeout: 60 * time.Millisecond}); e == nil {
		t.Fatal("offline task completed")
	}
	state := j.States()[0]
	state.Input = "changed"
	if e = j.save(state); e != nil {
		t.Fatal(e)
	}
	if e = j.Reconcile(context.Background(), Options{Timeout: time.Second}); e == nil {
		t.Fatal("identity conflict ignored")
	}
	if calls(t, s) != 1 {
		t.Fatal("recovery emitted a call")
	}
}

func TestFailedDependencyAndCanceledRunNeverDispatch(t *testing.T) {
	s, url := fixture(t)
	p := testPlan()
	p.URL = url
	p.Tasks = []Task{{ID: "a", Target: "offline", Text: "a"}, {ID: "b", Target: "offline", DependsOn: []string{"a"}}}
	j, e := Open(filepath.Join(t.TempDir(), "plan.db"), p)
	if e != nil {
		t.Fatal(e)
	}
	defer j.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e = j.Run(ctx, Options{Timeout: time.Second}); e == nil {
		t.Fatal("cancel ignored")
	}
	if calls(t, s) != 0 {
		t.Fatal("canceled run dispatched")
	}
	state := j.States()[0]
	state.Status = "failed"
	state.Error = "known local failure"
	if e = j.save(state); e != nil {
		t.Fatal(e)
	}
	if e = j.Run(context.Background(), Options{Timeout: time.Second}); e == nil {
		t.Fatal("failed plan succeeded")
	}
	if j.States()[1].Status != "blocked" || calls(t, s) != 0 {
		t.Fatal("failed dependency dispatched")
	}
}

func TestRealDAGExecutesInstalledLearnedCapability(t *testing.T) {
	s, url := fixture(t)
	p := testPlan()
	p.URL = url
	p.Tasks = p.Tasks[:2]
	p.Tasks[1].Target = "learner"
	p.Tasks[1].Capability = "cap.learned.text.decorate"
	p.Tasks[1].Text = "join"
	p.Tasks[1].DependsOn = []string{"a"}
	local, e := skills.Open(filepath.Join(t.TempDir(), "skills.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer local.Close()
	pkg := skills.Package{Format: 1, Capability: p.Tasks[1].Capability, Version: 1, Operations: []skills.Operation{{Op: "lower"}, {Op: "prefix", Value: "learned:"}}, Tests: []skills.Case{{Input: "ABC", Want: "learned:abc"}}}
	b, e := json.Marshal(pkg)
	if e != nil {
		t.Fatal(e)
	}
	if e = local.Install(bytes.NewReader(b), skills.Digest(b), pkg.Capability, 1); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	worker(t, ctx, url, "one", nil)
	x, e := agent.OpenExecutor(filepath.Join(t.TempDir(), "learner.db"))
	if e != nil {
		t.Fatal(e)
	}
	x.UseSkills(local)
	caps, e := x.Capabilities()
	if e != nil {
		t.Fatal(e)
	}
	c, e := agent.Dial(url, "room", "learner", caps)
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- agent.RunExecutor(ctx, c, x, nil) }()
	defer func() { c.Close(); <-done; x.Close() }()
	path := filepath.Join(t.TempDir(), "plan.db")
	j, e := Open(path, p)
	if e != nil {
		t.Fatal(e)
	}
	defer j.Close()
	if e = j.Run(ctx, Options{Timeout: time.Second}); e != nil {
		t.Fatal(e)
	}
	state := j.States()[1]
	if state.Result.Output.Text != "learned:join|hello" {
		t.Fatal(state.Result)
	}
	// Simulate loss of coordinator acknowledgement, then recover using the exact
	// learned capability in the durable original invocation.
	state.Status = "unknown"
	state.Result = nil
	if e = j.save(state); e != nil {
		t.Fatal(e)
	}
	if e = j.Reconcile(ctx, Options{Timeout: time.Second}); e != nil {
		t.Fatal(e)
	}
	if j.States()[1].Status != "completed" || calls(t, s) != 2 {
		t.Fatal("learned recovery failed or resubmitted")
	}
	// Recovery with an altered capability must not reuse the same request ID.
	c2, e := agent.Dial(url, "room", p.Caller, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer c2.Close()
	if _, _, e = c2.RecoverTextCapability(state.RequestID, agent.TextCapability, state.Target, state.Input); e == nil {
		t.Fatal("changed capability accepted")
	}
	if calls(t, s) != 2 {
		t.Fatal("recovery submitted a task")
	}
}

func TestCapabilityValidationAndCompatibility(t *testing.T) {
	p := testPlan()
	if p.Tasks[0].CapabilityID() != agent.TextCapability {
		t.Fatal("missing compatibility default")
	}
	for _, capability := range []string{"shell.exec", "cap.echo", "cap.learned.text.", "cap.learned.text.bad/name"} {
		p.Tasks[0].Capability = capability
		if p.Validate() == nil {
			t.Fatalf("accepted %s", capability)
		}
	}
	p.Tasks[0].Capability = "cap.learned.text.lower"
	if e := p.Validate(); e != nil {
		t.Fatal(e)
	}
	// This text grows under uppercase, but a learned lower operation need not
	// grow it. Only the actual selected worker validates operation output.
	p.Tasks[0].Text = strings.Repeat("ȿ", 2048)
	if e := p.Validate(); e != nil {
		t.Fatal(e)
	}
}
