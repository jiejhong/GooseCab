package gateway

import (
	"github.com/jiejhong/GooseCab/internal/protocol"
	"testing"
)

func TestLearnedCapabilitiesKeepExecutionGuards(t *testing.T) {
	call := protocol.CapabilityInvoke{RequestID: "learned", CapabilityID: "cap.learned.text.slug", TargetAgentID: "learner", Payload: map[string]any{"text": "Hi"}}
	noTarget := call
	noTarget.TargetAgentID = ""
	if validateExecution(nil, "caller", "capability.invoke", noTarget) == nil {
		t.Fatal("learned call allowed no target")
	}
	inv := protocol.Event{Type: "capability.invoke", CorrelationID: "learned", Actor: protocol.Actor{Kind: "agent", ID: "caller"}, Payload: call}
	result := protocol.CapabilityResult{RequestID: "learned", Status: "completed", ResultAvailable: true, Output: &protocol.TextOutput{Text: "hi", MediaType: "text/plain"}}
	if validateExecution([]protocol.Event{inv}, "intruder", "capability.result", result) == nil {
		t.Fatal("accepted forged learned result")
	}
	p := protocol.Event{Type: "capability.progress", CorrelationID: "learned", Actor: protocol.Actor{Kind: "agent", ID: "learner"}, Payload: protocol.CapabilityProgress{RequestID: "learned", Status: "completed", Percent: 100}}
	if err := validateExecution([]protocol.Event{inv, p}, "learner", "capability.result", result); err != nil {
		t.Fatal(err)
	}
	changed := call
	changed.CapabilityID = "cap.echo"
	if validateExecution([]protocol.Event{inv}, "caller", "capability.invoke", changed) == nil {
		t.Fatal("accepted executable-to-metadata downgrade")
	}
}
