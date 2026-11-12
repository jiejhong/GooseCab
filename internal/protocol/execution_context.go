package protocol

import "context"

type invocationActorKey struct{}
type invocationActor struct{ room, caller string }

// WithInvocationActor is set by the worker from a verified event envelope,
// never from caller-provided payload fields. IDs still share Gateway trust.
func WithInvocationActor(ctx context.Context, room, caller string) context.Context {
	return context.WithValue(ctx, invocationActorKey{}, invocationActor{room, caller})
}

func InvocationActor(ctx context.Context) (room, caller string, ok bool) {
	v, ok := ctx.Value(invocationActorKey{}).(invocationActor)
	return v.room, v.caller, ok && v.room != "" && v.caller != ""
}
