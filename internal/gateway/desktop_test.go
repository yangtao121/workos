package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/gateway/auth/domain"
	authtransport "github.com/yangtao121/workos/internal/gateway/auth/transport"
	"github.com/yangtao121/workos/internal/platform/config"
	"github.com/yangtao121/workos/internal/platform/identity"
)

type revokingStreamStore struct {
	*gateStore
	calls atomic.Int32
}

func (s *revokingStreamStore) ResolveSession(_ context.Context, hash string) (domain.DeviceSession, domain.Device, error) {
	if s.calls.Add(1) > 1 {
		return domain.DeviceSession{}, domain.Device{}, domain.ErrAuthenticationFailed
	}
	if hash != s.session.TokenHash {
		return domain.DeviceSession{}, domain.Device{}, domain.ErrAuthenticationFailed
	}
	return s.session, s.device, nil
}
func TestSharedPersistentStreamsRevalidateDevice(t *testing.T) {
	for _, path := range []string{"/workos.desktop.v1.DesktopService/WatchDesktop", "/workos.agent.v1.AgentSessionService/WatchSessionEvents"} {
		t.Run(path, func(t *testing.T) {
			stopped := make(chan struct{})
			core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get(identity.UserHeader) != testOwnerID || r.Header.Get(identity.DeviceHeader) != testDeviceID {
					t.Error("untrusted proxy identity")
				}
				w.Header().Set("Content-Type", "application/connect+json")
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				close(stopped)
			}))
			defer core.Close()
			store := &revokingStreamStore{gateStore: newGateStore(true)}
			handler, err := New(config.Config{Services: config.URLs{Core: core.URL, Runtime: "http://127.0.0.1:1"}, Auth: config.Auth{OwnerID: testOwnerID, PublicOrigin: testOrigin}}, newTestLogger(), newTestAuthStack(t, store))
			if err != nil {
				t.Fatal(err)
			}
			handler.streamRevalidation = 5 * time.Millisecond
			request := httptest.NewRequest(http.MethodPost, testOrigin+path, nil)
			request.Header.Set("Origin", testOrigin)
			request.AddCookie(&http.Cookie{Name: authtransport.SessionCookieName, Value: testSessionToken})
			request.Header.Set(identity.UserHeader, "spoofed")
			ctx, cancel := context.WithTimeout(request.Context(), time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer func() {
					if p := recover(); p != nil && p != http.ErrAbortHandler {
						panic(p)
					}
				}()
				handler.ServeHTTP(httptest.NewRecorder(), request.WithContext(ctx))
			}()
			select {
			case <-stopped:
			case <-ctx.Done():
				t.Fatal("revoked stream stayed open")
			}
			<-done
			if store.calls.Load() < 2 {
				t.Fatal("stream did not revalidate")
			}
		})
	}
}
func TestDesktopPublicAllowlistDoesNotExposePrivateNamespace(t *testing.T) {
	for _, p := range []string{"/workos.desktop.v1.DesktopService/GetDesktop", "/workos.desktop.v1.DesktopService/ApplyDesktopOperation", "/workos.desktop.v1.DesktopService/WatchDesktop"} {
		if !publicConnectPath(p) {
			t.Fatal("desktop inaccessible", p)
		}
	}
	for _, p := range []string{"/workos.desktop.v1.DesktopAdminService/PutState", "/workos.desktop.v1.DesktopServiceInternal/GetDesktop", "/workos.desktop.v1/PutState"} {
		if publicConnectPath(p) {
			t.Fatal("private namespace exposed", p)
		}
	}
}
