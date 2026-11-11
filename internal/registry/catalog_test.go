package registry

import (
	"github.com/jiejhong/GooseCab/internal/protocol"
	"testing"
)

func TestKindsFiltersAndManual(t *testing.T) {
	r := New()
	r.RegisterHello("worker", "Worker", "", []string{"cap.skill.guide", "cap.mcp.search", "cap.knowledge.lessons"})
	r.Announce("worker", CapabilityFromAnnouncement(protocol.CapabilityAnnounce{CapabilityID: "cap.skill.guide", Kind: "skill", Description: "Operator guide", Callable: false}))
	if got := r.SearchCapabilities("guide", "skill"); len(got) != 1 || len(got[0].Capabilities) != 1 || got[0].Capabilities[0].Callable {
		t.Fatal(got)
	}
	if len(r.SearchCapabilities("guide", "mcp")) != 0 {
		t.Fatal("filter mismatch")
	}
	r.RegisterHello("worker", "", "", []string{"cap.skill.guide"})
	if r.List()[0].Capabilities[0].Callable {
		t.Fatal("reconnect changed manual")
	}
}
