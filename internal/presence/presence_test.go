package presence

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestConnectionLifecycleIsRoomScoped(t *testing.T) {
	tracker := New(nil)
	now := time.Now()
	old := tracker.connect("work", "agent", now.Add(time.Minute))
	newer := tracker.connect("work", "agent", now.Add(time.Minute))
	second := tracker.connect("work", "another", now.Add(time.Minute))
	otherRoom := tracker.connect("demo", "agent", now.Add(time.Minute))
	defer otherRoom.close()

	want := func(room string, at time.Time, expected ...agent) {
		t.Helper()
		got := tracker.snapshot(room, at)
		if !slices.Equal(got, expected) {
			t.Fatalf("%s: got %+v, want %+v", room, got, expected)
		}
	}
	want("work", now, agent{ID: "agent", Connections: 2}, agent{ID: "another", Connections: 1})
	want("demo", now, agent{ID: "agent", Connections: 1})
	old.close()
	old.renew(now.Add(10 * time.Minute))
	want("work", now, agent{ID: "agent", Connections: 1}, agent{ID: "another", Connections: 1})

	newer.renew(now.Add(2 * time.Minute))
	want("work", now.Add(time.Minute), agent{ID: "agent", Connections: 1})
	want("work", now.Add(2*time.Minute))
	second.close()
	newer.close()
	reconnected := tracker.connect("work", "agent", now.Add(3*time.Minute))
	defer reconnected.close()
	newer.close()
	want("work", now.Add(2*time.Minute), agent{ID: "agent", Connections: 1})
}

func TestActivityIsRoomScopedAndRetainsLatestAcrossConnections(t *testing.T) {
	tracker := New(nil)
	now := time.Now()
	expires := now.Add(time.Hour)
	old := tracker.connect("room", "agent", expires)
	newer := tracker.connect("room", "agent", expires)
	other := tracker.connect("other", "agent", expires)
	defer other.close()
	check := func(room string, count int, want time.Time) {
		t.Helper()
		snapshot := tracker.snapshot(room, now.Add(time.Minute))
		if len(snapshot) != 1 || snapshot[0].Connections != count {
			t.Fatalf("%s has wrong online count: %+v", room, snapshot)
		}
		got := snapshot[0].LastActivityAt
		if (want.IsZero() && got != nil) || (!want.IsZero() && (got == nil || !got.Equal(want))) {
			t.Fatalf("%s: activity %v, want %v", room, got, want)
		}
	}
	old.renew(expires)
	check("room", 2, time.Time{})
	latest := now.Add(2 * time.Second)
	newer.recordActivity(latest)
	old.recordActivity(now.Add(time.Second))
	check("room", 2, latest)
	check("other", 1, time.Time{})
	old.close()
	old.recordActivity(now.Add(3 * time.Second))
	newer.recordActivity(expires)
	check("room", 1, latest)
	newer.close()
	fresh := tracker.connect("room", "agent", expires)
	defer fresh.close()
	newer.recordActivity(now.Add(4 * time.Second))
	check("room", 1, time.Time{})
}

func TestRoomSnapshotHTTP(t *testing.T) {
	tracker := New(nil)
	conn := tracker.connect("demo room", "worker", time.Now().Add(time.Minute))
	defer conn.close()
	path := "/api/rooms/presence?room_id=" + url.QueryEscape("demo room")

	get := httptest.NewRecorder()
	tracker.ServeHTTP(get, httptest.NewRequest(http.MethodGet, path, nil))
	var snapshot struct {
		Room   string  `json:"room_id"`
		Agents []agent `json:"agents"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if get.Code != http.StatusOK || get.Header().Get("Cache-Control") != "no-store" ||
		snapshot.Room != "demo room" || !slices.Equal(snapshot.Agents, []agent{{ID: "worker", Connections: 1}}) ||
		!strings.Contains(get.Body.String(), `"last_activity_at":null`) {
		t.Fatalf("unexpected snapshot: status=%d headers=%v body=%s", get.Code, get.Header(), get.Body.String())
	}
	for _, test := range []struct {
		method string
		path   string
		status int
	}{
		{http.MethodGet, "/api/rooms/presence", http.StatusBadRequest},
		{http.MethodGet, path + "&room_id=other", http.StatusBadRequest},
		{http.MethodGet, "/api/rooms/presence?room_id=" + url.QueryEscape(strings.Repeat("中", 86)), http.StatusBadRequest},
		{http.MethodPost, path, http.StatusMethodNotAllowed},
		{http.MethodGet, "/api/rooms/presence?room_id=other", http.StatusOK},
	} {
		w := httptest.NewRecorder()
		tracker.ServeHTTP(w, httptest.NewRequest(test.method, test.path, nil))
		if w.Code != test.status {
			t.Errorf("%s %s: got %d, want %d", test.method, test.path, w.Code, test.status)
		}
		if test.method == http.MethodPost && w.Header().Get("Allow") != http.MethodGet {
			t.Error("method response did not advertise GET")
		}
		if test.status == http.StatusOK && !strings.Contains(w.Body.String(), `"agents":[]`) {
			t.Errorf("empty room should return an empty array: %s", w.Body.String())
		}
	}
}

func TestRoomActivityDoesNotChangeOnlineSnapshot(t *testing.T) {
	at := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, test := range []struct {
		name      string
		result    *time.Time
		err       error
		available bool
		value     string
	}{
		{"activity", &at, nil, true, `"2020-01-02T03:04:05Z"`},
		{"empty", nil, nil, true, "null"},
		{"unavailable", &at, errors.New("private database path"), false, "null"},
	} {
		t.Run(test.name, func(t *testing.T) {
			tracker := New(func(ctx context.Context, room string) (*time.Time, error) {
				if room != "demo" {
					t.Fatalf("activity lookup used room %q", room)
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("activity lookup had no deadline")
				}
				return test.result, test.err
			})
			worker := tracker.connect("demo", "worker", time.Now().Add(time.Minute))
			defer worker.close()
			response := httptest.NewRecorder()
			tracker.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/rooms/presence?room_id=demo", nil))
			var snapshot struct {
				Agents    []agent         `json:"agents"`
				Activity  json.RawMessage `json:"last_activity_at"`
				Available bool            `json:"activity_available"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusOK || !slices.Equal(snapshot.Agents, []agent{{ID: "worker", Connections: 1}}) ||
				string(snapshot.Activity) != test.value || snapshot.Available != test.available ||
				strings.Contains(response.Body.String(), "private database path") {
				t.Fatalf("unexpected snapshot: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestWebSocketHeartbeat(t *testing.T) {
	for _, responsive := range []bool{true, false} {
		name := "silent"
		if responsive {
			name = "responsive"
		}
		t.Run(name, func(t *testing.T) {
			tracker := New(nil)
			tracker.pingInterval = 20 * time.Millisecond
			tracker.pongTimeout = 300 * time.Millisecond
			watched := make(chan error, 1)
			closed := make(chan struct{})
			upgrader := websocket.Upgrader{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(closed)
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					watched <- err
					return
				}
				defer conn.Close()
				session, err := tracker.Watch(conn, "demo", "worker")
				watched <- err
				if err != nil {
					return
				}
				defer session.Close()
				for {
					if _, _, err := conn.ReadMessage(); err != nil {
						return
					}
				}
			}))
			defer server.Close()
			conn, _, err := websocket.DefaultDialer.Dial("ws"+server.URL[4:], nil)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := <-watched; err != nil {
				t.Fatal(err)
			}
			if !responsive {
				conn.SetPingHandler(func(string) error { return nil })
			}
			go func() { _, _, _ = conn.ReadMessage() }()

			if responsive {
				// Two initial lease lengths pass while the client answers Pings.
				select {
				case <-closed:
					t.Fatal("responsive connection closed")
				case <-time.After(2 * tracker.pongTimeout):
				}
				if got := tracker.snapshot("demo", time.Now()); len(got) != 1 || got[0].Connections != 1 || got[0].LastActivityAt != nil {
					t.Fatalf("responsive agent missing: %+v", got)
				}
				_ = conn.Close()
			}
			select {
			case <-closed:
			case <-time.After(3 * time.Second):
				t.Fatal("server did not release the connection")
			}
			if got := tracker.snapshot("demo", time.Now()); len(got) != 0 {
				t.Fatalf("disconnected agent still visible: %+v", got)
			}
		})
	}
}
