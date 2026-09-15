package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	nativehostapp "github.com/yangtao121/workos/internal/runtime/nativehost/application"
	nativedomain "github.com/yangtao121/workos/internal/runtime/nativehost/domain"
	ptyhostapp "github.com/yangtao121/workos/internal/runtime/ptyhost/application"
	ptydomain "github.com/yangtao121/workos/internal/runtime/ptyhost/domain"
	surfaceapp "github.com/yangtao121/workos/internal/runtime/surface/application"
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
	workload, err := a.manager.Ensure(ctx, workloadports.EnsureCommand{
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
	})
	if err != nil {
		return surfaceports.WorkloadHandle{}, mapWorkloadError(err)
	}
	return surfaceports.WorkloadHandle{ID: workload.ID, Generation: workload.Generation, Endpoint: workload.Endpoint}, nil
}

func (a *surfaceWorkloadLauncher) LookupSurfaceWorkload(ctx context.Context, workloadID string, generation int64) (surfaceports.WorkloadHandle, error) {
	workload, err := a.manager.LookupRunning(ctx, workloadID, generation)
	if err != nil {
		return surfaceports.WorkloadHandle{}, mapWorkloadError(err)
	}
	return surfaceports.WorkloadHandle{ID: workload.ID, Generation: workload.Generation, Endpoint: workload.Endpoint}, nil
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
		return workloadports.LaunchUnknown, fmt.Errorf("workload installation digest drifted: %w", workloadports.ErrDrift)
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
	pty    *ptyhostapp.Service
	native *nativehostapp.Service
}

var _ surfaceports.InteractiveWorkloadRuntime = (*surfaceInteractiveRuntime)(nil)

func (a *surfaceInteractiveRuntime) sessionWorkload(kind surfaceports.WorkloadKind, session ptydomain.Session) surfaceports.InteractiveWorkload {
	_ = kind
	return surfaceports.InteractiveWorkload{
		WorkloadID: session.SessionID, Kind: surfaceports.WorkloadKindPty, OwnerUserID: session.OwnerUserID,
		ProjectID: session.ProjectID, State: string(session.State), Terminal: session.State.Terminal(),
		CreatedAt: session.CreatedAt, ExpiresAt: session.ExpiresAt,
	}
}

func (a *surfaceInteractiveRuntime) nativeWorkload(session nativedomain.Session) surfaceports.InteractiveWorkload {
	return surfaceports.InteractiveWorkload{
		WorkloadID: session.SessionID, Kind: surfaceports.WorkloadKindNative, OwnerUserID: session.OwnerUserID,
		ProjectID: session.ProjectID, State: string(session.State), Terminal: session.State.Terminal(),
		CreatedAt: session.CreatedAt, ExpiresAt: session.ExpiresAt,
		Width: session.Width, Height: session.Height,
	}
}

// Resolve finds the owner's interactive workload by id in the configured
// runners; terminal sessions are returned with their true state so Attach
// can report the stopped verdict.
func (a *surfaceInteractiveRuntime) Resolve(ctx context.Context, ownerUserID, workloadID string) (surfaceports.InteractiveWorkload, error) {
	if a.pty != nil {
		if session, err := a.pty.Get(ctx, ownerUserID, workloadID); err == nil {
			return a.sessionWorkload(surfaceports.WorkloadKindPty, session), nil
		}
	}
	if a.native != nil {
		if session, err := a.native.Get(ctx, ownerUserID, workloadID); err == nil {
			return a.nativeWorkload(session), nil
		}
	}
	return surfaceports.InteractiveWorkload{}, surfaceports.ErrContinuityNotFound
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
	return workloads, nil
}

func (a *surfaceInteractiveRuntime) DetachWorkload(ctx context.Context, kind surfaceports.WorkloadKind, ownerUserID, workloadID string) error {
	switch kind {
	case surfaceports.WorkloadKindNative:
		if a.native == nil {
			return surfaceports.ErrContinuityNotFound
		}
		if _, err := a.native.Detach(ctx, ownerUserID, workloadID); err != nil {
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
	case errors.Is(err, ptydomain.ErrNotFound), errors.Is(err, nativedomain.ErrNotFound):
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
