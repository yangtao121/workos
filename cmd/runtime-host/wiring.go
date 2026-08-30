package main

import (
	"context"
	"errors"
	"fmt"
	"time"

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
