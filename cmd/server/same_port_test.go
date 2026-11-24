package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jiejhong/GooseCab/internal/agent"
	"github.com/jiejhong/GooseCab/internal/gateway"
	"github.com/jiejhong/GooseCab/internal/protocol"
	"github.com/jiejhong/GooseCab/internal/relay"
	"github.com/jiejhong/GooseCab/internal/store"
)

func samePortTestCertificate(t *testing.T) (string, string, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	binaryCert, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	binaryKey, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cert, keyFile := filepath.Join(dir, "test-cert.pem"), filepath.Join(dir, "test-key.pem")
	encodedCert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: binaryCert})
	for path, value := range map[string][]byte{cert: encodedCert,
		keyFile: pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: binaryKey})} {
		if err := os.WriteFile(path, value, 0600); err != nil {
			t.Fatal(err)
		}
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(encodedCert) {
		t.Fatal("could not load synthetic TLS certificate")
	}
	return cert, keyFile, roots
}

func TestGatewaySamePortProtectedChat(t *testing.T) {
	t.Setenv("GOOSECAB_AGENT_TOKEN", "")
	path := filepath.Join(t.TempDir(), "gateway.db")
	createPriorRoomChatDB(t, path)
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server := httptest.NewUnstartedServer(nil)
	defer server.Close()
	addr := server.Listener.Addr().String()
	origin := "https://" + addr

	writer, writerToken := roomChatFixtureMember("writer", 1, true, false)
	reader, readerToken := roomChatFixtureMember("reader", 2, false, false)
	worker, workerToken := roomChatFixtureMember("worker", 3, true, false)
	policy := &gateway.RoomChatConfig{Origin: origin, RequireUnusedRooms: true, Rooms: []gateway.RoomChatRoom{
		{ID: "demo", Humans: []gateway.RoomChatMember{writer, reader}, Agents: []gateway.RoomChatMember{worker}},
	}}
	cert, key, roots := samePortTestCertificate(t)
	tlsConfig, err := samePortTLS(policy, addr, cert, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := assembleServerHandler(s, addr, path, relay.Config{}, policy); err == nil || !strings.Contains(err.Error(), "new room IDs") {
		t.Fatal("same-port setup must preserve an existing legacy room", err)
	}
	if err := s.CheckRoomChatProtection(context.Background(), nil); err != nil {
		t.Fatal("rejected setup changed the room protection marker", err)
	}
	policy.Rooms[0].ID = "protected-fixture"
	handler, err := assembleServerHandler(s, addr, path, relay.Config{}, policy)
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.Config.TLSConfig = tlsConfig
	server.Config.ReadHeaderTimeout = 3 * time.Second
	server.Listener = newSamePortListener(server.Listener, tlsConfig)
	server.Start()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	legacy, err := agent.DialWithTokenContext(ctx, "ws://"+addr+"/api/v1/agent/connect", "demo", "legacy-fixture", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	// An idle connection waiting for its first byte must not hold up legacy or
	// protected users on the same listener.
	idle, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	rawClient := &http.Client{Timeout: 3 * time.Second}
	dialTLS := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	secureTransport := &http.Transport{TLSClientConfig: dialTLS}
	defer secureTransport.CloseIdleConnections()
	secureClient := &http.Client{Transport: secureTransport, Timeout: 3 * time.Second}
	request := func(client *http.Client, method, url, token, body string, status int) []byte {
		t.Helper()
		r, err := http.NewRequestWithContext(ctx, method, url, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			r.Header.Set("Authorization", "GooseCabRoom "+token)
		}
		if method == "POST" {
			r.Header.Set("Origin", origin)
			r.Header.Set("Content-Type", "application/json")
		}
		res, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, err := io.ReadAll(res.Body)
		if err != nil || res.StatusCode != status {
			t.Fatalf("%s %s got %d, want %d: %s (%v)", method, url, res.StatusCode, status, data, err)
		}
		return data
	}
	request(rawClient, "GET", server.URL+"/healthz", "", "", 200)
	request(rawClient, "GET", server.URL+"/room-chat", "", "", 403)
	request(secureClient, "GET", origin+"/room-chat", "", "", 200)

	dialer := websocket.Dialer{TLSClientConfig: dialTLS, HandshakeTimeout: 2 * time.Second}
	protected, _, err := dialer.DialContext(ctx, "wss://"+addr+"/api/v1/agent/connect?room_id=protected-fixture",
		http.Header{"Authorization": {"Bearer " + workerToken}})
	if err != nil {
		t.Fatal(err)
	}
	defer protected.Close()
	_ = protected.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := protected.WriteJSON(map[string]string{"type": "agent.hello", "agent_id": "worker"}); err != nil {
		t.Fatal(err)
	}
	var welcome map[string]any
	if err := protected.ReadJSON(&welcome); err != nil || welcome["type"] != "agent.welcome" {
		t.Fatal("protected WSS Agent did not finish its normal handshake", welcome, err)
	}

	const text = "synthetic protected chat"
	post := request(secureClient, "POST", origin+"/api/room-chat/messages?room_id=protected-fixture", writerToken,
		`{"text":"`+text+`","mention":{"kind":"agent","id":"worker"}}`, 201)
	var event protocol.Event
	if err := json.Unmarshal(post, &event); err != nil || event.EventID == "" || event.Actor.Kind != "human" {
		t.Fatal("HTTPS write did not return the committed human event", err)
	}
	var live protocol.Event
	if err := protected.ReadJSON(&live); err != nil || live.EventID != event.EventID {
		t.Fatal("authorized WSS Agent did not receive the committed event", err)
	}
	pending, err := s.RoomChatPending(ctx, "protected-fixture", protocol.Actor{Kind: "agent", ID: "worker"}, []protocol.Event{live})
	if err != nil || len(pending) != 1 || pending[0] != event.EventID {
		t.Fatal("Agent WSS streaming changed unread before a trusted receipt", pending, err)
	}

	for _, route := range []string{"/api/events", "/api/events/export.html"} {
		history := request(rawClient, "GET", server.URL+route+"?room_id=demo", "", "", 200)
		if !bytes.Contains(history, []byte("prior conversation")) || bytes.Contains(history, []byte(text)) {
			t.Fatal("original HTTP history changed room visibility", route)
		}
		private := request(secureClient, "GET", origin+route+"?room_id=protected-fixture", readerToken, "", 200)
		if !bytes.Contains(private, []byte(text)) || bytes.Contains(private, []byte("prior conversation")) {
			t.Fatal("protected HTTPS history changed room visibility", route)
		}
	}
	request(rawClient, "GET", server.URL+"/api/rooms/presence?room_id=demo", "", "", 200)
	request(secureClient, "GET", origin+"/api/rooms/presence?room_id=protected-fixture", readerToken, "", 200)
	if err := legacy.SendChat("legacy fixture stayed connected"); err != nil {
		t.Fatal(err)
	}
	sawLegacyReplay := false
	for {
		received, err := legacy.ReadContext(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if received["room_id"] != "demo" || received["event_id"] == event.EventID {
			t.Fatal("already-connected legacy Agent left its room")
		}
		if received["event_id"] == "before-a" {
			sawLegacyReplay = true
		}
		if payload, _ := received["payload"].(map[string]any); payload["text"] == "legacy fixture stayed connected" {
			break
		}
	}

	if !sawLegacyReplay {
		t.Fatal("legacy WS reconnect replay did not retain the original event ID")
	}
	_ = legacy.Close()
	_ = protected.Close()
	server.Close()
	policy.Rooms[0].ReadOnly = true
	if _, err := assembleServerHandler(s, addr, path, relay.Config{}, policy); err != nil {
		t.Fatal("read-only restart must accept an already marked room", err)
	}
	if _, err := assembleServerHandler(s, addr, path, relay.Config{}, nil); err == nil {
		t.Fatal("removing protection must refuse to serve the current database")
	}
}

func TestSamePortTLSRequiresMatchingListenerAndCertificate(t *testing.T) {
	cert, key, _ := samePortTestCertificate(t)
	for _, row := range []struct {
		origin, addr string
		proxy        bool
	}{
		{"http://127.0.0.1:8081", ":8081", false},
		{"https://127.0.0.1:8082", ":8081", false},
		{"https://example.invalid:8081", ":8081", false},
		{"https://127.0.0.1:8081", ":8081", true},
	} {
		policy := &gateway.RoomChatConfig{Origin: row.origin, TrustLoopbackProxy: row.proxy}
		if cfg, err := samePortTLS(policy, row.addr, cert, key); err == nil || cfg != nil {
			t.Errorf("invalid same-port configuration was accepted: %+v", row)
		}
	}
	if cfg, err := samePortTLS(nil, ":8081", "", ""); err != nil || cfg != nil {
		t.Fatal("default HTTP startup selected TLS", err)
	}
	if _, err := samePortTLS(nil, ":8081", cert, key); err == nil {
		t.Fatal("TLS activated without a room policy")
	}
}
