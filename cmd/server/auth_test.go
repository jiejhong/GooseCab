package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/jiejhong/GooseCab/internal/registry"
	"github.com/jiejhong/GooseCab/internal/store"
)

func testServerHandler(t *testing.T) (http.Handler, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "private-database.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return newServerHandler(s, ":8312", path), path
}

func request(h http.Handler, method, path, auth, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestServerProtectsEveryAPIRoute(t *testing.T) {
	t.Setenv("GOOSECAB_AGENT_TOKEN", "secret")
	h, database := testServerHandler(t)
	// Exercise every registered route family through the actual production stack.
	// Malformed JSON deliberately avoids mutations in authorized POST requests.
	routes := []struct {
		method, path     string
		authorizedStatus int
	}{
		{"GET", "/api/status", 200},
		{"GET", "/api/agents", 200}, {"POST", "/api/agents", 400},
		{"GET", "/api/capabilities?q=test", 200},
		{"POST", "/api/invocations", 400}, {"POST", "/api/invocations/missing/progress", 400}, {"POST", "/api/invocations/missing/result", 400},
		{"POST", "/api/teach-sessions", 400}, {"POST", "/api/skills", 400}, {"POST", "/api/skills/missing/rollback", 400}, {"POST", "/api/evaluations", 400},
		{"POST", "/api/plans", 400}, {"POST", "/api/tasks", 400}, {"POST", "/api/tasks/missing/retry", 400},
		{"GET", "/api/capability-resolve?q=test", 200}, {"POST", "/api/blackboards", 400}, {"POST", "/api/private-sessions", 400},
		{"POST", "/api/transfers", 400}, {"POST", "/api/transfers/missing/progress", 400},
		{"POST", "/api/v1/relay", 400}, {"GET", "/api/v1/relay/connect", 404},
		{"GET", "/api/events?room_id=test", 200}, {"GET", "/api/events/export.html?room_id=test", 200},
		{"GET", "/api/v1/agent/connect", 400}, {"GET", "/ws", 400},
		{"GET", "/api/future-route", 404}, {"GET", "/api", 301},
	}
	for _, route := range routes {
		for _, auth := range []string{"", "Bearer wrong", "Bearer secret"} {
			t.Run(route.method+" "+route.path+" "+auth, func(t *testing.T) {
				w := request(h, route.method, route.path, auth, "{")
				want := http.StatusUnauthorized
				if auth == "Bearer secret" {
					want = route.authorizedStatus
				}
				if w.Code != want {
					t.Fatalf("status=%d, want %d: %s", w.Code, want, w.Body)
				}
				if want == http.StatusUnauthorized && strings.Contains(w.Body.String(), database) {
					t.Fatal("unauthenticated response leaked database path")
				}
			})
		}
	}
	for _, path := range []string{"/", "/index.html", "/healthz"} {
		w := request(h, "GET", path, "", "")
		if w.Code != http.StatusOK {
			t.Errorf("public %s status=%d", path, w.Code)
		}
		if strings.Contains(w.Body.String(), database) {
			t.Errorf("public %s leaks database path", path)
		}
	}
}

func TestServerAuthenticatedWritesAndLegacyMode(t *testing.T) {
	for _, token := range []string{"secret", ""} {
		t.Run("token="+token, func(t *testing.T) {
			t.Setenv("GOOSECAB_AGENT_TOKEN", token)
			h, _ := testServerHandler(t)
			auth := ""
			if token != "" {
				for _, bad := range []string{"", "Bearer wrong"} {
					w := request(h, "POST", "/api/agents", bad, `{"id":"intruder"}`)
					if w.Code != 401 {
						t.Fatalf("unauthorized write status=%d", w.Code)
					}
				}
				auth = "Bearer " + token
			}
			w := request(h, "POST", "/api/agents", auth, `{"id":"trusted"}`)
			if w.Code != 201 {
				t.Fatalf("authorized write status=%d: %s", w.Code, w.Body)
			}
			w = request(h, "GET", "/api/agents", auth, "")
			var agents []registry.Agent
			if err := json.Unmarshal(w.Body.Bytes(), &agents); err != nil {
				t.Fatal(err)
			}
			if len(agents) != 1 || agents[0].ID != "trusted" {
				t.Fatalf("unauthorized mutation or missing authorized mutation: %+v", agents)
			}
			for _, path := range []string{"/api/status", "/api/events?room_id=test", "/api/capabilities", "/api/capability-resolve?q=test"} {
				if w := request(h, "GET", path, auth, ""); w.Code != 200 {
					t.Errorf("%s status=%d", path, w.Code)
				}
			}
			if token == "" {
				if w := request(h, "GET", "/api/agents", "Bearer ignored-in-legacy-mode", ""); w.Code != 200 {
					t.Errorf("legacy status=%d", w.Code)
				}
			}
		})
	}
}

func TestServerWebSocketAuthAndSharedRegistry(t *testing.T) {
	for _, token := range []string{"secret", ""} {
		for _, endpoint := range []string{"/api/v1/agent/connect", "/ws"} {
			t.Run("token="+token+endpoint, func(t *testing.T) {
				t.Setenv("GOOSECAB_AGENT_TOKEN", token)
				h, _ := testServerHandler(t)
				srv := httptest.NewServer(h)
				defer srv.Close()
				url := "ws" + strings.TrimPrefix(srv.URL, "http") + endpoint + "?room_id=registry"
				if token != "" {
					for _, bad := range []string{"", "Bearer wrong"} {
						c, response, err := websocket.DefaultDialer.Dial(url, http.Header{"Authorization": []string{bad}})
						if c != nil {
							c.Close()
							t.Fatal("unauthorized WebSocket upgraded")
						}
						if response != nil {
							response.Body.Close()
						}
						if err == nil || response == nil || response.StatusCode != 401 {
							t.Fatalf("expected unauthorized handshake: response=%v err=%v", response, err)
						}
					}
				}
				auth := ""
				headers := http.Header{}
				if token != "" {
					auth = "Bearer " + token
					headers.Set("Authorization", auth)
				}
				c, _, err := websocket.DefaultDialer.Dial(url, headers)
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				if err := c.WriteJSON(map[string]any{"type": "agent.hello", "agent_id": "websocket-agent", "capabilities": []string{"cap.test"}}); err != nil {
					t.Fatal(err)
				}
				c.SetReadDeadline(time.Now().Add(3 * time.Second))
				if endpoint == "/ws" {
					var frame map[string]any
					if err := c.ReadJSON(&frame); err != nil {
						t.Fatal(err)
					}
					if frame["type"] != "error" || frame["code"] != "read_only" {
						t.Fatalf("observer accepted hello: %v", frame)
					}
					for _, path := range []string{"/api/agents", "/api/capabilities?q=cap.test", "/api/capability-resolve?q=cap.test"} {
						w := request(h, "GET", path, auth, "")
						if w.Code != 200 || strings.Contains(w.Body.String(), "websocket-agent") {
							t.Errorf("observer changed %s: status=%d body=%s", path, w.Code, w.Body)
						}
					}
					w := request(h, "GET", "/api/events?room_id=registry", auth, "")
					var history struct {
						Total int `json:"total"`
					}
					if err := json.Unmarshal(w.Body.Bytes(), &history); err != nil {
						t.Fatal(err)
					}
					if w.Code != 200 || history.Total != 0 {
						t.Fatalf("observer persisted events: status=%d body=%s", w.Code, w.Body)
					}
					return
				}
				// The persisted announce follows hello registration and avoids a
				// timing assumption between the welcome frame and registry update.
				for {
					var frame map[string]any
					if err := c.ReadJSON(&frame); err != nil {
						t.Fatal(err)
					}
					if frame["type"] == "capability.announce" {
						break
					}
				}
				for _, path := range []string{"/api/agents", "/api/capabilities?q=cap.test", "/api/capability-resolve?q=cap.test"} {
					w := request(h, "GET", path, auth, "")
					if w.Code != 200 || !strings.Contains(w.Body.String(), "websocket-agent") {
						t.Errorf("WS registry missing from %s: status=%d body=%s", path, w.Code, w.Body)
					}
				}
			})
		}
	}
}
