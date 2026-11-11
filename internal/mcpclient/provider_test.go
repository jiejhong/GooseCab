package mcpclient

import (
	"context"
	"encoding/json"
	"github.com/jiejhong/GooseCab/internal/protocol"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

var schema = json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`)

func manifest(url string) []byte {
	b, _ := json.Marshal(Manifest{Format: 1, Name: "test", Endpoint: url, Tools: []Tool{{Capability: "cap.mcp.echo", Name: "echo", InputSchema: schema}}})
	return b
}
func setup(t *testing.T, url string) (*Store, string) {
	t.Helper()
	s, e := Open(filepath.Join(t.TempDir(), "mcp.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	d, e := s.Import(manifest(url))
	if e != nil {
		t.Fatal(e)
	}
	return s, d
}
func actor() context.Context {
	return protocol.WithInvocationActor(context.Background(), "room", "caller")
}
func TestMCPRoundTripAndInertTransfer(t *testing.T) {
	var calls atomic.Int32
	var sequence []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			ID     int            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		json.NewDecoder(r.Body).Decode(&q)
		sequence = append(sequence, q.Method)
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Error("missing local auth")
		}
		w.Header().Set("Content-Type", "application/json")
		var result any
		switch q.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-03-26"}
		case "notifications/initialized":
			w.WriteHeader(202)
			return
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "echo", "inputSchema": schema}, map[string]any{"name": "unapproved", "inputSchema": schema}}}
		case "tools/call":
			calls.Add(1)
			if q.Params["name"] != "echo" {
				t.Error("wrong tool")
			}
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "hello secret-token"}}}
		}
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": q.ID, "result": result})
	}))
	defer server.Close()
	s, d := setup(t, server.URL)
	p, _ := s.Provider()
	ids, _ := p.CapabilityIDs()
	if len(ids) != 0 {
		t.Fatal("import activated manifest")
	}
	t.Setenv("TEST_MCP_TOKEN", "secret-token")
	if e := s.Approve(d, Binding{AllowedCallers: []string{"caller"}, AllowedRooms: []string{"room"}, TokenEnv: "TEST_MCP_TOKEN", AllowHTTPLoopback: true}); e != nil {
		t.Fatal(e)
	}
	p, e := s.Provider()
	if e != nil {
		t.Fatal(e)
	}
	v := protocol.CapabilityInvoke{RequestID: "r", CapabilityID: "cap.mcp.echo", Payload: map[string]any{"text": "hi"}}
	if r := p.Invoke(context.Background(), v); r.Status != "failed" {
		t.Fatal("missing caller accepted")
	}
	r := p.Invoke(actor(), v)
	if r.Status != "completed" || r.Output.Text != "hello [redacted]" || calls.Load() != 1 {
		t.Fatalf("bad result %#v", r)
	}
	if strings.Join(sequence, ",") != "initialize,notifications/initialized,tools/list,tools/call" {
		t.Fatal(sequence)
	}
	v.CapabilityID = "cap.mcp.unapproved"
	if p.Invoke(actor(), v).Status != "failed" || calls.Load() != 1 {
		t.Fatal("allowlist bypass")
	}
	raw, _ := s.Export(d)
	if strings.Contains(string(raw), "secret-token") || strings.Contains(string(raw), "TEST_MCP_TOKEN") {
		t.Fatal("credentials exported")
	}
	other, _ := setup(t, server.URL)
	other.Import(raw)
	op, _ := other.Provider()
	ids, _ = op.CapabilityIDs()
	if len(ids) != 0 {
		t.Fatal("transfer enabled tools")
	}
	anns, _ := p.Announcements()
	b, _ := json.Marshal(anns)
	if strings.Contains(string(b), server.URL) || strings.Contains(string(b), "secret-token") {
		t.Fatal("announcement leaked connection")
	}
}
func TestUnknownOutcomeNeverRetries(t *testing.T) {
	for _, mode := range []string{"disconnect", "sse", "oversize", "image", "http_error", "rpc_error", "schema_changed", "null_result", "empty_object", "null_content", "missing_text", "null_text", "structured_content", "null_block", "invalid_error_flag"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var q struct {
					ID     int    `json:"id"`
					Method string `json:"method"`
				}
				json.NewDecoder(r.Body).Decode(&q)
				w.Header().Set("Content-Type", "application/json")
				result := any(map[string]any{})
				switch q.Method {
				case "initialize":
					result = map[string]any{"protocolVersion": "2025-03-26"}
				case "notifications/initialized":
					w.WriteHeader(202)
					return
				case "tools/list":
					sc := schema
					if mode == "schema_changed" {
						sc = json.RawMessage(`{"type":"object"}`)
					}
					result = map[string]any{"tools": []any{map[string]any{"name": "echo", "inputSchema": sc}}}
				case "tools/call":
					calls.Add(1)
					switch mode {
					case "disconnect":
						conn, _, _ := w.(http.Hijacker).Hijack()
						conn.Close()
						return
					case "sse":
						w.Header().Set("Content-Type", "text/event-stream")
						w.Write([]byte("data: {}\n\n"))
						return
					case "oversize":
						w.Write([]byte(strings.Repeat("x", MaxManifestBytes+1)))
						return
					case "http_error":
						w.WriteHeader(500)
						w.Write([]byte("secret"))
						return
					case "rpc_error":
						json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": q.ID, "error": map[string]any{"code": -32602, "message": "secret"}})
						return
					case "null_result":
						result = nil
					case "empty_object":
						result = map[string]any{}
					case "null_content":
						result = map[string]any{"content": nil}
					case "missing_text":
						result = map[string]any{"content": []any{map[string]any{"type": "text"}}}
					case "null_text":
						result = map[string]any{"content": []any{map[string]any{"type": "text", "text": nil}}}
					case "structured_content":
						result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "fine"}}, "structuredContent": map[string]any{"secret": "hidden"}}
					case "null_block":
						result = map[string]any{"content": []any{nil}}
					case "invalid_error_flag":
						result = map[string]any{"content": []any{}, "isError": nil}
					case "image":
						result = map[string]any{"content": []any{map[string]any{"type": "image", "data": "secret"}}}
					}
				}
				json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": q.ID, "result": result})
			}))
			defer server.Close()
			s, d := setup(t, server.URL)
			if e := s.Approve(d, Binding{AllowedCallers: []string{"caller"}, AllowHTTPLoopback: true}); e != nil {
				t.Fatal(e)
			}
			p, _ := s.Provider()
			r := p.Invoke(actor(), protocol.CapabilityInvoke{RequestID: "x", CapabilityID: "cap.mcp.echo", Payload: map[string]any{}})
			want := "unknown"
			count := int32(1)
			if mode == "rpc_error" || mode == "schema_changed" {
				want = "failed"
			}
			if mode == "schema_changed" {
				count = 0
			}
			if r.Status != want || calls.Load() != count || strings.Contains(r.Error, "secret") || r.ResultAvailable || r.Output != nil {
				t.Fatalf("status=%s calls=%d error=%s", r.Status, calls.Load(), r.Error)
			}
		})
	}
}
func TestRejectUnsafeManifestsAndApproval(t *testing.T) {
	for _, url := range []string{"file:///tmp/a", "https://user:pass@example.com/mcp", "https://example.com/mcp?token=x", "https://example.com/#x"} {
		s, _ := setup(t, "https://example.com/mcp")
		if _, e := s.Import(manifest(url)); e == nil {
			t.Fatal("unsafe endpoint", url)
		}
	}
	s, d := setup(t, "http://example.com/mcp")
	if s.Approve(d, Binding{AllowedCallers: []string{"caller"}, AllowHTTPLoopback: true}) == nil {
		t.Fatal("remote HTTP allowed")
	}
	if s.Approve(d, Binding{}) == nil {
		t.Fatal("missing caller approval")
	}
	for _, field := range []string{`"command":"npm"`, `"headers":{"Authorization":"secret"}`, `"approved":true`, `"token_env":"SECRET"`, `"transport":"stdio"`} {
		b := manifest("https://example.com/mcp")
		b = append(b[:len(b)-1], []byte(","+field+"}")...)
		if _, e := s.Import(b); e == nil {
			t.Fatal("automatic config allowed", field)
		}
	}
}
func TestPrivateAndRedirectDenied(t *testing.T) {
	a := approved{manifest: Manifest{Endpoint: "https://127.0.0.1/mcp"}}
	c, e := clientFor(a)
	if e != nil {
		t.Fatal(e)
	}
	_, e = c.Get(a.manifest.Endpoint)
	if e == nil || !strings.Contains(e.Error(), "private endpoint") {
		t.Fatal("private address not blocked", e)
	}
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer source.Close()
	a.manifest.Endpoint = source.URL
	a.binding.AllowHTTPLoopback = true
	c, _ = clientFor(a)
	resp, e := c.Get(source.URL)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 307 || targetCalls.Load() != 0 {
		t.Fatal("redirect followed")
	}
}

func TestLiveApprovalRevocationAndBindingChanges(t *testing.T) {
	for _, change := range []string{"revoke", "callers", "rooms", "token", "private", "loopback", "closed_store"} {
		t.Run(change, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(500) }))
			defer server.Close()
			s, d := setup(t, server.URL)
			b := Binding{AllowedCallers: []string{"caller"}, AllowHTTPLoopback: true}
			if e := s.Approve(d, b); e != nil {
				t.Fatal(e)
			}
			p, e := s.Provider()
			if e != nil {
				t.Fatal(e)
			}
			before, _ := s.Export(d)
			switch change {
			case "revoke":
				if e = s.Revoke(d); e != nil {
					t.Fatal(e)
				}
			case "callers":
				b.AllowedCallers = []string{"other"}
			case "rooms":
				b.AllowedRooms = []string{"other"}
			case "token":
				b.TokenEnv = "OTHER_MCP_TOKEN"
			case "private":
				b.AllowPrivate = true
			case "loopback":
				_, e = s.db.Exec(`UPDATE mcp_approvals SET loopback_http=0 WHERE digest=?`, d)
				if e != nil {
					t.Fatal(e)
				}
			case "closed_store":
				s.Close()
			}
			if change != "revoke" && change != "closed_store" && change != "loopback" {
				if e = s.Approve(d, b); e != nil {
					t.Fatal(e)
				}
			}
			result := p.Invoke(actor(), protocol.CapabilityInvoke{RequestID: "r", CapabilityID: "cap.mcp.echo", Payload: map[string]any{"approved": true, "allowed_callers": []string{"caller"}}})
			if result.Status != "failed" || requests.Load() != 0 || result.ResultAvailable {
				t.Fatalf("revocation bypass: %#v requests=%d", result, requests.Load())
			}
			if change != "closed_store" {
				after, e := s.Export(d)
				if e != nil || string(before) != string(after) {
					t.Fatal("approval changed exported manifest")
				}
			}
		})
	}
}
