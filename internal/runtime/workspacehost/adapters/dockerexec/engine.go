// Package dockerexec runs workspace commands in Runtime-owned OCI containers.
// The Docker socket is used only here; no container receives it or host env.
package dockerexec

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/yangtao121/workos/internal/platform/containerprocess"
	"github.com/yangtao121/workos/internal/runtime/workspacehost/domain"
)

type Engine struct {
	client *http.Client
	image  string
}

func New(socket, image string) *Engine {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	return &Engine{client: &http.Client{Transport: transport}, image: image}
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
	req, err := http.NewRequestWithContext(ctx, method, "http://docker/v1.47"+endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := e.client.Do(req)
	if err != nil {
		return domain.ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return domain.ErrUnavailable
	}
	if output != nil {
		return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(output)
	}
	return nil
}
func (e *Engine) Execute(ctx context.Context, root string, readOnly bool, op domain.Operation) (domain.Result, error) {
	command, _ := op.Arguments["command"].(string)
	cwd, _ := op.Arguments["workdir"].(string)
	if command == "" || len(command) > 65536 || e.image == "" {
		return nil, domain.ErrInvalid
	}
	if cwd == "" {
		cwd = "/workspace"
	}
	cwd = path.Clean(cwd)
	if cwd != "/workspace" && !strings.HasPrefix(cwd, "/workspace/") {
		return nil, domain.ErrDenied
	}
	timeout := 120 * time.Second
	if value, present := op.Arguments["timeoutMs"]; present {
		ms, ok := value.(float64)
		if !ok || math.IsNaN(ms) || math.IsInf(ms, 0) || ms < 1 {
			return nil, domain.ErrInvalid
		}
		timeout = time.Duration(min(ms, float64((5*time.Minute).Milliseconds()))) * time.Millisecond
	}
	limit := 256 * 1024
	if requested, ok := op.Arguments["stdoutMaxBytes"].(float64); ok && requested > 0 && requested < float64(limit) {
		limit = int(requested)
	}
	mode := "rw"
	if readOnly {
		mode = "ro"
	}
	configuration := map[string]any{
		"Image": e.image, "User": fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "WorkingDir": cwd,
		"Cmd":          []string{"/usr/bin/timeout", "--signal=KILL", fmt.Sprintf("%.3fs", timeout.Seconds()), "/bin/bash", "--noprofile", "--norc", "-c", command},
		"Env":          []string{"HOME=/tmp", "PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin", "TMPDIR=/tmp", "GOCACHE=/tmp/go-build", "GOPATH=/tmp/go", "GOPROXY=off", "TZ=UTC"},
		"AttachStdout": true, "AttachStderr": true,
		"HostConfig": map[string]any{"Binds": []string{root + ":/workspace:" + mode}, "NetworkMode": "none", "ReadonlyRootfs": true, "CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges:true"}, "PidsLimit": 128, "Memory": int64(1024 * 1024 * 1024), "NanoCpus": int64(2_000_000_000), "Tmpfs": map[string]string{"/tmp": "rw,nosuid,nodev,size=268435456,mode=1777"}, "LogConfig": map[string]any{"Type": "json-file", "Config": map[string]string{"max-size": "1m", "max-file": "1"}}},
		"Labels":     map[string]string{"workos.owner": "runtime-workspace", "workos.runtime": containerprocess.Namespace(), "workos.operation": op.ID},
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := e.call(ctx, "POST", "/containers/create?name=workos-ws-"+op.ID, configuration, &created); err != nil {
		return nil, err
	}
	// Removal owns all descendants, including commands that backgrounded a child.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = e.call(cleanup, "DELETE", "/containers/"+created.ID+"?force=true&v=true", nil, nil)
	}()
	started := time.Now()
	if err := e.call(ctx, "POST", "/containers/"+created.ID+"/start", nil, nil); err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var waited struct {
		StatusCode int `json:"StatusCode"`
	}
	err := e.call(runCtx, "POST", "/containers/"+created.ID+"/wait?condition=not-running", nil, &waited)
	// The in-container deadline can win the race with this host deadline.
	timedOut := ctx.Err() == nil && (errors.Is(runCtx.Err(), context.DeadlineExceeded) || (err == nil && waited.StatusCode == 137 && time.Since(started) >= timeout))
	aborted := ctx.Err() != nil
	if err != nil && !timedOut && !aborted {
		return nil, err
	}
	if timedOut || aborted {
		killCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		if err := e.call(killCtx, "POST", "/containers/"+created.ID+"/kill", nil, nil); err != nil {
			// Docker returns conflict when its timeout has already stopped the
			// process. Confirm that fact instead of treating the race as failure.
			var inspection struct {
				State struct {
					Running *bool `json:"Running"`
				} `json:"State"`
			}
			if err := e.call(killCtx, "GET", "/containers/"+created.ID+"/json", nil, &inspection); err != nil || inspection.State.Running == nil || *inspection.State.Running {
				return nil, domain.ErrUnavailable
			}
		}
	}
	logsCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	req, _ := http.NewRequestWithContext(logsCtx, "GET", "http://docker/v1.47/containers/"+created.ID+"/logs?stdout=1&stderr=1", nil)
	response, err := e.client.Do(req)
	if err != nil {
		return nil, domain.ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, domain.ErrUnavailable
	}
	stdout, stderr := bytes.Buffer{}, bytes.Buffer{}
	truncated := false
	reader := io.LimitReader(response.Body, 2<<20)
	for {
		var header [8]byte
		if _, err := io.ReadFull(reader, header[:]); err != nil {
			if !errors.Is(err, io.EOF) {
				truncated = true
			}
			break
		}
		size := int64(binary.BigEndian.Uint32(header[4:]))
		if size > 2<<20 {
			return nil, domain.ErrUnavailable
		}
		target := &stdout
		if header[0] == 2 {
			target = &stderr
		}
		keep := min(size, int64(max(0, limit-target.Len())))
		if _, err := io.CopyN(target, reader, keep); err != nil {
			truncated = true
			break
		}
		if size > keep {
			truncated = true
			if _, err := io.CopyN(io.Discard, reader, size-keep); err != nil {
				break
			}
		}
	}
	capture := func(buffer *bytes.Buffer) map[string]any {
		return map[string]any{"text": strings.ToValidUTF8(buffer.String(), "�"), "truncated": truncated, "totalBytes": buffer.Len()}
	}
	var signal any
	var code any = waited.StatusCode
	if timedOut || aborted {
		signal = "SIGKILL"
		code = nil
	}
	return domain.Result{"exitCode": code, "signal": signal, "timedOut": timedOut, "aborted": aborted, "timeoutMs": timeout.Milliseconds(), "stdout": capture(&stdout), "stderr": capture(&stderr), "sandbox": map[string]any{"mode": map[bool]string{true: "read-only", false: "workspace-write"}[readOnly], "denied": false}}, nil
}
