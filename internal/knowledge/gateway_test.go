package knowledge_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiejhong/GooseCab/internal/agent"
	"github.com/jiejhong/GooseCab/internal/eventbus"
	"github.com/jiejhong/GooseCab/internal/gateway"
	"github.com/jiejhong/GooseCab/internal/knowledge"
	"github.com/jiejhong/GooseCab/internal/registry"
	"github.com/jiejhong/GooseCab/internal/store"
)

func TestKnowledgeGatewayExecutorRoundTrip(t *testing.T) {
	local, e := knowledge.Open(filepath.Join(t.TempDir(), "knowledge.db"), "alice")
	if e != nil {
		t.Fatal(e)
	}
	defer local.Close()
	ref, e := local.Put(knowledge.Record{ID: "retry", Version: 1, Title: "Retry lesson", Body: "PRIVATE_KNOWLEDGE_BODY", Source: "/private/SOURCE_SECRET?token=PRIVATE_SOURCE_TOKEN", Tags: []string{"PRIVATE_TAG"}})
	if e != nil {
		t.Fatal(e)
	}
	if e = local.Share(knowledge.Collection{Name: "lessons", Records: []knowledge.Ref{ref}, Callers: []string{"bob"}}); e != nil {
		t.Fatal(e)
	}
	db, e := store.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	catalog := registry.New()
	srv := httptest.NewServer((&gateway.WS{Store: db, Bus: eventbus.New(), Registry: catalog}).Handler(http.NotFoundHandler()))
	defer srv.Close()
	executor, e := agent.OpenExecutor(filepath.Join(t.TempDir(), "execution.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer executor.Close()
	if e = executor.UseProvider(local); e != nil {
		t.Fatal(e)
	}
	caps, e := executor.Capabilities()
	if e != nil {
		t.Fatal(e)
	}
	url := "ws" + srv.URL[4:] + "/api/v1/agent/connect"
	worker, e := agent.Dial(url, "private-room", "alice", caps)
	if e != nil {
		t.Fatal(e)
	}
	defer worker.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- agent.RunExecutor(ctx, worker, executor, nil) }()
	defer func() { worker.Close(); <-done }()
	bob, e := agent.Dial(url, "private-room", "bob", nil)
	if e != nil {
		t.Fatal(e)
	}
	defer bob.Close()
	// A hello lists capability IDs but deliberately does not authorize execution.
	// Wait for the persisted rich announcement before submitting the first call.
	ready := false
	for !ready {
		if advertised, ok := catalog.Get("alice"); ok {
			for _, capability := range advertised.Capabilities {
				if capability.ID == "cap.knowledge.lessons" && capability.Callable && capability.Kind == "knowledge" && capability.Knowledge != nil && capability.Knowledge.Collection == "lessons" {
					ready = true
					break
				}
			}
		}
		if !ready {
			select {
			case <-ctx.Done():
				t.Fatal("knowledge announcement not ready:", ctx.Err())
			case <-time.After(time.Millisecond):
			}
		}
	}
	r, e := bob.InvokeCapability(ctx, "allowed-1", "cap.knowledge.lessons", "alice", map[string]any{"query": "retry"}, nil)
	if e != nil || r.Status != "completed" || r.Output == nil || !strings.Contains(r.Output.Text, "Retry lesson") {
		t.Fatalf("result=%+v error=%v", r, e)
	}
	mallory, e := agent.Dial(url, "private-room", "mallory", nil)
	if e != nil {
		t.Fatal(e)
	}
	defer mallory.Close()
	denied, e := mallory.InvokeCapability(ctx, "denied-1", "cap.knowledge.lessons", "alice", map[string]any{"query": "retry"}, nil)
	if e != nil || denied.Status != "failed" || denied.Output != nil {
		t.Fatalf("caller ACL: result=%+v error=%v", denied, e)
	}
	events, e := db.EventsAfter(ctx, "private-room", 0)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(events)
	for _, secret := range []string{"PRIVATE_KNOWLEDGE_BODY", "SOURCE_SECRET", "PRIVATE_SOURCE_TOKEN", "PRIVATE_TAG"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("private content leaked into gateway events: %s", secret)
		}
	}
}
