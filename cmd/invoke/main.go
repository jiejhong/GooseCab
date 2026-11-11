// Command invoke submits one bounded text task and prints its result as JSON.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
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
	text := flag.String("text", "", "UTF-8 input text, maximum 4096 bytes; visible in room history")
	timeout := flag.Duration("timeout", 30*time.Second, "overall result wait; timeout never triggers a retry")
	token := flag.String("token", os.Getenv("GOOSECAB_AGENT_TOKEN"), "Gateway bearer token")
	flag.Parse()
	if *request == "" || *target == "" || *id == "" || *timeout <= 0 {
		fmt.Fprintln(os.Stderr, "-request-id, -target, a caller -id and positive -timeout are required")
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
	result, err := c.InvokeTextCapability(ctx, *request, *capability, *target, *text, func(p protocol.CapabilityProgress) { _ = json.NewEncoder(os.Stderr).Encode(p) })
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
