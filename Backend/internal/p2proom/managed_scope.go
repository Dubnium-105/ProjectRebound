package p2proom

import "context"

// ManagedAttemptScope is attached only by the authoritative MatchAttempt
// transport service. P2P VNT operations validate it inside their transaction,
// so an attempt abort or route change cannot race a secret issuance or state
// mutation after an earlier HTTP read.
type ManagedAttemptScope struct {
	AttemptID       string
	PlayerID        string
	RosterRevision  int64
	RouteGeneration int
	RequiredRole    string
}

type managedAttemptScopeKey struct{}

func WithManagedAttemptScope(ctx context.Context, scope ManagedAttemptScope) context.Context {
	return context.WithValue(ctx, managedAttemptScopeKey{}, scope)
}

func managedAttemptScopeFromContext(ctx context.Context) (ManagedAttemptScope, bool) {
	scope, ok := ctx.Value(managedAttemptScopeKey{}).(ManagedAttemptScope)
	return scope, ok
}
