package identity

import (
	"context"
	"errors"
	"net/http"
)

const (
	UserHeader   = "X-WorkOS-User-ID"
	DeviceHeader = "X-WorkOS-Device-ID"
)

type contextKey struct{}

type Identity struct {
	UserID   string
	DeviceID string
}

func WithContext(ctx context.Context, value Identity) context.Context {
	return context.WithValue(ctx, contextKey{}, value)
}

func FromContext(ctx context.Context) (Identity, error) {
	value, ok := ctx.Value(contextKey{}).(Identity)
	if !ok || value.UserID == "" || value.DeviceID == "" {
		return Identity{}, errors.New("workos identity is missing")
	}
	return value, nil
}

// Middleware trusts identity headers only on private service listeners.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := Identity{UserID: r.Header.Get(UserHeader), DeviceID: r.Header.Get(DeviceHeader)}
		next.ServeHTTP(w, r.WithContext(WithContext(r.Context(), id)))
	})
}
