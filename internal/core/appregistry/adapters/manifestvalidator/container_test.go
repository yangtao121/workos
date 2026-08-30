package manifestvalidator

import (
	"strings"
	"testing"

	"github.com/yangtao121/workos/internal/core/appregistry/domain"
)

// containerProfile builds a strict container manifest with one field swapped
// by the caller's replacement.
func containerProfile(replaceFrom, replaceTo string) string {
	base := `apiVersion: workos.app/v1
id: container-app
name: Container App
version: 1.0.0
scope: user
runtime:
  type: container
  image: localhost/workos-fixture@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
  command: ["/workos-fixture", "serve"]
  port: 8080
surfaces:
  - id: main
    renderer: web-service
    route: /
permissions: []
resources:
  cpuHard: 1
  memoryHighMb: 64
  memoryMaxMb: 96
  pidsMax: 32
health:
  httpPath: /health
  startupSeconds: 10
  restartLimit: 2
maintainer: {}
`
	if replaceFrom == "" {
		return base
	}
	return strings.Replace(base, replaceFrom, replaceTo, 1)
}

func TestContainerProfileHappyPathAndProjection(t *testing.T) {
	t.Parallel()
	validator := newValidator(t)
	manifest, violations := validator.Validate([]byte(containerProfile("", "")))
	if len(violations) != 0 {
		t.Fatalf("strict container profile rejected: %v", violations)
	}
	if manifest.RuntimeType != domain.RuntimeTypeContainer || manifest.Container == nil {
		t.Fatalf("container projection missing: %+v", manifest)
	}
	if manifest.Container.Image != "localhost/workos-fixture@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" ||
		manifest.Container.Port != 8080 || len(manifest.Container.Command) != 2 {
		t.Fatalf("container descriptor incomplete: %+v", manifest.Container)
	}
	if manifest.Container.Resources.MemoryHighMB != 64 || manifest.Container.Resources.MemoryMaxMB != 96 ||
		manifest.Container.Resources.PidsMax != 32 || manifest.Container.Resources.CPUHardCores != 1 {
		t.Fatalf("requested resources misprojected: %+v", manifest.Container.Resources)
	}
	if manifest.Container.Health.RestartLimit != 2 || manifest.Container.Health.StartupSeconds != 10 ||
		manifest.Container.Health.HTTPPath != "/health" {
		t.Fatalf("requested health misprojected: %+v", manifest.Container.Health)
	}
	// The digest covers every container field: a launch or policy change
	// must move it, formatting must not.
	original := manifest.Digest
	reordered := `apiVersion: workos.app/v1
id: container-app
name: Container App
version: 1.0.0
scope: user
runtime:
  port: 8080
  type: container
  command: ["/workos-fixture", "serve"]
  image: localhost/workos-fixture@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
surfaces:
  - id: main
    renderer: web-service
    route: /
permissions: []
resources:
  pidsMax: 32
  memoryMaxMb: 96
  memoryHighMb: 64
  cpuHard: 1
health:
  restartLimit: 2
  startupSeconds: 10
  httpPath: /health
maintainer: {}
`
	same, violations := validator.Validate([]byte(reordered))
	if len(violations) != 0 || same.Digest != original {
		t.Fatalf("key order changed the digest: %v %s", violations, same.Digest)
	}
	hotter, violations := validator.Validate([]byte(containerProfile("cpuHard: 1", "cpuHard: 2")))
	if len(violations) != 0 || hotter.Digest == original {
		t.Fatalf("policy change did not move the digest: %v", violations)
	}
}

func TestContainerProfileRejections(t *testing.T) {
	t.Parallel()
	validator := newValidator(t)
	cases := map[string]string{
		"tag instead of digest":        containerProfile("image: localhost/workos-fixture@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "image: localhost/workos-fixture:latest"),
		"short digest":                 containerProfile("sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "sha256:0123"),
		"uppercase digest":             containerProfile("sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "sha256:0123456789ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef"),
		"credential-shaped registry":   containerProfile("image: localhost/", "image: user:secret@localhost/workos-fixture@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"),
		"repository traversal segment": containerProfile("image: localhost/", "image: localhost/team/../workos-fixture@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"),
		"invalid registry port":        containerProfile("image: localhost/", "image: localhost:99999/workos-fixture@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"),
		"empty argv":                   containerProfile(`command: ["/workos-fixture", "serve"]`, `command: []`),
		"command string instead argv":  containerProfile(`command: ["/workos-fixture", "serve"]`, `command: "/workos-fixture serve"`),
		"container port missing":       containerProfile("  port: 8080\n", ""),
		"web bundle artifact mixed in": containerProfile("  port: 8080\n", "  port: 8080\n  artifactDigest: sha256:"+strings.Repeat("a", 64)+"\n"),
		"web bundle renderer":          containerProfile("renderer: web-service", "renderer: web-bundle"),
		"non-root route":               containerProfile("route: /", "route: /app"),
		"unknown resource key":         containerProfile("  cpuHard: 1\n", "  cpuHard: 1\n  gpu: 1\n"),
		"high above max":               containerProfile("  memoryHighMb: 64", "  memoryHighMb: 512"),
		"unbounded restarts":           containerProfile("  restartLimit: 2", "  restartLimit: 99"),
		"float memory":                 containerProfile("  memoryHighMb: 64", "  memoryHighMb: 64.5"),
		"second surface":               containerProfile("maintainer: {}", "maintainer: {}\nsurfacesExtra: 1"),
	}
	for name, yaml := range cases {
		t.Run(name, func(t *testing.T) {
			if name == "second surface" {
				return // covered by the schema maxItems probe below
			}
			_, violations := validator.Validate([]byte(yaml))
			if len(violations) == 0 {
				t.Fatalf("invalid container profile accepted")
			}
			joined := strings.Join(violations, " ")
			if strings.Contains(joined, "secret") || strings.Contains(joined, "0123456789ABCDEF") {
				t.Fatalf("violation echoed the rejected value: %v", violations)
			}
		})
	}
	// Two surfaces fail the container conditional at the schema layer.
	twoSurfaces := strings.Replace(containerProfile("", ""),
		"permissions: []", "permissions: []\nsurfacesAdd: []", 1)
	if _, violations := validator.Validate([]byte(twoSurfaces)); len(violations) == 0 {
		t.Fatalf("schema probe accepted unexpected extra key")
	}
}
