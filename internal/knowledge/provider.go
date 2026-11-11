package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jiejhong/GooseCab/internal/protocol"
)

func (s *Store) CapabilityIDs() ([]string, error) {
	rows, e := s.db.Query(`SELECT name FROM knowledge_collections ORDER BY name`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var name string
		if e = rows.Scan(&name); e != nil {
			return nil, e
		}
		ids = append(ids, "cap.knowledge."+name)
	}
	return ids, rows.Err()
}
func (s *Store) Announcements() ([]protocol.CapabilityAnnounce, error) {
	ids, e := s.CapabilityIDs()
	if e != nil {
		return nil, e
	}
	out := []protocol.CapabilityAnnounce{}
	for _, id := range ids {
		out = append(out, protocol.CapabilityAnnounce{AgentID: s.agentID, CapabilityID: id, Name: "Shared knowledge metadata search", Version: "1", Kind: "knowledge", Callable: true, Description: "Search explicitly shared record metadata. Full records require separately approved file relay and import.", Knowledge: &protocol.KnowledgeDescriptor{Collection: strings.TrimPrefix(id, "cap.knowledge."), SharePolicy: "named_callers"}, InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`)})
	}
	return out, nil
}

// Hit deliberately has no body, tags, snippets or local filesystem path.
type Hit struct {
	Ref      Ref    `json:"ref"`
	Title    string `json:"title"`
	SourceID string `json:"source_id"`
}

func (s *Store) Invoke(ctx context.Context, in protocol.CapabilityInvoke) protocol.CapabilityResult {
	result := protocol.CapabilityResult{RequestID: in.RequestID, Status: "failed"}
	fail := func() protocol.CapabilityResult {
		result.Error = "knowledge request not authorized or invalid"
		return result
	}
	_, caller, ok := protocol.InvocationActor(ctx)
	if !ok || caller == "" || !strings.HasPrefix(in.CapabilityID, "cap.knowledge.") {
		return fail()
	}
	name := strings.TrimPrefix(in.CapabilityID, "cap.knowledge.")
	c, e := s.collection(name)
	if e != nil || !allowed(c, caller) {
		return fail()
	}
	b, e := json.Marshal(in.Payload)
	if e != nil || len(b) > 1024 {
		return fail()
	}
	var payload struct {
		Query *string `json:"query"`
	}
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if e = d.Decode(&payload); e != nil || payload.Query == nil || !validText(*payload.Query, 256) {
		return fail()
	}
	hits := []Hit{}
	truncated := false
	query := strings.ToLower(*payload.Query)
	for _, ref := range c.Records {
		if ctx.Err() != nil {
			return fail()
		}
		r, e := s.get(ref)
		if e != nil {
			return fail()
		}
		if strings.Contains(strings.ToLower(r.Title), query) {
			candidate := append(hits, Hit{ref, r.Title, "sha256:" + Digest([]byte(r.Source))})
			encoded, _ := json.Marshal(struct {
				Hits      []Hit `json:"hits"`
				Truncated bool  `json:"truncated"`
			}{candidate, false})
			if len(encoded) > 4096 || len(candidate) > 8 {
				truncated = true
				break
			}
			hits = candidate
		}
	}
	b, e = json.Marshal(struct {
		Hits      []Hit `json:"hits"`
		Truncated bool  `json:"truncated"`
	}{hits, truncated})
	if e != nil {
		return fail()
	}
	result.Status = "completed"
	result.ResultAvailable = true
	result.Output = &protocol.TextOutput{Text: string(b), MediaType: "text/plain"}
	return result
}
func (r Ref) String() string { return fmt.Sprintf("%s/%s@%d:%s", r.Origin, r.ID, r.Version, r.Digest) }
