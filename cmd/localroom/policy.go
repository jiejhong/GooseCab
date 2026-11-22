package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jiejhong/GooseCab/internal/humanauth"
	"github.com/jiejhong/GooseCab/internal/protocol"
)

const (
	localScheme     = "GooseCabLocal"
	maxParticipants = 64
)

type localConfig struct {
	Enabled        bool          `json:"enabled"`
	RoomID         string        `json:"room_id"`
	Participants   []participant `json:"participants"`
	AgentTransport bool          `json:"agent_transport"`
}

type participant struct {
	Kind        string    `json:"kind"`
	ID          string    `json:"id"`
	DisplayName string    `json:"display_name"`
	TokenSHA256 string    `json:"token_sha256"`
	ExpiresAt   time.Time `json:"expires_at"`
	Read        bool      `json:"read"`
	Write       bool      `json:"write"`
	Broadcast   bool      `json:"broadcast"`
}

type roomPolicy struct {
	room           string
	agentTransport bool
	humans         *humanauth.Authorizer
	agents         *humanauth.Authorizer
	members        map[protocol.Actor]participant
}

func newRoomPolicy(cfg localConfig) (*roomPolicy, error) {
	if !cfg.Enabled || strings.TrimSpace(cfg.RoomID) == "" || len(cfg.RoomID) > 256 ||
		len(cfg.Participants) == 0 || len(cfg.Participants) > maxParticipants {
		return nil, errors.New("local room requires explicit enablement, one room and 1–64 participants")
	}
	var humans, agents humanauth.Config
	humans.Enabled, agents.Enabled = true, true
	p := &roomPolicy{room: cfg.RoomID, agentTransport: cfg.AgentTransport, members: make(map[protocol.Actor]participant)}
	digests := make(map[string]bool)
	for i, member := range cfg.Participants {
		if (member.Kind != "user" && member.Kind != "agent") ||
			(member.Write && (!member.Read || (member.Kind == "agent" && !cfg.AgentTransport))) ||
			(member.Broadcast && (!member.Write || member.Kind != "user")) {
			return nil, fmt.Errorf("invalid participant role or grants at entry %d", i+1)
		}
		name := strings.TrimSpace(member.DisplayName)
		if len(name) > 128 || !utf8.ValidString(name) || strings.IndexFunc(name, unicode.IsControl) >= 0 {
			return nil, fmt.Errorf("invalid participant display name at entry %d", i+1)
		}
		actor := protocol.Actor{Kind: member.Kind, ID: member.ID}
		digest := strings.ToLower(member.TokenSHA256)
		if _, exists := p.members[actor]; exists || digests[digest] {
			return nil, fmt.Errorf("duplicate participant or credential at entry %d", i+1)
		}
		digests[digest] = true
		credential := humanauth.Credential{
			HumanID: member.ID, TokenSHA256: digest, ExpiresAt: member.ExpiresAt,
			Rooms: map[string]humanauth.RoomGrants{cfg.RoomID: {Read: member.Read, Write: member.Write}},
		}
		if member.Kind == "user" {
			humans.Credentials = append(humans.Credentials, credential)
		} else {
			// Reuse digest and expiry checks in a separate namespace. A result
			// from this verifier is bound to the Agent role by the server.
			agents.Credentials = append(agents.Credentials, credential)
		}
		p.members[actor] = participant{Read: member.Read, Write: member.Write, Broadcast: member.Broadcast, ExpiresAt: member.ExpiresAt, DisplayName: name}
	}
	var err error
	p.humans, err = humanauth.New(humans, humanauth.ReservedDigests{})
	if err != nil {
		return nil, err
	}
	p.agents, err = humanauth.New(agents, humanauth.ReservedDigests{})
	if err != nil {
		return nil, err
	}
	return p, nil
}

func requestAuthorization(r *http.Request) []string {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return nil
	}
	scheme, token, ok := strings.Cut(values[0], " ")
	if !ok || !strings.EqualFold(scheme, localScheme) {
		return nil
	}
	return []string{"Bearer " + token}
}

func (p *roomPolicy) readHuman(auth []string) (protocol.Actor, humanauth.ReadLease, bool) {
	lease, ok := p.humans.AuthorizeRead(auth, p.room)
	if !ok {
		return protocol.Actor{}, humanauth.ReadLease{}, false
	}
	return protocol.Actor{Kind: "user", ID: lease.Principal().HumanID}, lease, true
}

func (p *roomPolicy) readStream(auth []string) (protocol.Actor, humanauth.ReadLease, bool) {
	if actor, lease, ok := p.readHuman(auth); ok {
		return actor, lease, true
	}
	return p.readAgent(auth)
}

func (p *roomPolicy) readAgent(auth []string) (protocol.Actor, humanauth.ReadLease, bool) {
	if lease, ok := p.agents.AuthorizeRead(auth, p.room); ok {
		return protocol.Actor{Kind: "agent", ID: lease.Principal().HumanID}, lease, true
	}
	return protocol.Actor{}, humanauth.ReadLease{}, false
}

func (p *roomPolicy) displayName(actor protocol.Actor) string {
	if name := p.members[actor].DisplayName; name != "" {
		return name
	}
	return actor.ID
}

func (p *roomPolicy) canMention(actor protocol.Actor, now time.Time) bool {
	member, ok := p.members[actor]
	return ok && member.Read && now.Before(member.ExpiresAt)
}

func (p *roomPolicy) canBroadcast(actor protocol.Actor) bool {
	return p.members[actor].Broadcast
}

func (p *roomPolicy) broadcastRecipients(sender protocol.Actor, now time.Time) []protocol.Actor {
	var recipients []protocol.Actor
	for actor, member := range p.members {
		if actor != sender && member.Read && now.Before(member.ExpiresAt) {
			recipients = append(recipients, actor)
		}
	}
	return recipients
}

func (p *roomPolicy) writer(auth []string) (protocol.Actor, bool) {
	principal, ok := p.humans.Authorize(auth, p.room, humanauth.WriteRoom)
	if !ok {
		return protocol.Actor{}, false
	}
	return protocol.Actor{Kind: "user", ID: principal.HumanID}, true
}

// Public room actors use "human". The v2 policy and unread recipient keys
// keep their existing "user" role so outstanding mentions remain readable.
func publicRoomActor(actor protocol.Actor) protocol.Actor {
	if actor.Kind == "user" {
		actor.Kind = "human"
	}
	return actor
}

func decodeJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("expected a single JSON value")
	}
	return nil
}

func readJSONBody(w http.ResponseWriter, r *http.Request, limit int64, target any) int {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return http.StatusUnsupportedMediaType
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return http.StatusRequestEntityTooLarge
	}
	if err != nil || !utf8.Valid(body) || decodeJSON(body, target) != nil {
		return http.StatusBadRequest
	}
	return 0
}
