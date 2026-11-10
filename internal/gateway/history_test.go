package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiejhong/GooseCab/internal/protocol"
	"github.com/jiejhong/GooseCab/internal/store"
)

func historyFixture(t *testing.T) (*store.Store, http.Handler) {
	t.Helper()
	t.Setenv("GOOSECAB_AGENT_TOKEN", "")
	s, err := store.Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, (&HistoryAPI{Store: s}).Handler(http.NotFoundHandler())
}

func appendHistory(t *testing.T, s *store.Store, event protocol.Event) {
	t.Helper()
	if err := s.Append(context.Background(), event); err != nil {
		t.Fatal(err)
	}
}

func historyEvent(room, id string, seq int64, typ string, payload any) protocol.Event {
	return protocol.Event{EventID: id, RoomID: room, Seq: seq, Type: typ, Actor: protocol.Actor{Kind: "agent", ID: "agent-a"}, TaskID: "task-1", CapabilityID: "cap.echo", SessionID: "session-1", CorrelationID: "request-1", CausationID: "cause-1", TS: time.Date(2026, 11, 10, 2, 0, 0, 0, time.UTC), Payload: payload}
}

func historyRequest(h http.Handler, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestHistoryHTMLIsEscapedRoomScopedAndRedacted(t *testing.T) {
	s, h := historyFixture(t)
	t.Setenv("GOOSECAB_AGENT_TOKEN", "gateway-secret-token")
	attack := `<script>alert("xss")</script><img src="https://evil.invalid/leak" onerror="alert(1)">`
	room := `room <b>甲</b>`
	appendHistory(t, s, historyEvent(room, "chat", 1, "chat.message", map[string]any{"text": attack, "access_token": "payload-secret", "paths": []any{"/home/alice/private.txt"}, "nested": map[string]any{"Authorization": "hidden-auth"}, "note": "Bearer hidden-bearer, file:///worker/result.json C:\\Users\\Alice\\secret.txt gateway-secret-token", "number": json.Number("9007199254740993")}))
	appendHistory(t, s, historyEvent(room, "invoke", 2, "capability.invoke", map[string]any{"target_agent_id": "worker"}))
	appendHistory(t, s, historyEvent(room, "progress", 3, "capability.progress", map[string]any{"percent": 50, "message": "working"}))
	appendHistory(t, s, historyEvent(room, "result", 4, "capability.result", map[string]any{"status": "completed", "output_ref": "file:///worker/out.json"}))
	appendHistory(t, s, historyEvent("other", "other", 1, "chat.message", map[string]any{"text": "OTHER_ROOM_SECRET"}))
	path := "/api/events/export.html?room_id=" + url.QueryEscape(room)
	w := historyRequest(h, path, "gateway-secret-token")
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, forbidden := range []string{"<script>", "<img ", "payload-secret", "hidden-auth", "hidden-bearer", "gateway-secret-token", "/home/alice", "file:///worker", `C:\\Users`, "OTHER_ROOM_SECRET"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("unsafe export contains %q", forbidden)
		}
	}
	for _, expected := range []string{"&lt;script&gt;", "room &lt;b&gt;甲&lt;/b&gt;", "能力调用", "执行进度", "执行结果", "50%", "9007199254740993", "session-1", "cause-1", "[已隐藏]", "全部 4 条记录"} {
		if !strings.Contains(body, expected) {
			t.Errorf("missing %q", expected)
		}
	}
	if w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment;") || !strings.Contains(w.Header().Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatal("missing safe export headers")
	}
	// JSON list applies the exact same redaction and retains full envelope fields.
	w = historyRequest(h, "/api/events?room_id="+url.QueryEscape(room), "gateway-secret-token")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var result struct {
		Events   []protocol.Event `json:"events"`
		Redacted bool             `json:"redacted"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 4 || !result.Redacted || result.Events[0].SessionID != "session-1" {
		t.Fatalf("invalid list: %+v", result)
	}
	if strings.Contains(w.Body.String(), "payload-secret") || strings.Contains(w.Body.String(), "gateway-secret-token") {
		t.Fatal("JSON leaked secret")
	}
	// Export must not mutate or redact the stored event itself.
	stored, err := s.EventsAfter(context.Background(), room, 0)
	if err != nil {
		t.Fatal(err)
	}
	if stored[0].Payload.(map[string]any)["access_token"] != "payload-secret" {
		t.Fatal("stored data modified")
	}
}

func TestHistoryPaginationSnapshotTieCursorAndValidation(t *testing.T) {
	s, h := historyFixture(t)
	for i, seq := range []int64{1, 1, 2} {
		appendHistory(t, s, historyEvent("demo", fmt.Sprint(i), seq, "chat.message", map[string]any{"text": fmt.Sprint(i)}))
	}
	read := func(path string) (int64, string, int64, bool, []protocol.Event) {
		t.Helper()
		w := historyRequest(h, path, "")
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		var p struct {
			Next     int64            `json:"next_seq"`
			Cursor   string           `json:"next_cursor"`
			Snapshot int64            `json:"snapshot_seq"`
			More     bool             `json:"has_more"`
			Events   []protocol.Event `json:"events"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		return p.Next, p.Cursor, p.Snapshot, p.More, p.Events
	}
	seq, cursor, snapshot, more, events := read("/api/events?room_id=demo&limit=1")
	if len(events) != 1 || !more || snapshot != 2 {
		t.Fatal("invalid initial page")
	}
	appendHistory(t, s, historyEvent("demo", "later", 3, "chat.message", "new append"))
	ids := []string{events[0].EventID}
	for more {
		seq, cursor, _, more, events = read(fmt.Sprintf("/api/events?room_id=demo&limit=1&after_seq=%d&after_cursor=%s&through_seq=%d", seq, cursor, snapshot))
		for _, e := range events {
			ids = append(ids, e.EventID)
		}
	}
	if strings.Join(ids, ",") != "0,1,2" {
		t.Fatalf("lost/duplicated snapshot rows %v", ids)
	}
	for _, query := range []string{"", "room_id=", "room_id=demo&room_id=other", "room_id=demo&after_seq=-1", "room_id=demo&after_seq=x", "room_id=demo&after_seq=3&through_seq=2", "room_id=demo&through_seq=", "room_id=demo&limit=0", "room_id=demo&limit=1001", "room_id=demo&after_cursor=abc", "room_id=demo&token=bad"} {
		if w := historyRequest(h, "/api/events?"+query, ""); w.Code != 400 {
			t.Errorf("query %q: status %d", query, w.Code)
		}
	}
	w := historyRequest(h, "/api/events/export.html?room_id=demo&after_seq=1&through_seq=2", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "全部 1 条记录") {
		t.Fatalf("bad ranged export: %d %s", w.Code, w.Body.String())
	}
	w = historyRequest(h, "/api/events/export.html?room_id=empty", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "还没有记录") {
		t.Fatal("empty export failed")
	}
}

func TestHistoryAuthenticationAndMethods(t *testing.T) {
	_, h := historyFixture(t)
	t.Setenv("GOOSECAB_AGENT_TOKEN", "required-token")
	for _, path := range []string{"/api/events?room_id=demo", "/api/events/export.html?room_id=demo"} {
		for _, token := range []string{"", "wrong"} {
			if w := historyRequest(h, path, token); w.Code != 401 {
				t.Fatal("auth bypass")
			}
		}
		if w := historyRequest(h, path, "required-token"); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set("Authorization", "Bearer required-token")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 405 || w.Header().Get("Allow") != "GET" {
			t.Fatal("method accepted")
		}
	}
	if w := historyRequest(h, "/missing", ""); w.Code != 404 {
		t.Fatal("did not delegate unknown route")
	}
}

func TestHistoryErrorsDoNotExposeStoragePath(t *testing.T) {
	s, h := historyFixture(t)
	_ = s.Close()
	w := historyRequest(h, "/api/events/export.html?room_id=demo", "")
	if w.Code != 500 || strings.Contains(w.Body.String(), "history.db") {
		t.Fatal("unsafe storage error")
	}
}

func TestHistoryRedactionRecognizesQuotedSecretsAndPaths(t *testing.T) {
	r := newHistoryRedactor("active-secret")
	for _, input := range []string{`password="hunter two"`, `{"token":"abc"}`, `api_key='key with spaces'`, `failed:/home/alice/private.txt`, `[/Users/alice/a.txt]`, `Bearer bearer-secret`, `file:///local/item`, `active-secret`} {
		if got := r.text(input); got == input || !strings.Contains(got, redactedValue) {
			t.Errorf("not redacted: %q => %q", input, got)
		}
	}
	if got := r.text(`<script>alert("hello")</script>`); got != `<script>alert("hello")</script>` {
		t.Fatalf("content changed instead of being escaped later: %s", got)
	}
}

func TestHistoryHTMLRefusesOversizedSnapshot(t *testing.T) {
	t.Setenv("GOOSECAB_AGENT_TOKEN", "")
	path := filepath.Join(t.TempDir(), "history.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 10001; i++ {
		if _, err = tx.Exec(`INSERT INTO events(event_id,room_id,seq,type,payload,created_at) VALUES(?,'large',?,'note','{}','2026-11-10T00:00:00Z')`, fmt.Sprint(i), i); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	h := (&HistoryAPI{Store: s}).Handler(http.NotFoundHandler())
	w := historyRequest(h, "/api/events/export.html?room_id=large", "")
	if w.Code != 413 || strings.Contains(w.Body.String(), "<!doctype") || w.Header().Get("Content-Disposition") != "" {
		t.Fatalf("silently exported partial snapshot: %d", w.Code)
	}
	w = historyRequest(h, "/api/events/export.html?room_id=large&after_seq=10000", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "全部 1 条记录") {
		t.Fatalf("range failed: %d", w.Code)
	}
}
