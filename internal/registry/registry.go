package registry

import (
	"encoding/json"
	"github.com/jiejhong/GooseCab/internal/protocol"
	"sort"
	"strings"
	"sync"
)

type Capability struct {
	Kind         string                        `json:"kind"`
	Callable     bool                          `json:"callable"`
	Skill        *protocol.SkillDescriptor     `json:"skill,omitempty"`
	MCP          *protocol.MCPDescriptor       `json:"mcp,omitempty"`
	Knowledge    *protocol.KnowledgeDescriptor `json:"knowledge,omitempty"`
	ID           string                        `json:"id"`
	Name         string                        `json:"name"`
	Description  string                        `json:"description"`
	Version      string                        `json:"version"`
	InputSchema  any                           `json:"input_schema,omitempty"`
	OutputSchema any                           `json:"output_schema,omitempty"`
	Risk         string                        `json:"risk,omitempty"`
	Manifest     any                           `json:"manifest,omitempty"`
}
type Agent struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Role         string       `json:"role,omitempty"`
	Persona      string       `json:"persona,omitempty"`
	Capabilities []Capability `json:"capabilities"`
}
type Registry struct {
	mu     sync.RWMutex
	agents map[string]Agent
}

func New() *Registry { return &Registry{agents: map[string]Agent{}} }
func clone(a Agent) Agent {
	raw, _ := json.Marshal(a)
	var out Agent
	_ = json.Unmarshal(raw, &out)
	return out
}
func (r *Registry) Upsert(a Agent) { r.mu.Lock(); defer r.mu.Unlock(); r.agents[a.ID] = clone(a) }
func (r *Registry) Get(id string) (Agent, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.agents[id]
	return clone(a), ok
}
func (r *Registry) List() []Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Agent, 0, len(r.agents))
	for _, a := range r.agents {
		out = append(out, clone(a))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (r *Registry) FindCapability(q string) []Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Agent, 0)
	for _, a := range r.agents {
		for _, c := range a.Capabilities {
			if q == "" || c.ID == q || c.Name == q {
				out = append(out, clone(a))
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// RegisterHello replaces the advertised capability set on reconnect while
// preserving richer metadata for capability IDs the agent still advertises.
// Registry is a gateway-wide metadata catalog, not an online-presence service.
func (r *Registry) RegisterHello(id, name, role string, ids []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.agents[id]
	a.ID = id
	if name != "" {
		a.Name = name
	}
	if role != "" {
		a.Role = role
	}
	previous := map[string]Capability{}
	for _, c := range a.Capabilities {
		previous[c.ID] = c
	}
	a.Capabilities = make([]Capability, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		c := previous[id]
		c.ID = id
		if c.Kind == "" {
			c.Kind = protocol.CapabilityKind(id)
			c.Callable = c.Kind == protocol.CapabilityKindText && protocol.IsExecutableTextCapability(id)
		}
		a.Capabilities = append(a.Capabilities, c)
	}
	r.agents[a.ID] = a
}

// Announce atomically merges one capability without dropping concurrent ones.
func (r *Registry) Announce(id string, c Capability) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.agents[id]
	a.ID = id
	a = clone(a)
	for i, old := range a.Capabilities {
		if old.ID == c.ID {
			a.Capabilities[i] = c
			r.agents[id] = a
			return
		}
	}
	a.Capabilities = append(a.Capabilities, c)
	r.agents[id] = a
}

// SearchCapabilities filters individual capabilities rather than returning unrelated entries.
func (r *Registry) SearchCapabilities(q, kind string) []Agent {
	out := []Agent{}
	q = strings.ToLower(strings.TrimSpace(q))
	for _, a := range r.List() {
		caps := []Capability{}
		for _, c := range a.Capabilities {
			if (kind == "" || c.Kind == kind) && (q == "" || strings.Contains(strings.ToLower(c.ID+" "+c.Name+" "+c.Description), q)) {
				caps = append(caps, c)
			}
		}
		if len(caps) > 0 {
			a.Capabilities = caps
			out = append(out, a)
		}
	}
	return out
}
func CapabilityFromAnnouncement(a protocol.CapabilityAnnounce) Capability {
	return Capability{ID: a.CapabilityID, Name: a.Name, Version: a.Version, Description: a.Description, Kind: a.Kind, Callable: a.Callable, InputSchema: a.InputSchema, Skill: a.Skill, MCP: a.MCP, Knowledge: a.Knowledge}
}
