package dockerbuild

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/runtime/buildtest/domain"
	"github.com/yangtao121/workos/internal/runtime/buildtest/ports"
)

const pinnedImage = "golang@sha256:e8c859f5632dcfde7b32d2012b4351728f6437930887c2f6a91ea242459e5514"

type stageScript struct {
	exit   int
	output string
	block  bool // wait until the client deadline (timeout path)
	big    bool // emit output past the log budget
}

// fakeEngineAPI scripts container stages and records every create request.
type fakeEngineAPI struct {
	mu        sync.Mutex
	creates   []map[string]any
	kills     []string
	deletes   []string
	imageFail bool
	script    []stageScript
	next      int
	output    []byte
}

func muxFrame(stream byte, payload []byte) []byte {
	frame := make([]byte, 8+len(payload))
	frame[0] = stream
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(payload)))
	copy(frame[8:], payload)
	return frame
}

func (f *fakeEngineAPI) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /volumes/create", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(201) })
	mux.HandleFunc("DELETE /volumes/{name}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	mux.HandleFunc("PUT /containers/{id}/archive", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("GET /containers/{id}/archive", func(w http.ResponseWriter, r *http.Request) {
		if len(f.output) == 0 {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write(f.output)
	})
	mux.HandleFunc("GET /_ping", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})
	mux.HandleFunc("GET /images/{ref}/json", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		fail := f.imageFail
		f.mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"no such image"}`))
			return
		}
		ref := r.PathValue("ref")
		_ = json.NewEncoder(w).Encode(map[string]any{"RepoDigests": []string{ref}})
	})
	mux.HandleFunc("POST /containers/create", func(w http.ResponseWriter, r *http.Request) {
		var configuration map[string]any
		if err := json.NewDecoder(r.Body).Decode(&configuration); err != nil {
			t.Errorf("decode create: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if command, ok := configuration["Entrypoint"].([]any); ok && len(command) == 1 && command[0] == "/bin/sleep" {
			_ = json.NewEncoder(w).Encode(map[string]string{"Id": "keeper"})
			return
		}
		f.mu.Lock()
		f.creates = append(f.creates, configuration)
		index := len(f.creates)
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{"Id": fmt.Sprintf("cid%d", index)})
	})
	mux.HandleFunc("POST /containers/{id}/start", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /containers/{id}/wait", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		index := len(f.creates)
		var stage stageScript
		if index-1 < len(f.script) {
			stage = f.script[index-1]
		}
		f.mu.Unlock()
		if stage.block {
			<-r.Context().Done()
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]int{"StatusCode": stage.exit})
	})
	mux.HandleFunc("GET /containers/{id}/logs", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		index := len(f.creates)
		var stage stageScript
		if index-1 < len(f.script) {
			stage = f.script[index-1]
		}
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/vnd.docker.multiplexed-stream")
		payload := []byte(stage.output)
		if stage.big {
			payload = make([]byte, logBudget+4096)
			for i := range payload {
				payload[i] = 'x'
			}
			for frame, offset := muxFrame(1, payload), 0; offset+65536 <= len(frame); offset += 65536 {
				if _, err := w.Write(frame[offset : offset+65536]); err != nil {
					return
				}
			}
			return
		}
		_, _ = w.Write(muxFrame(1, payload))
		_, _ = w.Write(muxFrame(2, nil))
	})
	mux.HandleFunc("POST /containers/{id}/kill", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.kills = append(f.kills, r.PathValue("id"))
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /containers/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.deletes = append(f.deletes, r.PathValue("id"))
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func newFakeEngine(t *testing.T, script []stageScript) (*Engine, *fakeEngineAPI) {
	t.Helper()
	api := &fakeEngineAPI{script: script}
	server := httptest.NewServer(api.handler(t))
	t.Cleanup(server.Close)
	return newEngine(Config{}, server.Client(), server.URL), api
}

func bundleSpec(t *testing.T) ports.RunSpec {
	t.Helper()
	return ports.RunSpec{
		ScratchRoot:     t.TempDir(),
		BaseImage:       pinnedImage,
		BuildCommand:    []string{"go", "build", "./..."},
		TestCommand:     []string{"go", "test", "./..."},
		Files:           []domain.File{{Path: "main.go", Content: []byte("package main\n")}},
		OutputDirectory: "dist",
		RuntimeCommand:  []string{"./server"},
		Timeout:         5 * time.Second,
	}
}

func writeOutput(t *testing.T, spec ports.RunSpec, name string, mode os.FileMode) {
	t.Helper()
	output := filepath.Join(spec.ScratchRoot, spec.OutputDirectory)
	if err := os.MkdirAll(output, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, name), []byte("#!/bin/sh\n"), mode); err != nil {
		t.Fatal(err)
	}
}

func hostConfig(configuration map[string]any) map[string]any {
	raw, _ := configuration["HostConfig"].(map[string]any)
	return raw
}

func TestRunSuccessVerifiesOutputAndPinsImage(t *testing.T) {
	engine, api := newFakeEngine(t, []stageScript{{exit: 0, output: "build ok"}, {exit: 0, output: "test ok"}})
	spec := bundleSpec(t)
	api.output = outputTar(t, "server", 0o755)
	result, err := engine.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Failure != domain.FailureNone || result.OutputDir == "" {
		t.Fatalf("expected verified output, got %+v", result)
	}
	if !result.Facts.ImagePinned || !result.Facts.NetworkIsolated || result.Facts.Engine != "docker" {
		t.Fatalf("facts must reflect the enforced isolation: %+v", result.Facts)
	}
	if want := filepath.Join(spec.ScratchRoot, "frozen-output"); result.OutputDir != want {
		t.Fatalf("output dir: %q want %q", result.OutputDir, want)
	}
	if len(api.creates) != 2 {
		t.Fatalf("build and test must run in separate containers, got %d", len(api.creates))
	}
	for index, configuration := range api.creates {
		if configuration["Image"] != pinnedImage {
			t.Fatalf("container %d must use the pinned image", index)
		}
		host := hostConfig(configuration)
		if host["NetworkMode"] != "none" || host["ReadonlyRootfs"] != true {
			t.Fatalf("container %d missing isolation: %v", index, host)
		}
		if drops, ok := host["CapDrop"].([]any); !ok || len(drops) != 1 || drops[0] != "ALL" {
			t.Fatalf("container %d must drop all capabilities", index)
		}
		if binds, ok := host["Binds"].([]any); !ok || len(binds) != 1 || !strings.HasPrefix(binds[0].(string), "workos-build-") || !strings.HasSuffix(binds[0].(string), ":/src:rw") {
			t.Fatalf("container %d must bind the job scratch at /src: %v", index, binds)
		}
		if host["PidsLimit"].(float64) <= 0 || host["Memory"].(float64) <= 0 {
			t.Fatalf("container %d missing cgroup limits", index)
		}
	}
	if len(api.deletes) < 2 {
		t.Fatalf("stage containers must be removed, got %v", api.deletes)
	}
}

func TestStaticFactsNeverClaimPinning(t *testing.T) {
	engine, _ := newFakeEngine(t, nil)
	facts := engine.Facts()
	if facts.ImagePinned {
		t.Fatal("static facts must not claim image_pinned without a run readback")
	}
}

func TestBuildFailureYieldsNoOutput(t *testing.T) {
	engine, api := newFakeEngine(t, []stageScript{{exit: 2, output: "boom"}})
	spec := bundleSpec(t)
	api.output = outputTar(t, "server", 0o755)
	result, err := engine.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Failure != domain.FailureBuildFailed || result.OutputDir != "" {
		t.Fatalf("build failure must not produce a bundle: %+v", result)
	}
}

func TestTestFailureYieldsNoOutput(t *testing.T) {
	engine, api := newFakeEngine(t, []stageScript{{exit: 0}, {exit: 1, output: "assertion"}})
	spec := bundleSpec(t)
	api.output = outputTar(t, "server", 0o755)
	result, err := engine.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Failure != domain.FailureTestFailed || result.OutputDir != "" {
		t.Fatalf("test failure must not produce a bundle: %+v", result)
	}
}

func TestTimeoutKillsAndFails(t *testing.T) {
	engine, api := newFakeEngine(t, []stageScript{{block: true}})
	spec := bundleSpec(t)
	spec.Timeout = 150 * time.Millisecond
	result, err := engine.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Failure != domain.FailureTimeout || result.OutputDir != "" {
		t.Fatalf("deadline must be a terminal timeout: %+v", result)
	}
	if len(api.kills) == 0 {
		t.Fatal("the timed-out container must be killed")
	}
}

func TestMissingOutputFailsClosed(t *testing.T) {
	engine, _ := newFakeEngine(t, []stageScript{{exit: 0}, {exit: 0}})
	result, err := engine.Run(context.Background(), bundleSpec(t))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Failure != domain.FailureOutputFailed || result.OutputDir != "" {
		t.Fatalf("missing output directory must fail the verify stage: %+v", result)
	}
}

func TestMissingEntrypointFailsClosed(t *testing.T) {
	engine, api := newFakeEngine(t, []stageScript{{exit: 0}, {exit: 0}})
	spec := bundleSpec(t)
	api.output = outputTar(t, "server", 0o644) // present but not executable
	if result, err := engine.Run(context.Background(), spec); err != nil || result.Failure != domain.FailureOutputFailed {
		t.Fatalf("non-executable entrypoint must fail verify: %+v %v", result, err)
	}
	engine2, api2 := newFakeEngine(t, []stageScript{{exit: 0}, {exit: 0}})
	spec2 := bundleSpec(t)
	api2.output = outputTar(t, "other", 0o755)
	if result, err := engine2.Run(context.Background(), spec2); err != nil || result.Failure != domain.FailureOutputFailed {
		t.Fatalf("missing entrypoint must fail verify: %+v %v", result, err)
	}
}

func TestUnpinnedImageReferenceRejected(t *testing.T) {
	engine, api := newFakeEngine(t, nil)
	spec := bundleSpec(t)
	spec.BaseImage = "golang:1.26.7-bookworm"
	result, err := engine.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Failure != domain.FailureInputDrift {
		t.Fatalf("floating image reference is invalid input: %+v", result)
	}
	if len(api.creates) != 0 {
		t.Fatal("no container may be created from an unpinned reference")
	}
}

func TestMissingOutputDeclarationRejected(t *testing.T) {
	engine, api := newFakeEngine(t, nil)
	spec := bundleSpec(t)
	spec.OutputDirectory = ""
	if result, err := engine.Run(context.Background(), spec); err != nil || result.Failure != domain.FailureInputDrift {
		t.Fatalf("bundle tier requires a declared output: %+v %v", result, err)
	}
	spec.RuntimeCommand = nil
	if result, err := engine.Run(context.Background(), spec); err != nil || result.Failure != domain.FailureInputDrift {
		t.Fatalf("bundle tier requires a runtime entrypoint: %+v %v", result, err)
	}
	if len(api.creates) != 0 {
		t.Fatal("invalid payloads must never reach container creation")
	}
}

func TestImageNotPresentLocallyFailsClosed(t *testing.T) {
	api := &fakeEngineAPI{imageFail: true}
	server := httptest.NewServer(api.handler(t))
	defer server.Close()
	engine := newEngine(Config{}, server.Client(), server.URL)
	if _, err := engine.Run(context.Background(), bundleSpec(t)); err == nil {
		t.Fatal("absent toolchain image must be an engine error, never a pull")
	}
}

func TestOutputBudgetIsTerminal(t *testing.T) {
	engine, _ := newFakeEngine(t, []stageScript{{exit: 0, big: true}})
	spec := bundleSpec(t)
	result, err := engine.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Failure != domain.FailureOutputBudget {
		t.Fatalf("log budget overrun must be a terminal verdict: %+v", result)
	}
}

func TestAvailableFailsWithoutDaemon(t *testing.T) {
	engine := newEngine(Config{}, &http.Client{}, "http://127.0.0.1:1")
	if err := engine.Available(context.Background()); err == nil {
		t.Fatal("unreachable daemon must be unavailable")
	}
}

func outputTar(t *testing.T, name string, mode int64) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	content := []byte("#!/bin/sh\n")
	if err := w.WriteHeader(&tar.Header{Name: "dist/" + name, Typeflag: tar.TypeReg, Mode: mode, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
