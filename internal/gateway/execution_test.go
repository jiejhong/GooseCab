package gateway

import (
	"context"
	"github.com/jiejhong/GooseCab/internal/eventbus"
	"github.com/jiejhong/GooseCab/internal/protocol"
	"github.com/jiejhong/GooseCab/internal/store"
	"testing"
)

func TestExecutionValidation(t *testing.T) {
	invoke := protocol.CapabilityInvoke{RequestID: "r", CapabilityID: executableTextCapability, TargetAgentID: "worker", Payload: map[string]any{"text": "hi"}}
	event := func(actor, typ string, v any) protocol.Event {
		return protocol.Event{CorrelationID: "r", Actor: protocol.Actor{Kind: "agent", ID: actor}, Type: typ, Payload: v}
	}
	events := []protocol.Event{event("caller", "capability.invoke", invoke)}
	running := protocol.CapabilityProgress{RequestID: "r", Status: "running"}
	complete := protocol.CapabilityProgress{RequestID: "r", Status: "completed", Percent: 100}
	result := protocol.CapabilityResult{RequestID: "r", Status: "completed", ResultAvailable: true, Output: &protocol.TextOutput{Text: "HI", MediaType: "text/plain"}}
	assert := func(actor, typ string, v any, wantErr bool) {
		t.Helper()
		err := validateExecution(events, actor, typ, v)
		if (err != nil) != wantErr {
			t.Fatalf("actor=%s type=%s error=%v want error=%v", actor, typ, err, wantErr)
		}
	}
	assert("caller", "capability.invoke", invoke, false)
	assert("intruder", "capability.invoke", invoke, true)
	changed := invoke
	changed.TargetAgentID = "intruder"
	assert("caller", "capability.invoke", changed, true)
	changed = invoke
	changed.Payload = map[string]any{"text": "different"}
	assert("caller", "capability.invoke", changed, true)
	assert("intruder", "capability.progress", running, true)
	assert("intruder", "capability.result", result, true)
	assert("worker", "capability.result", result, true) // completion not yet reported
	assert("worker", "capability.progress", running, false)
	events = append(events, event("worker", "capability.progress", complete))
	assert("worker", "capability.progress", running, true)
	assert("worker", "capability.result", result, false)
	bad := result
	bad.Output = nil
	assert("worker", "capability.result", bad, true)
	bad = result
	bad.ResultAvailable = false
	assert("worker", "capability.result", bad, true)
	bad = result
	bad.OutputRef = "file:///private"
	assert("worker", "capability.result", bad, true)
	events = append(events, event("worker", "capability.result", result))
	assert("worker", "capability.result", result, false)     // exact replay
	assert("worker", "capability.progress", complete, false) // cached result recovery
	bad = result
	bad.Output = &protocol.TextOutput{Text: "changed", MediaType: "text/plain"}
	assert("worker", "capability.result", bad, true)
	assert("worker", "capability.progress", running, true)
}
func TestExecutionUnknownCanBeReconciledButNeverRegressed(t *testing.T) {
	invoke := protocol.CapabilityInvoke{RequestID: "r", CapabilityID: executableTextCapability, TargetAgentID: "worker"}
	events := []protocol.Event{{Type: "capability.invoke", CorrelationID: "r", Actor: protocol.Actor{ID: "caller"}, Payload: invoke}, {Type: "capability.result", CorrelationID: "r", Actor: protocol.Actor{ID: "worker"}, Payload: protocol.CapabilityResult{RequestID: "r", Status: "unknown", Error: "interrupted"}}}
	p := protocol.CapabilityProgress{RequestID: "r", Status: "completed", Percent: 100}
	if err := validateExecution(events, "worker", "capability.progress", p); err != nil {
		t.Fatal(err)
	}
	events = append(events, protocol.Event{Type: "capability.progress", CorrelationID: "r", Actor: protocol.Actor{ID: "worker"}, Payload: p})
	r := protocol.CapabilityResult{RequestID: "r", Status: "completed", ResultAvailable: true, Output: &protocol.TextOutput{MediaType: "text/plain", Text: "HI"}}
	if err := validateExecution(events, "worker", "capability.result", r); err != nil {
		t.Fatal(err)
	}
}

func TestInvocationLimitRejectsAtBoundaryAndKeepsOtherCalls(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for i := 0; i < store.MaxInvocationEvents; i++ {
		event := protocol.Event{RoomID: "room", Type: "capability.progress", CorrelationID: "full", Payload: protocol.CapabilityProgress{RequestID: "full"}}
		if err = s.AppendNext(ctx, &event); err != nil {
			t.Fatal(err)
		}
	}
	w := &WS{Store: s, Bus: eventbus.New()}
	if err = w.publishCapability(ctx, "room", "caller", "capability.invoke", "cap.echo", "full", protocol.CapabilityInvoke{RequestID: "full", CapabilityID: "cap.echo"}); err == nil {
		t.Fatal("allowed event 2049")
	}
	events, err := s.InvocationEvents(ctx, "room", "full")
	if err != nil || len(events) != store.MaxInvocationEvents {
		t.Fatalf("changed saturated call: %d %v", len(events), err)
	}
	if err = w.publishCapability(ctx, "room", "caller", "capability.invoke", "cap.echo", "new", protocol.CapabilityInvoke{RequestID: "new", CapabilityID: "cap.echo"}); err != nil {
		t.Fatal("unrelated call blocked:", err)
	}
}
