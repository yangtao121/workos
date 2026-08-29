package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	surfacev1connect "github.com/yangtao121/workos/gen/go/workos/surface/v1/surfacev1connect"
	appregistryapp "github.com/yangtao121/workos/internal/core/appregistry/application"
	appregistrydomain "github.com/yangtao121/workos/internal/core/appregistry/domain"
	artifactapp "github.com/yangtao121/workos/internal/core/artifact/application"
	artifactdomain "github.com/yangtao121/workos/internal/core/artifact/domain"
	"github.com/yangtao121/workos/internal/core/orchestration"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
	"github.com/yangtao121/workos/internal/platform/identity"
)

// resolverInstallations/resolverRegistry/resolverArtifacts are the minimal
// structural fakes for the unexported resolver interfaces; they let the wire
// handler be exercised without any database.

type resolverInstallations struct {
	installation projectdomain.Installation
}

func (f *resolverInstallations) ResolveActiveInstallation(context.Context, string, string, string) (projectdomain.Installation, error) {
	return f.installation, nil
}

type resolverRegistry struct{}

func (resolverRegistry) ResolveWebBundle(context.Context, string, string, string) (appregistryapp.WebBundleResolution, error) {
	return appregistryapp.WebBundleResolution{
		ManifestDigest: "sha256:" + resolverHex('a'),
		Ref: appregistrydomain.WebBundleRef{
			ArtifactID:     "0198d7ea-2110-7c42-b659-c5e4d73bc343",
			ArtifactDigest: "sha256:" + resolverHex('b'),
		},
	}, nil
}

type resolverArtifacts struct{}

func (resolverArtifacts) VerifyWebBundle(context.Context, string, string, string) (artifactapp.BundleSummary, error) {
	return artifactapp.BundleSummary{Entrypoint: "index.html"}, nil
}

func (resolverArtifacts) ReadVerifiedWebBundleAsset(context.Context, string, string, string, string) (artifactdomain.BundleFile, error) {
	return artifactdomain.BundleFile{}, nil
}

func resolverHex(char rune) string {
	value := make([]rune, 64)
	for index := range value {
		value[index] = char
	}
	return string(value)
}

// TestSurfaceResolverResponseCarriesGrantRevision pins the private wire
// contract of ADR-0003: ResolveWebBundle returns the authoritative grant
// epoch next to the grant set so runtime-host can persist both into the
// surface session.
func TestSurfaceResolverResponseCarriesGrantRevision(t *testing.T) {
	t.Parallel()
	resolver, err := orchestration.NewSurfaceLaunchResolver(
		&resolverInstallations{installation: projectdomain.Installation{
			ID: "0198d7ea-2110-7c42-b659-c5e4d73bc342", AppID: "notes", Version: "1.2.0",
			ManifestDigest:     "sha256:" + resolverHex('a'),
			GrantedPermissions: []string{"agent.task.run"},
			GrantRevision:      6,
		}},
		resolverRegistry{},
		resolverArtifacts{},
	)
	if err != nil {
		t.Fatal(err)
	}
	path, handler := NewSurfaceResolverConnectHandler(resolver)
	mux := http.NewServeMux()
	mux.Handle(path, identity.Middleware(handler))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := surfacev1connect.NewSurfaceLaunchResolverServiceClient(server.Client(), server.URL)
	request := connect.NewRequest(&surfacev1.ResolveWebBundleRequest{
		ProjectId: "0198d7ea-2110-7c42-b659-c5e4d73bc341", AppInstanceId: "0198d7ea-2110-7c42-b659-c5e4d73bc342",
	})
	request.Header().Set(identity.UserHeader, "owner-1")
	request.Header().Set(identity.DeviceHeader, "device-1")
	response, err := client.ResolveWebBundle(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response.Msg.GetGrantRevision() != 6 {
		t.Fatalf("resolver response must carry the authoritative grant revision, got %d", response.Msg.GetGrantRevision())
	}
	if len(response.Msg.GetGrantedPermissions()) != 1 || response.Msg.GetGrantedPermissions()[0] != "agent.task.run" {
		t.Fatalf("resolver response grant set drifted: %v", response.Msg.GetGrantedPermissions())
	}
}
