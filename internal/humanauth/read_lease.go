package humanauth

import (
	"context"
	"time"
)

// ReadLease represents one authorized room read with a fixed credential expiry.
// A zero value is already expired.
type ReadLease struct {
	principal Principal
	expiresAt time.Time
}

// AuthorizeRead obtains a lease from the same room grant used by Authorize.
func (a *Authorizer) AuthorizeRead(authorization []string, room string) (ReadLease, bool) {
	c, ok := a.authorizeEntryAt(authorization, room, ReadRoom, time.Now())
	if !ok {
		return ReadLease{}, false
	}
	return ReadLease{principal: Principal{HumanID: c.humanID}, expiresAt: c.expiresAt}, true
}

func (l ReadLease) Principal() Principal { return l.principal }

// Context bounds an operation by both its parent and the credential expiry.
// A caller serving a live socket must also stop writes and close the socket
// when this context finishes.
func (l ReadLease) Context(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithDeadline(parent, l.expiresAt)
}
