package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/jiejhong/GooseCab/internal/eventbus"
	"github.com/jiejhong/GooseCab/internal/protocol"
)

func main() {
	bus := eventbus.New()
	var seq int64
	http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	http.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		evt := protocol.Event{EventID: "evt-bootstrap", RoomID: r.URL.Query().Get("room_id"), Seq: atomic.AddInt64(&seq, 1), Type: "room.ready", Actor: protocol.Actor{Kind: "system", ID: "gateway"}, TS: time.Now().UTC()}
		bus.Publish(evt)
		_ = json.NewEncoder(w).Encode(evt)
	})
	log.Println("GooseCab gateway listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
