package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/jiejhong/GooseCab/internal/eventbus"
	"github.com/jiejhong/GooseCab/internal/gateway"
	"github.com/jiejhong/GooseCab/internal/store"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jiejhong/GooseCab/internal/relay"
	"github.com/jiejhong/GooseCab/internal/skills"
)

func TestTeachOverRelayThenExecuteOfflineAfterReopen(t *testing.T) {
	// Teacher and learner have separate disks; only the streaming relay joins them.
	teacherDir, learnerDir := t.TempDir(), t.TempDir()
	p := skills.Package{Format: 1, Capability: "cap.learned.text.greeting", Version: 1, Operations: []skills.Operation{{Op: "trim"}, {Op: "lower"}, {Op: "prefix", Value: "hello "}}, Tests: []skills.Case{{Input: " WORLD ", Want: "hello world"}}}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(teacherDir, "package.json")
	if err = os.WriteFile(source, b, 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(relay.New(relay.Config{Timeout: 5 * time.Second}))
	defer server.Close()
	client := relay.Client{URL: server.URL}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ticket, err := client.Create(ctx, "teacher", "learner", int64(len(b)), skills.Digest(b))
	if err != nil {
		t.Fatal(err)
	}
	sourceFile, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	sent := make(chan error, 1)
	go func() { defer sourceFile.Close(); sent <- client.Send(ctx, ticket, sourceFile) }()
	target, err := client.ReceiveFile(ctx, ticket, learnerDir, "received.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = <-sent; err != nil {
		t.Fatal(err)
	}
	// The teacher and relay are gone before installation or execution.
	server.Close()
	if err = os.Remove(source); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(learnerDir, "skills.db")
	local, err := skills.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	err = local.Install(f, skills.Digest(b), p.Capability, 1)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	local.Close()
	local, err = skills.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	journal := filepath.Join(learnerDir, "executor.db")
	e, err := OpenExecutor(journal)
	if err != nil {
		t.Fatal(err)
	}
	e.UseSkills(local)
	evt := invocation("learned-1", " GOOSE ")
	evt["payload"].(map[string]any)["capability_id"] = p.Capability
	var frames []map[string]any
	send := func(v any) error { frames = append(frames, normalized(t, v)); return nil }
	handled, err := e.Handle(context.Background(), "room", "worker", evt, send)
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if frames[len(frames)-1]["output"].(map[string]any)["text"] != "hello goose" {
		t.Fatalf("%#v", frames)
	}
	e.Close()
	e, err = OpenExecutor(journal)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	e.UseSkills(local)
	frames = nil
	if _, err = e.Handle(context.Background(), "room", "worker", evt, send); err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 || frames[0]["status"] != "completed" {
		t.Fatalf("learned invocation did not replay durable result: %#v", frames)
	}
	// A new invocation after executor reopen executes the installed recipe offline.
	evt = invocation("learned-2", " CAB ")
	evt["payload"].(map[string]any)["capability_id"] = p.Capability
	frames = nil
	if _, err = e.Handle(context.Background(), "room", "worker", evt, send); err != nil {
		t.Fatal(err)
	}
	if len(frames) != 3 || frames[2]["output"].(map[string]any)["text"] != "hello cab" {
		t.Fatalf("%#v", frames)
	}
}

func TestLearnedCapabilityGatewayRoundTrip(t *testing.T) {
	p := skills.Package{Format: 1, Capability: "cap.learned.text.lower", Version: 1, Operations: []skills.Operation{{Op: "lower"}}, Tests: []skills.Case{{Input: "ABC", Want: "abc"}}}
	b, _ := json.Marshal(p)
	local, err := skills.Open(filepath.Join(t.TempDir(), "skills.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	if err = local.Install(bytes.NewReader(b), skills.Digest(b), p.Capability, 1); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	srv := httptest.NewServer((&gateway.WS{Store: s, Bus: eventbus.New()}).Handler(http.NotFoundHandler()))
	defer srv.Close()
	e, err := OpenExecutor(filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	e.UseSkills(local)
	caps, err := e.Capabilities()
	if err != nil {
		t.Fatal(err)
	}
	url := "ws" + srv.URL[4:] + "/api/v1/agent/connect"
	worker, err := Dial(url, "room", "worker", caps)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- RunExecutor(ctx, worker, e, nil) }()
	defer func() { worker.Close(); <-done }()
	caller, err := Dial(url, "room", "caller", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer caller.Close()
	result, err := caller.InvokeTextCapability(ctx, "learned-network", p.Capability, "worker", "GOOSE", nil)
	if err != nil || result.Output == nil || result.Output.Text != "goose" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}
