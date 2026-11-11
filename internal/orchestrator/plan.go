// Package orchestrator executes explicitly assigned, bounded text DAGs. It is
// not an AI planner and never retries work with an uncertain outcome.
package orchestrator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/jiejhong/GooseCab/internal/agent"
	"github.com/jiejhong/GooseCab/internal/protocol"
)

type Task struct {
	Capability string   `json:"capability,omitempty"`
	ID         string   `json:"id"`
	Target     string   `json:"target"`
	Text       string   `json:"text"`
	DependsOn  []string `json:"depends_on,omitempty"`
}

// Each task input is Text followed by dependency outputs in DependsOn order,
// joined with Separator. Targets and bounded text capabilities are explicit.
type Plan struct {
	ID          string `json:"id"`
	URL         string `json:"url"`
	Room        string `json:"room"`
	Caller      string `json:"caller"`
	Concurrency int    `json:"concurrency"`
	Separator   string `json:"separator"`
	Tasks       []Task `json:"tasks"`
}

func (p Plan) Validate() error {
	for _, s := range []string{p.ID, p.Room, p.Caller} {
		if strings.TrimSpace(s) == "" || len(s) > 128 || !utf8.ValidString(s) {
			return fmt.Errorf("plan, room and caller IDs require 1–128 UTF-8 bytes")
		}
	}
	u, e := url.Parse(p.URL)
	if e != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("url must be a ws/wss endpoint without credentials, query or fragment")
	}
	if p.Concurrency < 1 || p.Concurrency > 8 || len(p.Tasks) < 1 || len(p.Tasks) > 64 {
		return fmt.Errorf("require 1–64 tasks and concurrency 1–8")
	}
	if !utf8.ValidString(p.Separator) || len(p.Separator) > 64 {
		return fmt.Errorf("separator exceeds 64 UTF-8 bytes")
	}
	tasks := map[string]Task{}
	for _, t := range p.Tasks {
		if strings.TrimSpace(t.ID) == "" || len(t.ID) > 128 || !utf8.ValidString(t.ID) || strings.TrimSpace(t.Target) == "" || len(t.Target) > 128 || !utf8.ValidString(t.Target) {
			return fmt.Errorf("task and target IDs require 1–128 UTF-8 bytes")
		}
		if _, ok := tasks[t.ID]; ok {
			return fmt.Errorf("duplicate task %q", t.ID)
		}
		if !utf8.ValidString(t.Text) || len(t.Text) > agent.MaxTextBytes {
			return fmt.Errorf("task %q text exceeds UTF-8 input bounds", t.ID)
		}
		if !protocol.IsExecutableTextCapability(t.CapabilityID()) {
			return fmt.Errorf("unsupported text capability %q", t.Capability)
		}
		tasks[t.ID] = t
	}
	seen := map[string]int{}
	var visit func(string) error
	visit = func(id string) error {
		if seen[id] == 1 {
			return fmt.Errorf("cycle at %q", id)
		}
		if seen[id] == 2 {
			return nil
		}
		seen[id] = 1
		deps := map[string]bool{}
		for _, d := range tasks[id].DependsOn {
			if _, ok := tasks[d]; !ok {
				return fmt.Errorf("missing dependency %q", d)
			}
			if deps[d] {
				return fmt.Errorf("duplicate dependency %q", d)
			}
			deps[d] = true
			if e := visit(d); e != nil {
				return e
			}
		}
		seen[id] = 2
		return nil
	}
	for id := range tasks {
		if e := visit(id); e != nil {
			return e
		}
	}
	return nil
}
func (p Plan) RequestID(task string) string {
	b, _ := json.Marshal([]string{p.Room, p.Caller, p.ID, task})
	h := sha256.Sum256(b)
	return "plan-" + hex.EncodeToString(h[:])
}

// CapabilityID preserves uppercase as the compatibility default.
func (t Task) CapabilityID() string {
	if t.Capability == "" {
		return agent.TextCapability
	}
	return t.Capability
}
