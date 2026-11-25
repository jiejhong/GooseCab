package gateway

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRoomChatTransportUsesActualTLSOrExplicitLoopbackProxy(t *testing.T) {
	digest := sha256.Sum256([]byte("synthetic only room credential"))
	member := RoomChatMember{ID: "fixture-reader", TokenSHA256: hex.EncodeToString(digest[:]), ExpiresAt: time.Now().Add(time.Minute)}
	cfg := RoomChatConfig{Origin: "https://gateway.example.invalid", TrustLoopbackProxy: true, Rooms: []RoomChatRoom{
		{ID: "demo", Humans: []RoomChatMember{member}},
	}}
	policy, err := newRoomChatPolicy(cfg, "synthetic-shared")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		host, remote, proto string
		tls, allow          bool
	}{
		{"gateway.example.invalid", "192.0.2.1:1234", "https", false, false},
		{"gateway.example.invalid", "127.0.0.1:1234", "", false, false},
		{"gateway.example.invalid", "127.0.0.1:1234", "https", false, true},
		{"gateway.example.invalid", "192.0.2.1:1234", "", true, true},
		{"different.example.invalid", "127.0.0.1:1234", "https", false, false},
	} {
		r := httptest.NewRequest("GET", "/room-chat", nil)
		r.Host, r.RemoteAddr = tc.host, tc.remote
		r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8000}))
		r.Header.Set("X-Forwarded-For", "127.0.0.1")
		if tc.proto != "" {
			r.Header.Set("X-Forwarded-Proto", tc.proto)
		}
		if tc.tls {
			r.TLS = &tls.ConnectionState{}
		}
		if policy.protectedTransport(r) != tc.allow {
			t.Fatalf("unexpected transport decision for %+v", tc)
		}
	}
	if _, err := newRoomChatPolicy(cfg, ""); err != nil {
		t.Fatal("ordinary public rooms should not require a shared credential", err)
	}
	insecure := cfg
	insecure.Origin = "http://127.0.0.1:8000"
	if _, err := newRoomChatPolicy(insecure, "synthetic-shared"); err == nil {
		t.Fatal("protected production origin accepted HTTP")
	}
	legacy := sha256.Sum256([]byte("synthetic-shared"))
	cfg.Rooms[0].Humans[0].TokenSHA256 = hex.EncodeToString(legacy[:])
	if _, err := newRoomChatPolicy(cfg, "synthetic-shared"); err == nil {
		t.Fatal("shared credential became a human credential")
	}
}
