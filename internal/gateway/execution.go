package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jiejhong/GooseCab/internal/protocol"
	"github.com/jiejhong/GooseCab/internal/store"
)

const executableTextCapability = "cap.text.uppercase"

// publishCapability validates the opt-in executable protocol under the same
// lock as append/broadcast. Existing metadata-only capabilities remain readable.
// The persisted event log is the authority across Gateway restarts.
func (w *WS) publishCapability(ctx context.Context, room, actor, typ, capID, id string, payload any) error {
	w.eventMu.Lock()
	defer w.eventMu.Unlock()
	events, err := w.Store.InvocationEvents(ctx, room, id)
	if err != nil {
		return fmt.Errorf("could not read invocation history")
	}
	if len(events) >= store.MaxInvocationEvents {
		return fmt.Errorf("invocation history reached safety limit")
	}
	if err = validateExecution(events, actor, typ, payload); err != nil {
		return err
	}
	evt := protocol.Event{RoomID: room, Actor: protocol.Actor{Kind: "agent", ID: actor}, Type: typ, CapabilityID: capID, CorrelationID: id, Payload: payload}
	if err = w.Store.AppendNext(ctx, &evt); err != nil {
		return fmt.Errorf("could not persist execution event")
	}
	w.Bus.Publish(evt)
	return nil
}

func validateExecution(events []protocol.Event, actor, typ string, payload any) error {
	var id string
	strict := false
	switch v := payload.(type) {
	case protocol.CapabilityInvoke:
		id = v.RequestID
		strict = v.CapabilityID == executableTextCapability
	case protocol.CapabilityProgress:
		id = v.RequestID
	case protocol.CapabilityResult:
		id = v.RequestID
	}
	var original *protocol.CapabilityInvoke
	var caller string
	var terminal *protocol.CapabilityResult
	var progress string
	for _, event := range events {
		if event.CorrelationID != id {
			continue
		}
		raw, err := json.Marshal(event.Payload)
		if err != nil {
			return err
		}
		switch event.Type {
		case "capability.invoke":
			var v protocol.CapabilityInvoke
			if err = json.Unmarshal(raw, &v); err != nil {
				return err
			}
			if original == nil {
				original = &v
				caller = event.Actor.ID
				strict = strict || v.CapabilityID == executableTextCapability
			}
		case "capability.progress":
			if original != nil && event.Actor.ID == original.TargetAgentID {
				var v protocol.CapabilityProgress
				if err = json.Unmarshal(raw, &v); err != nil {
					return err
				}
				progress = v.Status
			}
		case "capability.result":
			if original != nil && event.Actor.ID == original.TargetAgentID {
				var v protocol.CapabilityResult
				if err = json.Unmarshal(raw, &v); err != nil {
					return err
				}
				terminal = &v
			}
		}
	}
	if !strict {
		return nil
	}
	if strings.TrimSpace(id) == "" || len(id) > 128 {
		return fmt.Errorf("request_id must contain 1 to 128 bytes")
	}
	switch v := payload.(type) {
	case protocol.CapabilityInvoke:
		if strings.TrimSpace(v.TargetAgentID) == "" || len(v.TargetAgentID) > 256 {
			return fmt.Errorf("executable calls require an explicit target_agent_id")
		}
		if original != nil {
			a, _ := json.Marshal(original)
			b, _ := json.Marshal(v)
			if caller != actor || string(a) != string(b) {
				return fmt.Errorf("request ID conflict; keep the original caller, target, capability and payload")
			}
		}
		raw, _ := json.Marshal(v.Payload)
		if len(raw) > 32768 {
			return fmt.Errorf("text request payload too large")
		}
	case protocol.CapabilityProgress:
		if original == nil || original.TargetAgentID != actor {
			return fmt.Errorf("only the invocation target can report execution progress")
		}
		if terminal != nil && !(v.Status == "completed" && (terminal.Status == "completed" || terminal.Status == "unknown")) {
			return fmt.Errorf("execution already has a terminal result")
		}
		if v.Status != "running" && v.Status != "completed" {
			return fmt.Errorf("execution progress status must be running or completed")
		}
		if progress == "completed" && v.Status != "completed" {
			return fmt.Errorf("execution status cannot regress")
		}
		if v.Percent < 0 || v.Percent > 100 {
			return fmt.Errorf("invalid execution percentage")
		}
	case protocol.CapabilityResult:
		if original == nil || original.TargetAgentID != actor {
			return fmt.Errorf("only the invocation target can report an execution result")
		}
		switch v.Status {
		case "completed":
			if progress != "completed" || !v.ResultAvailable || v.Output == nil || v.Output.MediaType != "text/plain" || !utf8.ValidString(v.Output.Text) || len(v.Output.Text) > 4096 || v.Error != "" {
				return fmt.Errorf("completed text execution requires completed progress and an available bounded text/plain output")
			}
		case "failed", "unknown":
			if v.ResultAvailable || v.Output != nil || strings.TrimSpace(v.Error) == "" {
				return fmt.Errorf("failed or unknown execution must explain the state without claiming a result")
			}
		default:
			return fmt.Errorf("invalid execution result status")
		}
		if v.OutputRef != "" {
			return fmt.Errorf("text executions cannot return local file references")
		}
		if terminal != nil {
			a, _ := json.Marshal(terminal)
			b, _ := json.Marshal(v)
			if string(a) != string(b) && terminal.Status != "unknown" {
				return fmt.Errorf("terminal execution result cannot change")
			}
		}
	}
	return nil
}
