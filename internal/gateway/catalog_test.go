package gateway

import (
	"encoding/json"
	"github.com/jiejhong/GooseCab/internal/protocol"
	"github.com/jiejhong/GooseCab/internal/registry"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewKindsStrictLifecycle(t *testing.T) {
	for _, id := range []string{"cap.mcp.search", "cap.knowledge.lessons", "cap.skill.upper"} {
		payload := map[string]any{"query": "hello"}
		if protocol.IsExecutableTextCapability(id) {
			payload = map[string]any{"text": "hello"}
		}
		v := protocol.CapabilityInvoke{RequestID: "r", CapabilityID: id, TargetAgentID: "worker", Payload: payload}
		if err := validateExecution(nil, "caller", "capability.invoke", v); err != nil {
			t.Fatal(err)
		}
		events := []protocol.Event{{Type: "capability.invoke", CorrelationID: "r", Actor: protocol.Actor{Kind: "agent", ID: "caller"}, Payload: v}}
		if validateExecution(events, "intruder", "capability.invoke", v) == nil {
			t.Fatal("replay caller bypass")
		}
		if validateExecution(events, "intruder", "capability.result", protocol.CapabilityResult{RequestID: "r", Status: "failed", Error: "no"}) == nil {
			t.Fatal("result owner bypass")
		}
		v.TargetAgentID = "other"
		if validateExecution(events, "caller", "capability.invoke", v) == nil {
			t.Fatal("target replay bypass")
		}
	}
}
func TestUnsafeAnnounceNotPersisted(t *testing.T) {
	s, srv, _ := securityServer(t)
	c := securityDial(t, srv, "/api/v1/agent/connect", "catalog")
	writeFrame(t, c, map[string]any{"type": "agent.hello", "agent_id": "worker"})
	securityRead(t, c, "agent.welcome")
	expectProtocolError(t, c, map[string]any{"type": "capability.announce", "capability_id": "cap.mcp.search", "mcp": map[string]any{"server_name": "server", "tool_name": "search", "connection_manifest_id": "local", "token": "secret"}}, "invalid_announce")
	if len(storedEvents(t, s, "catalog")) != 1 {
		t.Fatal("unsafe announcement persisted")
	}
}

func TestManualCapabilityCannotBeInvoked(t *testing.T) {
	s, srv, reg := securityServer(t)
	reg.RegisterHello("manual", "Manual", "", []string{"cap.skill.guide"})
	reg.Announce("manual", registry.Capability{ID: "cap.skill.guide", Kind: "skill", Callable: false})
	c := securityDial(t, srv, "/api/v1/agent/connect", "manual-room")
	writeFrame(t, c, map[string]any{"type": "agent.hello", "agent_id": "caller"})
	securityRead(t, c, "agent.welcome")
	expectProtocolError(t, c, map[string]any{"type": "capability.invoke", "request_id": "manual-call", "capability_id": "cap.skill.guide", "target_agent_id": "manual", "payload": map[string]any{"text": "hello"}}, "invalid_execution")
	if len(storedEvents(t, s, "manual-room")) != 1 {
		t.Fatal("manual invocation persisted")
	}
}
func TestCatalogHTTPFilters(t *testing.T) {
	r := registry.New()
	r.RegisterHello("worker", "Worker", "", []string{"cap.mcp.search", "cap.skill.guide"})
	api := (&API{Registry: r}).Handler()
	req := httptest.NewRequest("GET", "/api/capabilities?kind=mcp&q=search", nil)
	w := httptest.NewRecorder()
	api.ServeHTTP(w, req)
	var got []registry.Agent
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || len(got) != 1 || len(got[0].Capabilities) != 1 || got[0].Capabilities[0].Kind != "mcp" || got[0].Capabilities[0].Callable {
		t.Fatal(w.Body.String())
	}
	w = httptest.NewRecorder()
	api.ServeHTTP(w, httptest.NewRequest("GET", "/api/capabilities?kind=unknown", nil))
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	w = httptest.NewRecorder()
	api.ServeHTTP(w, httptest.NewRequest("POST", "/api/agents", strings.NewReader(`{"id":"unsafe","capabilities":[{"id":"cap.mcp.tool","mcp":{"server_name":"x","tool_name":"y","connection_manifest_id":"z","headers":{"token":"secret"}}}]}`)))
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	if _, ok := r.Get("unsafe"); ok {
		t.Fatal("unsafe catalog metadata accepted")
	}
}
