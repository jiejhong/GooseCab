package skillbundle

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/jiejhong/GooseCab/internal/agent"
	"github.com/jiejhong/GooseCab/internal/eventbus"
	"github.com/jiejhong/GooseCab/internal/gateway"
	"github.com/jiejhong/GooseCab/internal/registry"
	"github.com/jiejhong/GooseCab/internal/store"
)

func TestProviderAnnouncementsAndInvocationThroughGateway(t *testing.T) {
	for _, manual := range []bool{false, true} {
		name := "recipe"
		if manual {
			name = "manual"
		}
		t.Run(name, func(t *testing.T) {
			local := openStore(t)
			in, e := local.Stage(bytes.NewReader(bundle(t, 1, "!", manual, 0)))
			if e != nil {
				t.Fatal(e)
			}
			if e = local.Approve(in.SHA256, in.CapabilityID, 1); e != nil {
				t.Fatal(e)
			}
			db, e := store.Open(filepath.Join(t.TempDir(), "gateway.db"))
			if e != nil {
				t.Fatal(e)
			}
			defer db.Close()
			reg := registry.New()
			server := httptest.NewServer((&gateway.WS{Store: db, Bus: eventbus.New(), Registry: reg}).Handler(http.NotFoundHandler()))
			defer server.Close()
			ex, e := agent.OpenExecutor(filepath.Join(t.TempDir(), "journal.db"))
			if e != nil {
				t.Fatal(e)
			}
			defer ex.Close()
			if e = ex.UseProvider(local); e != nil {
				t.Fatal(e)
			}
			ids, e := ex.Capabilities()
			if e != nil {
				t.Fatal(e)
			}
			url := "ws" + server.URL[4:] + "/api/v1/agent/connect"
			worker, e := agent.Dial(url, "room", "worker", ids)
			if e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- agent.RunExecutor(ctx, worker, ex, nil) }()
			defer func() { worker.Close(); <-done }()
			// Wait for the validated descriptor, not merely the ID from agent.hello.
			accepted := false
			for !accepted {
				a, ok := reg.Get("worker")
				if ok {
					for _, c := range a.Capabilities {
						if c.ID == in.CapabilityID && c.Skill != nil && c.Skill.Digest == in.SHA256 {
							if c.Callable == manual {
								t.Fatal("incorrect callable flag")
							}
							accepted = true
						}
					}
				}
				if accepted {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("Skill announcement never accepted")
				case <-time.After(5 * time.Millisecond):
				}
			}
			events, e := db.EventsAfter(ctx, "room", 0)
			if e != nil {
				t.Fatal(e)
			}
			found := false
			for _, event := range events {
				if event.Type == "capability.announce" {
					found = true
				}
			}
			if !found {
				t.Fatal("announcement not persisted")
			}
			if !manual {
				caller, e := agent.Dial(url, "room", "caller", nil)
				if e != nil {
					t.Fatal(e)
				}
				defer caller.Close()
				r, e := caller.InvokeTextCapability(ctx, "bundle-call", in.CapabilityID, "worker", " WORLD ", nil)
				if e != nil || r.Output == nil || r.Output.Text != "world!" {
					t.Fatalf("round trip: %+v %v", r, e)
				}
			}
		})
	}
}
