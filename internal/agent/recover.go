package agent

import (
	"encoding/json"
	"fmt"

	"github.com/jiejhong/GooseCab/internal/protocol"
)

// CachedTextResult reads only the pre-welcome replay on a freshly dialed
// connection. It never submits an invocation, even when no result exists.
// A result arriving after the replay is deliberately left for a later check.
func (c *Client) CachedTextResult(requestID, target, text string) (protocol.CapabilityResult, bool, error) {
	return c.RecoverTextCapability(requestID, TextCapability, target, text)
}

// RecoverTextCapability reads verified replay only, never submitting missing work.
func (c *Client) RecoverTextCapability(requestID, capabilityID, target, text string) (protocol.CapabilityResult, bool, error) {
	if !protocol.IsExecutableTextCapability(capabilityID) {
		return protocol.CapabilityResult{}, false, fmt.Errorf("unsupported text capability")
	}
	wanted := protocol.CapabilityInvoke{RequestID: requestID, CapabilityID: capabilityID, TargetAgentID: target, Payload: map[string]any{"text": text}}
	expected, _ := json.Marshal(wanted)
	known := false
	var result protocol.CapabilityResult
	for {
		m, ok := c.dequeue()
		if !ok {
			break
		}
		if isProtocolError(m) {
			return result, false, fmt.Errorf("replay rejected: %v", m["message"])
		}
		if m["room_id"] != c.RoomID || m["correlation_id"] != requestID {
			continue
		}
		b, e := json.Marshal(m)
		if e != nil {
			return result, false, e
		}
		var event protocol.Event
		if e = json.Unmarshal(b, &event); e != nil {
			return result, false, e
		}
		b, e = json.Marshal(event.Payload)
		if e != nil {
			return result, false, e
		}
		if event.Type == "capability.invoke" {
			var got protocol.CapabilityInvoke
			if e = json.Unmarshal(b, &got); e != nil {
				return result, false, e
			}
			v, _ := json.Marshal(got)
			if event.Actor.Kind != "agent" || event.Actor.ID != c.AgentID || string(v) != string(expected) {
				return result, false, fmt.Errorf("recovery invocation identity conflict")
			}
			known = true
		}
		if known && event.Type == "capability.result" && event.Actor.Kind == "agent" && event.Actor.ID == target {
			result = protocol.CapabilityResult{}
			if e = json.Unmarshal(b, &result); e != nil {
				return result, false, e
			}
		}
	}
	return result, known && result.RequestID == requestID && (result.Status == "completed" || result.Status == "failed"), nil
}
