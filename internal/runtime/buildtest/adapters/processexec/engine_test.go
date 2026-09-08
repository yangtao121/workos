//go:build engineexec

// Real-subprocess engine evidence runs behind the engineexec tag: the
// parallel unit suite shares one uid whose thread count varies with host
// load, while these tests prove kernel limits against live children. They
// run in make test-build-engine and the repair-buildtest gate.
package processexec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/runtime/buildtest/domain"
	"github.com/yangtao121/workos/internal/runtime/buildtest/ports"
)

func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	root := t.TempDir()
	// The dev/test host shares uid 1000 across many containers whose threads
	// count toward RLIMIT_NPROC; the dedicated bound stays kernel enforced.
	engine, err := New(Config{ScratchRoot: root, Processes: 65534})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	return engine
}

func TestEngineFactsAreHonest(t *testing.T) {
	facts := newTestEngine(t).Facts()
	if facts.Engine != "process" || facts.NetworkIsolated || facts.ImagePinned {
		t.Fatalf("process engine must report its reduced isolation honestly: %+v", facts)
	}
	for _, limit := range []string{"cpu-seconds", "address-space", "wall-clock", "output-bytes"} {
		found := false
		for _, enforced := range facts.EnforcedLimits {
			if enforced == limit {
				found = true
			}
		}
		if !found {
			t.Fatalf("engine facts must list the enforced limit %q", limit)
		}
	}
}

// The success path runs a real build and a real test over materialized
// candidate files through bash ulimit kernel limits.
func TestEngineSuccessRunsRealBuildAndTest(t *testing.T) {
	engine := newTestEngine(t)
	result, err := engine.Run(context.Background(), ports.RunSpec{
		BaseImage:    "golang:1.26.7-bookworm",
		BuildCommand: []string{"sh", "-c", "cat main.go > built.txt && echo built >> built.txt"},
		TestCommand:  []string{"sh", "-c", "grep -q repair built.txt"},
		Files: []domain.File{
			{Path: "main.go", Content: []byte("package main\n// repair candidate\nfunc main() {}\n")},
		},
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Failure != domain.FailureNone || result.Stage != domain.StageVerify {
		t.Fatalf("expected verify success, got stage=%s failure=%s log=%s", result.Stage, result.Failure, result.LogTail)
	}
	if result.BuildExitCode != 0 || result.TestExitCode != 0 {
		t.Fatalf("exit codes must be zero: build=%d test=%d", result.BuildExitCode, result.TestExitCode)
	}
}

func TestEngineTestFailureIsTerminal(t *testing.T) {
	engine := newTestEngine(t)
	result, err := engine.Run(context.Background(), ports.RunSpec{
		BaseImage:    "golang:1.26.7-bookworm",
		BuildCommand: []string{"true"},
		TestCommand:  []string{"false"},
		Files:        []domain.File{{Path: "go.mod", Content: []byte("module fixture\n")}},
		Timeout:      30 * time.Second,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Failure != domain.FailureTestFailed || result.Stage != domain.StageTest {
		t.Fatalf("expected test failure at test stage, got %+v", result)
	}
	if result.TestExitCode == 0 {
		t.Fatal("test exit code must be non-zero")
	}
}

func TestEngineBuildFailureNeverRunsTest(t *testing.T) {
	engine := newTestEngine(t)
	result, err := engine.Run(context.Background(), ports.RunSpec{
		BaseImage:    "golang:1.26.7-bookworm",
		BuildCommand: []string{"sh", "-c", "exit 7"},
		TestCommand:  []string{"sh", "-c", "echo should-not-run >&2; exit 0"},
		Files:        []domain.File{{Path: "go.mod", Content: []byte("module fixture\n")}},
		Timeout:      30 * time.Second,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Failure != domain.FailureBuildFailed || result.Stage != domain.StageBuild || result.BuildExitCode != 7 {
		t.Fatalf("expected build failure verdict, got %+v", result)
	}
	if strings.Contains(result.LogTail, "should-not-run") {
		t.Fatal("test stage must not run after a failed build")
	}
}

func TestEngineWallClockTimeoutIsEnforced(t *testing.T) {
	engine := newTestEngine(t)
	start := time.Now()
	result, err := engine.Run(context.Background(), ports.RunSpec{
		BaseImage:    "golang:1.26.7-bookworm",
		BuildCommand: []string{"sh", "-c", "sleep 30"},
		TestCommand:  []string{"true"},
		Files:        []domain.File{{Path: "go.mod", Content: []byte("module fixture\n")}},
		Timeout:      2 * time.Second,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Failure != domain.FailureTimeout {
		t.Fatalf("expected timeout verdict, got %+v", result)
	}
	if time.Since(start) > 15*time.Second {
		t.Fatal("timeout must bound the wall clock")
	}
}

func TestEngineOutputBudgetIsTerminal(t *testing.T) {
	engine := newTestEngine(t)
	result, err := engine.Run(context.Background(), ports.RunSpec{
		BaseImage:    "golang:1.26.7-bookworm",
		BuildCommand: []string{"sh", "-c", "yes flooding"},
		TestCommand:  []string{"true"},
		Files:        []domain.File{{Path: "go.mod", Content: []byte("module fixture\n")}},
		Timeout:      20 * time.Second,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Failure != domain.FailureOutputBudget {
		t.Fatalf("expected output budget verdict, got %+v", result)
	}
}

// The address-space ulimit is kernel enforced: a build trying to claim 8 GiB
// dies even though the wall clock has room.
func TestEngineAddressSpaceLimitIsKernelEnforced(t *testing.T) {
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("bash is required for the ulimit launcher")
	}
	engine := newTestEngine(t)
	result, err := engine.Run(context.Background(), ports.RunSpec{
		BaseImage:    "golang:1.26.7-bookworm",
		BuildCommand: []string{"sh", "-c", `ulimit -v; awk 'BEGIN{a="xxxxxxxx"; while(1) a=a a}' 2>/dev/null; exit 0`},
		TestCommand:  []string{"true"},
		Files:        []domain.File{{Path: "go.mod", Content: []byte("module fixture\n")}},
		Timeout:      30 * time.Second,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Failure == domain.FailureTimeout {
		t.Fatalf("unbounded memory must die by the kernel limit, not the wall clock: %+v", result)
	}
}

func TestEngineRejectsPathTraversalCandidates(t *testing.T) {
	engine := newTestEngine(t)
	result, err := engine.Run(context.Background(), ports.RunSpec{
		BaseImage:    "golang:1.26.7-bookworm",
		BuildCommand: []string{"true"},
		TestCommand:  []string{"true"},
		Files:        []domain.File{{Path: "../escape.txt", Content: []byte("no")}},
		Timeout:      10 * time.Second,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Failure != domain.FailureInputDrift || result.Stage != domain.StageMaterialize {
		t.Fatalf("expected materialize input drift, got %+v", result)
	}
}

func TestEngineMinimalEnvironmentHasNoProxy(t *testing.T) {
	engine := newTestEngine(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "probe.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(context.Background(), ports.RunSpec{
		BaseImage:    "golang:1.26.7-bookworm",
		BuildCommand: []string{"env"},
		TestCommand:  []string{"true"},
		Files:        []domain.File{{Path: "go.mod", Content: []byte("module fixture\n")}},
		Timeout:      30 * time.Second,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Failure != domain.FailureNone {
		t.Fatalf("env probe must succeed: %+v %s", result, result.LogTail)
	}
	for _, banned := range []string{"http_proxy=", "HTTP_PROXY=", "https_proxy=", "HTTPS_PROXY=", "NO_PROXY="} {
		if strings.Contains(result.LogTail, banned) {
			t.Fatalf("engine environment must not carry %s", banned)
		}
	}
	if !strings.Contains(result.LogTail, "GOPROXY=off") {
		t.Fatal("engine environment must disable implicit module fetches")
	}
}
