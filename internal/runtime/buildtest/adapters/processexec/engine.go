// Package processexec is the process-tier Build/Test engine (ADR-0026): a
// 0700 private working tree, kernel-enforced rlimits via bash ulimit, a hard
// wall-clock deadline with process-group SIGKILL, byte-capped output and a
// minimal environment with no proxy and no implicit module fetches. It
// honestly reports network_namespace=false and image_pinned=false; container
// grade isolation stays with the rootless engine on hosts that provide it.
package processexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/yangtao121/workos/internal/runtime/buildtest/domain"
	"github.com/yangtao121/workos/internal/runtime/buildtest/ports"
)

const (
	outputBudgetBytes = 1 << 20
	// Kernel-enforced defaults for one build/test run.
	cpuSeconds    = 900 // 15 minutes of CPU per stage
	addressSpace  = 4 << 30
	fileSizeBytes = 512 << 20
	processCount  = 256
	openFiles     = 1024
)

// Config sizes the sandbox. ScratchRoot must exist and be writable.
// Processes caps the kernel NPROC limit; hosts sharing one uid between many
// supervised processes raise it, the bound stays kernel enforced either way.
type Config struct {
	ScratchRoot string
	Processes   int
}

type Engine struct{ config Config }

func New(config Config) (*Engine, error) {
	if config.ScratchRoot == "" || !filepath.IsAbs(config.ScratchRoot) {
		return nil, errors.New("process engine requires an absolute scratch root")
	}
	if config.Processes <= 0 {
		config.Processes = processCount
	}
	return &Engine{config: config}, nil
}

func (e *Engine) Facts() ports.EngineFacts {
	return ports.EngineFacts{
		Engine:          "process",
		NetworkIsolated: false,
		ImagePinned:     false,
		EnforcedLimits: []string{
			"cpu-seconds", "address-space", "file-size", "processes",
			"open-files", "wall-clock", "output-bytes",
		},
	}
}

// Available verifies bash (the ulimit launcher) and a usable scratch root,
// creating the engine-owned root when missing. It never fabricates the
// toolchain: a missing build command still fails the run honestly.
func (e *Engine) Available(ctx context.Context) error {
	if _, err := exec.LookPath("bash"); err != nil {
		return ports.ErrEngineUnavailable
	}
	if info, err := os.Stat(e.config.ScratchRoot); err == nil {
		if !info.IsDir() {
			return ports.ErrEngineUnavailable
		}
		return nil
	}
	return os.MkdirAll(e.config.ScratchRoot, 0o700)
}

// ulimitScript is the kernel-enforced preamble applied to every stage.
func ulimitScript(processes int) string {
	return "ulimit -t " + strconv.Itoa(cpuSeconds) +
		"; ulimit -v " + strconv.Itoa(addressSpace) +
		"; ulimit -f " + strconv.Itoa(fileSizeBytes>>10) +
		"; ulimit -u " + strconv.Itoa(processes) +
		"; ulimit -n " + strconv.Itoa(openFiles) +
		`; exec "$@"`
}

func (e *Engine) Run(ctx context.Context, spec ports.RunSpec) (ports.RunResult, error) {
	facts := e.Facts()
	result := ports.RunResult{Facts: facts}
	if err := domain.ValidatePayload(domain.Payload{BaseImage: spec.BaseImage, BuildCmd: spec.BuildCommand, TestCmd: spec.TestCommand, Files: spec.Files}); err != nil {
		return ports.RunResult{Facts: facts, Stage: domain.StageMaterialize, Failure: domain.FailureInputDrift}, nil
	}
	if err := e.Available(ctx); err != nil {
		return ports.RunResult{}, err
	}
	directory, err := os.MkdirTemp(e.config.ScratchRoot, "build-")
	if err != nil {
		return ports.RunResult{}, fmt.Errorf("scratch directory: %w", err)
	}
	defer os.RemoveAll(directory)
	if err := os.Chmod(directory, 0o700); err != nil {
		return ports.RunResult{}, fmt.Errorf("scratch permissions: %w", err)
	}
	if err := materialize(directory, spec.Files); err != nil {
		return ports.RunResult{Facts: facts, Stage: domain.StageMaterialize, Failure: domain.FailureInputDrift, LogTail: domain.SanitizeLogTail([]byte(err.Error()))}, nil
	}
	result.Stage = domain.StageBuild
	buildExit, buildLog, buildErr := e.runStage(ctx, directory, spec.BuildCommand, spec.Timeout)
	result.LogTail = domain.SanitizeLogTail(buildLog)
	if buildErr != nil {
		if errors.Is(buildErr, context.DeadlineExceeded) || errors.Is(buildErr, context.Canceled) {
			result.Failure = domain.FailureTimeout
			return result, nil
		}
		if errors.Is(buildErr, errOutputBudget) {
			result.Failure = domain.FailureOutputBudget
			return result, nil
		}
		return ports.RunResult{}, buildErr
	}
	result.BuildExitCode = buildExit
	if buildExit != 0 {
		result.Failure = domain.FailureBuildFailed
		return result, nil
	}
	result.Stage = domain.StageTest
	testExit, testLog, testErr := e.runStage(ctx, directory, spec.TestCommand, spec.Timeout)
	result.LogTail = domain.SanitizeLogTail(append(buildLog, append([]byte("\n"), testLog...)...))
	if testErr != nil {
		if errors.Is(testErr, context.DeadlineExceeded) || errors.Is(testErr, context.Canceled) {
			result.Failure = domain.FailureTimeout
			return result, nil
		}
		if errors.Is(testErr, errOutputBudget) {
			result.Failure = domain.FailureOutputBudget
			return result, nil
		}
		return ports.RunResult{}, testErr
	}
	result.TestExitCode = testExit
	if testExit != 0 {
		result.Failure = domain.FailureTestFailed
		return result, nil
	}
	result.Stage = domain.StageVerify
	result.Failure = domain.FailureNone
	return result, nil
}

var errOutputBudget = errors.New("output budget exceeded")

// runStage executes one argv through the ulimit launcher with a private
// environment. A non-zero exit is a terminal verdict, never an error.
func (e *Engine) runStage(ctx context.Context, directory string, command []string, timeout time.Duration) (int32, []byte, error) {
	stageCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	commandCtx, stop := context.WithCancelCause(stageCtx)
	defer stop(nil)
	argv := append([]string{"bash", "-c", ulimitScript(e.config.Processes), "stage"}, command...)
	process := exec.CommandContext(commandCtx, argv[0], argv[1:]...)
	process.Dir = directory
	// Go refuses modules under its temp root, so the sandbox keeps TMPDIR
	// in a dedicated subdirectory rather than the module root itself.
	tempDir := filepath.Join(directory, ".tmp")
	_ = os.Mkdir(tempDir, 0o700)
	process.Env = []string{
		"HOME=" + directory,
		"TMPDIR=" + tempDir,
		"PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin",
		"LANG=C.UTF-8",
		"TZ=UTC",
		"GOPROXY=off",

		"GOCACHE=" + filepath.Join(directory, ".gocache"),
		"GOMODCACHE=" + filepath.Join(directory, ".gomodcache"),
		"GOTMPDIR=" + directory,
	}
	process.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	process.Cancel = func() error { return killGroup(process) }
	process.WaitDelay = time.Second
	process.Stdin = bytes.NewReader(nil)
	var combined bytes.Buffer
	capped := &cappedWriter{sink: &combined, budget: outputBudgetBytes}
	process.Stdout = capped
	process.Stderr = capped
	if err := process.Start(); err != nil {
		return 0, combined.Bytes(), fmt.Errorf("start stage: %w", err)
	}
	waitErr := process.Wait()
	if capped.exceeded {
		// The budget is a terminal verdict even if the process exited zero.
		_ = killGroup(process)
		return 0, combined.Bytes(), errOutputBudget
	}
	if commandCtx.Err() != nil {
		return 0, combined.Bytes(), context.Cause(commandCtx)
	}
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			return int32(exitErr.ExitCode()), combined.Bytes(), nil
		}
		return 0, combined.Bytes(), fmt.Errorf("wait stage: %w", waitErr)
	}
	return 0, combined.Bytes(), nil
}

func killGroup(process *exec.Cmd) error {
	if process.Process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-process.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

// materialize writes the candidate tree: regular files only, bounded depth,
// explicit modes, inside the private directory.
func materialize(directory string, files []domain.File) error {
	for _, file := range files {
		if !domain.ValidFilePath(file.Path) {
			return fmt.Errorf("candidate path rejected")
		}
		target := filepath.Join(directory, file.Path)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return fmt.Errorf("create directory: %w", err)
		}
		mode := os.FileMode(0o600)
		if file.Executable {
			mode = 0o700
		}
		if err := os.WriteFile(target, file.Content, mode); err != nil {
			return fmt.Errorf("write file: %w", err)
		}
	}
	return nil
}

type cappedWriter struct {
	sink     io.Writer
	budget   int
	written  int
	exceeded bool
}

func (w *cappedWriter) Write(chunk []byte) (int, error) {
	if w.written >= w.budget {
		w.exceeded = true
		return len(chunk), nil
	}
	if w.written+len(chunk) > w.budget {
		kept := w.budget - w.written
		w.written = w.budget
		w.exceeded = true
		if _, err := w.sink.Write(chunk[:kept]); err != nil {
			return len(chunk), err
		}
		return len(chunk), nil
	}
	w.written += len(chunk)
	return w.sink.Write(chunk)
}
