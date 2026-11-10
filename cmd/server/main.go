package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/jiejhong/GooseCab/internal/eventbus"
	"github.com/jiejhong/GooseCab/internal/gateway"
	"github.com/jiejhong/GooseCab/internal/p1"
	"github.com/jiejhong/GooseCab/internal/p2"
	"github.com/jiejhong/GooseCab/internal/registry"
	"github.com/jiejhong/GooseCab/internal/store"
	"github.com/jiejhong/GooseCab/internal/transfer"
)

const defaultAddr = ":8312"

func main() {
	path := os.Getenv("GOOSECAB_DB")
	if path == "" {
		path = "goosecab.db"
	}
	addr := os.Getenv("GOOSECAB_ADDR")
	if addr == "" {
		addr = defaultAddr
	}
	flag.StringVar(&addr, "addr", addr, "HTTP and WebSocket listen address (also GOOSECAB_ADDR)")
	flag.Parse()

	s, err := store.Open(path)
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()

	api := &gateway.API{Registry: registry.New()}
	base := http.NewServeMux()
	base.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "addr": addr})
	})
	base.HandleFunc("/api/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"service":         "goosecab-gateway",
			"addr":            addr,
			"database":        path,
			"token_required":  os.Getenv("GOOSECAB_AGENT_TOKEN") != "",
			"agent_endpoint":  websocketEndpoint(addr),
		})
	})
	base.Handle("/api/", api.Handler())

	mux := (&gateway.P1API{Service: p1.New(s)}).Handler(base)
	mux = (&gateway.P2API{Service: p2.New(s, api.Registry)}).Handler(mux)
	bus := eventbus.New()
	mux = (&gateway.TransferAPI{Service: transfer.New(s, bus)}).Handler(mux)
	mux = gateway.Observer(mux)
	h := (&gateway.WS{Store: s, Bus: bus}).Handler(mux)
	log.Printf("GooseCab gateway listening on %s (database %s)", addr, path)
	log.Fatal(http.ListenAndServe(addr, h))
}

func websocketEndpoint(addr string) string {
	addr = strings.TrimRight(strings.TrimSpace(addr), "/")
	if strings.HasPrefix(addr, ":") {
		// Keep the status value a valid, directly usable URI while making it
		// clear that the server may be bound on another interface.
		addr = "127.0.0.1" + addr
	}
	return "ws://" + addr + "/api/v1/agent/connect"
}
