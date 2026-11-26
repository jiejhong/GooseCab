package gateway

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jiejhong/GooseCab/internal/humanauth"
	"github.com/jiejhong/GooseCab/internal/protocol"
)

type RoomChatConfig struct {
	Origin             string         `json:"origin"`
	TrustLoopbackProxy bool           `json:"trust_loopback_proxy"`
	Rooms              []RoomChatRoom `json:"rooms"`
	RequireUnusedRooms bool           `json:"-"`
}

type RoomChatRoom struct {
	ID                              string           `json:"id"`
	ReadOnly                        bool             `json:"read_only"`
	LegacyReadersMayReadNewMessages bool             `json:"legacy_readers_may_read_new_messages,omitempty"`
	TrackMentionReceipts            bool             `json:"track_mention_receipts,omitempty"`
	Humans                          []RoomChatMember `json:"humans"`
	Agents                          []RoomChatMember `json:"agents"`
}

type RoomChatMember struct {
	ID          string    `json:"id"`
	DisplayName string    `json:"display_name"`
	TokenSHA256 string    `json:"token_sha256"`
	ExpiresAt   time.Time `json:"expires_at"`
	Write       bool      `json:"write"`
	Broadcast   bool      `json:"broadcast"`
}

type roomChatPolicy struct {
	origin             *url.URL
	trustLoopbackProxy bool
	rooms              map[string]roomChatGrants
}

type roomChatGrants struct {
	humans, agents *humanauth.Authorizer
	readOnly       bool
	legacyAudience bool
	trackMentions  bool
	members        map[protocol.Actor]RoomChatMember
}

func ReadRoomChatConfig(filename string) (RoomChatConfig, error) {
	var policy RoomChatConfig
	if info, err := os.Lstat(filename); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return policy, errors.New("room chat configuration requires a private regular file")
	}
	f, err := os.Open(filename)
	if err != nil {
		return policy, errors.New("could not open room chat configuration")
	}
	defer f.Close()
	const maxSize = 128 << 10
	data, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil || len(data) > maxSize || decodeRoomChatJSON(strings.NewReader(string(data)), &policy) != nil {
		return RoomChatConfig{}, errors.New("invalid room chat configuration")
	}
	return policy, nil
}

func newRoomChatPolicy(config RoomChatConfig, legacy string) (*roomChatPolicy, error) {
	origin, err := url.Parse(config.Origin)
	if err != nil || origin.Hostname() == "" || origin.User != nil || origin.Path != "" || origin.RawPath != "" ||
		origin.RawQuery != "" || origin.ForceQuery || origin.Fragment != "" || origin.String() != config.Origin ||
		origin.Scheme != "https" {
		return nil, errors.New("protected room origin must be HTTPS")
	}
	if port := origin.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, errors.New("invalid room chat origin port")
		}
	}
	if len(config.Rooms) == 0 || len(config.Rooms) > 64 {
		return nil, errors.New("room chat requires 1–64 configured rooms")
	}
	policy := &roomChatPolicy{origin: origin, trustLoopbackProxy: config.TrustLoopbackProxy, rooms: make(map[string]roomChatGrants)}
	digests := make(map[string]bool)
	reserved := humanauth.ReservedDigests{Shared: sha256.Sum256([]byte(legacy))}
	for i, room := range config.Rooms {
		if _, exists := policy.rooms[room.ID]; exists || (legacy != "" && strings.Contains(room.ID, legacy)) || len(room.Humans) == 0 || len(room.Humans)+len(room.Agents) > 64 ||
			(room.TrackMentionReceipts && room.LegacyReadersMayReadNewMessages) {
			return nil, fmt.Errorf("invalid room chat scope at entry %d", i+1)
		}
		grants := roomChatGrants{readOnly: room.ReadOnly, legacyAudience: room.LegacyReadersMayReadNewMessages, members: make(map[protocol.Actor]RoomChatMember)}
		grants.trackMentions = room.TrackMentionReceipts
		for _, group := range []struct {
			kind    string
			members []RoomChatMember
			target  **humanauth.Authorizer
		}{{"human", room.Humans, &grants.humans}, {"agent", room.Agents, &grants.agents}} {
			auth := humanauth.Config{Enabled: true}
			for j, member := range group.members {
				actor := protocol.Actor{Kind: group.kind, ID: member.ID}
				digest := strings.ToLower(member.TokenSHA256)
				name := strings.TrimSpace(member.DisplayName)
				if _, exists := grants.members[actor]; exists || (legacy != "" && (strings.Contains(member.ID, legacy) || strings.Contains(name, legacy))) || digests[digest] || len(name) > 128 || !utf8.ValidString(name) ||
					strings.IndexFunc(name, unicode.IsControl) >= 0 ||
					(member.Broadcast && (!member.Write || group.kind == "agent" || room.LegacyReadersMayReadNewMessages)) {
					return nil, fmt.Errorf("invalid room chat member at room %d entry %d", i+1, j+1)
				}
				digests[digest] = true
				auth.Credentials = append(auth.Credentials, humanauth.Credential{
					HumanID: member.ID, TokenSHA256: digest, ExpiresAt: member.ExpiresAt,
					Rooms: map[string]humanauth.RoomGrants{room.ID: {Read: true, Write: member.Write}},
				})
				grants.members[actor] = RoomChatMember{ID: member.ID, DisplayName: name, ExpiresAt: member.ExpiresAt,
					Write: member.Write, Broadcast: member.Broadcast}
			}
			*group.target, err = humanauth.New(auth, reserved)
			if err != nil {
				return nil, fmt.Errorf("invalid room chat credentials at room %d: %w", i+1, err)
			}
		}
		policy.rooms[room.ID] = grants
	}
	return policy, nil
}

func (p *roomChatPolicy) protectedTransport(r *http.Request) bool {
	if r.Host != p.origin.Host || r.URL.IsAbs() {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	peer := net.ParseIP(host)
	loopback := err == nil && peer != nil && peer.IsLoopback()
	if r.TLS != nil {
		return true
	}
	proto := r.Header.Values("X-Forwarded-Proto")
	local, ok := r.Context().Value(http.LocalAddrContextKey).(*net.TCPAddr)
	return p.trustLoopbackProxy && loopback && ok && local.IP.IsLoopback() && len(proto) == 1 && proto[0] == "https"
}

func roomChatAuthorization(r *http.Request, credentialScheme string) (string, []string) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return "", nil
	}
	scheme, token, ok := strings.Cut(values[0], " ")
	if !ok || scheme != credentialScheme {
		return "", nil
	}
	return token, []string{"Bearer " + token}
}

func decodeRoomChatJSON(r io.Reader, target any) error {
	d := json.NewDecoder(r)
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("expected a single JSON object")
	}
	return nil
}
