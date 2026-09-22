package main

import (
	"context"
	"errors"
	artifactdomain "github.com/yangtao121/workos/internal/runtime/artifactstore/domain"
	buildtestdomain "github.com/yangtao121/workos/internal/runtime/buildtest/domain"
	buildtestports "github.com/yangtao121/workos/internal/runtime/buildtest/ports"
	ptyports "github.com/yangtao121/workos/internal/runtime/ptyhost/ports"
	"time"

	nativehostapp "github.com/yangtao121/workos/internal/runtime/nativehost/application"
	nativedomain "github.com/yangtao121/workos/internal/runtime/nativehost/domain"
	ptyhostapp "github.com/yangtao121/workos/internal/runtime/ptyhost/application"
	ptydomain "github.com/yangtao121/workos/internal/runtime/ptyhost/domain"
	surfaceapp "github.com/yangtao121/workos/internal/runtime/surface/application"
	surfacedomain "github.com/yangtao121/workos/internal/runtime/surface/domain"
	surfaceports "github.com/yangtao121/workos/internal/runtime/surface/ports"
	workloadapp "github.com/yangtao121/workos/internal/runtime/workload/application"
	workloaddomain "github.com/yangtao121/workos/internal/runtime/workload/domain"
	workloadports "github.com/yangtao121/workos/internal/runtime/workload/ports"
)

// surfaceWorkloadLauncher adapts the Workload Manager to the Surface
// Broker's narrow WorkloadRuntime port. It lives in the composition root
// because it is exactly the seam between the two runtime modules; neither
// module imports the other.
type surfaceWorkloadLauncher struct {
	manager *workloadapp.Manager
}

func (a *surfaceWorkloadLauncher) EnsureSurfaceWorkload(ctx context.Context, query surfaceports.SurfaceWorkloadQuery) (surfaceports.WorkloadHandle, error) {
	workload, err := a.manager.Ensure(ctx, workloadports.EnsureCommand{LifecycleMode: query.LifecycleMode,
		OwnerUserID: query.OwnerUserID, ProjectID: query.ProjectID,
		AppInstanceID: query.AppInstanceID, AppID: query.AppID,
		AppVersion: query.AppVersion, ManifestDigest: query.ManifestDigest,
		Image: query.Image, Command: query.Command, Port: query.Port,
		Requested: workloaddomain.RequestedPolicy{
			CPUHardCores: query.Resources.CPUHardCores, MemoryHighMB: query.Resources.MemoryHighMB,
			MemoryMaxMB: query.Resources.MemoryMaxMB, PidsMax: query.Resources.PidsMax,
			HTTPPath: query.Health.HTTPPath, StartupSeconds: query.Health.StartupSeconds,
			RestartLimit: query.Health.RestartLimit,
		},
		OperationKey: query.OperationKey,
		ArtifactID:   query.ArtifactID, ArtifactDigest: query.ArtifactDigest,
	})
	if err != nil {
		return surfaceports.WorkloadHandle{}, mapWorkloadError(err)
	}
	return surfaceports.WorkloadHandle{LifecycleMode: workload.LifecycleMode, ID: workload.ID, Generation: workload.Generation, Endpoint: workload.Endpoint, OwnerUserID: workload.OwnerUserID, ProjectID: workload.ProjectID, AppInstanceID: workload.AppInstanceID, AppID: workload.AppID, AppVersion: workload.AppVersion, ManifestDigest: workload.ManifestDigest}, nil
}

func (a *surfaceWorkloadLauncher) LookupSurfaceWorkload(ctx context.Context, workloadID string, generation int64) (surfaceports.WorkloadHandle, error) {
	workload, err := a.manager.LookupRunning(ctx, workloadID, generation)
	if err != nil {
		return surfaceports.WorkloadHandle{}, mapWorkloadError(err)
	}
	return surfaceports.WorkloadHandle{LifecycleMode: workload.LifecycleMode, ID: workload.ID, Generation: workload.Generation, Endpoint: workload.Endpoint, OwnerUserID: workload.OwnerUserID, ProjectID: workload.ProjectID, AppInstanceID: workload.AppInstanceID, AppID: workload.AppID, AppVersion: workload.AppVersion, ManifestDigest: workload.ManifestDigest}, nil
}

func mapWorkloadError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, workloaddomain.ErrRunnerUnavailable):
		return surfaceports.ErrWorkloadRunnerUnavailable
	case errors.Is(err, workloaddomain.ErrImageMissing):
		return surfaceports.ErrWorkloadImageMissing
	case errors.Is(err, workloaddomain.ErrUnsupported):
		return surfaceports.ErrWorkloadUnsupported
	case errors.Is(err, workloaddomain.ErrUnavailable):
		return surfaceports.ErrWorkloadUnavailable
	case errors.Is(err, workloaddomain.ErrIdempotencyConflict):
		return surfaceports.ErrWorkloadConflict
	default:
		return err
	}
}

// coreInstallationVerifier adapts the Surface Broker's Core resolver client
// to the Workload Manager's InstallationVerifier port. The verifier folds
// Core verdicts into the neutral trichotomy the reconcile loop consumes; a
// digest mismatch is stored-fact corruption and stays an error, never a
// "gone" verdict that would silently stop a healthy workload.
type coreInstallationVerifier struct {
	resolver surfaceports.LaunchResolver
}

func (v *coreInstallationVerifier) VerifyLaunch(ctx context.Context, query workloadports.LaunchQuery) (workloadports.LaunchVerdict, error) {
	resolved, err := v.resolver.ResolveSurfaceLaunch(ctx, surfaceports.ResolveQuery{
		ProjectID: query.ProjectID, AppInstanceID: query.AppInstanceID,
	})
	switch {
	case err != nil:
		if errors.Is(err, surfaceports.ErrResolverNotFound) {
			return workloadports.LaunchGone, nil
		}
		if errors.Is(err, surfaceports.ErrResolverUnavailable) {
			return workloadports.LaunchUnknown, nil
		}
		if errors.Is(err, surfaceports.ErrResolverUnsupported) {
			// The installed instance exists but no longer resolves to a
			// supported surface: treat as gone for the supervised workload.
			return workloadports.LaunchGone, nil
		}
		return workloadports.LaunchUnknown, nil
	case resolved.ManifestDigest != query.ManifestDigest:
		return workloadports.LaunchGone, nil
	default:
		return workloadports.LaunchInstalled, nil
	}
}

// surfaceReferenceSource adapts the Surface Broker's session store to the
// Workload Manager's idle-TTL source.
type surfaceReferenceSource struct {
	sessions surfaceports.SessionRepository
}

func (s *surfaceReferenceSource) HasActiveSurface(ctx context.Context, ownerUserID, appInstanceID string) (bool, error) {
	return s.sessions.HasActiveSurface(ctx, ownerUserID, appInstanceID, time.Now().UTC())
}

// surfaceInteractiveRuntime adapts the PTY and native runner services to the
// surface continuity ports (ADR-0031). It lives in the composition root like
// the workload launcher: it is the seam between the runtime modules, and
// neither module imports the other. An unconfigured runner is an honest
// not-found, never an invented workload.
type surfaceInteractiveRuntime struct {
	resolver surfaceports.LaunchResolver
	manager  *workloadapp.Manager
	pty      *ptyhostapp.Service
	native   *nativehostapp.Service
}

var _ surfaceports.InteractiveWorkloadRuntime = (*surfaceInteractiveRuntime)(nil)

func (a *surfaceInteractiveRuntime) sessionWorkload(kind surfaceports.WorkloadKind, session ptydomain.Session) surfaceports.InteractiveWorkload {
	_ = kind
	return surfaceports.InteractiveWorkload{
		Generation: session.Generation, WorkloadID: session.SessionID, Kind: surfaceports.WorkloadKindPty, OwnerUserID: session.OwnerUserID,
		ProjectID: session.ProjectID, State: string(session.State), Terminal: session.State.Terminal(),
		CreatedAt: session.CreatedAt, ExpiresAt: session.ExpiresAt, UpdatedAt: session.UpdatedAt, LifecycleMode: int32(session.LifecycleMode),
	}
}

func (a *surfaceInteractiveRuntime) nativeWorkload(session nativedomain.Session) surfaceports.InteractiveWorkload {
	return surfaceports.InteractiveWorkload{
		Generation: session.Generation, WorkloadID: session.SessionID, Kind: surfaceports.WorkloadKindNative, OwnerUserID: session.OwnerUserID,
		ProjectID: session.ProjectID, State: string(session.State), Terminal: session.State.Terminal(),
		CreatedAt: session.CreatedAt, ExpiresAt: session.ExpiresAt, UpdatedAt: session.UpdatedAt, LifecycleMode: int32(session.LifecycleMode),
		Width: session.Width, Height: session.Height,
	}
}

// Resolve finds the owner's interactive workload by id in the configured
// runners; terminal sessions are returned with their true state so Attach
// can report the stopped verdict.
func (a *surfaceInteractiveRuntime) Resolve(ctx context.Context, owner, id string) (surfaceports.InteractiveWorkload, error) {
	if a.pty != nil {
		row, err := a.pty.Get(ctx, owner, id)
		if err == nil {
			return a.sessionWorkload(surfaceports.WorkloadKindPty, row), nil
		}
		if !errors.Is(err, ptydomain.ErrNotFound) {
			return surfaceports.InteractiveWorkload{}, mapInteractiveError(err)
		}
	}
	if a.native != nil {
		row, err := a.native.Get(ctx, owner, id)
		if err == nil {
			return a.nativeWorkload(row), nil
		}
		if !errors.Is(err, nativedomain.ErrNotFound) {
			return surfaceports.InteractiveWorkload{}, mapInteractiveError(err)
		}
	}
	if a.manager != nil {
		row, err := a.manager.GetOwned(ctx, owner, id)
		if err == nil {
			return a.appWorkload(row), nil
		}
		if !errors.Is(err, workloaddomain.ErrNotFound) {
			return surfaceports.InteractiveWorkload{}, surfaceports.ErrContinuityStoreUnavailable
		}
	}
	return surfaceports.InteractiveWorkload{}, surfaceports.ErrContinuityNotFound
}
func (a *surfaceInteractiveRuntime) appWorkload(row workloaddomain.Workload) surfaceports.InteractiveWorkload {
	started := row.CreatedAt
	if row.StartedAt != nil {
		started = *row.StartedAt
	}
	return surfaceports.InteractiveWorkload{WorkloadID: row.ID, Kind: surfaceports.WorkloadKindApp, OwnerUserID: row.OwnerUserID, ProjectID: row.ProjectID, AppInstanceID: row.AppInstanceID, AppID: row.AppID, AppVersion: row.AppVersion, Generation: row.Generation, State: string(row.State), Terminal: row.State.Terminal(), CreatedAt: started, UpdatedAt: row.UpdatedAt, LifecycleMode: max(int32(1), row.LifecycleMode), IdleStopSeconds: a.manager.IdleLimitSeconds()}
}

func (a *surfaceInteractiveRuntime) ListProject(ctx context.Context, ownerUserID, projectID string) ([]surfaceports.InteractiveWorkload, error) {
	workloads := make([]surfaceports.InteractiveWorkload, 0)
	if a.pty != nil {
		sessions, err := a.pty.ListProject(ctx, ownerUserID, projectID)
		if err != nil {
			return nil, err
		}
		for _, session := range sessions {
			workloads = append(workloads, a.sessionWorkload(surfaceports.WorkloadKindPty, session))
		}
	}
	if a.native != nil {
		sessions, err := a.native.ListProject(ctx, ownerUserID, projectID)
		if err != nil {
			return nil, err
		}
		for _, session := range sessions {
			workloads = append(workloads, a.nativeWorkload(session))
		}
	}
	if a.manager != nil {
		rows, err := a.manager.ListProject(ctx, ownerUserID, projectID)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			workloads = append(workloads, a.appWorkload(row))
		}
	}
	return workloads, nil
}

func (a *surfaceInteractiveRuntime) DetachWorkload(ctx context.Context, kind surfaceports.WorkloadKind, ownerUserID, workloadID, deviceID string) error {
	switch kind {
	case surfaceports.WorkloadKindNative:
		if a.native == nil {
			return surfaceports.ErrContinuityNotFound
		}
		if _, err := a.native.Detach(ctx, ownerUserID, workloadID, deviceID); err != nil {
			if errors.Is(err, nativedomain.ErrNotFound) || errors.Is(err, nativedomain.ErrInvalid) {
				return surfaceports.ErrContinuityNotFound
			}
			return err
		}
		return nil
	case surfaceports.WorkloadKindPty:
		// A PTY session's only per-device connection state is the attachment
		// row itself; there is no media peer to release.
		return nil
	default:
		return surfaceports.ErrContinuityNotFound
	}
}

func (a *surfaceInteractiveRuntime) StopWorkload(ctx context.Context, kind surfaceports.WorkloadKind, ownerUserID, workloadID string) (surfaceports.InteractiveWorkload, error) {
	switch kind {
	case surfaceports.WorkloadKindPty:
		if a.pty == nil {
			return surfaceports.InteractiveWorkload{}, surfaceports.ErrContinuityNotFound
		}
		session, err := a.pty.Close(ctx, ownerUserID, workloadID)
		if err != nil {
			return surfaceports.InteractiveWorkload{}, mapInteractiveError(err)
		}
		return a.sessionWorkload(kind, session), nil
	case surfaceports.WorkloadKindNative:
		if a.native == nil {
			return surfaceports.InteractiveWorkload{}, surfaceports.ErrContinuityNotFound
		}
		session, err := a.native.Close(ctx, ownerUserID, workloadID)
		if err != nil {
			return surfaceports.InteractiveWorkload{}, mapInteractiveError(err)
		}
		return a.nativeWorkload(session), nil
	default:
		return surfaceports.InteractiveWorkload{}, surfaceports.ErrContinuityNotFound
	}
}

// mapInteractiveError folds the runner sentinels into the continuity
// not-found verdict; everything else keeps its own error value.
func mapInteractiveError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ptydomain.ErrIdempotencyDrift), errors.Is(err, nativedomain.ErrIdempotencyDrift), errors.Is(err, workloaddomain.ErrIdempotencyConflict):
		return surfacedomain.ErrIdempotencyConflict
	case errors.Is(err, ptydomain.ErrInvalid), errors.Is(err, nativedomain.ErrInvalid), errors.Is(err, workloaddomain.ErrInvalid):
		return surfacedomain.ErrInvalid
	case errors.Is(err, ptydomain.ErrStoreUnavailable), errors.Is(err, nativedomain.ErrStoreUnavailable), errors.Is(err, workloaddomain.ErrUnavailable):
		return surfaceports.ErrContinuityStoreUnavailable
	case errors.Is(err, ptydomain.ErrEngineUnavailable), errors.Is(err, nativedomain.ErrEngineUnavailable), errors.Is(err, workloaddomain.ErrUnsupported), errors.Is(err, workloaddomain.ErrRunnerUnavailable), errors.Is(err, workloaddomain.ErrRestartLimitExhausted):
		return surfacedomain.ErrUnsupported
	case errors.Is(err, workloaddomain.ErrNotFound), errors.Is(err, ptydomain.ErrNotFound), errors.Is(err, nativedomain.ErrNotFound):
		return surfaceports.ErrContinuityNotFound
	default:
		return err
	}
}

// continuityAuthorization adapts the surface continuity application to the
// runner ControlAuthorizer ports: the single-controller lease gate the PTY
// write/resize and native input paths consult on every request.
type continuityAuthorization struct {
	service *surfaceapp.ContinuityService
}

func (a continuityAuthorization) AuthorizeInput(ctx context.Context, ownerUserID, workloadID, deviceID string) error {
	return a.service.AuthorizeInput(ctx, ownerUserID, workloadID, deviceID)
}

func (a *surfaceInteractiveRuntime) RestartWorkload(ctx context.Context, kind surfaceports.WorkloadKind, owner, id, key string, fence func() error, modes ...int32) (surfaceports.InteractiveWorkload, error) {
	mode := int32(1)
	if len(modes) > 0 {
		mode = modes[0]
	}
	switch kind {
	case surfaceports.WorkloadKindApp:
		if a.manager == nil {
			return surfaceports.InteractiveWorkload{}, surfaceports.ErrContinuityNotFound
		}
		previous, err := a.manager.GetOwned(ctx, owner, id)
		if err != nil {
			return surfaceports.InteractiveWorkload{}, mapInteractiveError(err)
		}
		if a.resolver == nil {
			return surfaceports.InteractiveWorkload{}, surfacedomain.ErrUnsupported
		}
		resolved, err := a.resolver.ResolveSurfaceLaunch(ctx, surfaceports.ResolveQuery{ProjectID: previous.ProjectID, AppInstanceID: previous.AppInstanceID})
		if err != nil {
			if errors.Is(err, surfaceports.ErrResolverUnavailable) {
				return surfaceports.InteractiveWorkload{}, surfaceports.ErrContinuityStoreUnavailable
			}
			return surfaceports.InteractiveWorkload{}, surfaceports.ErrContinuityNotFound
		}
		if resolved.Kind != surfaceports.LaunchKindWebServiceContainer || resolved.AppID != previous.AppID || resolved.Version != previous.AppVersion || resolved.ManifestDigest != previous.ManifestDigest {
			return surfaceports.InteractiveWorkload{}, surfacedomain.ErrUnsupported
		}
		row, err := a.manager.Restart(ctx, workloadports.RestartCommand{WorkloadID: id, OperationKey: "surface-restart:" + key, LifecycleMode: mode})
		if err != nil {
			return surfaceports.InteractiveWorkload{}, mapInteractiveError(err)
		}
		return a.appWorkload(row), nil

	case surfaceports.WorkloadKindPty:
		if a.pty == nil {
			return surfaceports.InteractiveWorkload{}, surfaceports.ErrContinuityNotFound
		}
		session, err := a.pty.Restart(ctx, owner, id, key, fence, ptydomain.LifecycleMode(mode))
		if err != nil {
			return surfaceports.InteractiveWorkload{}, mapInteractiveError(err)
		}
		return a.sessionWorkload(kind, session), nil
	case surfaceports.WorkloadKindNative:
		if a.native == nil {
			return surfaceports.InteractiveWorkload{}, surfaceports.ErrContinuityNotFound
		}
		session, err := a.native.Restart(ctx, owner, id, key, fence, nativedomain.LifecycleMode(mode))
		if err != nil {
			return surfaceports.InteractiveWorkload{}, mapInteractiveError(err)
		}
		return a.nativeWorkload(session), nil
	default:
		return surfaceports.InteractiveWorkload{}, surfaceports.ErrContinuityNotFound
	}
}

func (a *surfaceInteractiveRuntime) StopWorkloadAction(ctx context.Context, kind surfaceports.WorkloadKind, owner, id, key string, fence func() error) (surfaceports.InteractiveWorkload, error) {
	switch kind {
	case surfaceports.WorkloadKindApp:
		if a.manager == nil {
			return surfaceports.InteractiveWorkload{}, surfaceports.ErrContinuityNotFound
		}
		if _, err := a.manager.GetOwned(ctx, owner, id); err != nil {
			return surfaceports.InteractiveWorkload{}, surfaceports.ErrContinuityNotFound
		}
		if err := a.manager.Terminate(ctx, workloadports.TerminateCommand{WorkloadID: id, OperationKey: "surface-stop:" + key, Reason: "policy"}); err != nil {
			return surfaceports.InteractiveWorkload{}, mapInteractiveError(err)
		}
		row, err := a.manager.GetOwned(ctx, owner, id)
		if err != nil {
			return surfaceports.InteractiveWorkload{}, mapInteractiveError(err)
		}
		return a.appWorkload(row), nil

	case surfaceports.WorkloadKindPty:
		if a.pty != nil {
			session, err := a.pty.Stop(ctx, owner, id, key, fence)
			return a.sessionWorkload(kind, session), mapInteractiveError(err)
		}
	case surfaceports.WorkloadKindNative:
		if a.native != nil {
			session, err := a.native.Stop(ctx, owner, id, key, fence)
			return a.nativeWorkload(session), mapInteractiveError(err)
		}
	}
	return surfaceports.InteractiveWorkload{}, surfaceports.ErrContinuityNotFound
}

func (a continuityAuthorization) AuthorizeInputGeneration(ctx context.Context, owner, workload, device string, generation int64) error {
	return a.service.AuthorizeInputGeneration(ctx, owner, workload, device, generation)
}

var _ ptyports.EpochControlAuthorizer = continuityAuthorization{}

func buildArtifactAuthority(store buildtestports.JobStore) func(context.Context, artifactdomain.Artifact) (bool, error) {
	return func(ctx context.Context, artifact artifactdomain.Artifact) (bool, error) {
		job, err := store.GetJobByTask(ctx, artifact.TaskID)
		if errors.Is(err, buildtestdomain.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return job.State == buildtestdomain.StateSucceeded && job.ID == artifact.JobID &&
			job.OwnerUserID == artifact.OwnerUserID && job.ArtifactID == artifact.ID &&
			job.ArtifactDigest == artifact.Digest, nil
	}
}
