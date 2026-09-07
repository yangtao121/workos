// Package localembedding owns one offline CPU child. No listener, independent
// daemon lifecycle, database access or provider credentials enter this adapter.
package localembedding

import (
	"bufio"
	"context"
	"crypto/sha256"
	_ "embed"
	"fmt"
	"io"
	"math"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
	"unicode/utf8"

	indexv1 "github.com/yangtao121/workos/gen/go/workos/index/v1"
	"github.com/yangtao121/workos/internal/indexer/domain"
	"github.com/yangtao121/workos/internal/indexer/ports"
	"google.golang.org/protobuf/encoding/protojson"
)

//go:embed model.json
var recipe []byte

const inputLimit = 16 << 10

type Config struct {
	PythonPath string
	WorkerPath string
	ModelDir   string
}

type Model struct {
	config Config
	ctx    context.Context
	cancel context.CancelFunc
	slot   chan struct{}
	cmd    *exec.Cmd
	input  io.WriteCloser
	output *bufio.Reader
	idle   *time.Timer
}

var _ ports.EmbeddingModel = (*Model)(nil)

// New verifies the pinned runtime and weights with a real bounded inference.
// The parent owns shutdown; an idle child also exits after thirty seconds.
func New(ctx context.Context, config Config) (*Model, error) {
	for _, path := range []string{config.PythonPath, config.WorkerPath, config.ModelDir} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return nil, ports.ErrEmbeddingUnavailable
		}
	}
	childContext, cancel := context.WithCancel(ctx)
	m := &Model{config: config, ctx: childContext, cancel: cancel, slot: make(chan struct{}, 1)}
	m.slot <- struct{}{}
	if _, err := m.Query(ctx, "local embedding readiness"); err != nil {
		m.Close()
		return nil, err
	}
	return m, nil
}

func (*Model) Fingerprint() string { return fmt.Sprintf("sha256:%x", sha256.Sum256(recipe)) }

func (m *Model) Query(ctx context.Context, text string) ([]float32, error) {
	return m.infer(ctx, text, indexv1.LocalEmbeddingInputKind_LOCAL_EMBEDDING_INPUT_KIND_QUERY)
}

// Document embeds the leading bounded projection; the full text remains lexical.
func (m *Model) Document(ctx context.Context, text string) ([]float32, error) {
	if !utf8.ValidString(text) {
		return nil, domain.ErrInvalid
	}
	if len(text) > inputLimit {
		text = text[:inputLimit]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
	}
	return m.infer(ctx, text, indexv1.LocalEmbeddingInputKind_LOCAL_EMBEDDING_INPUT_KIND_DOCUMENT)
}

func (m *Model) infer(parent context.Context, text string, kind indexv1.LocalEmbeddingInputKind) ([]float32, error) {
	if text == "" || len(text) > inputLimit || !utf8.ValidString(text) {
		return nil, domain.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	select {
	case <-ctx.Done():
		if parent.Err() != nil {
			return nil, parent.Err()
		}
		return nil, ports.ErrEmbeddingUnavailable
	case <-m.ctx.Done():
		return nil, ports.ErrEmbeddingUnavailable
	case <-m.slot:
	}
	defer func() { m.slot <- struct{}{} }()
	if m.ctx.Err() != nil {
		return nil, ports.ErrEmbeddingUnavailable
	}
	if m.idle != nil {
		m.idle.Stop()
	}
	if m.cmd == nil {
		if err := m.start(); err != nil {
			return nil, ports.ErrEmbeddingUnavailable
		}
	}
	cmd := m.cmd
	killed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = cmd.Cancel()
		close(killed)
	})
	request, err := protojson.Marshal(&indexv1.LocalEmbeddingRequest{InputKind: kind, Text: text})
	if err == nil {
		_, err = m.input.Write(append(request, '\n'))
	}
	var line []byte
	if err == nil {
		line, err = m.output.ReadSlice('\n')
	}
	if !stop() {
		<-killed
		err = ctx.Err()
	}
	var response indexv1.LocalEmbeddingResponse
	if err == nil {
		err = protojson.Unmarshal(line, &response)
	}
	if err != nil || response.ModelFingerprint != m.Fingerprint() || !validVector(response.Vector) {
		m.stop()
		if parent.Err() != nil {
			return nil, parent.Err()
		}
		return nil, ports.ErrEmbeddingUnavailable
	}
	m.idle = time.AfterFunc(30*time.Second, func() {
		select {
		case <-m.slot:
			m.stop()
			m.slot <- struct{}{}
		default:
		}
	})
	return response.Vector, nil
}

func validVector(vector []float32) bool {
	if len(vector) != domain.EmbeddingDimensions {
		return false
	}
	var norm float64
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return false
		}
		norm += float64(value) * float64(value)
	}
	return math.Abs(norm-1) < 1e-5
}

func (m *Model) start() error {
	cmd := exec.CommandContext(m.ctx, m.config.PythonPath, "-I", m.config.WorkerPath, m.config.ModelDir)
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "TOKENIZERS_PARALLELISM=false", "HF_HUB_OFFLINE=1", "OMP_NUM_THREADS=1"}
	cmd.Stderr = io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL, Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		_ = input.Close()
		return err
	}
	if err := cmd.Start(); err != nil {
		_ = input.Close()
		_ = output.Close()
		return err
	}
	m.cmd, m.input, m.output = cmd, input, bufio.NewReaderSize(output, 32<<10)
	return nil
}

// stop runs only with the request slot held, including timeout/error recovery.
func (m *Model) stop() {
	if m.cmd != nil {
		_ = m.cmd.Cancel()
		_ = m.input.Close()
		_ = m.cmd.Wait()
		m.cmd, m.input, m.output = nil, nil, nil
	}
}

func (m *Model) Close() {
	m.cancel()
	<-m.slot
	if m.idle != nil {
		m.idle.Stop()
	}
	m.stop()
	m.slot <- struct{}{}
}
