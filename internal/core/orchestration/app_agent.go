package orchestration

import (
	"context"
	"errors"

	agentapp "github.com/yangtao121/workos/internal/core/agent/application"
	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	appregistrydomain "github.com/yangtao121/workos/internal/core/appregistry/domain"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
)

// The bridge capabilities this slice actually implements. A grant only becomes
// an effective capability when a working executor exists for it; everything
// else stays a stored grant fact and never appears in a session's capability
// list.
const (
	AppBridgeCapabilityAgentTaskRun    = "agent.task.run"
	AppBridgeCapabilityAgentEventWatch = "agent.event.watch"
	// AppBridgeCapabilityKnowledgeSearch is the read-only knowledge search
	// capability. It is negotiated only when the manifest requests
	// `knowledge.read`, the installation actually holds that grant, and the
	// exact current grant revision matches the session snapshot (ADR-0013).
	AppBridgeCapabilityKnowledgeRead = "knowledge.read"
)

// ErrAppNotGranted marks a validated session asking for a capability the
// installation's grant snapshot does not carry. Transport maps it to a
// sanitized PermissionDenied.
var ErrAppNotGranted = errors.New("app capability is not granted")

// ErrAppGrantStale marks a private run/watch call whose session-derived grant
// revision no longer equals the active installation's current epoch — the
// immediate-revocation verdict of ADR-0003. It is deliberately the same
// verdict for a mismatch and for an absent (<= 0) revision so no caller can
// distinguish why the epoch check failed, and the sanitized message never
// reveals the current revision or grants.
var ErrAppGrantStale = errors.New("app surface session grant revision is stale")

// errAppGrantCorrupt marks a stored grant snapshot containing a capability ID
// outside the canonical vocabulary. Like every immutable-invariant drift it is
// a sanitized Internal verdict, never a silent downgrade.
var errAppGrantCorrupt = errors.New("stored app grant snapshot is inconsistent")

// AppTaskGateway is the narrow Agent-module surface the App Agent service
// needs. The TaskRouter implements it; the composition root passes the
// concrete service. Provider selection, project binding snapshots, and the
// Agent tables stay inside the Agent module.
type AppTaskGateway interface {
	SubmitForApp(ctx context.Context, input agentapp.AppSubmitInput) (agentdomain.Task, error)
	GetAppTaskByIdempotency(ctx context.Context, ownerUserID, appInstanceID, clientKey string) (agentdomain.Task, string, bool, error)
	GetAppTask(ctx context.Context, ownerUserID, appInstanceID, taskID string) (agentdomain.Task, string, error)
	AppTaskEvents(ctx context.Context, ownerUserID, appInstanceID, taskID string, after int64, limit int) ([]agentdomain.Event, error)
}

// AppAgentService is the private Core authority for App bridge calls: every
// request re-resolves the active installation and its grant snapshot from
// authoritative facts, enforces the capability per method, and forces the
// installation's project scope. Runtime-supplied identifiers are derived from
// the validated surface session; they are re-checked here, never trusted.
type AppAgentService struct {
	installations installationSource
	tasks         AppTaskGateway
}

func NewAppAgentService(installations installationSource, tasks AppTaskGateway) (*AppAgentService, error) {
	if installations == nil || tasks == nil {
		return nil, errors.New("app agent service requires installation and task dependencies")
	}
	return &AppAgentService{installations: installations, tasks: tasks}, nil
}

// RunAgentTask authorizes one project-scoped App task submission and routes it
// through the Task Router. The canonical request digest covers only the
// bounded client input (role, goal); replay returns the first provider
// snapshot without re-resolving the binding. installationGrantRevision is the
// session-persisted epoch derived by the runtime from its validated surface
// session; it is compared for exact equality against the re-resolved active
// installation on every call, never trusted.
func (s *AppAgentService) RunAgentTask(ctx context.Context, ownerUserID, projectID, appInstanceID string, installationGrantRevision int64, clientKey, role, goal string) (agentdomain.Task, error) {
	if _, err := s.authorize(ctx, ownerUserID, projectID, appInstanceID, installationGrantRevision, AppBridgeCapabilityAgentTaskRun); err != nil {
		return agentdomain.Task{}, err
	}
	return s.tasks.SubmitForApp(ctx, agentapp.AppSubmitInput{
		OwnerUserID:          ownerUserID,
		AppInstanceID:        appInstanceID,
		ClientIdempotencyKey: clientKey,
		RequestDigest:        agentdomain.AppTaskRequestDigest(role, goal),
		ProjectID:            projectID,
		Role:                 role,
		Goal:                 goal,
	})
}

// WatchAgentTaskEvents authorizes one App event watch: same owner, same
// project, and a task whose durable provenance maps to exactly this app
// installation. Knowing a task ID string grants nothing. The returned task
// carries the state and last event sequence the stream loop needs. Every
// polling round re-runs the full authorization including the grant-revision
// equality check, so a real grant change terminates the stream on the next
// round instead of streaming new events to the old epoch.
func (s *AppAgentService) WatchAgentTaskEvents(ctx context.Context, ownerUserID, projectID, appInstanceID string, installationGrantRevision int64, taskID string, after int64, limit int) (agentdomain.Task, []agentdomain.Event, error) {
	if _, err := s.authorize(ctx, ownerUserID, projectID, appInstanceID, installationGrantRevision, AppBridgeCapabilityAgentEventWatch); err != nil {
		return agentdomain.Task{}, nil, err
	}
	task, mappedProject, err := s.tasks.GetAppTask(ctx, ownerUserID, appInstanceID, taskID)
	if err != nil {
		return agentdomain.Task{}, nil, err
	}
	if mappedProject != projectID {
		// The provenance snapshot proves the task belongs to a different
		// project than the caller's installation: a sanitized miss.
		return agentdomain.Task{}, nil, agentdomain.ErrNotFound
	}
	events, err := s.tasks.AppTaskEvents(ctx, ownerUserID, appInstanceID, taskID, after, limit)
	if err != nil {
		return agentdomain.Task{}, nil, err
	}
	return task, events, nil
}

// AuthorizeAppKnowledge re-verifies, per call, that the app instance is an
// active installation of this owner under this non-archived project whose
// exact current grant revision matches the session snapshot and whose grant
// carries `knowledge.read`. On success it returns the canonical trusted
// binding the runtime must use for the scoped indexer call; every failure
// is one indistinguishable sanitized verdict with no existence oracle and
// no current-revision leak.
func (s *AppAgentService) AuthorizeAppKnowledge(ctx context.Context, ownerUserID, projectID, appInstanceID string, installationGrantRevision int64) (string, string, error) {
	if _, err := s.authorize(ctx, ownerUserID, projectID, appInstanceID, installationGrantRevision, AppBridgeCapabilityKnowledgeRead); err != nil {
		return "", "", err
	}
	return ownerUserID, projectID, nil
}

// authorize walks the authoritative chain shared by both bridge methods:
// canonical capability, active same-owner installation under a non-archived
// project, the session-derived grant epoch, and the exact grant. Every
// verdict here is derived from Core facts, never from the runtime's snapshot.
// The epoch comparison precedes grant validation so any real grant change —
// even one that keeps the requested capability in the new set — fails every
// method of an old session (ADR-0003).
func (s *AppAgentService) authorize(ctx context.Context, ownerUserID, projectID, appInstanceID string, installationGrantRevision int64, capability string) (projectdomain.Installation, error) {
	if ownerUserID == "" || !appregistrydomain.KnownPermission(capability) {
		return projectdomain.Installation{}, projectdomain.ErrInvalid
	}
	installation, err := s.installations.ResolveActiveInstallation(ctx, ownerUserID, projectID, appInstanceID)
	if err != nil {
		return projectdomain.Installation{}, err
	}
	// A private request can only carry a revision derived from a validated
	// session snapshot. Absent (<= 0) and mismatched revisions are the same
	// indistinguishable stale verdict; the current revision never leaks.
	if installationGrantRevision <= 0 || installation.GrantRevision != installationGrantRevision {
		return projectdomain.Installation{}, ErrAppGrantStale
	}
	if err := validateStoredGrant(installation.GrantedPermissions); err != nil {
		return projectdomain.Installation{}, err
	}
	for _, granted := range installation.GrantedPermissions {
		if granted == capability {
			return installation, nil
		}
	}
	return projectdomain.Installation{}, ErrAppNotGranted
}

// validateStoredGrant checks the entire stored grant snapshot before any
// capability decision: every entry must belong to the canonical vocabulary,
// the list must be canonically sorted and duplicate-free. Checking the whole
// snapshot first (instead of returning on the first membership hit) keeps
// trailing corruption — e.g. a valid capability followed by an unknown or
// duplicated entry — a fail-closed Internal verdict rather than a successful
// authorization. Stored grants are written canonical; any drift is internal
// corruption and must not degrade into "no capability" or a silent grant.
func validateStoredGrant(granted []string) error {
	previous := ""
	for _, entry := range granted {
		if !appregistrydomain.KnownPermission(entry) {
			return errAppGrantCorrupt
		}
		if previous != "" && entry <= previous {
			// Unsorted or duplicated: both violate the canonical form.
			return errAppGrantCorrupt
		}
		previous = entry
	}
	return nil
}
