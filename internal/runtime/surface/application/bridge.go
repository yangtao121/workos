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
	// knowledge is the scoped search pipeline (Core re-authorization +
	// indexer call). It is nil when the runtime has no configured indexer
	// adapter: then knowledge.search is never negotiated and every call
	// fails closed without touching Core or the indexer.
	knowledge *KnowledgeSearchPipeline
	now       func() time.Time
}

// KnowledgeSearchPipeline composes the Core knowledge authorizer with the
// scoped indexer client.
type KnowledgeSearchPipeline struct {
	authorizer ports.AppAgentClient
	indexer    ports.KnowledgeSearchClient
}

func NewKnowledgeSearchPipeline(authorizer ports.AppAgentClient, indexer ports.KnowledgeSearchClient) (*KnowledgeSearchPipeline, error) {
	if authorizer == nil || indexer == nil {
		return nil, errors.New("knowledge pipeline requires the app agent authorizer and the indexer client")
	}
	return &KnowledgeSearchPipeline{authorizer: authorizer, indexer: indexer}, nil
}

// NewBridgeService composes the bridge use cases on the same session facts
// and the private Core App Agent client. knowledge may be nil when the
// runtime has no indexer adapter configured.
func NewBridgeService(repository ports.SessionRepository, appAgent ports.AppAgentClient, knowledge *KnowledgeSearchPipeline) (*BridgeService, error) {
	if repository == nil || appAgent == nil {
		return nil, errors.New("bridge service requires the session repository and the core app agent client")
	}
	return &BridgeService{repository: repository, appAgent: appAgent, knowledge: knowledge, now: func() time.Time { return time.Now().UTC() }}, nil
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

// Knowledge search bounds mirror the indexer contract; the bridge body can
// only carry bounded query/page facts.
const (
	maxKnowledgeQueryCodePoints = 256
	defaultKnowledgePageSize    = 20
	maxKnowledgePageSize        = 50
)

// SearchKnowledge executes the read-only knowledge bridge method. The fixed
// order (ADR-0013 §E1): token/session/capability gate — knowledge.search can
// only ever be negotiated for a real `knowledge.read` grant — then Core
// re-verification of the installation, project, and the exact current grant
// revision, then the scoped indexer call with the session-derived binding,
// then response projection. Every failure before the indexer call means the
// indexer is provably never touched.
func (s *BridgeService) SearchKnowledge(ctx context.Context, ownerUserID, deviceID, token, query string, pageSize int32, pageToken string) (ports.KnowledgeSearchPage, error) {
	if s.knowledge == nil {
		// No configured executor: the method must never have been negotiated,
		// so a call is a sanitized denial without touching Core or indexer.
		return ports.KnowledgeSearchPage{}, domain.ErrPermissionDenied
	}
	if len([]rune(query)) == 0 || len([]rune(query)) > maxKnowledgeQueryCodePoints ||
		pageSize < 0 || pageSize > maxKnowledgePageSize {
		return ports.KnowledgeSearchPage{}, domain.ErrInvalid
	}
	size := pageSize
	if size == 0 {
		size = defaultKnowledgePageSize
	}
	session, err := s.authorize(ctx, ownerUserID, deviceID, token, domain.BridgeCapabilityKnowledgeSearch)
	if err != nil {
		return ports.KnowledgeSearchPage{}, err
	}
	// Core re-verifies the active installation, the non-archived project, and
	// the session's exact grant revision. Deny/not-found/revision drift is
	// one indistinguishable sanitized denial; a Core outage is Unavailable
	// and never silently degrades into an allow.
	binding, err := s.knowledge.authorizer.AuthorizeAppKnowledge(ctx, ports.AppKnowledgeAuthQuery{
		ProjectID:                 session.ProjectID,
		AppInstanceID:             session.AppInstanceID,
		InstallationGrantRevision: session.InstallationGrantRevision,
	})
	if err != nil {
		switch {
		case errors.Is(err, ports.ErrAppAgentDenied):
			return ports.KnowledgeSearchPage{}, domain.ErrPermissionDenied
		case errors.Is(err, ports.ErrAppAgentUnavailable), errors.Is(err, ports.ErrStoreUnavailable):
			return ports.KnowledgeSearchPage{}, domain.ErrUnavailable
		default:
			return ports.KnowledgeSearchPage{}, err
		}
	}
	if binding.OwnerUserID != session.OwnerUserID || binding.ProjectID != session.ProjectID {
		// The authoritative binding disagrees with the session-derived scope:
		// fail closed rather than calling the indexer with anything else.
		return ports.KnowledgeSearchPage{}, domain.ErrPermissionDenied
	}
	return s.knowledge.indexer.Search(ctx, ports.KnowledgeSearchQuery{
		OwnerUserID: binding.OwnerUserID,
		ProjectID:   binding.ProjectID,
		Query:       query,
		PageSize:    size,
		PageToken:   pageToken,
	})
}
