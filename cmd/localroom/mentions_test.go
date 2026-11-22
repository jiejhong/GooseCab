package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/jiejhong/GooseCab/internal/protocol"
)

func fetchMentions(t *testing.T, endpoint, token, before string) mentionPage {
	t.Helper()
	path := endpoint + "/api/mentions"
	if before != "" {
		path += "?before=" + before
	}
	req, err := http.NewRequest(http.MethodGet, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", localScheme+" "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("mention history returned %d", res.StatusCode)
	}
	var page mentionPage
	if err := json.NewDecoder(res.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	return page
}

func markMentions(t *testing.T, endpoint, token string, ids ...string) {
	t.Helper()
	body, err := json.Marshal(map[string][]string{"event_ids": ids})
	if err != nil {
		t.Fatal(err)
	}
	res := sendFixture(t, endpoint, "/api/mentions/read", endpoint, localScheme+" "+token, "application/json", string(body))
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("marking mentions returned %d", res.StatusCode)
	}
}

func postMentionFixture(t *testing.T, endpoint, token, body string, want int) protocol.Event {
	t.Helper()
	res := sendFixture(t, endpoint, "/api/messages", endpoint, localScheme+" "+token, "application/json", body)
	defer res.Body.Close()
	if res.StatusCode != want {
		t.Fatalf("message returned %d, want %d", res.StatusCode, want)
	}
	var event protocol.Event
	if want == http.StatusCreated {
		if err := json.NewDecoder(res.Body).Decode(&event); err != nil {
			t.Fatal(err)
		}
	}
	return event
}

func TestBroadcastGrantAndSeparateRecipientReceipts(t *testing.T) {
	now := time.Now()
	expiry := now.Add(time.Minute)
	sender, ordinary, reader, agent := fixtureToken(70), fixtureToken(71), fixtureToken(72), fixtureToken(73)
	broadcaster := fixtureParticipant("user", "broadcaster", sender, expiry, true, true)
	broadcaster.Broadcast = true
	srv, _ := fixtureServer(t, broadcaster,
		fixtureParticipant("user", "ordinary", ordinary, expiry, true, true),
		fixtureParticipant("user", "same-id", reader, expiry, true, false),
		fixtureParticipant("agent", "same-id", agent, expiry, true, false),
		fixtureParticipant("user", "expired", fixtureToken(74), now.Add(-time.Minute), true, false),
		fixtureParticipant("agent", "no-read", fixtureToken(75), expiry, false, false),
	)
	const all = `{"text":"fixture room","mention_all":true}`
	postMentionFixture(t, srv.URL, ordinary, all, http.StatusForbidden)
	postMentionFixture(t, srv.URL, sender, `{"text":"both","mention_all":true,"mention":{"kind":"user","id":"same-id"}}`, http.StatusBadRequest)
	event := postMentionFixture(t, srv.URL, sender, all, http.StatusCreated)
	if event.Seq != 1 || event.Payload.(map[string]any)["mention_all"] != true {
		t.Fatal("broadcast lost its structure or a rejected message was persisted")
	}
	for _, token := range []string{ordinary, reader, agent} {
		page := fetchMentions(t, srv.URL, token, "")
		if page.UnreadCount != 1 || len(page.Events) != 1 || page.Events[0].EventID != event.EventID {
			t.Fatal("eligible recipient did not get its own receipt")
		}
	}
	if page := fetchMentions(t, srv.URL, sender, ""); page.UnreadCount != 0 {
		t.Fatal("broadcast included the sender")
	}
	for _, token := range []string{fixtureToken(74), fixtureToken(75)} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/mentions", nil)
		req.Header.Set("Authorization", localScheme+" "+token)
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("ungranted mentions read returned %d", res.StatusCode)
		}
	}
	markMentions(t, srv.URL, reader, event.EventID)
	if fetchMentions(t, srv.URL, reader, "").UnreadCount != 0 ||
		fetchMentions(t, srv.URL, agent, "").UnreadCount != 1 {
		t.Fatal("marking read for a human affected a same-ID Agent")
	}
	denied := sendFixture(t, srv.URL, "/api/mentions/read", "http://127.0.0.1:1",
		localScheme+" "+agent, "application/json", `{"event_ids":["`+event.EventID+`"]}`)
	_ = denied.Body.Close()
	if denied.StatusCode != http.StatusForbidden || fetchMentions(t, srv.URL, agent, "").UnreadCount != 1 {
		t.Fatal("cross-origin acknowledgement changed unread state")
	}

	denied = sendFixture(t, srv.URL, "/api/mentions/read", srv.URL,
		localScheme+" "+agent, "application/json", `{"event_ids":["`+event.EventID+`"]}`)
	_ = denied.Body.Close()
	markMentions(t, srv.URL, reader, event.EventID)
	if denied.StatusCode != http.StatusUnauthorized || fetchMentions(t, srv.URL, reader, "").UnreadCount != 0 ||
		fetchMentions(t, srv.URL, agent, "").UnreadCount != 1 ||
		fetchMentions(t, srv.URL, ordinary, "").UnreadCount != 1 {
		t.Fatal("acknowledgement was not isolated, idempotent and human-only on HTTP")
	}
}

func TestUnreadPagingOldTabsAndSameSequenceInOtherRoom(t *testing.T) {
	expiry := time.Now().Add(time.Minute)
	token, otherToken := fixtureToken(76), fixtureToken(77)
	actor := protocol.Actor{Kind: "user", ID: "reader"}
	srv, room := fixtureServer(t, fixtureParticipant(actor.Kind, actor.ID, token, expiry, true, false))
	appendUnread := func(handler *roomServer, prefix string, n int) protocol.Event {
		t.Helper()
		event := fixtureEvent()
		event.RoomID = handler.policy.room
		event.EventID = fmt.Sprintf("%s-%03d", prefix, n)
		event.Payload = chatSubmission{Text: "synthetic direct mention", Mention: &actor}
		handler.mu.Lock()
		defer handler.mu.Unlock()
		committed, err := handler.db.append(event, []protocol.Actor{actor})
		if err != nil {
			t.Fatal(err)
		}
		return committed
	}
	for i := 1; i <= mentionPageSize+2; i++ {
		appendUnread(room, "first", i)
	}
	first := fetchMentions(t, srv.URL, token, "")
	if first.UnreadCount != 22 || len(first.Events) != mentionPageSize || first.NextBefore == "" {
		t.Fatal("first unread page did not page")
	}
	older := fetchMentions(t, srv.URL, token, first.NextBefore)
	if len(older.Events) != 2 || older.UnreadCount != 22 || older.NextBefore != "" ||
		older.Events[1].Seq != 1 {
		t.Fatal("older unread page lost events")
	}

	// Simulate one tab acknowledging what it rendered, while another tab
	// acknowledges one of those events and a new mention arrives.
	markMentions(t, srv.URL, token, older.Events[0].EventID)
	newest := appendUnread(room, "first", 23)
	markMentions(t, srv.URL, token, older.Events[0].EventID, older.Events[1].EventID)
	after := fetchMentions(t, srv.URL, token, "")
	if after.UnreadCount != 21 || after.Events[0].EventID != newest.EventID {
		t.Fatal("old tab acknowledgement cleared a newer unseen mention")
	}
	// Acknowledge the pagination anchor, then use it again. The cursor
	// identifies an immutable event, even after its unread item is gone.
	markMentions(t, srv.URL, token, first.NextBefore)
	if page := fetchMentions(t, srv.URL, token, first.NextBefore); len(page.Events) != 0 || page.UnreadCount != 20 {
		t.Fatal("read receipt was used instead of a stable paging boundary")
	}
	for _, event := range first.Events[:2] {
		if !slices.ContainsFunc(after.Events, func(got protocol.Event) bool { return event.EventID == got.EventID }) {
			t.Fatal("paging or acknowledging older events cleared newer events")
		}
	}

	another, other := fixtureRoomServer(t, "another-room", fixtureParticipant(actor.Kind, actor.ID, otherToken, expiry, true, false))
	sameSequence := appendUnread(other, "second", 1)
	if sameSequence.Seq != older.Events[1].Seq {
		t.Fatal("fixture sequences are not equal")
	}
	markMentions(t, another.URL, otherToken, older.Events[1].EventID)
	page := fetchMentions(t, another.URL, otherToken, "")
	if page.UnreadCount != 1 || page.Events[0].EventID != sameSequence.EventID {
		t.Fatal("same sequence in another room was acknowledged")
	}
	denied := sendFixture(t, another.URL, "/api/mentions/read", another.URL, localScheme+" "+token,
		"application/json", `{"event_ids":["`+sameSequence.EventID+`"]}`)
	_ = denied.Body.Close()
	if denied.StatusCode != http.StatusUnauthorized {
		t.Fatal("cross-room credential could acknowledge a mention")
	}
}

func TestFullRoomRejectsNextWriteAndPreservesHistory(t *testing.T) {
	expiry := time.Now().Add(time.Minute)
	sender, other := fixtureToken(78), fixtureToken(79)
	recipient := protocol.Actor{Kind: "user", ID: "other"}
	srv, handler := fixtureServer(t,
		fixtureParticipant("user", "sender", sender, expiry, true, true),
		fixtureParticipant(recipient.Kind, recipient.ID, other, expiry, true, true),
	)

	handler.mu.Lock()
	next := handler.db.state
	for i := 1; i < maxEvents; i++ {
		event := fixtureEvent()
		event.EventID = fmt.Sprintf("full-%03d", i)
		event.Seq = int64(i)
		next.Events = append(next.Events, event)
	}
	err := handler.db.commit(next)
	handler.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	sub := handler.bus.SubscribeRoom(testRoom, 2)
	defer handler.bus.Unsubscribe(sub)
	last := postMentionFixture(t, srv.URL, sender, `{"text":"last slot","mention":{"kind":"user","id":"other"}}`, http.StatusCreated)
	if last.Seq != maxEvents {
		t.Fatalf("final available sequence is %d", last.Seq)
	}
	if published := <-sub; published.EventID != last.EventID {
		t.Fatal("final stored event was not published")
	}
	path := filepath.Join(handler.db.dir, dbFile)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	postMentionFixture(t, srv.URL, other, `{"text":"over capacity"}`, http.StatusConflict)
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatal("full-room rejection modified persisted history")
	}
	select {
	case <-sub:
		t.Fatal("rejected write was broadcast")
	default:
	}
	if page := fetchMentions(t, srv.URL, other, ""); page.UnreadCount != 1 {
		t.Fatal("full room stopped serving unread receipts")
	}
	markMentions(t, srv.URL, other, last.EventID)
	var history []protocol.Event
	var anchor string
	for {
		page := getRoomHistory(t, srv.URL, other, anchor, http.StatusOK)
		history = append(history, page.Events...)
		anchor = page.NextBefore
		if anchor == "" {
			break
		}
		if len(history) >= maxEvents {
			t.Fatal("history pagination repeated events")
		}
	}
	if len(history) != maxEvents || history[0].EventID != last.EventID ||
		history[maxEvents-1].EventID != "full-001" {
		t.Fatal("readable history was overwritten or evicted")
	}
}
