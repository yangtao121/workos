package dockerbuild

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/runtime/buildtest/domain"
	"github.com/yangtao121/workos/internal/runtime/buildtest/ports"
)

// The real-engine test runs only when a Docker socket and the pinned
// toolchain image are explicitly provided (gate/host evidence, ADR-0033).
// The pinned digest below is the recorded RepoDigest of
// golang:1.26.7-bookworm from the C00 probe.
const probeImageDigest = "golang@sha256:e8c859f5632dcfde7b32d2012b4351728f6437930887c2f6a91ea242459e5514"

func realDockerEngine(t *testing.T) *Engine {
	t.Helper()
	if os.Getenv("WORKOS_TEST_DOCKER_BUILD") == "" {
		t.Skip("WORKOS_TEST_DOCKER_BUILD not set; real engine run disabled")
	}
	socket := os.Getenv("WORKOS_TEST_DOCKER_SOCKET")
	if socket == "" {
		socket = "/var/run/docker.sock"
	}
	if _, err := os.Stat(socket); err != nil {
		t.Fatalf("required docker socket %s not reachable: %v", socket, err)
	}
	engine, err := New(Config{Socket: socket})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	if err := engine.Available(context.Background()); err != nil {
		t.Fatalf("required docker daemon unavailable: %v", err)
	}
	return engine
}

func realSpec(t *testing.T) ports.RunSpec {
	t.Helper()
	root := t.TempDir()
	wd, err := os.Getwd()
	if err == nil {
		for dir := wd; ; dir = filepath.Dir(dir) {
			if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
				_ = os.MkdirAll(filepath.Join(dir, "tmp"), 0o755)
				if scratch, mkErr := os.MkdirTemp(filepath.Join(dir, "tmp"), "dockerbuild-real-"); mkErr == nil {
					root = scratch
					t.Cleanup(func() { _ = os.RemoveAll(scratch) })
				}
				break
			}
			if filepath.Dir(dir) == dir {
				break
			}
		}
	}
	return ports.RunSpec{
		ScratchRoot:  root,
		BaseImage:    probeImageDigest,
		BuildCommand: []string{"sh", "-c", "mkdir -p dist && printf '#!/bin/sh\\necho real-bundle\\n' > dist/server && chmod 755 dist/server"},
		TestCommand:  []string{"sh", "-c", "test -x dist/server && grep -q real-bundle dist/server"},
		Files: []domain.File{
			{Path: "go.mod", Content: []byte("module fixture\n\ngo 1.26\n")},
		},
		OutputDirectory: "dist",
		RuntimeCommand:  []string{"./server"},
		Timeout:         5 * time.Minute,
	}
}

func TestRealEngineFreezesVerifiedOutput(t *testing.T) {
	engine := realDockerEngine(t)
	spec := realSpec(t)
	result, err := engine.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("real run: %v", err)
	}
	if result.Failure != domain.FailureNone || result.OutputDir == "" {
		t.Fatalf("expected verified output: stage=%s failure=%s log=%s", result.Stage, result.Failure, result.LogTail)
	}
	if !result.Facts.ImagePinned || !result.Facts.NetworkIsolated {
		t.Fatalf("real run must report enforced isolation: %+v", result.Facts)
	}
	server := filepath.Join(result.OutputDir, "server")
	info, err := os.Stat(server)
	if err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("frozen output must contain the executable entrypoint: %v", err)
	}
	// The scratch tree survives the run for the service-side freeze.
	if _, err := os.Stat(spec.ScratchRoot); err != nil {
		t.Fatalf("engine must leave the job tree in place: %v", err)
	}
}

func TestRealEngineBuildFailureYieldsNoOutput(t *testing.T) {
	engine := realDockerEngine(t)
	spec := realSpec(t)
	spec.BuildCommand = []string{"sh", "-c", "echo refuses >&2; exit 3"}
	result, err := engine.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("real run: %v", err)
	}
	if result.Failure != domain.FailureBuildFailed || result.OutputDir != "" || result.BuildExitCode != 3 {
		t.Fatalf("failing build must not yield output: %+v", result)
	}
}

func TestRealEngineFailureMatrix(t *testing.T) {
	engine := realDockerEngine(t)
	for _, tc := range []struct {
		name, build, test string
		failure           domain.FailureReason
		timeout           time.Duration
	}{
		{name: "missing-output", build: "true", failure: domain.FailureOutputFailed},
		{name: "empty-output", build: "mkdir dist", failure: domain.FailureOutputFailed},
		{name: "missing-entrypoint", build: "mkdir dist; echo data > dist/other", failure: domain.FailureOutputFailed},
		{name: "file-count", build: "mkdir dist; echo real-bundle > dist/server; chmod 755 dist/server; i=0; while [ $i -lt 1024 ]; do echo x > dist/f$i; i=$((i+1)); done", failure: domain.FailureOutputFailed},
		{name: "single-file-limit", build: "mkdir dist; echo real-bundle > dist/server; truncate -s 33M dist/server; chmod 755 dist/server", failure: domain.FailureOutputFailed},
		{name: "total-limit", build: "mkdir dist; echo real-bundle > dist/server; chmod 755 dist/server; for i in 1 2 3 4 5; do dd if=/dev/zero of=dist/f$i bs=1M count=27 2>/dev/null; done", failure: domain.FailureOutputFailed},
		{name: "test-nonzero", test: "exit 7", failure: domain.FailureTestFailed},
		{name: "timeout", build: "sleep 30", failure: domain.FailureTimeout, timeout: 5 * time.Second},
		{name: "log-budget", build: "yes budget", failure: domain.FailureOutputBudget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := realSpec(t)
			if tc.build != "" {
				spec.BuildCommand = []string{"sh", "-c", tc.build}
			}
			if tc.test != "" {
				spec.TestCommand = []string{"sh", "-c", tc.test}
			}
			if tc.timeout != 0 {
				spec.Timeout = tc.timeout
			}
			result, err := engine.Run(context.Background(), spec)
			if err != nil || result.Failure != tc.failure || result.OutputDir != "" {
				t.Fatalf("failure must leave no deployable output: %+v, %v", result, err)
			}
			if tc.name == "test-nonzero" && (result.BuildExitCode != 0 || result.TestExitCode != 7) {
				t.Fatalf("incorrect build/test exit facts: %+v", result)
			}
		})
	}
}
