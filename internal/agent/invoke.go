package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jiejhong/GooseCab/internal/protocol"
)

// InvokeText submits once and waits for an authoritative result on this
// connection. A timeout/disconnect is uncertainty, never permission to resubmit
// with a new ID. Repeating the exact same call/ID can retrieve a cached result.
func (c *Client) InvokeText(ctx context.Context, requestID, target, text string, onProgress func(protocol.CapabilityProgress)) (protocol.CapabilityResult, error) {
	return c.InvokeTextCapability(ctx, requestID, TextCapability, target, text, onProgress)
}

// InvokeTextCapability uses the same guarded lifecycle for a locally installed
// declarative text skill. The caller cannot use it to execute arbitrary tools.
func (c *Client) InvokeTextCapability(ctx context.Context, requestID, capabilityID, target, text string, onProgress func(protocol.CapabilityProgress)) (protocol.CapabilityResult, error) {
	if !protocol.IsExecutableTextCapability(capabilityID) || !utf8.ValidString(text) || len(text) > MaxTextBytes {
		return protocol.CapabilityResult{}, fmt.Errorf("bounded text capability and input required")
	}
	return c.InvokeCapability(ctx, requestID, capabilityID, target, map[string]any{"text": text}, onProgress)
}

// InvokeCapability submits a bounded JSON-object call without granting provider permissions.
func (c *Client) InvokeCapability(ctx context.Context, requestID, capabilityID, target string, payload any, onProgress func(protocol.CapabilityProgress)) (protocol.CapabilityResult, error) {
	var result protocol.CapabilityResult
	if strings.TrimSpace(target) == "" || strings.TrimSpace(requestID) == "" || len(requestID) > 128 || !protocol.IsExecutableCapability(capabilityID) {
		return result, fmt.Errorf("executable capability, target and request ID required")
	}
	if err := protocol.ValidateCapabilityPayload(capabilityID, payload); err != nil {
		return result, err
	}
	wanted := protocol.CapabilityInvoke{RequestID: requestID, CapabilityID: capabilityID, TargetAgentID: target, Payload: payload}
	// Dial buffered the complete pre-welcome replay. Fold it before acting:
	// an old unknown may have been reconciled by a later durable result. A
	// completed cached call can be retrieved even with the worker offline.
	var cached protocol.CapabilityResult
	known := false
	for {
		msg, ok := c.dequeue()
		if !ok {
			break
		}
		if msg["room_id"] != c.RoomID || msg["correlation_id"] != requestID {
			continue
		}
		raw, err := json.Marshal(msg)
		if err != nil {
			return result, err
		}
		var event protocol.Event
		if err = json.Unmarshal(raw, &event); err != nil {
			return result, err
		}
		payload, err := json.Marshal(event.Payload)
		if err != nil {
			return result, err
		}
		if event.Type == "capability.invoke" {
			var got protocol.CapabilityInvoke
			if err = json.Unmarshal(payload, &got); err != nil {
				return result, err
			}
			a, _ := json.Marshal(got)
			b, _ := json.Marshal(wanted)
			if event.Actor.ID != c.AgentID || string(a) != string(b) {
				return result, fmt.Errorf("request ID conflict; inspect the original call instead of resubmitting")
			}
			known = true
		}
		if known && event.Type == "capability.result" && event.Actor.Kind == "agent" && event.Actor.ID == target {
			cached = protocol.CapabilityResult{}
			if err = json.Unmarshal(payload, &cached); err != nil {
				return result, err
			}
		}
	}
	if known && (cached.Status == "completed" || cached.Status == "failed") {
		return cached, nil
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := c.Invoke(requestID, capabilityID, target, wanted.Payload); err != nil {
		return result, fmt.Errorf("submission outcome uncertain; do not create a new request ID: %w", err)
	}
	accepted := false
	for {
		msg, err := c.ReadContext(ctx)
		if err != nil {
			return result, fmt.Errorf("execution outcome uncertain; retain request ID %q: %w", requestID, err)
		}
		if isProtocolError(msg) {
			return result, fmt.Errorf("gateway rejected request (%v): %v", msg["code"], msg["message"])
		}
		if msg["room_id"] != c.RoomID || msg["correlation_id"] != requestID {
			continue
		}
		raw, err := json.Marshal(msg)
		if err != nil {
			return result, err
		}
		var event protocol.Event
		if err = json.Unmarshal(raw, &event); err != nil {
			return result, err
		}
		payload, err := json.Marshal(event.Payload)
		if err != nil {
			return result, err
		}
		if event.Type == "capability.invoke" {
			var got protocol.CapabilityInvoke
			if err = json.Unmarshal(payload, &got); err != nil {
				return result, err
			}
			a, _ := json.Marshal(got)
			b, _ := json.Marshal(wanted)
			if event.Actor.ID != c.AgentID || string(a) != string(b) {
				return result, fmt.Errorf("request ID conflict; inspect the original call instead of resubmitting")
			}
			accepted = true
		}
		if !accepted || event.Actor.Kind != "agent" || event.Actor.ID != target {
			continue
		}
		if event.Type == "capability.progress" && onProgress != nil {
			var p protocol.CapabilityProgress
			if err = json.Unmarshal(payload, &p); err != nil {
				return result, err
			}
			onProgress(p)
		}
		if event.Type == "capability.result" {
			if err = json.Unmarshal(payload, &result); err != nil {
				return result, err
			}
			return result, nil
		}
	}
}
