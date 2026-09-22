package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/runtime/nativehost/domain"
	"github.com/yangtao121/workos/internal/runtime/nativehost/ports"
)

type connectivityFixture struct{ calls int }

func (f *connectivityFixture) Issue() (ports.Connectivity, error) {
	f.calls++
	return ports.Connectivity{Mode: "relay", RelayOnly: true, ExpiresAt: time.Now().Add(time.Minute)}, nil
}

type connectivityGate struct {
	gateAuthorizer
	epoch int64
}

func (g *connectivityGate) AuthorizeInputGeneration(ctx context.Context, owner, session, device string, epoch int64) error {
	if epoch != g.epoch {
		return domain.ErrControlDenied
	}
	return g.AuthorizeInput(ctx, owner, session, device)
}
func TestConnectivityRequiresCurrentOwnerDeviceEpochAndLiveSession(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	session, err := s.Create(ctx, testOwner, testProject, "connectivity", 800, 600)
	if err != nil {
		t.Fatal(err)
	}
	f := &connectivityFixture{}
	gate := &connectivityGate{gateAuthorizer: gateAuthorizer{allowed: testDevice}, epoch: 7}
	s.WithConnectivity(f).WithControlAuthorization(gate)
	if _, err := s.Connectivity(ctx, testProject, testDevice, session.SessionID, 7); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("foreign owner obtained configuration")
	}
	if _, err := s.Connectivity(ctx, testOwner, testProject, session.SessionID, 7); !errors.Is(err, domain.ErrControlDenied) {
		t.Fatal("observer obtained configuration")
	}
	if _, err := s.Connectivity(ctx, testOwner, testDevice, session.SessionID, 6); !errors.Is(err, domain.ErrControlDenied) {
		t.Fatal("stale control epoch obtained configuration")
	}
	if f.calls != 0 {
		t.Fatal("refused requests minted credentials")
	}
	if _, err := s.Connectivity(ctx, testOwner, testDevice, session.SessionID, 7); err != nil || f.calls != 1 {
		t.Fatalf("current controller: %v", err)
	}
	gate.epoch++
	if _, err := s.Connectivity(ctx, testOwner, testDevice, session.SessionID, 7); !errors.Is(err, domain.ErrControlDenied) {
		t.Fatal("revoked control retained configuration access")
	}
	if _, err := s.Close(ctx, testOwner, session.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Connectivity(ctx, testOwner, testDevice, session.SessionID, 8); !errors.Is(err, domain.ErrEngineUnavailable) {
		t.Fatal("closed session obtained configuration")
	}
	if f.calls != 1 {
		t.Fatal("retired scope minted credentials")
	}
}
