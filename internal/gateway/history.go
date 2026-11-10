package gateway

import (
	"bytes"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jiejhong/GooseCab/internal/protocol"
	"github.com/jiejhong/GooseCab/internal/store"
)

//go:embed web/export.html
var exportFS embed.FS

var historyTemplate = template.Must(template.ParseFS(exportFS, "web/export.html"))

// HistoryAPI exposes only persisted, room-scoped events, never referenced files.
type HistoryAPI struct{ Store *store.Store }

func (a *HistoryAPI) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/events" && r.URL.Path != "/api/events/export.html" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		token := os.Getenv("GOOSECAB_AGENT_TOKEN")
		if token != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="GooseCab"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		a.serve(w, r, token)
	})
}

func (a *HistoryAPI) serve(w http.ResponseWriter, r *http.Request, token string) {
	q := r.URL.Query()
	room := q.Get("room_id")
	if strings.TrimSpace(room) == "" || len(room) > 256 {
		http.Error(w, "room_id is required (maximum 256 bytes)", http.StatusBadRequest)
		return
	}
	// Reject duplicate parameters rather than accepting an ambiguous room/cursor.
	for key, values := range q {
		if len(values) != 1 || (key != "room_id" && key != "after_seq" && key != "after_cursor" && key != "through_seq" && key != "through_cursor" && key != "limit") {
			http.Error(w, "invalid history parameters", http.StatusBadRequest)
			return
		}
	}
	parseSeq := func(key string) (int64, error) {
		if !q.Has(key) {
			return 0, nil
		}
		n, err := strconv.ParseInt(q.Get(key), 10, 64)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("invalid %s", key)
		}
		return n, nil
	}
	after, err := parseSeq("after_seq")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var through *int64
	if q.Has("through_seq") {
		n, err := parseSeq("through_seq")
		if err != nil || n < after {
			http.Error(w, "invalid through_seq", http.StatusBadRequest)
			return
		}
		through = &n
	}
	if cursor := q.Get("after_cursor"); q.Has("after_cursor") {
		if n, e := strconv.ParseInt(cursor, 10, 64); e != nil || n < 1 {
			http.Error(w, "invalid after_cursor", http.StatusBadRequest)
			return
		}
	}
	var throughCursor *int64
	if q.Has("through_cursor") {
		n, err := parseSeq("through_cursor")
		if err != nil {
			http.Error(w, "invalid through_cursor", http.StatusBadRequest)
			return
		}
		throughCursor = &n
	}
	isExport := r.URL.Path == "/api/events/export.html"
	limit := 200
	if isExport {
		limit = 10000
		if q.Has("limit") || q.Has("after_cursor") || q.Has("through_cursor") {
			http.Error(w, "HTML export accepts room_id, after_seq and through_seq only", http.StatusBadRequest)
			return
		}
	} else if q.Has("limit") {
		limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil || limit < 1 || limit > 1000 {
			http.Error(w, "limit must be between 1 and 1000", http.StatusBadRequest)
			return
		}
	}
	page, err := a.Store.History(r.Context(), room, after, q.Get("after_cursor"), through, throughCursor, limit)
	if err != nil {
		// Do not expose database filenames or stored content in error responses.
		http.Error(w, "could not read event history", http.StatusInternalServerError)
		return
	}
	if isExport && page.HasMore {
		http.Error(w, "Export exceeds 10000 events. Select a smaller after_seq / through_seq range; no partial document was exported.", http.StatusRequestEntityTooLarge)
		return
	}
	redactor := newHistoryRedactor(token)
	events := make([]protocol.Event, 0, len(page.Events))
	for _, event := range page.Events {
		safe, err := redactor.event(event)
		if err != nil {
			http.Error(w, "could not prepare event history", http.StatusInternalServerError)
			return
		}
		events = append(events, safe)
	}
	if !isExport {
		nextSeq, nextID := after, q.Get("after_cursor")
		if len(page.Events) > 0 {
			last := page.Events[len(page.Events)-1]
			nextSeq, nextID = last.Seq, page.NextCursor
		}
		// Cursor is a SQLite row ID, never a potentially sensitive event ID.
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"room_id": redactor.text(room), "snapshot_seq": page.SnapshotSeq,
			"snapshot_cursor": strconv.FormatInt(page.SnapshotCursor, 10), "total": page.Total, "events": events, "has_more": page.HasMore,
			"next_seq": nextSeq, "next_cursor": nextID, "redacted": true,
		})
		return
	}
	doc := historyDocument{Room: redactor.text(room), Generated: time.Now().UTC().Format(time.RFC3339), After: after, Through: page.SnapshotSeq, Total: page.Total}
	actors := map[string]bool{}
	for _, event := range events {
		card := makeHistoryCard(event)
		doc.Events = append(doc.Events, card)
		if event.Actor.ID != "" {
			actors[event.Actor.ID] = true
		}
		switch card.Kind {
		case "chat":
			doc.Chats++
		case "invoke":
			doc.Invocations++
		case "result":
			doc.Results++
		}
	}
	doc.Agents = len(actors)
	var body bytes.Buffer
	if err = historyTemplate.Execute(&body, doc); err != nil {
		http.Error(w, "could not render event history", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="goosecab-chat-history.html"`)
	w.Header().Set("Content-Security-Policy", historyCSP)
	_, _ = w.Write(body.Bytes())
}

const historyCSP = "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

type historyDocument struct {
	Room, Generated                     string
	After, Through, Total               int64
	Events                              []historyCard
	Agents, Chats, Invocations, Results int
}

type historyCard struct {
	Kind, Label, Actor, ActorKind, Time, Type, Text, JSON, Capability, Correlation, Status string
	Seq                                                                                    int64
}

func makeHistoryCard(event protocol.Event) historyCard {
	card := historyCard{Kind: "system", Label: "事件", Actor: event.Actor.ID, ActorKind: event.Actor.Kind, Time: event.TS.UTC().Format(time.RFC3339Nano), Seq: event.Seq, Type: event.Type, Capability: event.CapabilityID, Correlation: event.CorrelationID}
	if card.Actor == "" {
		card.Actor = "未知参与者 / 系统"
	}
	if event.TS.IsZero() {
		card.Time = "时间未记录"
	}
	payload, _ := event.Payload.(map[string]any)
	value := func(key string) string {
		v := payload[key]
		if v == nil {
			return ""
		}
		if s, ok := v.(string); ok {
			return s
		}
		b, _ := json.Marshal(v)
		return string(b)
	}
	if card.Correlation == "" {
		card.Correlation = value("request_id")
	}
	if card.Capability == "" {
		card.Capability = value("capability_id")
	}
	switch event.Type {
	case "chat.message", "chat":
		card.Kind, card.Label, card.Text = "chat", "聊天", value("text")
		if card.Text == "" {
			card.Text = value("message")
		}
		if s, ok := event.Payload.(string); ok {
			card.Text = s
		}
	case "capability.invoke", "capability.invoke.request", "invocation.created":
		card.Kind, card.Label, card.Text = "invoke", "能力调用", "发起能力请求"
		if target := value("target_agent_id"); target != "" {
			card.Text += " → " + target
		}
	case "capability.progress", "capability.invoke.progress", "invocation.progress":
		card.Kind, card.Label, card.Text = "progress", "执行进度", value("message")
		if percent := value("percent"); percent != "" {
			card.Text = percent + "% · " + card.Text
		}
	case "capability.result", "capability.invoke.result", "invocation.completed":
		card.Kind, card.Label, card.Status = "result", "执行结果", value("status")
		card.Text = value("error")
		if card.Text == "" {
			card.Text = value("message")
		}
		if card.Text == "" {
			card.Text = "结果已记录；文件内容保留在 Agent 本地。"
		}
	case "agent.hello":
		card.Label, card.Text = "加入房间", value("name")
	case "capability.announce":
		card.Label, card.Text = "能力声明", "公布可用能力"
	default:
		card.Text = value("text")
		if card.Text == "" {
			card.Text = value("message")
		}
	}
	if card.Text == "" {
		card.Text = "已记录事件，展开查看脱敏数据。"
	}
	b, _ := json.MarshalIndent(event, "", "  ")
	card.JSON = string(b)
	return card
}
