package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jiejhong/GooseCab/internal/eventbus"
	"github.com/jiejhong/GooseCab/internal/gateway"
	"github.com/jiejhong/GooseCab/internal/p1"
	"github.com/jiejhong/GooseCab/internal/p2"
	"github.com/jiejhong/GooseCab/internal/registry"
	"github.com/jiejhong/GooseCab/internal/relay"
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
	roomChatFile := flag.String("room-chat-config", "", "private policy file for opt-in Gateway room chat")
	roomChatCert := flag.String("room-chat-tls-cert", "", "certificate chain for HTTPS on the same port as existing HTTP")
	roomChatKey := flag.String("room-chat-tls-key", "", "private key for protected HTTPS on the existing port")
	demoWriteFile := flag.String("demo-write-config", "", "private policy for the allowlisted demo writer")
	flag.Parse()
	demo, err := readDemoWriteConfig(*demoWriteFile)
	if err != nil {
		log.Fatal(err)
	}

	relayConfig, err := relayConfigFromEnv()
	if err != nil {
		log.Fatal(err)
	}

	var roomChat *gateway.RoomChatConfig
	if *roomChatFile != "" {
		config, err := gateway.ReadRoomChatConfig(*roomChatFile)
		if err != nil {
			log.Fatal(err)
		}
		roomChat = &config
	}
	tlsConfig, err := samePortTLS(roomChat, addr, *roomChatCert, *roomChatKey)
	if err != nil {
		log.Fatal(err)
	}
	if roomChat != nil && tlsConfig == nil {
		host, _, err := net.SplitHostPort(addr)
		if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() || !roomChat.TrustLoopbackProxy {
			log.Fatal("protected rooms require same-port TLS or an explicit loopback listener behind a trusted HTTPS proxy")
		}
	}
	var listener net.Listener
	if tlsConfig != nil {
		roomChat.RequireUnusedRooms = true
		listener, err = net.Listen("tcp", addr)
		if err != nil {
			log.Fatal(err)
		}
		defer listener.Close()
	}
	s, err := store.Open(path)
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()
	h, writer, err := assembleGatewayHandlers(s, addr, path, relayConfig, roomChat, demo)
	if err != nil {
		log.Fatal(err)
	}
	if writer != nil {
		socket, err := listenDemoWriteSocket(demo.Socket, demo.IngressUID)
		if err != nil {
			log.Fatal(err)
		}
		defer socket.Close()
		server := &http.Server{Handler: writer, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
		go func() { log.Printf("demo write socket stopped: %v", server.Serve(socket)) }()
	}
	log.Printf("GooseCab gateway listening on %s (database %s)", addr, path)
	if listener != nil {
		server := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, TLSConfig: tlsConfig}
		log.Fatal(server.Serve(newSamePortListener(listener, tlsConfig)))
	}
	log.Fatal(http.ListenAndServe(addr, h))
}

// newServerHandler assembles the production stack with authentication outside
// every route, including status and both WebSocket endpoints.
func newServerHandler(s *store.Store, addr, path string) http.Handler {
	return newServerHandlerWithRelayConfig(s, addr, path, relay.Config{})
}

func newServerHandlerWithRelayConfig(s *store.Store, addr, path string, relayConfig relay.Config) http.Handler {
	h, err := assembleServerHandler(s, addr, path, relayConfig, nil)
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "Gateway configuration unavailable", http.StatusServiceUnavailable)
		})
	}
	return h
}

func assembleServerHandler(s *store.Store, addr, path string, relayConfig relay.Config, config *gateway.RoomChatConfig) (http.Handler, error) {
	handler, _, err := assembleGatewayHandlers(s, addr, path, relayConfig, config, nil)
	return handler, err
}

func assembleGatewayHandlers(s *store.Store, addr, path string, relayConfig relay.Config, config *gateway.RoomChatConfig, demo *demoWriteConfig) (http.Handler, http.Handler, error) {
	api := &gateway.API{Registry: registry.New()}
	bus := eventbus.New()
	ws := &gateway.WS{Store: s, Bus: bus, Registry: api.Registry}
	var writer http.Handler
	if demo != nil {
		if config != nil {
			for _, room := range config.Rooms {
				if room.ID == gateway.DemoWriteRoom {
					return nil, nil, errors.New("the allowlisted demo writer cannot share a room-chat policy for demo")
				}
			}
		}
		var err error
		writer, err = gateway.NewDemoWriter(ws, demo.Origin, os.Getenv("GOOSECAB_AGENT_TOKEN"))
		if err != nil {
			return nil, nil, err
		}
	}
	var chat *gateway.RoomChat
	if config != nil {
		var err error
		chat, err = gateway.NewRoomChat(context.Background(), ws, *config, os.Getenv("GOOSECAB_AGENT_TOKEN"))
		if err != nil {
			return nil, nil, err
		}
	} else if err := s.CheckRoomChatProtection(context.Background(), nil); err != nil {
		return nil, nil, err
	}
	base := http.NewServeMux()
	// The relay is deliberately outside the event/store stack. It only pairs
	// bounded streams; agent-local recipients verify and store the bytes.
	relayHandler := relay.New(relayConfig)
	base.Handle("/api/v1/relay", relayHandler)
	base.Handle("/api/v1/relay/", relayHandler)
	base.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "addr": addr})
	})
	base.HandleFunc("/api/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"service":        "goosecab-gateway",
			"addr":           addr,
			"database":       path,
			"token_required": os.Getenv("GOOSECAB_AGENT_TOKEN") != "",
			"agent_endpoint": websocketEndpoint(addr),
		})
	})
	base.Handle("/api/", api.Handler())

	mux := (&gateway.P1API{Service: p1.New(s), RoomProtected: chat.ProtectedRoom}).Handler(base)
	mux = (&gateway.P2API{Service: p2.New(s, api.Registry)}).Handler(mux)
	mux = (&gateway.TransferAPI{Service: transfer.New(s, bus), RoomProtected: chat.ProtectedRoom}).Handler(mux)
	mux = (&gateway.HistoryAPI{Store: s}).Handler(mux)
	mux = gateway.Observer(mux)
	legacy := gateway.Authenticate(ws.Handler(mux))
	if chat == nil {
		return legacy, writer, nil
	}
	return chat.Handler(legacy), writer, nil
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
