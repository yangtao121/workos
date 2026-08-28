package transport

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"

	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	surfacev1connect "github.com/yangtao121/workos/gen/go/workos/surface/v1/surfacev1connect"
	appregistrydomain "github.com/yangtao121/workos/internal/core/appregistry/domain"
	appregistryports "github.com/yangtao121/workos/internal/core/appregistry/ports"
	artifactdomain "github.com/yangtao121/workos/internal/core/artifact/domain"
	artifactports "github.com/yangtao121/workos/internal/core/artifact/ports"
	"github.com/yangtao121/workos/internal/core/orchestration"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
	projectports "github.com/yangtao121/workos/internal/core/project/ports"
	"github.com/yangtao121/workos/internal/platform/identity"
)

// SurfaceResolverHandler exposes the private Core launch resolver. It is
// never registered on the gateway allowlist; only runtime-host reaches it on
// the private listener with forwarded trusted identity.
type SurfaceResolverHandler struct {
	resolver *orchestration.SurfaceLaunchResolver
}

func NewSurfaceResolver(resolver *orchestration.SurfaceLaunchResolver) *SurfaceResolverHandler {
	return &SurfaceResolverHandler{resolver: resolver}
}

// NewSurfaceResolverConnectHandler wires the private resolver transport. The
// read limit is small: these RPCs never carry bundle bytes out (only single
// bounded assets up to 512 KiB).
func NewSurfaceResolverConnectHandler(resolver *orchestration.SurfaceLaunchResolver) (string, http.Handler) {
	return surfacev1connect.NewSurfaceLaunchResolverServiceHandler(
		NewSurfaceResolver(resolver),
		connect.WithReadMaxBytes(4*1024),
	)
}

func (h *SurfaceResolverHandler) ResolveWebBundle(ctx context.Context, req *connect.Request[surfacev1.ResolveWebBundleRequest]) (*connect.Response[surfacev1.ResolveWebBundleResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	descriptor, err := h.resolver.ResolveWebBundle(ctx, id.UserID, req.Msg.GetProjectId(), req.Msg.GetAppInstanceId())
	if err != nil {
		return nil, mapResolverError(err)
	}
	return connect.NewResponse(&surfacev1.ResolveWebBundleResponse{
		Launch: &surfacev1.WebBundleLaunchDescriptor{
			AppId: descriptor.AppID, Version: descriptor.Version,
			ManifestDigest: descriptor.ManifestDigest,
			ArtifactId:     descriptor.ArtifactID, ArtifactDigest: descriptor.ArtifactDigest,
			Entrypoint: descriptor.Entrypoint,
		},
	}), nil
}

func (h *SurfaceResolverHandler) ReadWebBundleAsset(ctx context.Context, req *connect.Request[surfacev1.ReadWebBundleAssetRequest]) (*connect.Response[surfacev1.ReadWebBundleAssetResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	file, err := h.resolver.ReadWebBundleAsset(ctx, id.UserID, req.Msg.GetProjectId(), req.Msg.GetAppInstanceId(), req.Msg.GetAssetPath())
	if err != nil {
		return nil, mapResolverError(err)
	}
	return connect.NewResponse(&surfacev1.ReadWebBundleAssetResponse{
		Content: file.Content, MediaType: file.MediaType, Etag: file.FileDigest,
	}), nil
}

// mapResolverError converts resolution verdicts to sanitized Connect codes:
// no SQL, constraint names, manifests, or bundle content. A temporarily
// unreachable Project/Registry/Artifact store is Unavailable so runtime-host
// can fail its own path with 503 instead of mistaking an outage for a
// missing instance; digest drift and stored-fact corruption stay Internal.
func mapResolverError(err error) error {
	switch {
	case errors.Is(err, projectdomain.ErrInvalid), errors.Is(err, appregistrydomain.ErrInvalid), errors.Is(err, artifactdomain.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("installed instance reference is invalid"))
	case errors.Is(err, projectdomain.ErrNotFound), errors.Is(err, appregistrydomain.ErrNotFound), errors.Is(err, artifactdomain.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("installed instance is not available"))
	case errors.Is(err, orchestration.ErrLaunchUnsupported):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("installed app version has no supported web bundle"))
	case errors.Is(err, projectports.ErrStoreUnavailable), errors.Is(err, appregistryports.ErrStoreUnavailable), errors.Is(err, artifactports.ErrStoreUnavailable):
		return connect.NewError(connect.CodeUnavailable, errors.New("surface resolution is temporarily unavailable"))
	case orchestration.IsLaunchCorrupt(err):
		return connect.NewError(connect.CodeInternal, errors.New("surface resolution failed"))
	default:
		return connect.NewError(connect.CodeInternal, errors.New("surface resolution failed"))
	}
}
