// Command invoke submits one bounded text task and prints its result as JSON.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jiejhong/GooseCab/internal/agent"
	"github.com/jiejhong/GooseCab/internal/protocol"
)

func main() { os.Exit(run()) }
func run() int {
	url := flag.String("url", "ws://127.0.0.1:8312/api/v1/agent/connect", "Gateway WebSocket URL")
	room := flag.String("room", "demo", "room ID")
	id := flag.String("id", "caller", "caller Agent ID; keep it stable for the same request")
	target := flag.String("target", "", "worker Agent ID (required)")
	capability := flag.String("capability", agent.TextCapability, "bounded text capability ID (built-in or explicitly installed skill)")
	request := flag.String("request-id", "", "stable unique invocation ID (required; reuse only for identical calls)")
	payloadFile := flag.String("payload", "", "local JSON object file (maximum 16 KiB; input is visible in room history)")
	inlineJSON := flag.String("json", "", "JSON object input; do not include secrets (visible in room history and process arguments)")
	timeout := flag.Duration("timeout", 30*time.Second, "overall result wait; timeout never triggers a retry")
	token := flag.String("token", os.Getenv("GOOSECAB_AGENT_TOKEN"), "Gateway bearer token")
	flag.Parse()
	if *request == "" || *target == "" || *id == "" || *timeout <= 0 {
		fmt.Fprintln(os.Stderr, "-request-id, -target, a caller -id and positive -timeout are required")
		return 2
	}
	if (*payloadFile == "") == (*inlineJSON == "") {
		fmt.Fprintln(os.Stderr, "exactly one of -payload or -json is required")
		return 2
	}
	var reader io.Reader = strings.NewReader(*inlineJSON)
	if *payloadFile != "" {
		f, err := os.Open(*payloadFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		defer f.Close()
		reader = f
	}
	raw, err := io.ReadAll(io.LimitReader(reader, protocol.MaxCapabilityPayloadBytes+1))
	if err != nil || len(raw) > protocol.MaxCapabilityPayloadBytes {
		fmt.Fprintln(os.Stderr, "payload exceeds 16 KiB or cannot be read")
		return 2
	}
	var payload any
	if json.Unmarshal(raw, &payload) != nil || protocol.ValidateCapabilityPayload(*capability, payload) != nil {
		fmt.Fprintln(os.Stderr, "invalid bounded capability payload")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	c, err := agent.DialWithTokenContext(ctx, *url, *room, *id, nil, *token)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		return 1
	}
	defer c.Close()
	result, err := c.InvokeCapability(ctx, *request, *capability, *target, payload, func(p protocol.CapabilityProgress) { _ = json.NewEncoder(os.Stderr).Encode(p) })
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err = json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, "write result:", err)
		return 1
	}
	if result.Status != "completed" || !result.ResultAvailable || result.Output == nil {
		return 1
	}
	return 0
}
