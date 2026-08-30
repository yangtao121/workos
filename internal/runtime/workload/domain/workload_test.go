package domain

import (
	"strings"
	"testing"
)

func TestValidImageRejectsNonCanonicalRepositoryPaths(t *testing.T) {
	t.Parallel()
	digest := "@sha256:" + strings.Repeat("a", 64)
	valid := []string{
		"localhost/workos-fixture" + digest,
		"registry.example:5000/team/workos_fixture.v1" + digest,
	}
	for _, image := range valid {
		if !ValidImage(image) {
			t.Errorf("ValidImage(%q) rejected a canonical reference", image)
		}
	}
	invalid := []string{
		"localhost/team//fixture" + digest,
		"localhost/team/../fixture" + digest,
		"localhost/Team/fixture" + digest,
		"localhost/team/fixture latest" + digest,
		"localhost/team/fixture:latest" + digest,
	}
	for _, image := range invalid {
		if ValidImage(image) {
			t.Errorf("ValidImage(%q) accepted a non-canonical reference", image)
		}
	}
}

func TestValidDescriptorRequiresCanonicalVersion(t *testing.T) {
	t.Parallel()
	policy := RequestedPolicy{
		CPUHardCores: 1, MemoryHighMB: 64, MemoryMaxMB: 96, PidsMax: 32,
		HTTPPath: "/health", StartupSeconds: 10, RestartLimit: 2,
	}
	image := "localhost/workos-fixture@sha256:" + strings.Repeat("a", 64)
	digest := "sha256:" + strings.Repeat("b", 64)
	for _, version := range []string{"v1.0", "01.0.0", "1.0.0-.rc1", "1.0.0+build"} {
		if ValidDescriptor("notes-app", version, digest, image, []string{"/app"}, 8080, policy) {
			t.Errorf("ValidDescriptor accepted version %q", version)
		}
	}
	if !ValidDescriptor("notes-app", "1.0.0-rc.1", digest, image, []string{"/app"}, 8080, policy) {
		t.Fatal("ValidDescriptor rejected a canonical prerelease")
	}
	// The manifest schema/Core parser place no independent 32-byte cap on a
	// prerelease identifier (the canonical manifest itself is bounded). The
	// runtime must accept the same language instead of inventing a narrower
	// cross-process contract.
	longPrerelease := "1.0.0-" + strings.Repeat("a", 64)
	if !ValidDescriptor("notes-app", longPrerelease, digest, image, []string{"/app"}, 8080, policy) {
		t.Fatal("ValidDescriptor diverged from Core on a canonical long prerelease")
	}
}

func TestRequestedPolicyRejectsCPUBelowCanonicalMinimum(t *testing.T) {
	t.Parallel()
	policy := RequestedPolicy{
		CPUHardCores: MinCPUHardCores - 0.01, MemoryHighMB: 64, MemoryMaxMB: 96, PidsMax: 32,
		HTTPPath: "/health", StartupSeconds: 10, RestartLimit: 2,
	}
	if policy.Valid() {
		t.Fatal("sub-minimum CPU request was accepted")
	}
	policy.CPUHardCores = MinCPUHardCores
	if !policy.Valid() {
		t.Fatal("canonical minimum CPU request was rejected")
	}
}

func TestBoundaryStringsRequireCanonicalUTF8AndEndpoint(t *testing.T) {
	t.Parallel()
	invalidUTF8 := string([]byte{0xff})
	if ValidCommand([]string{invalidUTF8}) || ValidOperationKey(invalidUTF8) {
		t.Fatal("invalid UTF-8 crossed a canonical string boundary")
	}
	for _, endpoint := range []string{"127.0.0.1:+80", "127.0.0.1:080", "127.0.0.1:0", "localhost:80"} {
		if ValidLoopbackEndpoint(endpoint) {
			t.Errorf("ValidLoopbackEndpoint(%q) accepted a non-canonical endpoint", endpoint)
		}
	}
	if !ValidLoopbackEndpoint("127.0.0.1:8080") {
		t.Fatal("canonical loopback endpoint was rejected")
	}
}
