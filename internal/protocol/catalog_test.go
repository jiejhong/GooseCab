package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCatalogKindsAndPayloads(t *testing.T) {
	for _, kind := range []string{"skill", "mcp", "knowledge"} {
		id := "cap." + kind + ".demo"
		a := CapabilityAnnounce{CapabilityID: id}
		if err := NormalizeAnnouncement(&a); err != nil || a.Kind != kind || a.Callable || !IsExecutableCapability(id) {
			t.Fatalf("%s: %+v %v", id, a, err)
		}
	}
	if IsExecutableTextCapability("cap.mcp.demo") {
		t.Fatal("MCP is not text execution")
	}
	for _, payload := range []any{nil, []any{}, map[string]any{"x": strings.Repeat("x", MaxCapabilityPayloadBytes)}} {
		if ValidateCapabilityPayload("cap.mcp.demo", payload) == nil {
			t.Fatal("invalid accepted")
		}
	}
	if err := ValidateCapabilityPayload("cap.mcp.demo", map[string]any{"query": "hello"}); err != nil {
		t.Fatal(err)
	}
}
func TestRejectUnsafeAnnouncements(t *testing.T) {
	for _, raw := range []string{
		`{"capability_id":"cap.mcp.demo","kind":"skill"}`,
		`{"capability_id":"cap.mcp.demo","mcp":{"server_name":"demo","tool_name":"search","connection_manifest_id":"https://example.test?token=secret"}}`,
		`{"capability_id":"cap.mcp.demo","mcp":{"server_name":"demo","tool_name":"search","connection_manifest_id":"local","headers":{"Authorization":"secret"}}}`,
		`{"capability_id":"cap.skill.demo","manifest":{"files":{"secret":"value"}}}`,
		`{"capability_id":"cap.knowledge.demo","knowledge":{"collection":"/private/path","share_policy":"shared"}}`,
		`{"capability_id":"cap.mcp.demo","input_schema":{"type":"object","default":"secret"}}`,
	} {
		var a CapabilityAnnounce
		if ValidateAnnouncementJSON([]byte(raw), &a) == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	a := CapabilityAnnounce{CapabilityID: "cap.mcp.demo", Kind: "mcp", Callable: true, MCP: &MCPDescriptor{ServerName: "demo", ToolName: "search", ConnectionManifestID: "local"}, InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`)}
	raw, _ := json.Marshal(a)
	if err := ValidateAnnouncementJSON(raw, &a); err != nil {
		t.Fatal(err)
	}
}

func TestExplicitFalseAndDescriptorRequired(t *testing.T) {
	for _, id := range []string{"cap.text.uppercase", "cap.skill.guide", "cap.mcp.search", "cap.knowledge.lessons"} {
		var a CapabilityAnnounce
		raw := []byte(`{"capability_id":"` + id + `","callable":false}`)
		if err := ValidateAnnouncementJSON(raw, &a); err != nil || a.Callable {
			t.Fatalf("explicit false lost: %+v %v", a, err)
		}
	}
	for _, id := range []string{"cap.skill.guide", "cap.mcp.search", "cap.knowledge.lessons"} {
		a := CapabilityAnnounce{CapabilityID: id, Callable: true}
		if NormalizeAnnouncement(&a) == nil {
			t.Fatalf("callable without descriptor: %s", id)
		}
	}
}
