package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/jiejhong/GooseCab/internal/protocol"
)

func getRoomHistory(t *testing.T, endpoint, token, before string, status int) roomHistoryPage {
	t.Helper()
	path := endpoint + "/api/events"
	if before != "" {
		path += "?before=" + url.QueryEscape(before)
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
	if res.StatusCode != status {
		t.Fatalf("history returned %d, want %d", res.StatusCode, status)
	}
	var page roomHistoryPage
	if status == http.StatusOK {
		if err := json.NewDecoder(res.Body).Decode(&page); err != nil {
			t.Fatal(err)
		}
	}
	return page
}

func TestHistoryBoundIdentityAndStablePaging(t *testing.T) {
	expires := time.Now().Add(time.Minute)
	token := fixtureToken(91)
	actor := protocol.Actor{Kind: "user", ID: "real-account"}
	member := fixtureParticipant(actor.Kind, actor.ID, token, expires, true, true)
	member.DisplayName = "合成房主"
	srv, handler := fixtureServer(t, member)
	postMentionFixture(t, srv.URL, token, `{"text":"spoof","user":"fake","name":"fake"}`, http.StatusBadRequest)
	postMentionFixture(t, srv.URL, token, `{"text":"spoof","author_display_name":"fake"}`, http.StatusBadRequest)
	posted := postMentionFixture(t, srv.URL, token, `{"text":"fixture"}`, http.StatusCreated)
	publicActor := protocol.Actor{Kind: "human", ID: actor.ID}
	if posted.Actor != publicActor || posted.Payload.(map[string]any)["author_display_name"] != member.DisplayName {
		t.Fatal("receipt did not bind author and name to the trusted identity")
	}

	appendEvent := func(id string) {
		t.Helper()
		handler.mu.Lock()
		defer handler.mu.Unlock()
		event := fixtureEvent()
		event.EventID = id
		if _, err := handler.db.append(event, []protocol.Actor{actor}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 2; i <= 43; i++ {
		appendEvent(fmt.Sprintf("history-%02d", i))
	}

	first := getRoomHistory(t, srv.URL, token, "", http.StatusOK)
	if first.DisplayName != member.DisplayName || first.Actor != publicActor || len(first.Events) != historyPageSize ||
		first.Events[0].Seq != 43 || first.NextBefore == "" {
		t.Fatalf("unexpected initial page %+v", first)
	}
	appendEvent("history-new")
	seq := int64(43)
	for page := first; ; page = getRoomHistory(t, srv.URL, token, page.NextBefore, http.StatusOK) {
		for _, event := range page.Events {
			if event.Actor.Kind != "human" {
				t.Fatal("history page returned a noncanonical human author")
			}
			if event.Seq != seq {
				t.Fatalf("history repeated or lost an event: got %d, want %d", event.Seq, seq)
			}
			seq--
		}
		if page.NextBefore == "" {
			break
		}
	}
	if seq != 0 || fetchMentions(t, srv.URL, token, "").UnreadCount != 43 {
		t.Fatal("history paging dropped an event or changed unread")
	}
	getRoomHistory(t, srv.URL, token, first.Events[len(first.Events)-1].EventID, http.StatusBadRequest)
	otherToken := fixtureToken(92)
	other, _ := fixtureRoomServer(t, "different-room", fixtureParticipant(actor.Kind, actor.ID, otherToken, expires, true, true))
	getRoomHistory(t, other.URL, otherToken, first.NextBefore, http.StatusBadRequest)
}
