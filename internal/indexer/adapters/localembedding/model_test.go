package localembedding

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	indexv1 "github.com/yangtao121/workos/gen/go/workos/index/v1"
	"github.com/yangtao121/workos/internal/indexer/domain"
	"github.com/yangtao121/workos/internal/indexer/ports"
	"google.golang.org/protobuf/encoding/protojson"
)

func fixture(t *testing.T, script string) Config {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "inference-fixture")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	return Config{PythonPath: path, WorkerPath: filepath.Join(dir, "worker"), ModelDir: dir}
}

func response(t *testing.T, fingerprint string, vector []float32) string {
	t.Helper()
	data, err := protojson.Marshal(&indexv1.LocalEmbeddingResponse{ModelFingerprint: fingerprint, Vector: vector})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func unitVector() []float32 {
	vector := make([]float32, domain.EmbeddingDimensions)
	vector[0] = 1
	return vector
}

func TestModelRejectsInvalidFrames(t *testing.T) {
	fingerprint := (*Model)(nil).Fingerprint()
	nonfinite := unitVector()
	nonfinite[0] = float32(math.NaN())
	for name, data := range map[string]string{
		"malformed":  "{",
		"oversized":  strings.Repeat("x", 32<<10),
		"identity":   response(t, "sha256:"+strings.Repeat("0", 64), unitVector()),
		"dimensions": response(t, fingerprint, []float32{1}),
		"zero":       response(t, fingerprint, make([]float32, domain.EmbeddingDimensions)),
		"nonfinite":  response(t, fingerprint, nonfinite),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := New(context.Background(), fixture(t, "read -r request\nprintf '%s\\n' '"+data+"'\n"))
			if !errors.Is(err, ports.ErrEmbeddingUnavailable) {
				t.Fatalf("invalid model output: %v", err)
			}
		})
	}
}

func TestModelSerializesRequestsAndRecoversAfterChildExit(t *testing.T) {
	data := response(t, (*Model)(nil).Fingerprint(), unitVector())
	// Each child handles one request then exits; the next request must fail closed
	// and subsequent requests must be able to start a new child.
	model, err := New(context.Background(), fixture(t, "read -r request\nprintf '%s\\n' '"+data+"'\n"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(model.Close)
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			vector, err := model.Query(context.Background(), "synthetic query")
			if err != nil && !errors.Is(err, ports.ErrEmbeddingUnavailable) {
				t.Error(err)
			}
			if err == nil && !validVector(vector) {
				t.Error("invalid vector returned")
			}
		})
	}
	workers.Wait()
	for range 2 {
		if vector, err := model.Query(context.Background(), "recover"); err == nil && validVector(vector) {
			return
		}
	}
	t.Fatal("child did not recover")
}

func TestModelDeadlineKillsChildGroup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := New(ctx, fixture(t, "read -r request\nsleep 10\n"))
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("deadline was not bounded: %v after %v", err, time.Since(start))
	}
}

func TestModelInputAndClose(t *testing.T) {
	data := response(t, (*Model)(nil).Fingerprint(), unitVector())
	model, err := New(context.Background(), fixture(t, "while IFS= read -r request; do printf '%s\\n' '"+data+"'; done\n"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(model.Close)
	for _, text := range []string{"", string([]byte{0xff}), strings.Repeat("a", inputLimit+1)} {
		if _, err := model.Query(context.Background(), text); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("invalid query accepted: %v", err)
		}
	}
	if _, err := model.Document(context.Background(), strings.Repeat("知识", inputLimit)); err != nil {
		t.Fatalf("bounded UTF-8 document: %v", err)
	}
	model.Close()
	if _, err := model.Query(context.Background(), "after close"); !errors.Is(err, ports.ErrEmbeddingUnavailable) {
		t.Fatalf("closed model accepted a request: %v", err)
	}
}

// Run in the pinned inference container with networking disabled. Normal Go
// tests exercise the process failure matrix without downloading model weights.
func TestPinnedOfflineModel(t *testing.T) {
	directory := os.Getenv("WORKOS_LOCAL_EMBEDDING_MODEL_DIR")
	if directory == "" {
		t.Skip("requires the pinned offline inference container")
	}
	workerPath, err := filepath.Abs("worker.py")
	if err != nil {
		t.Fatal(err)
	}
	model, err := New(context.Background(), Config{PythonPath: "/usr/local/bin/python", WorkerPath: workerPath, ModelDir: directory})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(model.Close)
	documents := []string{
		"Device cryptographic keys are kept in protected hardware so applications cannot read the private material.",
		"花园里的番茄需要定期浇水，成熟后可以采摘。",
		"A crashed service is restored by restarting its process, with a retry limit to prevent endless failures.",
	}
	var vectors [][domain.EmbeddingDimensions]float32
	for _, text := range documents {
		vector, err := model.Document(context.Background(), text)
		if err != nil {
			t.Fatal(err)
		}
		vectors = append(vectors, [domain.EmbeddingDimensions]float32(vector))
	}
	for _, item := range []struct {
		query string
		want  int
	}{
		{"如何安全保存设备密钥？", 0},
		{"怎样在程序崩溃后恢复服务？", 2},
		{"When should I water and harvest my tomatoes?", 1},
	} {
		query, err := model.Query(context.Background(), item.query)
		if err != nil {
			t.Fatal(err)
		}
		best, score := -1, float32(-1)
		for i, vector := range vectors {
			if next := domain.CosineSimilarity([domain.EmbeddingDimensions]float32(query), vector); next > score {
				best, score = i, next
			}
		}
		if best != item.want {
			t.Fatalf("cross-language retrieval selected document %d, want %d", best, item.want)
		}
		again, err := model.Query(context.Background(), item.query)
		if err != nil {
			t.Fatal(err)
		}
		for i := range query {
			if query[i] != again[i] {
				t.Fatal("same-process CPU inference changed")
			}
		}
	}
	// Process restart must retain the model identity and query representation.
	before, err := model.Query(context.Background(), "设备密钥")
	if err != nil {
		t.Fatal(err)
	}
	<-model.slot
	model.stop()
	model.slot <- struct{}{}
	after, err := model.Query(context.Background(), "设备密钥")
	if err != nil {
		t.Fatal(err)
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatal("CPU inference changed after child restart")
		}
	}
	badCache := t.TempDir()
	if err := os.WriteFile(filepath.Join(badCache, "tokenizer.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(context.Background(), Config{PythonPath: "/usr/local/bin/python", WorkerPath: workerPath, ModelDir: badCache}); !errors.Is(err, ports.ErrEmbeddingUnavailable) {
		t.Fatalf("corrupt cached tokenizer was not rejected: %v", err)
	}
}

func TestQueueDeadlineRemainsRetryable(t *testing.T) {
	// A saturated model slot must not leak its internal deadline as a fatal
	// ingestion error while the worker's parent context is still alive.
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	model := &Model{ctx: ctx, slot: make(chan struct{}, 1)}
	if _, err := model.Query(ctx, "queued query"); !errors.Is(err, ports.ErrEmbeddingUnavailable) || ctx.Err() != nil {
		t.Fatalf("queue overload is not retryable: %v (parent %v)", err, ctx.Err())
	}
}
