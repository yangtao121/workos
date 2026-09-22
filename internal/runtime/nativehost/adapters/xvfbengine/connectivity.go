package xvfbengine

import (
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/yangtao121/workos/internal/runtime/nativehost/domain"
	"github.com/yangtao121/workos/internal/runtime/nativehost/ports"
)

func (e *Engine) WithConnectivity(issuer ports.ConnectivityIssuer) *Engine {
	e.connectivity = issuer
	return e
}

func (e *Engine) peerConfiguration() (webrtc.Configuration, error) {
	configuration := webrtc.Configuration{}
	if e.Candidates != CandidatesRelay {
		return configuration, nil
	}
	if e.connectivity == nil {
		return configuration, domain.ErrEngineUnavailable
	}
	capability, err := e.connectivity.Issue()
	if err != nil || !capability.RelayOnly || len(capability.Servers) == 0 || !capability.ExpiresAt.After(time.Now()) {
		return configuration, domain.ErrEngineUnavailable
	}
	configuration.ICETransportPolicy = webrtc.ICETransportPolicyRelay
	for _, server := range capability.Servers {
		configuration.ICEServers = append(configuration.ICEServers, webrtc.ICEServer{URLs: server.URLs, Username: server.Username, Credential: server.Credential, CredentialType: webrtc.ICECredentialTypePassword})
	}
	return configuration, nil
}
