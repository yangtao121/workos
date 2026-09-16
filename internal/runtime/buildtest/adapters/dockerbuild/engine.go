// Package dockerbuild is the container-tier Build/Test engine (ADR-0033):
// each stage runs in a short-lived container from a digest-pinned toolchain
// image with network none, dropped capabilities, no-new-privileges, a
// read-only rootfs with a bounded tmpfs, cgroup memory/pids/cpu limits and a
// hard wall-clock deadline. The container create path never pulls images; a
// missing local image is a fail-closed engine error. A run only reaches the
// verify stage when build and test both exited zero, and only then does the
// engine leave the declared build output in place for the bundle freeze.
package dockerbuild

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yangtao121/workos/internal/platform/containerprocess"
	"github.com/yangtao121/workos/internal/runtime/buildtest/domain"
	"github.com/yangtao121/workos/internal/runtime/buildtest/ports"
)

const (
	apiVersion    = "v1.47"
	logBudget     = 1 << 20
	stageMemory   = int64(4 << 30)
	stagePids     = int64(512)
	stageNanoCPUs = int64(2_000_000_000)
	tmpfsSize     = "size=536870912"
)

// Config sizes the sandbox. Socket is the Docker Engine unix socket path;
// Memory, Pids and NanoCPUs override the per-stage cgroup defaults.
type Config struct {
	Socket   string
	Memory   int64
	Pids     int64
	NanoCPUs int64
}

type Engine struct {
	client  *http.Client
	baseURL string
	config  Config
	// verified refs cache the digest-readback result per image reference.
	verified map[string]bool
}

func New(config Config) (*Engine, error) {
	if config.Socket == "" {
		return nil, errors.New("docker build engine requires a socket path")
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", config.Socket)
		},
	}
	return newEngine(config, &http.Client{Transport: transport}, "http://docker/"+apiVersion), nil
}

// newEngine allows tests to drive the same control flow against a fake
// Engine API endpoint; production code always goes through New.
func newEngine(config Config, client *http.Client, baseURL string) *Engine {
	if config.Memory <= 0 {
		config.Memory = stageMemory
	}
	if config.Pids <= 0 {
		config.Pids = stagePids
	}
	if config.NanoCPUs <= 0 {
		config.NanoCPUs = stageNanoCPUs
	}
	return &Engine{client: client, baseURL: baseURL, config: config, verified: map[string]bool{}}
}

func (e *Engine) Facts() ports.EngineFacts {
	// image_pinned is only ever claimed per-run after the digest readback;
	// the static identity stays honest.
	return ports.EngineFacts{
		Engine:          "docker",
		NetworkIsolated: true,
		ImagePinned:     false,
		EnforcedLimits: []string{
			"memory-max", "pids-max", "cpu-max", "read-only-rootfs",
			"no-new-privileges", "cap-drop-all", "network-none",
			"wall-clock", "output-bytes",
		},
	}
}

// Available verifies the Docker socket answers and the scratch root is
// usable. It never fabricates the toolchain image: per-run creation fails
// closed when the pinned digest is not present locally.
func (e *Engine) Available(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", e.baseURL+"/_ping", nil)
	if err != nil {
		return ports.ErrEngineUnavailable
	}
	response, err := e.client.Do(req)
	if err != nil || response.StatusCode != 200 {
		if response != nil {
			response.Body.Close()
		}
		return ports.ErrEngineUnavailable
	}
	response.Body.Close()
	return nil
}

func (e *Engine) call(ctx context.Context, method, endpoint string, input, output any) error {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, e.baseURL+endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := e.client.Do(req)
	if err != nil {
		return errEngineIO
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return &apiError{status: response.StatusCode, detail: string(detail)}
	}
	if output != nil {
		return json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(output)
	}
	return nil
}

type apiError struct {
	status int
	detail string
}

func (a *apiError) Error() string {
	return fmt.Sprintf("docker api status %d: %s", a.status, a.detail)
}

func (a *apiError) notFound() bool { return a.status == 404 }

var errEngineIO = errors.New("docker engine io failed")

// Run executes build then test in fresh containers and, when both succeed,
// verifies the declared output subtree and the runtime entrypoint, leaving
// the output in spec.ScratchRoot for the service-side bundle freeze.
func (e *Engine) Run(ctx context.Context, spec ports.RunSpec) (ports.RunResult, error) {
	ctx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()
	facts := e.Facts()
	result := ports.RunResult{Facts: facts}
	payload := domain.Payload{
		BaseImage: spec.BaseImage, BuildCmd: spec.BuildCommand, TestCmd: spec.TestCommand,
		Files: spec.Files, OutputDirectory: spec.OutputDirectory, RuntimeCommand: spec.RuntimeCommand,
	}
	// The container tier exists to produce frozen bundles: an input without
	// a declared output directory or runtime entrypoint is not eligible here.
	if err := domain.ValidatePayload(payload); err != nil || spec.OutputDirectory == "" || len(spec.RuntimeCommand) == 0 {
		return ports.RunResult{Facts: facts, Stage: domain.StageMaterialize, Failure: domain.FailureInputDrift}, nil
	}
	if !strings.Contains(spec.BaseImage, "@sha256:") {
		return ports.RunResult{Facts: facts, Stage: domain.StageMaterialize, Failure: domain.FailureInputDrift}, nil
	}
	if err := e.Available(ctx); err != nil {
		return ports.RunResult{}, err
	}
	pinned, pinErr := e.imageDigestVerified(ctx, spec.BaseImage)
	if pinErr != nil {
		return ports.RunResult{}, pinErr
	}
	result.Facts.ImagePinned = pinned
	if err := os.MkdirAll(spec.ScratchRoot, 0o700); err != nil {
		return ports.RunResult{}, fmt.Errorf("scratch directory: %w", err)
	}
	hostScratch := spec.ScratchRoot
	volume, keeper, cleanup, err := e.workspace(ctx, spec)
	if err != nil {
		return ports.RunResult{}, err
	}
	defer cleanup()
	spec.ScratchRoot = volume

	result.Stage = domain.StageBuild
	buildExit, buildLog, buildTimeout, buildBudget, buildErr := e.runStage(ctx, spec, "build", spec.BuildCommand)
	if buildErr != nil {
		return ports.RunResult{}, buildErr
	}
	result.BuildExitCode = buildExit
	result.LogTail = domain.SanitizeLogTail(buildLog)
	switch {
	case buildTimeout:
		result.Failure = domain.FailureTimeout
		return result, nil
	case buildBudget:
		result.Failure = domain.FailureOutputBudget
		return result, nil
	case buildExit != 0:
		result.Failure = domain.FailureBuildFailed
		return result, nil
	}
	// Freeze before tests run. The test container only sees the bounded
	// workspace; it cannot mutate the immutable host snapshot being released.
	outputDir := filepath.Join(hostScratch, "frozen-output")
	if err := e.freezeOutput(ctx, keeper, spec.OutputDirectory, outputDir); err != nil {
		result.Failure = domain.FailureOutputFailed
		return result, nil
	}
	if err := verifyEntrypoint(outputDir, spec.RuntimeCommand); err != nil {
		result.Failure = domain.FailureOutputFailed
		return result, nil
	}

	result.Stage = domain.StageTest
	testExit, testLog, testTimeout, testBudget, testErr := e.runStage(ctx, spec, "test", spec.TestCommand)
	combined := append(append([]byte{}, buildLog...), append([]byte("\n"), testLog...)...)
	result.TestExitCode = testExit
	result.LogTail = domain.SanitizeLogTail(combined)
	switch {
	case testErr != nil:
		return ports.RunResult{}, testErr
	case testTimeout:
		result.Failure = domain.FailureTimeout
		return result, nil
	case testBudget:
		result.Failure = domain.FailureOutputBudget
		return result, nil
	case testExit != 0:
		result.Failure = domain.FailureTestFailed
		return result, nil
	}

	result.Stage = domain.StageVerify
	// outputDir is the pre-test frozen snapshot.
	info, err := os.Lstat(outputDir)
	if err != nil || !info.IsDir() {
		result.Failure = domain.FailureOutputFailed
		return result, nil
	}
	if err := verifyEntrypoint(outputDir, spec.RuntimeCommand); err != nil {
		result.LogTail = domain.SanitizeLogTail(append(combined, []byte("\n"+err.Error())...))
		result.Failure = domain.FailureOutputFailed
		return result, nil
	}
	result.OutputDir = outputDir
	result.Failure = domain.FailureNone
	return result, nil
}

// verifyEntrypoint checks the runtime command target exists inside the frozen
// subtree and is an executable regular file: a bundle whose entrypoint is
// missing is never deployable, so freezing it would manufacture a broken
// release.
func verifyEntrypoint(outputDir string, command []string) error {
	target := command[0]
	if !strings.HasPrefix(target, "/app/") && !strings.HasPrefix(target, "./") {
		return errors.New("entrypoint must name /app/ or ./ bundle path")
	}
	target = strings.TrimPrefix(target, "./")
	if strings.HasPrefix(target, "/") {
		if !strings.HasPrefix(target, "/app/") || len(target) <= len("/app/") {
			return fmt.Errorf("entrypoint %q is outside the bundle mount", command[0])
		}
		target = strings.TrimPrefix(target, "/app/")
	}
	if target == "" || strings.Contains(target, "..") {
		return fmt.Errorf("entrypoint %q does not name a bundle path", command[0])
	}
	info, err := os.Lstat(filepath.Join(outputDir, filepath.FromSlash(target)))
	if err != nil {
		return fmt.Errorf("entrypoint %q missing from build output", command[0])
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("entrypoint %q is not an executable file", command[0])
	}
	return nil
}

// runStage executes one fixed argv in a fresh container over the job-private
// source tree. A non-zero exit is a verdict, never an error; budget reports
// the stage output exceeding the log cap (a terminal verdict even on exit 0).
func (e *Engine) runStage(ctx context.Context, spec ports.RunSpec, stage string, command []string) (exit int32, logs []byte, timedOut bool, budget bool, err error) {
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return 0, nil, false, false, err
	}
	name := "workos-build-" + hex.EncodeToString(suffix)
	configuration := map[string]any{
		"Image": spec.BaseImage,
		"User":  strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()),
		// The engine owns the wrapper: one fixed argv, workdir /src, a
		// minimal environment with no proxy and no module fetches.
		"Entrypoint": []string{"/usr/bin/timeout", "--signal=KILL", strconv.Itoa(max(1, int(spec.Timeout.Seconds())))}, "Cmd": command, "WorkingDir": "/src",
		"Env": []string{
			"HOME=/src", "TMPDIR=/tmp", "LANG=C.UTF-8", "TZ=UTC",
			"PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin",
			"GOPROXY=off", "GOCACHE=/src/.gocache", "GOMODCACHE=/src/.gomodcache", "GOTMPDIR=/tmp",
		},
		"HostConfig": map[string]any{
			"Binds":          []string{spec.ScratchRoot + ":/src:rw"},
			"NetworkMode":    "none",
			"ReadonlyRootfs": true,
			"CapDrop":        []string{"ALL"},
			"SecurityOpt":    []string{"no-new-privileges:true"},
			"PidsLimit":      e.config.Pids,
			"Memory":         e.config.Memory,
			"MemorySwap":     e.config.Memory,
			"NanoCpus":       e.config.NanoCPUs,
			"Tmpfs":          map[string]string{"/tmp": "rw,nosuid,nodev," + tmpfsSize + ",exec,mode=1777"},
			"LogConfig":      map[string]any{"Type": "json-file", "Config": map[string]string{"max-size": "2m", "max-file": "2"}},
		},
		"Labels": map[string]string{
			"workos.purpose": "runtime-build", "workos.stage": stage,
			"workos.runtime": containerprocess.Namespace(),
		},
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := e.call(ctx, "POST", "/containers/create?name="+name, configuration, &created); err != nil {
		var apiErr *apiError
		if errors.As(err, &apiErr) && apiErr.notFound() {
			// The pinned image is not present locally and create never pulls.
			return 0, nil, false, false, fmt.Errorf("toolchain image %s is not present locally: %w", spec.BaseImage, err)
		}
		return 0, nil, false, false, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = e.call(cleanup, "DELETE", "/containers/"+created.ID+"?force=true&v=true", nil, nil)
	}()
	if err := e.call(ctx, "POST", "/containers/"+created.ID+"/start", nil, nil); err != nil {
		return 0, nil, false, false, err
	}
	stageCtx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()
	type logResult struct {
		data   []byte
		budget bool
		err    error
	}
	logResults := make(chan logResult, 1)
	go func() {
		data, over, err := e.logs(stageCtx, created.ID)
		if over {
			e.kill(created.ID)
		}
		logResults <- logResult{data, over, err}
	}()
	var waited struct {
		StatusCode int `json:"StatusCode"`
	}
	waitErr := e.call(stageCtx, "POST", "/containers/"+created.ID+"/wait?condition=not-running", nil, &waited)
	if stageCtx.Err() != nil {
		// Wall-clock deadline or service shutdown: kill and verdict timeout.
		e.kill(created.ID)
		return 0, nil, true, false, nil
	}
	if waitErr != nil {
		e.kill(created.ID)
		return 0, nil, false, false, waitErr
	}
	captured := <-logResults
	if captured.err != nil {
		return 0, nil, false, false, captured.err
	}
	return int32(waited.StatusCode), captured.data, false, captured.budget, nil
}

func (e *Engine) kill(id string) {
	killCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = e.call(killCtx, "POST", "/containers/"+id+"/kill", nil, nil)
}

// logs demultiplexes the container stream, keeping at most the budget bytes;
// budget reports whether output was dropped because the cap was hit.
func (e *Engine) logs(ctx context.Context, id string) (logs []byte, budget bool, err error) {
	req, err := http.NewRequestWithContext(ctx, "GET", e.baseURL+"/containers/"+id+"/logs?stdout=1&stderr=1&follow=1", nil)
	if err != nil {
		return nil, false, err
	}
	response, err := e.client.Do(req)
	if err != nil {
		return nil, false, errEngineIO
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, false, errEngineIO
	}
	var combined bytes.Buffer
	reader := io.LimitReader(response.Body, 16<<20)
	for {
		var header [8]byte
		if _, err := io.ReadFull(reader, header[:]); err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				return nil, false, errEngineIO
			}
			break
		}
		size := int64(binary.BigEndian.Uint32(header[4:]))
		if size < 0 || size > 16<<20 {
			return nil, false, errEngineIO
		}
		keep := size
		if remaining := int64(logBudget - combined.Len()); keep > remaining {
			keep = remaining
		}
		if _, err := io.CopyN(&combined, reader, keep); err != nil && !errors.Is(err, io.EOF) {
			break
		}
		if size > keep {
			return combined.Bytes(), true, nil
		}
	}
	return combined.Bytes(), budget, nil
}

// imageDigestVerified resolves the image reference locally and confirms the
// exact repo digest is recorded; only this readback justifies image_pinned.
func (e *Engine) imageDigestVerified(ctx context.Context, ref string) (bool, error) {
	var image struct {
		RepoDigests []string `json:"RepoDigests"`
	}
	if err := e.call(ctx, "GET", "/images/"+strings.ReplaceAll(ref, "/", "%2F")+"/json", nil, &image); err != nil {
		var apiErr *apiError
		if errors.As(err, &apiErr) && apiErr.notFound() {
			return false, fmt.Errorf("toolchain image %s is not present locally: %w", ref, err)
		}
		return false, err
	}
	pinned := false
	for _, digest := range image.RepoDigests {
		if digest == ref {
			pinned = true
			break
		}
	}
	if !pinned {
		return false, fmt.Errorf("toolchain image %s resolved without its pinned digest", ref)
	}
	return true, nil
}

// materialize writes the candidate tree: regular files only, inside the
// job-private directory.
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
