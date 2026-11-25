//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jiejhong/GooseCab/internal/agent"
	"github.com/jiejhong/GooseCab/internal/gateway"
	"github.com/jiejhong/GooseCab/internal/protocol"
	"github.com/jiejhong/GooseCab/internal/relay"
	"github.com/jiejhong/GooseCab/internal/store"
)

type demoWriteFixture struct {
	origin  string
	db      *store.Store
	dbPath  string
	backend *httptest.Server
	edge    *httptest.Server
	writer  *http.Server
}

func demoWriteFixtureSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "goose-demo-edge-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0750); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "writer.sock")
}

func startDemoWriteFixture(t *testing.T, cert, key string) *demoWriteFixture {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "existing.db")
	createPriorRoomChatDB(t, dbPath)
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	edge := httptest.NewUnstartedServer(nil)
	t.Cleanup(edge.Close)
	origin := "https://" + edge.Listener.Addr().String()

	// The fixture uses distinct sockets in one process to exercise the real
	// kernel UID check. Production requires separate Gateway and ingress UIDs.
	socketPath := demoWriteFixtureSocket(t)
	legacy, writerHandler, err := assembleGatewayHandlers(db, ":0", dbPath, relay.Config{}, nil,
		&demoWriteConfig{Origin: origin, Socket: socketPath, IngressUID: uint32(os.Geteuid())})
	if err != nil {
		t.Fatal(err)
	}
	sock, err := listenDemoWriteSocket(socketPath, uint32(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	writer := &http.Server{Handler: writerHandler}
	t.Cleanup(func() { _ = writer.Close() })
	go func() { _ = writer.Serve(sock) }()
	backend := httptest.NewServer(legacy)
	t.Cleanup(backend.Close)
	upstream, _ := url.Parse(backend.URL)
	oldProxy := httputil.NewSingleHostReverseProxy(upstream)
	dialer := &net.Dialer{Timeout: time.Second}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", socketPath)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	writeProxy := &httputil.ReverseProxy{Transport: transport, Rewrite: func(pr *httputil.ProxyRequest) {
		pr.Out.URL.Scheme, pr.Out.URL.Host = "http", "local-writer"
		pr.Out.Host = pr.In.Host
		pr.Out.Header.Del("Cookie")
		pr.Out.Header.Del("Authorization")
	}, ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "writer unavailable", http.StatusServiceUnavailable)
	}}
	edge.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/demo-write", "/demo-write.js", "/api/demo-write/messages":
			// On the synthetic ingress, only the original peer address
			// determines membership. Forwarded request headers are ignored.
			peer, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil || peer != "127.0.0.1" {
				http.Error(w, "fixture allowlist denied", http.StatusForbidden)
				return
			}
			writeProxy.ServeHTTP(w, r)
		default:
			oldProxy.ServeHTTP(w, r)
		}
	})
	if cert != "" {
		pair, err := tls.LoadX509KeyPair(cert, key)
		if err != nil {
			t.Fatal(err)
		}
		edge.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	}
	edge.StartTLS()
	return &demoWriteFixture{origin: origin, db: db, dbPath: dbPath, backend: backend, edge: edge, writer: writer}
}

func TestAllowlistedDemoWriteThroughExistingIngress(t *testing.T) {
	t.Setenv("GOOSECAB_AGENT_TOKEN", fixtureLegacyToken)
	f := startDemoWriteFixture(t, "", "")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	auth := "Bearer " + fixtureLegacyToken
	ws := "ws" + strings.TrimPrefix(f.backend.URL, "http") + "/api/v1/agent/connect"
	legacy, err := agent.DialWithTokenContext(ctx, ws, "demo", "old-fixture", nil, fixtureLegacyToken)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()

	roomChatHTTP(t, f.edge, "GET", "/demo-write", "", "", "", 200)
	roomChatHTTP(t, f.edge, "GET", "/demo-write.js", "", "", "", 200)
	request := func(client *http.Client, url, origin, body string, extra http.Header, want int) []byte {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		for key, values := range extra {
			req.Header[key] = values
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, err := io.ReadAll(res.Body)
		if err != nil || res.StatusCode != want {
			t.Fatalf("%s got %d want %d: %s (%v)", url, res.StatusCode, want, data, err)
		}
		return data
	}
	const path = "/api/demo-write/messages"
	fake := http.Header{"X-Forwarded-Proto": {"https"}, "X-Forwarded-For": {"127.0.0.1"}, "X-Authenticated-User": {"old-fixture"}}
	deniedTransport := f.edge.Client().Transport.(*http.Transport).Clone()
	deniedTransport.DialContext = (&net.Dialer{LocalAddr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 2)}}).DialContext
	defer deniedTransport.CloseIdleConnections()
	denied := &http.Client{Transport: deniedTransport, Timeout: 2 * time.Second}
	res, err := denied.Get(f.origin + "/demo-write")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("the disallowed TLS peer received the writer page", res.StatusCode)
	}
	request(denied, f.origin+path, f.origin, `{"text":"denied"}`, fake, 403)
	request(f.backend.Client(), f.backend.URL+path, f.origin, `{"text":"denied"}`, fake, 401)
	roomChatHTTP(t, f.backend, "POST", path, auth, f.origin, `{"text":"denied"}`, 404)
	request(f.edge.Client(), f.origin+path, "https://unrelated.example.invalid", `{"text":"denied"}`, nil, 403)
	request(f.edge.Client(), f.origin+path, "", `{"text":"denied"}`, nil, 403)
	request(f.edge.Client(), f.origin+path, f.origin, `{"text":"denied","actor":{"kind":"agent","id":"old-fixture"}}`, nil, 400)
	request(f.edge.Client(), f.origin+path, f.origin, `{"text":"denied","mention":{"kind":"agent","id":"old-fixture"}}`, nil, 400)
	roomChatHTTP(t, f.edge, "POST", "/api/room-chat/unread/ack?room_id=demo", "", f.origin, `{}`, 401)

	post := request(f.edge.Client(), f.origin+path, f.origin, `{"text":"synthetic guest text"}`, fake, 201)
	var committed protocol.Event
	if json.Unmarshal(post, &committed) != nil || committed.EventID == "" || committed.RoomID != "demo" ||
		committed.Actor != (protocol.Actor{Kind: "guest", ID: "白名单访客"}) {
		t.Fatal("unexpected guest attribution or commit receipt")
	}
	sawPrior := false
	for {
		frame, err := legacy.ReadContext(ctx)
		if err != nil {
			t.Fatal(err)
		}
		sawPrior = sawPrior || frame["event_id"] == "before-a"
		if frame["event_id"] == committed.EventID {
			body, _ := frame["payload"].(map[string]any)
			if len(body) != 1 || body["text"] != "synthetic guest text" {
				t.Fatal("old WS received something other than the ordinary text event")
			}
			break
		}
	}
	if !sawPrior {
		t.Fatal("old Agent did not receive the existing history")
	}
	if err := legacy.SendChat("old fixture reply"); err != nil {
		t.Fatal(err)
	}
	for {
		msg, err := legacy.ReadContext(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if body, ok := msg["payload"].(map[string]any); ok && body["text"] == "old fixture reply" {
			break
		}
	}

	replayed, err := agent.DialWithTokenContext(ctx, ws+"?resume_seq="+strconv.FormatInt(committed.Seq-1, 10),
		"demo", "reconnect-fixture", nil, fixtureLegacyToken)
	if err != nil {
		t.Fatal(err)
	}
	defer replayed.Close()
	again, err := replayed.ReadContext(ctx)
	if err != nil || again["event_id"] != committed.EventID {
		t.Fatal("legacy reconnect did not replay the same committed event", err)
	}

	// Disabling the writer leaves the original Gateway assembly and SQLite
	// data available. A stale edge route must fail rather than redirect writes.
	if err := f.writer.Close(); err != nil {
		t.Fatal(err)
	}
	request(f.edge.Client(), f.origin+path, f.origin, `{"text":"denied"}`, nil, 503)
	rolled := httptest.NewServer(newServerHandler(f.db, ":0", f.dbPath))
	defer rolled.Close()
	roomChatHTTP(t, rolled, "POST", path, auth, f.origin, `{"text":"denied"}`, 404)
	for _, route := range []string{"/api/events?room_id=demo", "/api/events/export.html?room_id=demo"} {
		history := roomChatHTTP(t, rolled, "GET", route, auth, "", "", 200)
		if !bytes.Contains(history, []byte("prior conversation")) || !bytes.Contains(history, []byte("synthetic guest text")) ||
			!bytes.Contains(history, []byte("old fixture reply")) || bytes.Contains(history, []byte("denied")) {
			t.Fatal("history or export changed after closing demo writes", route)
		}
	}
}

func TestDemoWriterRejectsUntrustedUnixPeerAndRoomOverlap(t *testing.T) {
	socket := demoWriteFixtureSocket(t)
	listener, err := listenDemoWriteSocket(socket, uint32(os.Geteuid())+1)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		if conn, err := listener.Accept(); err == nil {
			_ = conn.Close()
		}
	}()
	conn, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	var b [1]byte
	if n, err := conn.Read(b[:]); n != 0 || err != io.EOF {
		t.Fatal("Unix request from another UID was not closed", n, err)
	}

	dbPath := filepath.Join(t.TempDir(), "gateway.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	demo := &demoWriteConfig{Origin: "https://gateway.example.invalid"}
	both := &gateway.RoomChatConfig{Origin: demo.Origin, Rooms: []gateway.RoomChatRoom{{ID: "demo"}}}
	if _, _, err := assembleGatewayHandlers(db, ":0", dbPath, relay.Config{}, both, demo); err == nil {
		t.Fatal("the same room could enable personal features and the allowlisted writer together")
	}
	if err := db.ProtectRoomChat(context.Background(), []string{"demo"}, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := assembleGatewayHandlers(db, ":0", dbPath, relay.Config{}, nil, demo); err == nil {
		t.Fatal("writer reopened a durable protected room")
	}
}

func TestDemoWriterOptInRequiresADedicatedIngressAccount(t *testing.T) {
	if config, err := readDemoWriteConfig(""); err != nil || config != nil {
		t.Fatal("demo writer must default to disabled")
	}
	dir := t.TempDir()
	configFile := filepath.Join(dir, "writer.json")
	config := demoWriteConfig{Origin: "https://gateway.example.invalid", Socket: filepath.Join(dir, "writer.sock"),
		IngressUID: uint32(os.Geteuid())}
	writeConfig := func() {
		t.Helper()
		data, err := json.Marshal(config)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(configFile, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig()
	if _, err := readDemoWriteConfig(configFile); err == nil {
		t.Fatal("the Gateway account cannot also be the ingress account")
	}
	config.IngressUID++
	writeConfig()
	if got, err := readDemoWriteConfig(configFile); err != nil || got.IngressUID != config.IngressUID {
		t.Fatal("explicit ingress configuration was not accepted", err)
	}
}

func TestDemoWriterBrowserFixture(t *testing.T) {
	cert, key := os.Getenv("GOOSECAB_TEST_DEMO_CERT"), os.Getenv("GOOSECAB_TEST_DEMO_KEY")
	if cert == "" || key == "" {
		t.Skip("launched by the synthetic browser regression")
	}
	t.Setenv("GOOSECAB_AGENT_TOKEN", "")
	f := startDemoWriteFixture(t, cert, key)
	_, _ = os.Stdout.WriteString(f.origin + "\n" + f.backend.URL + "\n")
	_, _ = io.Copy(io.Discard, os.Stdin)
}
