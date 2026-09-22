package ports

import "context"

type delegationContextKey struct{}

// Only the adapter's native agent mapping supplies this canonical scope.
// It is carried separately from model tool arguments across TaskTool RPC.
func WithToolDelegation(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, delegationContextKey{}, id)
}
func ToolDelegation(ctx context.Context) string {
	id, _ := ctx.Value(delegationContextKey{}).(string)
	return id
}
