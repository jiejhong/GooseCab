// Command agent is a small protocol client for local development and
// deployment smoke checks. It supports a read-only event stream as well as a
// line-oriented interactive mode for publishing chat/events and invoking a
// capability. The client only makes an outbound WebSocket connection.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jiejhong/GooseCab/internal/agent"
)

const (
	modeReadOnly    = "read-only"
	modeInteractive = "interactive"
)

type options struct {
	url          string
	room         string
	id           string
	capabilities string
	token        string
	mode         string
}

func main() {
	opts := options{}
	flag.StringVar(&opts.url, "url", "ws://127.0.0.1:8312/api/v1/agent/connect", "Gateway WebSocket URL (ws:// or wss://; local or remote)")
	flag.StringVar(&opts.room, "room", "default", "room ID")
	flag.StringVar(&opts.id, "id", "", "agent ID (required)")
	flag.StringVar(&opts.capabilities, "capabilities", "", "comma-separated capability IDs announced in agent.hello")
	flag.StringVar(&opts.token, "token", "", "Gateway bearer token (optional; defaults to GOOSECAB_AGENT_TOKEN)")
	flag.StringVar(&opts.mode, "mode", modeReadOnly, "client mode: read-only or interactive")
	flag.Parse()

	if strings.TrimSpace(opts.id) == "" {
		log.Fatal("-id is required")
	}
	mode, err := normalizeMode(opts.mode)
	if err != nil {
		log.Fatal(err)
	}
	if opts.token == "" {
		opts.token = os.Getenv("GOOSECAB_AGENT_TOKEN")
	}

	c, err := agent.DialWithToken(opts.url, opts.room, opts.id, splitCSV(opts.capabilities), opts.token)
	if err != nil {
		log.Fatalf("connect agent %q: %v", opts.id, err)
	}
	defer c.Close()
	log.Printf("agent %s connected to room %s in %s mode; waiting for events", opts.id, opts.room, mode)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		_ = c.Close()
	}()

	if err := run(ctx, c, mode, os.Stdin, os.Stdout, os.Stderr); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}

func normalizeMode(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "read-only", "readonly", "read_only", "read":
		return modeReadOnly, nil
	case modeInteractive, "interact":
		return modeInteractive, nil
	default:
		return "", fmt.Errorf("unsupported -mode %q (want read-only or interactive)", raw)
	}
}

func run(ctx context.Context, c *agent.Client, mode string, in io.Reader, out, errOut io.Writer) error {
	switch mode {
	case modeReadOnly:
		return runReadOnly(ctx, c, out)
	case modeInteractive:
		return runInteractive(ctx, c, in, out, errOut)
	default:
		return fmt.Errorf("unsupported mode %q", mode)
	}
}

func runReadOnly(ctx context.Context, c *agent.Client, out io.Writer) error {
	encoder := json.NewEncoder(out)
	for {
		msg, err := c.ReadWithTimeout(time.Minute)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			return err
		}
		if err := encoder.Encode(msg); err != nil {
			return fmt.Errorf("write event: %w", err)
		}
	}
}

// runInteractive accepts one command per line while a reader goroutine emits
// every event, including capability.progress and capability.result, as JSON.
// Commands are intentionally small and map directly to Client methods:
//
//   chat <text>
//   event <event-type> [json-payload]
//   invoke <request-id> <capability-id> [target-agent-id] [json-payload]
//   help | quit
func runInteractive(ctx context.Context, c *agent.Client, in io.Reader, out, errOut io.Writer) error {
	var outputMu sync.Mutex
	writeJSON := func(msg map[string]any) error {
		outputMu.Lock()
		defer outputMu.Unlock()
		return json.NewEncoder(out).Encode(msg)
	}
	writeErr := func(err error) {
		outputMu.Lock()
		defer outputMu.Unlock()
		_, _ = fmt.Fprintln(errOut, "error:", err)
	}

	type readResult struct {
		msg map[string]any
		err error
	}
	events := make(chan readResult)
	go func() {
		defer close(events)
		for {
			msg, err := c.ReadWithTimeout(time.Minute)
			select {
			case events <- readResult{msg: msg, err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				var netErr net.Error
				if errors.As(err, &netErr) && netErr.Timeout() {
					continue
				}
				return
			}
		}
	}()

	lines := make(chan string)
	go func() {
		scanner := bufio.NewScanner(in)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()

	inputClosed := false
	var drain *time.Timer
	for {
		var drainC <-chan time.Time
		if drain != nil {
			drainC = drain.C
		}
		select {
		case <-ctx.Done():
			if drain != nil {
				drain.Stop()
			}
			return nil
		case result, ok := <-events:
			if !ok {
				if ctx.Err() != nil || inputClosed {
					return nil
				}
				return fmt.Errorf("read agent event: connection closed")
			}
			if result.err != nil {
				if ctx.Err() != nil || (inputClosed && isTimeout(result.err)) {
					continue
				}
				if inputClosed {
					return nil
				}
				return result.err
			}
			if err := writeJSON(result.msg); err != nil {
				return fmt.Errorf("write event: %w", err)
			}
		case line, ok := <-lines:
			if !ok {
				// A pipe commonly ends immediately after a command. Give the
				// Gateway a short window to broadcast that command before closing.
				inputClosed = true
				if drain == nil {
					drain = time.NewTimer(750 * time.Millisecond)
				}
				continue
			}
			if drain != nil {
				drain.Stop()
				drain = nil
			}
			if strings.TrimSpace(line) == "" {
				continue
			}
			quit, err := handleCommand(c, line, writeJSON)
			if err != nil {
				writeErr(err)
			}
			if quit {
				return nil
			}
		case <-drainC:
			return nil
		}
	}
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func handleCommand(c *agent.Client, line string, writeJSON func(map[string]any) error) (bool, error) {
	command, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
	switch strings.ToLower(command) {
	case "help", "?":
		return false, writeJSON(map[string]any{"type": "client.help", "commands": []string{
			"chat <text>", "event <event-type> [json-payload]", "invoke <request-id> <capability-id> [target-agent-id] [json-payload]", "help", "quit",
		}})
	case "quit", "exit":
		return true, c.Close()
	case "chat":
		if strings.TrimSpace(rest) == "" {
			return false, fmt.Errorf("usage: chat <text>")
		}
		return false, c.SendChat(strings.TrimSpace(rest))
	case "event":
		eventType, payloadRaw, _ := strings.Cut(strings.TrimSpace(rest), " ")
		if strings.TrimSpace(eventType) == "" {
			return false, fmt.Errorf("usage: event <event-type> [json-payload]")
		}
		payload, err := parsePayload(payloadRaw)
		if err != nil {
			return false, fmt.Errorf("event payload: %w", err)
		}
		return false, c.SendEvent(eventType, payload)
	case "invoke":
		requestID, remaining, ok := strings.Cut(strings.TrimSpace(rest), " ")
		if !ok {
			return false, fmt.Errorf("usage: invoke <request-id> <capability-id> [target-agent-id] [json-payload]")
		}
		capabilityID, remaining, _ := strings.Cut(strings.TrimSpace(remaining), " ")
		if strings.TrimSpace(requestID) == "" || strings.TrimSpace(capabilityID) == "" {
			return false, fmt.Errorf("usage: invoke <request-id> <capability-id> [target-agent-id] [json-payload]")
		}
		targetID, payloadRaw := parseInvokeTail(strings.TrimSpace(remaining))
		payload, err := parsePayload(payloadRaw)
		if err != nil {
			return false, fmt.Errorf("invoke payload: %w", err)
		}
		return false, c.Invoke(requestID, capabilityID, targetID, payload)
	default:
		return false, fmt.Errorf("unknown command %q (try help)", command)
	}
}

func parseInvokeTail(raw string) (targetID, payloadRaw string) {
	raw = strings.TrimSpace(raw)
	if raw == "" || looksLikeJSON(raw) {
		return "", raw
	}
	targetID, payloadRaw, _ = strings.Cut(raw, " ")
	return strings.TrimSpace(targetID), strings.TrimSpace(payloadRaw)
}

func looksLikeJSON(raw string) bool {
	switch raw[0] {
	case '{', '[', '"':
		return true
	default:
		return raw == "null" || raw == "true" || raw == "false" || (raw[0] >= '0' && raw[0] <= '9') || raw[0] == '-'
	}
}

func parsePayload(raw string) (any, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var payload any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, fmt.Errorf("must be valid JSON: %w", err)
	}
	return payload, nil
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
