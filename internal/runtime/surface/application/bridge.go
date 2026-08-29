// The bridge use cases: token-authorizing App Agent calls on behalf of one
// open surface session. Authorization is re-derived from durable facts on
// every call — the token resolves the session, the session binds owner and
// device, and the effective capability list gates each method — and the Core
// App Agent service re-validates installation and grant again server-side.
package application

import (
	"context"
	"errors"
	"time"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	"github.com/yangtao121/workos/internal/runtime/surface/domain"
	"github.com/yangtao121/workos/internal/runtime/surface/ports"
)

// BridgeService executes the public App Bridge use cases. It never trusts a
// client-supplied owner/device/project/app-instance: every identifier is
// derived from the gateway identity and the session the token resolves.
type BridgeService struct {
	repository ports.SessionRepository
	appAgent   ports.AppAgentClient
	now        func() time.Time
}

// NewBridgeService composes the bridge use cases on the same session facts
// and the private Core App Agent client.
func NewBridgeService(repository ports.SessionRepository, appAgent ports.AppAgentClient) (*BridgeService, error) {
	if repository == nil || appAgent == nil {
		return nil, errors.New("bridge service requires the session repository and the core app agent client")
	}
	return &BridgeService{repository: repository, appAgent: appAgent, now: func() time.Time { return time.Now().UTC() }}, nil
}

// RunAgentTask validates the presented bridge token and submits one
// project-scoped App task through the Core App Agent service. The canonical
// request is bounded to (idempotency key, role, goal); project, app instance,
// and the installation grant epoch come from the stored session — Core
// re-validates the epoch against the active installation on every call
// (ADR-0003 §7), so a superseded grant fails closed server-side even though
// the local capability snapshot still lists the method.
func (s *BridgeService) RunAgentTask(ctx context.Context, ownerUserID, deviceID, token, idempotencyKey, role, goal string) (ports.AppTaskSubmission, error) {
	session, err := s.authorize(ctx, ownerUserID, deviceID, token, domain.BridgeCapabilityAgentTaskRun)
	if err != nil {
		return ports.AppTaskSubmission{}, err
	}
	return s.appAgent.RunAgentTask(ctx, ports.AppAgentRunQuery{
		ProjectID: session.ProjectID, AppInstanceID: session.AppInstanceID,
		// Derived exclusively from the validated session row: a public bridge
		// body cannot influence the epoch that Core compares.
		InstallationGrantRevision: session.InstallationGrantRevision,
		ClientKey:                 idempotencyKey, Role: role, Goal: goal,
	})
}

// StreamAgentEvents validates the presented bridge token and streams one
// App-created task's persisted events from Core. Ending the stream never
// cancels the durable Agent task. The watch carries the session's persisted
// grant epoch; Core re-authorizes it on every polling round and ends the
// stream once a grant mutation supersedes it.
func (s *BridgeService) StreamAgentEvents(ctx context.Context, ownerUserID, deviceID, token, taskID string, after int64, onEvent func(*agentv1.AgentEvent) error) error {
	session, err := s.authorize(ctx, ownerUserID, deviceID, token, domain.BridgeCapabilityAgentEventWatch)
	if err != nil {
		return err
	}
	return s.appAgent.WatchAgentTaskEvents(ctx, ports.AppAgentWatchQuery{
		ProjectID: session.ProjectID, AppInstanceID: session.AppInstanceID,
		TaskID: taskID, AfterSequence: after,
		InstallationGrantRevision: session.InstallationGrantRevision,
	}, onEvent)
}

// authorize resolves and validates the bridge credential chain: token
// grammar, owner-scoped digest lookup, constant-time digest match, and
// trusted-device binding — then gates the requested capability against the
// session's effective list.
func (s *BridgeService) authorize(ctx context.Context, ownerUserID, deviceID, token, capability string) (domain.SurfaceSession, error) {
	if ownerUserID == "" || deviceID == "" || !domain.ValidBridgeToken(token) {
		return domain.SurfaceSession{}, domain.ErrUnauthenticated
	}
	presentedDigest := domain.HashBridgeToken(token)
	session, err := s.repository.GetActiveSessionByBridgeToken(ctx, ownerUserID, presentedDigest, s.now())
	if err != nil {
		switch {
		case errors.Is(err, ports.ErrStoreUnavailable):
			return domain.SurfaceSession{}, domain.ErrUnavailable
		case errors.Is(err, domain.ErrNotFound):
			// Unknown or expired credential: one sanitized verdict, never a
			// distinction between "no such token" and "expired token".
			return domain.SurfaceSession{}, domain.ErrUnauthenticated
		}
		return domain.SurfaceSession{}, err
	}
	if !domain.BridgeTokenMatches(session.BridgeTokenHash, presentedDigest) {
		return domain.SurfaceSession{}, domain.ErrUnauthenticated
	}
	if session.DeviceID != deviceID {
		// Knowing a valid token from another trusted device is still a
		// failed credential: the binding facts must all match.
		return domain.SurfaceSession{}, domain.ErrUnauthenticated
	}
	if !domain.BridgeCapabilityGranted(session.BridgeCapabilities, capability) {
		return domain.SurfaceSession{}, domain.ErrPermissionDenied
	}
	return session, nil
}
