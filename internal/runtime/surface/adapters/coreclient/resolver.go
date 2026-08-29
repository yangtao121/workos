// Package coreclient adapts the Surface Broker's neutral LaunchResolver port
// to the private Core SurfaceLaunchResolverService over Connect. It forwards
// the trusted owner/device identity from the context and maps transport
// verdicts to the port sentinels; it never imports Core packages.
package coreclient

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	surfacev1connect "github.com/yangtao121/workos/gen/go/workos/surface/v1/surfacev1connect"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/runtime/surface/ports"
)

// Resolver talks to Core's private resolver. Core is expected on the private
// loopback listener; the identity headers are re-set on every call so a
// spoofed upstream value can never survive.
type Resolver struct {
	client surfacev1connect.SurfaceLaunchResolverServiceClient
}

func New(client surfacev1connect.SurfaceLaunchResolverServiceClient) (*Resolver, error) {
	if client == nil {
		return nil, errors.New("surface resolver requires the Core resolver client")
	}
	return &Resolver{client: client}, nil
}

func (r *Resolver) ResolveWebBundle(ctx context.Context, query ports.ResolveQuery) (ports.LaunchDescriptor, error) {
	identityValue, err := identity.FromContext(ctx)
	if err != nil {
		return ports.LaunchDescriptor{}, err
	}
	request := connect.NewRequest(&surfacev1.ResolveWebBundleRequest{
		ProjectId: query.ProjectID, AppInstanceId: query.AppInstanceID,
	})
	request.Header().Set(identity.UserHeader, identityValue.UserID)
	request.Header().Set(identity.DeviceHeader, identityValue.DeviceID)
	response, err := r.client.ResolveWebBundle(ctx, request)
	if err != nil {
		return ports.LaunchDescriptor{}, mapError(err)
	}
	descriptor := response.Msg.GetLaunch()
	if descriptor == nil {
		return ports.LaunchDescriptor{}, fmt.Errorf("core resolver returned no descriptor")
	}
	return ports.LaunchDescriptor{
		AppID: descriptor.GetAppId(), Version: descriptor.GetVersion(),
		ManifestDigest: descriptor.GetManifestDigest(), ArtifactID: descriptor.GetArtifactId(),
		ArtifactDigest: descriptor.GetArtifactDigest(), Entrypoint: descriptor.GetEntrypoint(),
		GrantedPermissions: response.Msg.GetGrantedPermissions(),
		// The authoritative grant epoch rides with the same resolution as the
		// grant snapshot; the application layer refuses a value below 1
		// before anything is persisted (ADR-0003 §7).
		GrantRevision: response.Msg.GetGrantRevision(),
	}, nil
}

// ResolveSurfaceLaunch is the renderer-neutral resolution. The oneof response
// is projected onto the neutral port facts; a response without a launch side
// is a Core invariant failure surfaced as the sanitized corrupt verdict.
func (r *Resolver) ResolveSurfaceLaunch(ctx context.Context, query ports.ResolveQuery) (ports.ResolvedLaunch, error) {
	identityValue, err := identity.FromContext(ctx)
	if err != nil {
		return ports.ResolvedLaunch{}, err
	}
	request := connect.NewRequest(&surfacev1.ResolveSurfaceLaunchRequest{
		ProjectId: query.ProjectID, AppInstanceId: query.AppInstanceID,
	})
	request.Header().Set(identity.UserHeader, identityValue.UserID)
	request.Header().Set(identity.DeviceHeader, identityValue.DeviceID)
	response, err := r.client.ResolveSurfaceLaunch(ctx, request)
	if err != nil {
		return ports.ResolvedLaunch{}, mapError(err)
	}
	resolved := ports.ResolvedLaunch{
		GrantedPermissions: response.Msg.GetGrantedPermissions(),
		GrantRevision:      response.Msg.GetGrantRevision(),
	}
	switch typed := response.Msg.GetLaunch().(type) {
	case *surfacev1.ResolveSurfaceLaunchResponse_WebBundle:
		bundle := typed.WebBundle
		resolved.Kind = ports.LaunchKindWebBundle
		resolved.AppID = bundle.GetAppId()
		resolved.Version = bundle.GetVersion()
		resolved.ManifestDigest = bundle.GetManifestDigest()
		resolved.ArtifactID = bundle.GetArtifactId()
		resolved.ArtifactDigest = bundle.GetArtifactDigest()
		resolved.Entrypoint = bundle.GetEntrypoint()
		return resolved, nil
	case *surfacev1.ResolveSurfaceLaunchResponse_WebServiceContainer:
		container := typed.WebServiceContainer
		resolved.AppID = container.GetAppId()
		resolved.Version = container.GetVersion()
		resolved.ManifestDigest = container.GetManifestDigest()
		resolved.Kind = ports.LaunchKindWebServiceContainer
		resolved.Image = container.GetImage()
		resolved.Command = container.GetCommand()
		resolved.Port = int64(container.GetPort())
		if policy := container.GetResources(); policy != nil {
			resolved.Resources = ports.ContainerPolicy{
				CPUHardCores: policy.GetCpuHardCores(), MemoryHighMB: int64(policy.GetMemoryHighMb()),
				MemoryMaxMB: int64(policy.GetMemoryMaxMb()), PidsMax: int64(policy.GetPidsMax()),
			}
		}
		if policy := container.GetHealth(); policy != nil {
			resolved.Health = ports.HealthPolicy{
				HTTPPath: policy.GetHttpPath(), StartupSeconds: int64(policy.GetStartupSeconds()),
				RestartLimit: int64(policy.GetRestartLimit()),
			}
		}
		resolved.Route = container.GetSurfaceRoute()
		return resolved, nil
	default:
		return ports.ResolvedLaunch{}, fmt.Errorf("core surface resolver returned no launch descriptor")
	}
}

func (r *Resolver) ReadWebBundleAsset(ctx context.Context, query ports.AssetQuery) (ports.Asset, error) {
	identityValue, err := identity.FromContext(ctx)
	if err != nil {
		return ports.Asset{}, err
	}
	request := connect.NewRequest(&surfacev1.ReadWebBundleAssetRequest{
		ProjectId: query.ProjectID, AppInstanceId: query.AppInstanceID, AssetPath: query.AssetPath,
	})
	request.Header().Set(identity.UserHeader, identityValue.UserID)
	request.Header().Set(identity.DeviceHeader, identityValue.DeviceID)
	response, err := r.client.ReadWebBundleAsset(ctx, request)
	if err != nil {
		return ports.Asset{}, mapError(err)
	}
	return ports.Asset{
		Content: response.Msg.GetContent(), MediaType: response.Msg.GetMediaType(),
		Etag: response.Msg.GetEtag(),
	}, nil
}

// mapError converts Connect codes to the port sentinels. Unknown codes are
// infrastructure failures the application surfaces as sanitized Internal.
func mapError(err error) error {
	switch connect.CodeOf(err) {
	case connect.CodeNotFound:
		return ports.ErrResolverNotFound
	case connect.CodeFailedPrecondition:
		return ports.ErrResolverUnsupported
	case connect.CodeUnavailable, connect.CodeDeadlineExceeded, connect.CodeCanceled:
		return fmt.Errorf("%w: %s", ports.ErrResolverUnavailable, connect.CodeOf(err).String())
	default:
		return fmt.Errorf("core surface resolver call failed: %w", err)
	}
}
