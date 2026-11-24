// Package localtransport implements the native, loopback-only Agent transport.
package localtransport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jiejhong/GooseCab/internal/protocol"
)

func Dial(ctx context.Context, raw, room, id, token string) (*websocket.Conn, error) {
	return dial(ctx, raw, room, id, token, "", false)
}

func dial(ctx context.Context, raw, room, id, token, mode string, gateway bool) (*websocket.Conn, error) {
	u, err := url.Parse(raw)
	path, scheme := "/api/v1/agent/connect", "GooseCabLocal"
	if gateway {
		path, scheme = "/api/room-chat/agent", "GooseCabRoom"
	}
	if err != nil || u.Hostname() == "" ||
		(!(gateway && u.Scheme == "wss") && !(u.Scheme == "ws" && u.Hostname() == "127.0.0.1" && u.Port() != "")) ||
		u.User != nil || u.Path != path || u.RawPath != "" || u.RawQuery != "" ||
		u.ForceQuery || u.Fragment != "" || token == "" || strings.TrimSpace(room) == "" || strings.TrimSpace(id) == "" {
		return nil, errors.New("room transport requires its exact protected endpoint and an independent credential")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	u.RawQuery = url.Values{"room_id": {room}}.Encode()
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, response, err := dialer.DialContext(ctx, u.String(), http.Header{"Authorization": {scheme + " " + token}})
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, errors.New("isolated Agent connection rejected or unavailable")
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	success := false
	defer func() {
		if !success {
			_ = conn.Close()
		}
	}()
	deadline, _ := ctx.Deadline()
	if err := conn.SetWriteDeadline(deadline); err != nil {
		return nil, err
	}
	if err := conn.SetReadDeadline(deadline); err != nil {
		return nil, err
	}
	conn.SetReadLimit(protocol.LocalRoomPageBytes)
	hello := map[string]string{"type": "agent.hello", "agent_id": id}
	if mode != "" {
		hello["mode"] = mode
	}
	if err := conn.WriteJSON(hello); err != nil {
		return nil, errors.New("isolated Agent hello failed")
	}
	messageType, rawReply, err := conn.ReadMessage()
	var reply struct {
		Type  string `json:"type"`
		Room  string `json:"room_id"`
		Agent string `json:"agent_id"`
		Mode  string `json:"mode"`
	}
	if err != nil || messageType != websocket.TextMessage || json.Unmarshal(rawReply, &reply) != nil ||
		reply.Type != "agent.welcome" || reply.Room != room || reply.Agent != id || reply.Mode != mode {
		return nil, errors.New("isolated Agent welcome failed to confirm identity, room and mode")
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	success = true
	return conn, nil
}
