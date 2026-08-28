package orchestration

import (
	"context"
	"errors"
	"fmt"

	appregistryapp "github.com/yangtao121/workos/internal/core/appregistry/application"
	appregistrydomain "github.com/yangtao121/workos/internal/core/appregistry/domain"
	artifactapp "github.com/yangtao121/workos/internal/core/artifact/application"
	artifactdomain "github.com/yangtao121/workos/internal/core/artifact/domain"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
)

// LaunchDescriptor is the neutral immutable launch fact the private resolver
// hands to runtime-host: the pinned registry identity plus the exact bundle
// artifact and its entrypoint. GrantedPermissions rides along as the
// installation's separate immutable grant snapshot re-read on every
// resolution — it is authorization input for the runtime's effective bridge
// capability computation, not part of the launch identity.
type LaunchDescriptor struct {
	AppID              string
	Version            string
	ManifestDigest     string
	ArtifactID         string
	ArtifactDigest     string
	Entrypoint         string
	GrantedPermissions []string
}

// ErrLaunchUnsupported marks an installed app whose pinned version has no
// supported web bundle descriptor. The surface path reports FailedPrecondition:
// the installation is healthy but not launchable in this slice.
var ErrLaunchUnsupported = errors.New("installed app version has no supported web bundle runtime")

// errLaunchCorrupt marks a broken immutable invariant during resolution — a
// manifest digest drift or an artifact that no longer matches its pinned
// digest. It is a sanitized Internal verdict, never a client input error.
var errLaunchCorrupt = errors.New("installed instance launch facts are inconsistent")

// IsLaunchCorrupt reports whether err is the sanitized resolution-corruption
// verdict. Transport maps it to a content-free Internal error.
func IsLaunchCorrupt(err error) bool {
	return errors.Is(err, errLaunchCorrupt)
}

// The resolver depends on narrow consumer-side interfaces; the composition
// root passes the concrete module application services, and tests pass
// fakes. This keeps orchestration the only place modules see each other.
type installationSource interface {
	ResolveActiveInstallation(ctx context.Context, ownerUserID, projectID, installationID string) (projectdomain.Installation, error)
}

type webBundleRegistry interface {
	ResolveWebBundle(ctx context.Context, ownerUserID, appID, version string) (appregistryapp.WebBundleResolution, error)
}

type webBundleArtifacts interface {
	VerifyWebBundle(ctx context.Context, ownerUserID, artifactID, digest string) (artifactapp.BundleSummary, error)
	ReadVerifiedWebBundleAsset(ctx context.Context, ownerUserID, artifactID, digest, path string) (artifactdomain.BundleFile, error)
}

// SurfaceLaunchResolver composes the authoritative module services behind the
// private resolver RPC. Every call re-resolves from installation, registry,
// and artifact facts; a runtime session snapshot is never an input.
type SurfaceLaunchResolver struct {
	installations installationSource
	apps          webBundleRegistry
	artifacts     webBundleArtifacts
}

func NewSurfaceLaunchResolver(
	installations installationSource,
	apps webBundleRegistry,
	artifacts webBundleArtifacts,
) (*SurfaceLaunchResolver, error) {
	if installations == nil || apps == nil || artifacts == nil {
		return nil, errors.New("surface launch resolver requires installation, registry, and artifact services")
	}
	return &SurfaceLaunchResolver{installations: installations, apps: apps, artifacts: artifacts}, nil
}

// ResolveWebBundle resolves one installed instance to its exact launch
// descriptor: active same-owner installation, exact pinned registry version,
// manifest digest equal to the installation snapshot, and a same-owner
// artifact carrying exactly the referenced digest.
func (r *SurfaceLaunchResolver) ResolveWebBundle(ctx context.Context, ownerUserID, projectID, appInstanceID string) (LaunchDescriptor, error) {
	installation, resolution, err := r.resolveFacts(ctx, ownerUserID, projectID, appInstanceID)
	if err != nil {
		return LaunchDescriptor{}, err
	}
	bundle, err := r.artifacts.VerifyWebBundle(ctx, ownerUserID, resolution.Ref.ArtifactID, resolution.Ref.ArtifactDigest)
	switch {
	case errors.Is(err, artifactdomain.ErrNotFound):
		return LaunchDescriptor{}, artifactdomain.ErrNotFound
	case errors.Is(err, artifactdomain.ErrDigestMismatch):
		// The manifest pins the digest and artifacts are immutable; a drift can
		// only be internal data corruption.
		return LaunchDescriptor{}, errLaunchCorrupt
	case errors.Is(err, artifactdomain.ErrInvalid):
		return LaunchDescriptor{}, artifactdomain.ErrInvalid
	case err != nil:
		return LaunchDescriptor{}, fmt.Errorf("verify launch artifact: %w", err)
	}
	return LaunchDescriptor{
		AppID: installation.AppID, Version: installation.Version,
		ManifestDigest: resolution.ManifestDigest,
		ArtifactID:     resolution.Ref.ArtifactID, ArtifactDigest: resolution.Ref.ArtifactDigest,
		Entrypoint:         bundle.Entrypoint,
		GrantedPermissions: installation.GrantedPermissions,
	}, nil
}

// ReadWebBundleAsset resolves the instance and returns exactly one bounded
// file of its pinned bundle. The empty path means the entrypoint.
func (r *SurfaceLaunchResolver) ReadWebBundleAsset(ctx context.Context, ownerUserID, projectID, appInstanceID, assetPath string) (artifactdomain.BundleFile, error) {
	_, resolution, err := r.resolveFacts(ctx, ownerUserID, projectID, appInstanceID)
	if err != nil {
		return artifactdomain.BundleFile{}, err
	}
	file, err := r.artifacts.ReadVerifiedWebBundleAsset(ctx, ownerUserID, resolution.Ref.ArtifactID, resolution.Ref.ArtifactDigest, assetPath)
	switch {
	case errors.Is(err, artifactdomain.ErrNotFound):
		return artifactdomain.BundleFile{}, artifactdomain.ErrNotFound
	case artifactapp.IsReferenceCorrupt(err):
		return artifactdomain.BundleFile{}, errLaunchCorrupt
	case errors.Is(err, artifactdomain.ErrInvalid):
		return artifactdomain.BundleFile{}, artifactdomain.ErrInvalid
	case err != nil:
		return artifactdomain.BundleFile{}, fmt.Errorf("read launch asset: %w", err)
	}
	return file, nil
}

// resolveFacts walks the authoritative chain shared by both RPCs: active
// installation, exact pinned registry version, and the manifest digest
// equality that proves the version is the one the installation pinned.
func (r *SurfaceLaunchResolver) resolveFacts(ctx context.Context, ownerUserID, projectID, appInstanceID string) (projectdomain.Installation, appregistryapp.WebBundleResolution, error) {
	if ownerUserID == "" {
		return projectdomain.Installation{}, appregistryapp.WebBundleResolution{}, projectdomain.ErrInvalid
	}
	installation, err := r.installations.ResolveActiveInstallation(ctx, ownerUserID, projectID, appInstanceID)
	if err != nil {
		return projectdomain.Installation{}, appregistryapp.WebBundleResolution{}, err
	}
	resolution, err := r.apps.ResolveWebBundle(ctx, ownerUserID, installation.AppID, installation.Version)
	switch {
	case errors.Is(err, appregistrydomain.ErrNotFound), errors.Is(err, appregistrydomain.ErrInvalid):
		return projectdomain.Installation{}, appregistryapp.WebBundleResolution{}, err
	case errors.Is(err, appregistryapp.ErrUnsupportedRuntime):
		return projectdomain.Installation{}, appregistryapp.WebBundleResolution{}, ErrLaunchUnsupported
	case err != nil:
		return projectdomain.Installation{}, appregistryapp.WebBundleResolution{}, fmt.Errorf("resolve registry launch descriptor: %w", err)
	}
	// The installation pinned this manifest digest at command time and the
	// version row is immutable; any drift is internal corruption.
	if resolution.ManifestDigest != installation.ManifestDigest {
		return projectdomain.Installation{}, appregistryapp.WebBundleResolution{}, errLaunchCorrupt
	}
	return installation, resolution, nil
}

// webBundleVerifier is the narrow artifact surface the Register-time check
// needs; the composition root passes the concrete artifact service.
type webBundleVerifier interface {
	VerifyWebBundle(ctx context.Context, ownerUserID, artifactID, artifactDigest string) (artifactapp.BundleSummary, error)
}

// ArtifactDirectory adapts the Artifact application service to the App
// Registry's neutral verification port for Register-time checks.
type ArtifactDirectory struct {
	artifacts webBundleVerifier
}

func NewArtifactDirectory(artifacts webBundleVerifier) (*ArtifactDirectory, error) {
	if artifacts == nil {
		return nil, errors.New("artifact directory requires the artifact service")
	}
	return &ArtifactDirectory{artifacts: artifacts}, nil
}

// VerifyWebBundle denies foreign or unknown artifacts without leaking their
// existence, and reports digest mismatches as invalid references.
func (d *ArtifactDirectory) VerifyWebBundle(ctx context.Context, ownerUserID, artifactID, artifactDigest string) error {
	_, err := d.artifacts.VerifyWebBundle(ctx, ownerUserID, artifactID, artifactDigest)
	switch {
	case errors.Is(err, artifactdomain.ErrNotFound):
		return appregistryapp.ErrArtifactDenied
	case errors.Is(err, artifactdomain.ErrDigestMismatch), errors.Is(err, artifactdomain.ErrInvalid):
		return appregistryapp.ErrArtifactDenied
	case err != nil:
		return fmt.Errorf("verify manifest artifact reference: %w", err)
	}
	return nil
}
