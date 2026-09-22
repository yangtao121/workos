package xvfbengine

import (
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/yangtao121/workos/internal/runtime/nativehost/ports"
)

type connectivityIssuer struct{ value ports.Connectivity }

func (i connectivityIssuer) Issue() (ports.Connectivity, error) { return i.value, nil }

func TestRelayPolicyCannotFallBackToHostCandidates(t *testing.T) {
	e := &Engine{Candidates: CandidatesRelay}
	if _, err := e.peerConfiguration(); err == nil {
		t.Fatal("relay without credentials admitted")
	}
	valid := ports.Connectivity{RelayOnly: true, ExpiresAt: time.Now().Add(time.Minute), Servers: []ports.IceServer{{URLs: []string{"turn:relay.fixture:3478"}, Username: "ephemeral", Credential: "fixture"}}}
	e.WithConnectivity(connectivityIssuer{valid})
	configuration, err := e.peerConfiguration()
	if err != nil || configuration.ICETransportPolicy != webrtc.ICETransportPolicyRelay || len(configuration.ICEServers) != 1 {
		t.Fatal("relay policy widened")
	}
	valid.RelayOnly = false
	e.WithConnectivity(connectivityIssuer{valid})
	if _, err := e.peerConfiguration(); err == nil {
		t.Fatal("issuer widened relay policy")
	}
	valid.RelayOnly = true
	valid.ExpiresAt = time.Now().Add(-time.Second)
	e.WithConnectivity(connectivityIssuer{valid})
	if _, err := e.peerConfiguration(); err == nil {
		t.Fatal("expired capability admitted")
	}
}
