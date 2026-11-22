// Package humanauth verifies explicitly configured human credentials for a room.
package humanauth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"maps"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const tokenBytes = 32

type Config struct {
	Enabled     bool         `json:"enabled"`
	Credentials []Credential `json:"credentials"`
}

type Credential struct {
	HumanID     string                `json:"human_id"`
	TokenSHA256 string                `json:"token_sha256"`
	ExpiresAt   time.Time             `json:"expires_at"`
	Rooms       map[string]RoomGrants `json:"rooms"`
}

type RoomGrants struct {
	Read  bool `json:"read"`
	Write bool `json:"write"`
}

// ReservedDigests holds the SHA-256 digests of the legacy bearer values.
// Use a zero value for a credential that is not configured.
type ReservedDigests struct {
	Shared   [sha256.Size]byte
	Observer [sha256.Size]byte
}

type Permission string

const (
	ReadRoom  Permission = "read"
	WriteRoom Permission = "write"
)

type Principal struct {
	HumanID string
}

type Authorizer struct {
	credentials map[[sha256.Size]byte]entry
}

func (Authorizer) String() string     { return "humanauth.Authorizer" }
func (a Authorizer) GoString() string { return a.String() }

type entry struct {
	humanID   string
	expiresAt time.Time
	rooms     map[string]RoomGrants
}

// New validates and copies the enabled policy. A disabled policy grants nothing.
// Reserved digests must correspond to the legacy credentials in the same config.
func New(config Config, reserved ReservedDigests) (*Authorizer, error) {
	if !config.Enabled {
		return &Authorizer{}, nil
	}
	credentials := make(map[[sha256.Size]byte]entry, len(config.Credentials))
	people := make(map[string]struct{}, len(config.Credentials))
	for i, c := range config.Credentials {
		if !validID(c.HumanID) || c.ExpiresAt.IsZero() {
			return nil, fmt.Errorf("human credential %d: valid human ID and expiry required", i+1)
		}
		for room := range c.Rooms {
			if !validID(room) {
				return nil, fmt.Errorf("human credential %d: invalid room ID", i+1)
			}
		}
		var digest [sha256.Size]byte
		if len(c.TokenSHA256) != hex.EncodedLen(len(digest)) {
			return nil, fmt.Errorf("human credential %d: invalid SHA-256 digest", i+1)
		}
		if _, err := hex.Decode(digest[:], []byte(c.TokenSHA256)); err != nil {
			return nil, fmt.Errorf("human credential %d: invalid SHA-256 digest", i+1)
		}
		if digest == reserved.Shared || digest == reserved.Observer {
			return nil, fmt.Errorf("human credential %d: digest is reserved for a legacy bearer", i+1)
		}
		if _, exists := credentials[digest]; exists {
			return nil, fmt.Errorf("human credential %d: duplicate digest", i+1)
		}
		if _, exists := people[c.HumanID]; exists {
			return nil, fmt.Errorf("human credential %d: duplicate human ID", i+1)
		}
		credentials[digest] = entry{humanID: c.HumanID, expiresAt: c.ExpiresAt, rooms: maps.Clone(c.Rooms)}
		people[c.HumanID] = struct{}{}
	}
	return &Authorizer{credentials: credentials}, nil
}

func validID(id string) bool {
	return len(id) <= 256 && utf8.ValidString(id) && strings.TrimSpace(id) != "" &&
		strings.IndexFunc(id, unicode.IsControl) < 0
}

// Authorize checks one explicit Authorization header and one exact room grant.
// The principal comes only from the verified server-side entry. Every denial
// returns the same empty principal and false.
func (a *Authorizer) Authorize(authorization []string, room string, permission Permission) (Principal, bool) {
	return a.authorizeAt(authorization, room, permission, time.Now())
}

func (a *Authorizer) authorizeAt(authorization []string, room string, permission Permission, now time.Time) (Principal, bool) {
	c, ok := a.authorizeEntryAt(authorization, room, permission, now)
	if !ok {
		return Principal{}, false
	}
	return Principal{HumanID: c.humanID}, true
}

func (a *Authorizer) authorizeEntryAt(authorization []string, room string, permission Permission, now time.Time) (entry, bool) {
	if a == nil || len(a.credentials) == 0 || len(authorization) != 1 {
		return entry{}, false
	}
	scheme, token, ok := strings.Cut(authorization[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || len(token) != base64.RawURLEncoding.EncodedLen(tokenBytes) {
		return entry{}, false
	}
	var decoded [tokenBytes]byte
	if n, err := base64.RawURLEncoding.Strict().Decode(decoded[:], []byte(token)); err != nil || n != tokenBytes {
		return entry{}, false
	}
	c, ok := a.credentials[sha256.Sum256([]byte(token))]
	if !ok || !now.Before(c.expiresAt) {
		return entry{}, false
	}
	grant := c.rooms[room]
	switch permission {
	case ReadRoom:
		ok = grant.Read
	case WriteRoom:
		ok = grant.Write
	default:
		ok = false
	}
	if !ok {
		return entry{}, false
	}
	return c, true
}
