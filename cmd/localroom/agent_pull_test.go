package main

import (
	"testing"
	"time"

	"github.com/jiejhong/GooseCab/internal/protocol"
)

func TestAgentPageUsesExactEventsWhenSequenceTies(t *testing.T) {
	actor := protocol.Actor{Kind: "agent", ID: "worker"}
	s := &roomServer{policy: &roomPolicy{room: testRoom}, db: &roomDB{}}
	for _, id := range []string{"first", "tied", "next"} {
		event := fixtureEvent()
		event.EventID, event.Seq, event.TS = id, 7, time.Now()
		s.db.state.Events = append(s.db.state.Events, event)
	}
	s.db.state.Unread = map[string][]protocol.Actor{"first": {actor}, "tied": {actor}, "next": {actor}}
	first, code := s.agentReadPage(actor, protocol.LocalRoomReadQuery{Limit: 1})
	if code != "" || first.Page.NextSeq != 7 || len(first.Receipt.EventIDs) != 1 ||
		first.Receipt.EventIDs[0] != "first" {
		t.Fatal("first page confirmed a same-sequence event it did not contain")
	}
	second, code := s.agentReadPage(actor, protocol.LocalRoomReadQuery{AfterSeq: 7, AfterCursor: first.Page.NextCursor, Limit: 1})
	if code != "" || len(second.Receipt.EventIDs) != 1 || second.Receipt.EventIDs[0] != "tied" {
		t.Fatal("row cursor did not preserve events tied at the current sequence")
	}
	skipped, code := s.agentReadPage(actor, protocol.LocalRoomReadQuery{AfterSeq: 7, Limit: 1})
	if code != "" || len(skipped.Page.Events) != 0 || skipped.Receipt != nil {
		t.Fatal("a sequence-only request confirmed events it skipped")
	}
}
