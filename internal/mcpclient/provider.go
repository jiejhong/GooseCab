package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/jiejhong/GooseCab/internal/protocol"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type approved struct {
	manifest Manifest
	digest   string
	binding  Binding
	tool     Tool
}
type Provider struct {
	tools map[string]approved
	store *Store
}

func (s *Store) Provider() (*Provider, error) {
	rows, e := s.db.Query(`SELECT m.digest,m.body,a.token_env,a.private,a.loopback_http,a.callers,a.rooms FROM mcp_manifests m JOIN mcp_approvals a ON m.digest=a.digest`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	p := &Provider{tools: map[string]approved{}, store: s}
	for rows.Next() {
		var a approved
		var raw []byte
		var callers, rooms string
		if e = rows.Scan(&a.digest, &raw, &a.binding.TokenEnv, &a.binding.AllowPrivate, &a.binding.AllowHTTPLoopback, &callers, &rooms); e != nil {
			return nil, e
		}
		if json.Unmarshal([]byte(callers), &a.binding.AllowedCallers) != nil || len(a.binding.AllowedCallers) == 0 || json.Unmarshal([]byte(rooms), &a.binding.AllowedRooms) != nil {
			return nil, errors.New("invalid local caller approval")
		}
		if Digest(raw) != a.digest {
			return nil, errors.New("approved manifest changed")
		}
		a.manifest, e = parse(raw)
		if e != nil {
			return nil, e
		}
		for _, t := range a.manifest.Tools {
			if _, ok := p.tools[t.Capability]; ok {
				return nil, errors.New("duplicate approved MCP capability")
			}
			a.tool = t
			p.tools[t.Capability] = a
		}
	}
	return p, rows.Err()
}
func (p *Provider) CapabilityIDs() ([]string, error) {
	ids := []string{}
	for id := range p.tools {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}
func (p *Provider) Announcements() ([]protocol.CapabilityAnnounce, error) {
	out := []protocol.CapabilityAnnounce{}
	for id, a := range p.tools {
		out = append(out, protocol.CapabilityAnnounce{CapabilityID: id, Name: a.tool.Name, Kind: "mcp", Callable: true, MCP: &protocol.MCPDescriptor{ServerName: a.manifest.Name, ToolName: a.tool.Name, ConnectionManifestID: a.digest}})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CapabilityID < out[j].CapabilityID })
	return out, nil
}

// In addition to RFC1918/ULA, deny shared, benchmark, documentation and
// translation ranges which must not be treated as ordinary public endpoints.
var specialNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
}

func private(ip net.IP) bool {
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return true
	}
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	a = a.Unmap()
	for _, prefix := range specialNetworks {
		if prefix.Contains(a) {
			return true
		}
	}
	return false
}

func clientFor(a approved) (*http.Client, error) {
	u, e := endpoint(a.manifest.Endpoint)
	if e != nil {
		return nil, e
	}
	if u.Scheme == "http" {
		ip := net.ParseIP(u.Hostname())
		if !a.binding.AllowHTTPLoopback || ip == nil || !ip.IsLoopback() {
			return nil, errors.New("HTTP not approved")
		}
	}
	tr := &http.Transport{Proxy: nil, DisableKeepAlives: true, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(addr)
		if e != nil {
			return nil, errors.New("invalid address")
		}
		ips, e := net.DefaultResolver.LookupIPAddr(ctx, host)
		if e != nil || len(ips) == 0 {
			return nil, errors.New("endpoint resolution failed")
		}
		for _, v := range ips {
			loop := u.Scheme == "http" && a.binding.AllowHTTPLoopback && v.IP.IsLoopback()
			if private(v.IP) && !a.binding.AllowPrivate && !loop {
				return nil, errors.New("private endpoint requires local approval")
			}
		}
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}}
	return &http.Client{Transport: tr, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
}
type session struct {
	client              *http.Client
	endpoint, token, id string
	seq                 int
}

func (s *session) request(ctx context.Context, method string, params any, notify bool) (json.RawMessage, bool, error) {
	s.seq++
	body := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	if !notify {
		body["id"] = s.seq
	}
	b, _ := json.Marshal(body)
	req, e := http.NewRequestWithContext(ctx, "POST", s.endpoint, bytes.NewReader(b))
	if e != nil {
		return nil, false, errors.New("MCP request rejected")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2025-03-26")
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	if s.id != "" {
		req.Header.Set("Mcp-Session-Id", s.id)
	}
	resp, e := s.client.Do(req)
	if e != nil {
		return nil, true, errors.New("MCP transport outcome unknown")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, true, errors.New("MCP HTTP response rejected")
	}
	if id := resp.Header.Get("Mcp-Session-Id"); id != "" {
		if len(id) > 256 || strings.ContainsAny(id, "\r\n") {
			return nil, true, errors.New("invalid MCP session")
		}
		s.id = id
	}
	if notify {
		return nil, false, nil
	}
	media, _, e := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if e != nil || media != "application/json" {
		return nil, true, errors.New("only JSON MCP responses supported")
	}
	raw, e := io.ReadAll(io.LimitReader(resp.Body, MaxManifestBytes+1))
	if e != nil || len(raw) > MaxManifestBytes {
		return nil, true, errors.New("invalid bounded MCP response")
	}
	var r rpcResponse
	if json.Unmarshal(raw, &r) != nil || r.JSONRPC != "2.0" || r.ID != s.seq {
		return nil, true, errors.New("invalid MCP response envelope")
	}
	if len(r.Error) > 0 && string(r.Error) != "null" {
		return nil, false, errors.New("MCP tool rejected request")
	}
	if len(r.Result) == 0 {
		return nil, true, errors.New("missing MCP result")
	}
	return r.Result, false, nil
}
func sameSchema(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	aa, _ := json.Marshal(x)
	bb, _ := json.Marshal(y)
	return bytes.Equal(aa, bb)
}

// Approval snapshots are not authority: withdrawal or binding changes fail
// closed in already-running providers. A reload is required for changed grants.
func (p *Provider) stillApproved(a approved) bool {
	if p.store == nil {
		return false
	}
	current, e := p.store.binding(a.digest)
	return e == nil && reflect.DeepEqual(current, a.binding)
}
func (p *Provider) Invoke(ctx context.Context, v protocol.CapabilityInvoke) protocol.CapabilityResult {
	r := protocol.CapabilityResult{RequestID: v.RequestID, Status: "failed"}
	fail := func(msg string) protocol.CapabilityResult { r.Error = msg; return r }
	a, ok := p.tools[v.CapabilityID]
	if !ok {
		return fail("MCP capability not locally approved")
	}
	if !p.stillApproved(a) {
		return fail("MCP approval revoked or changed; reload required")
	}
	room, caller, verified := protocol.InvocationActor(ctx)
	allowed := false
	for _, id := range a.binding.AllowedCallers {
		if id == caller {
			allowed = true
		}
	}
	if !verified || !allowed {
		return fail("MCP caller not locally approved")
	}
	if len(a.binding.AllowedRooms) > 0 {
		allowed = false
		for _, id := range a.binding.AllowedRooms {
			if id == room {
				allowed = true
			}
		}
		if !allowed {
			return fail("MCP room not locally approved")
		}
	}
	args, e := json.Marshal(v.Payload)
	var obj map[string]any
	if e != nil || len(args) > 16384 || json.Unmarshal(args, &obj) != nil || obj == nil {
		return fail("MCP arguments must be a JSON object of at most 16384 bytes")
	}
	client, e := clientFor(a)
	if e != nil {
		return fail("MCP endpoint not locally approved")
	}
	token := ""
	if a.binding.TokenEnv != "" {
		token = os.Getenv(a.binding.TokenEnv)
		if token == "" || strings.ContainsAny(token, "\r\n") {
			return fail("local MCP credential unavailable")
		}
	}
	s := session{client: client, endpoint: a.manifest.Endpoint, token: token}
	init, _, e := s.request(ctx, "initialize", map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "GooseCab", "version": "1"}}, false)
	if e != nil {
		return fail("MCP initialization failed")
	}
	var initial struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(init, &initial) != nil || initial.ProtocolVersion != "2025-03-26" {
		return fail("MCP protocol version not supported")
	}
	if _, _, e = s.request(ctx, "notifications/initialized", map[string]any{}, true); e != nil {
		return fail("MCP initialization failed")
	}
	listed, _, e := s.request(ctx, "tools/list", map[string]any{}, false)
	if e != nil {
		return fail("MCP tool discovery failed")
	}
	var listing struct {
		Tools []struct {
			Name        string          `json:"name"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
		NextCursor string `json:"nextCursor"`
	}
	if json.Unmarshal(listed, &listing) != nil || listing.NextCursor != "" {
		return fail("MCP tool discovery must fit one bounded page")
	}
	matches := 0
	for _, t := range listing.Tools {
		if t.Name == a.tool.Name {
			matches++
			if !sameSchema(t.InputSchema, a.tool.InputSchema) {
				return fail("MCP tool schema differs from approved manifest")
			}
		}
	}
	if matches != 1 {
		return fail("approved MCP tool missing or ambiguous")
	}
	if !p.stillApproved(a) {
		return fail("MCP approval revoked or changed; reload required")
	}
	result, unknown, e := s.request(ctx, "tools/call", map[string]any{"name": a.tool.Name, "arguments": obj}, false)
	if e != nil {
		if unknown {
			r.Status = "unknown"
		}
		return fail(e.Error())
	}
	// Validate presence as well as types: Go's zero-value decoding would otherwise
	// mistake null/missing content or missing text for successful empty output.
	var fields map[string]json.RawMessage
	if json.Unmarshal(result, &fields) != nil || fields == nil {
		r.Status = "unknown"
		return fail("invalid MCP tool result")
	}
	if _, present := fields["structuredContent"]; present {
		r.Status = "unknown"
		return fail("structured MCP results are not supported")
	}
	rawContent, present := fields["content"]
	var blocks []struct {
		Type string  `json:"type"`
		Text *string `json:"text"`
	}
	if !present || json.Unmarshal(rawContent, &blocks) != nil || blocks == nil {
		r.Status = "unknown"
		return fail("invalid MCP content array")
	}
	isError := false
	if raw, present := fields["isError"]; present {
		if string(raw) != "true" && string(raw) != "false" {
			r.Status = "unknown"
			return fail("invalid MCP error flag")
		}
		isError = string(raw) == "true"
	}
	var text strings.Builder
	for _, c := range blocks {
		if c.Type != "text" || c.Text == nil || !utf8.ValidString(*c.Text) || text.Len()+len(*c.Text) > 4096 {
			r.Status = "unknown"
			return fail("MCP result is not bounded plain text")
		}
		text.WriteString(*c.Text)
	}
	if isError {
		return fail("MCP tool reported failure")
	}
	output := text.String()
	if token != "" {
		output = strings.ReplaceAll(output, token, "[redacted]")
	}
	if len(output) > 4096 {
		r.Status = "unknown"
		return fail("MCP output exceeds text limit")
	}
	r.Status = "completed"
	r.ResultAvailable = true
	r.Output = &protocol.TextOutput{Text: output, MediaType: "text/plain"}
	return r
}
