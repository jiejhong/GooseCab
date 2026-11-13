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
	root := flag.String("skill-root", "", "existing independently approved local skill store (disabled by default)")
	cap := flag.String("skill-capability", "", "exact approved capability")
	version := flag.Int("skill-version", 0, "exact approved version")
	digest := flag.String("skill-sha256", "", "exact approved archive SHA256")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	skill, err := chatmcp.OpenPinnedSkill(ctx, chatmcp.SkillConfig{Root: *root, Capability: *cap, Version: *version, Digest: *digest})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if skill != nil {
		defer skill.Close()
	}
	backend, err := chatmcp.Connect(ctx, chatmcp.Config{URL: *endpoint, Room: *room, AgentID: *id, Token: os.Getenv("GOOSECAB_AGENT_TOKEN")})
	if err == nil {
		defer backend.Close()
		err = (&chatmcp.Server{Backend: backend, Skill: skill}).Serve(ctx, os.Stdin, os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
