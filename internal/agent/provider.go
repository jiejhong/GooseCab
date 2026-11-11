package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jiejhong/GooseCab/internal/protocol"
)

// CapabilityProvider exposes only locally approved adapters. UseProvider is a
// startup operation; providers must independently enforce caller authorization.
type CapabilityProvider interface {
	CapabilityIDs() ([]string, error)
	Invoke(context.Context, protocol.CapabilityInvoke) protocol.CapabilityResult
	Announcements() ([]protocol.CapabilityAnnounce, error)
}

func (e *Executor) UseProvider(p CapabilityProvider) error {
	if p == nil {
		return fmt.Errorf("provider is required")
	}
	ids, err := p.CapabilityIDs()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !protocol.IsExecutableCapability(id) || id == TextCapability || seen[id] || e.supports(id) {
			return fmt.Errorf("invalid or duplicate provider capability %q", id)
		}
		seen[id] = true
	}
	previous, err := e.Announcements()
	if err != nil {
		return err
	}
	declared := map[string]bool{}
	for _, a := range previous {
		declared[a.CapabilityID] = true
	}
	announcements, err := p.Announcements()
	if err != nil {
		return err
	}
	callable := map[string]bool{}
	for _, a := range announcements {
		if err := protocol.NormalizeAnnouncement(&a); err != nil {
			return err
		}
		if declared[a.CapabilityID] {
			return fmt.Errorf("duplicate provider announcement %q", a.CapabilityID)
		}
		declared[a.CapabilityID] = true
		if a.Callable != seen[a.CapabilityID] {
			return fmt.Errorf("provider announcement does not match its executable capabilities")
		}
		callable[a.CapabilityID] = a.Callable
	}
	for _, id := range ids {
		if !callable[id] {
			return fmt.Errorf("provider is missing a callable descriptor for %q", id)
		}
	}
	if e.providers == nil {
		e.providers = map[string]CapabilityProvider{}
	}
	if e.providerOwners == nil {
		e.providerOwners = map[string]int{}
	}
	for _, id := range ids {
		e.providers[id] = p
		e.providerOwners[id] = len(e.providerList)
	}
	e.providerList = append(e.providerList, p)
	return nil
}

// Announcements includes inert, reviewed packages too, but their Callable flag
// remains false and their IDs are never registered as execution handlers.
func (e *Executor) Announcements() ([]protocol.CapabilityAnnounce, error) {
	var out []protocol.CapabilityAnnounce
	seen := map[string]bool{}
	for owner, p := range e.providerList {
		announcements, err := p.Announcements()
		if err != nil {
			return nil, err
		}
		for _, a := range announcements {
			if seen[a.CapabilityID] {
				return nil, fmt.Errorf("duplicate provider announcement %q", a.CapabilityID)
			}
			if err := protocol.NormalizeAnnouncement(&a); err != nil {
				return nil, err
			}
			if a.Callable && (e.providers[a.CapabilityID] == nil || e.providerOwners[a.CapabilityID] != owner) {
				return nil, fmt.Errorf("provider advertised an unregistered executor")
			}
			seen[a.CapabilityID] = true
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CapabilityID < out[j].CapabilityID })
	return out, nil
}

func validateProviderResult(id string, r protocol.CapabilityResult) protocol.CapabilityResult {
	valid := r.RequestID == id && r.OutputRef == ""
	switch r.Status {
	case "completed":
		valid = valid && r.ResultAvailable && r.Output != nil && r.Output.MediaType == "text/plain" && utf8.ValidString(r.Output.Text) && len(r.Output.Text) <= MaxTextBytes && r.Error == ""
	case "failed", "unknown":
		valid = valid && !r.ResultAvailable && r.Output == nil && strings.TrimSpace(r.Error) != "" && utf8.ValidString(r.Error) && len(r.Error) <= MaxTextBytes
	default:
		valid = false
	}
	if !valid {
		return protocol.CapabilityResult{RequestID: id, Status: "unknown", Error: "Provider returned an invalid result; inspect locally without retrying"}
	}
	return r
}
