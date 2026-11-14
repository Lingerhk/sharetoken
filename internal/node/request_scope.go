package node

import "context"

type requestScopeKey struct{}
type requestSessionKey struct{}

// WithRequestScope passes the authenticated buyer to an upstream locally.
// Adapters must derive an opaque identifier before sending it to a provider.
func WithRequestScope(ctx context.Context, buyer string) context.Context {
	return context.WithValue(ctx, requestScopeKey{}, buyer)
}

func RequestScope(ctx context.Context) string {
	value, _ := ctx.Value(requestScopeKey{}).(string)
	return value
}

func WithRequestSession(ctx context.Context, session string) context.Context {
	return context.WithValue(ctx, requestSessionKey{}, session)
}

func RequestSession(ctx context.Context) string {
	value, _ := ctx.Value(requestSessionKey{}).(string)
	return value
}
