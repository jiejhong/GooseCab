// chat-mcp is a fixed-identity MCP adapter. Stdout is reserved for JSON-RPC.
package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/jiejhong/GooseCab/internal/chatmcp"
	"os"
	"os/signal"
)

func main() {
	endpoint := flag.String("url", "", "ws(s)://host/api/v1/agent/connect")
	room := flag.String("room", "", "fixed room")
	id := flag.String("agent", "", "fixed agent ID")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	backend, err := chatmcp.Connect(ctx, chatmcp.Config{URL: *endpoint, Room: *room, AgentID: *id, Token: os.Getenv("GOOSECAB_AGENT_TOKEN")})
	if err == nil {
		defer backend.Close()
		err = (&chatmcp.Server{Backend: backend}).Serve(ctx, os.Stdin, os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
