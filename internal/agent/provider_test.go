package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jiejhong/GooseCab/internal/protocol"
)

type fixtureProvider struct {
	ids           []string
	announcements []protocol.CapabilityAnnounce
	calls         int
	actor         string
	invalid       bool
}

func (p *fixtureProvider) CapabilityIDs() ([]string, error) { return p.ids, nil }
func (p *fixtureProvider) Announcements() ([]protocol.CapabilityAnnounce, error) {
	return p.announcements, nil
}
func (p *fixtureProvider) Invoke(ctx context.Context, v protocol.CapabilityInvoke) protocol.CapabilityResult {
	p.calls++
	_, p.actor, _ = protocol.InvocationActor(ctx)
	if p.invalid {
		return protocol.CapabilityResult{RequestID: v.RequestID, Status: "completed", ResultAvailable: true}
	}
	return protocol.CapabilityResult{RequestID: v.RequestID, Status: "completed", ResultAvailable: true, Output: &protocol.TextOutput{Text: "ok", MediaType: "text/plain"}}
}
func TestProvidersRejectDescriptorCollisions(t *testing.T) {
	for _, firstCallable := range []bool{false, true} {
		e, err := OpenExecutor(filepath.Join(t.TempDir(), "worker.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer e.Close()
		makeProvider := func(callable bool) *fixtureProvider {
			p := &fixtureProvider{announcements: []protocol.CapabilityAnnounce{{CapabilityID: "cap.skill.demo", Kind: "skill", Callable: callable, Skill: &protocol.SkillDescriptor{Digest: strings.Repeat("a", 64), Version: "1", EntrypointKind: "manual"}}}}
			if callable {
				p.ids = []string{"cap.skill.demo"}
				p.announcements[0].Skill.EntrypointKind = "text-recipe"
			}
			return p
		}
		if err = e.UseProvider(makeProvider(firstCallable)); err != nil {
			t.Fatal(err)
		}
		if err = e.UseProvider(makeProvider(!firstCallable)); err == nil {
			t.Fatal("accepted duplicate descriptor with different execution ownership")
		}
	}
}
func TestProviderUnknownNeverRerunsAndActorComesFromEnvelope(t *testing.T) {
	e, err := OpenExecutor(filepath.Join(t.TempDir(), "worker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	p := &fixtureProvider{ids: []string{"cap.mcp.demo"}, invalid: true, announcements: []protocol.CapabilityAnnounce{{CapabilityID: "cap.mcp.demo", Kind: "mcp", Callable: true, MCP: &protocol.MCPDescriptor{ServerName: "fixture", ToolName: "demo", ConnectionManifestID: "fixture"}}}}
	if err = e.UseProvider(p); err != nil {
		t.Fatal(err)
	}
	event := map[string]any{"type": "capability.invoke", "room_id": "room", "correlation_id": "one", "actor": map[string]any{"kind": "agent", "id": "real-caller"}, "payload": map[string]any{"request_id": "one", "capability_id": "cap.mcp.demo", "target_agent_id": "worker", "payload": map[string]any{"caller": "forged"}}}
	send := func(any) error { return nil }
	for i := 0; i < 2; i++ {
		if handled, err := e.Handle(context.Background(), "room", "worker", event, send); err != nil || !handled {
			t.Fatalf("%v %v", handled, err)
		}
	}
	if p.calls != 1 || p.actor != "real-caller" {
		t.Fatalf("calls=%d actor=%s", p.calls, p.actor)
	}
}
