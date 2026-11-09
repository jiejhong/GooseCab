// Command agent is a small protocol client for local development and
// deployment smoke checks. It keeps one outbound websocket open and prints
// events as newline-delimited JSON; it does not execute capabilities.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jiejhong/GooseCab/internal/agent"
)

func main() {
	var (
		url          string
		room         string
		id           string
		capabilities string
		token        string
	)
	flag.StringVar(&url, "url", "ws://127.0.0.1:8080/api/v1/agent/connect", "gateway websocket URL")
	flag.StringVar(&room, "room", "default", "room ID")
	flag.StringVar(&id, "id", "", "agent ID (required)")
	flag.StringVar(&capabilities, "capabilities", "", "comma-separated capability IDs")
	flag.StringVar(&token, "token", "", "gateway bearer token (optional)")
	flag.Parse()

	if strings.TrimSpace(id) == "" {
		log.Fatal("-id is required")
	}

	caps := splitCSV(capabilities)
	c, err := agent.DialWithToken(url, room, id, caps, token)
	if err != nil {
		log.Fatalf("connect agent %q: %v", id, err)
	}
	defer c.Close()
	log.Printf("agent %s connected to room %s; waiting for events", id, room)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		_ = c.Close()
	}()

	encoder := json.NewEncoder(os.Stdout)
	for {
		msg, err := c.ReadWithTimeout(time.Minute)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			log.Fatalf("agent %s read: %v", id, err)
		}
		if err := encoder.Encode(msg); err != nil {
			log.Fatalf("write event: %v", err)
		}
	}
}

func splitCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	values := strings.Split(raw, ",")
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}
