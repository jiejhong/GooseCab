// chat-mcp is a fixed-identity MCP adapter. Stdout is reserved for JSON-RPC.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/jiejhong/GooseCab/internal/chatmcp"
)

func main() {
	endpoint := flag.String("url", "", "ws(s)://host/api/v1/agent/connect")
	room := flag.String("room", "", "fixed room")
	id := flag.String("agent", "", "fixed agent ID")
	localRoom := flag.Bool("local-room", false, "use room_read with an isolated loopback Agent")
	root := flag.String("skill-root", "", "existing independently approved local skill store (disabled by default)")
	cap := flag.String("skill-capability", "", "exact approved capability")
	version := flag.Int("skill-version", 0, "exact approved version")
	digest := flag.String("skill-sha256", "", "exact approved archive SHA256")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *localRoom {
		if *root != "" || *cap != "" || *version != 0 || *digest != "" {
			fmt.Fprintln(os.Stderr, "isolated room_read cannot enable a pinned skill")
			os.Exit(1)
		}
		if err := serveLocalRoom(ctx, *endpoint, *room, *id); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
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
		server := &chatmcp.Server{Backend: backend}
		if skill != nil {
			server.Skill = skill
		}
		err = server.Serve(ctx, os.Stdin, os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func serveLocalRoom(ctx context.Context, endpoint, room, id string) error {
	reader, err := chatmcp.ConnectLocalRoom(ctx, endpoint, room, id, os.Getenv("GOOSECAB_LOCALROOM_AGENT_TOKEN"))
	if err != nil {
		return err
	}
	defer reader.Close()
	return (&chatmcp.Server{LocalRoom: reader}).Serve(ctx, os.Stdin, os.Stdout)
}
