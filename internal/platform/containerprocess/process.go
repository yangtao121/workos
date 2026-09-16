// Package containerprocess is the Runtime's Docker transport. It carries no
// project policy: callers must derive paths and identities before starting.
package containerprocess

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"
)

var ErrUnavailable = errors.New("container process unavailable")

type Client struct {
	socket, image string
	namespace     string
	http          *http.Client
}

func New(socket, image string) *Client {
	return &Client{socket: socket, image: image, namespace: Namespace(), http: &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}}
}
func (c *Client) request(ctx context.Context, method, endpoint string, input, output any) error {
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
	response, err := c.http.Do(req)
	if err != nil {
		return ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ErrUnavailable
	}
	if output != nil {
		return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(output)
	}
	return nil
}

type Spec struct {
	ID, Workspace                  string
	ReadOnly, Tty                  bool
	Argv, Environment, ExtraMounts []string
	Lifetime                       time.Duration
}
type Process struct {
	client     *Client
	ID         string
	connection net.Conn
	reader     *bufio.Reader
	done       chan struct{}
	once       sync.Once
	cancel     context.CancelFunc
}

func (c *Client) Start(ctx context.Context, spec Spec) (*Process, error) {
	if c.image == "" || len(spec.Argv) == 0 || spec.Lifetime <= 0 || spec.Lifetime > 30*time.Minute {
		return nil, ErrUnavailable
	}
	mounts := append([]string{}, spec.ExtraMounts...)
	mode := "rw"
	if spec.ReadOnly {
		mode = "ro"
	}
	cwd := "/tmp"
	if spec.Workspace != "" {
		mounts = append(mounts, spec.Workspace+":/workspace:"+mode)
		cwd = "/workspace"
	}
	command := append([]string{"/usr/bin/timeout", "--signal=KILL", strconv.FormatInt(int64(spec.Lifetime.Seconds()), 10)}, spec.Argv...)
	env := append([]string{"HOME=/tmp", "PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8", "TERM=xterm-256color", "TZ=UTC"}, spec.Environment...)
	conf := map[string]any{"Image": c.image, "User": fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "WorkingDir": cwd, "Cmd": command, "Env": env, "Tty": spec.Tty, "OpenStdin": spec.Tty, "AttachStdin": spec.Tty, "AttachStdout": true, "AttachStderr": true,
		"Labels":     map[string]string{"workos.owner": "runtime-interactive", "workos.runtime": c.namespace, "workos.operation": spec.ID},
		"HostConfig": map[string]any{"Binds": mounts, "NetworkMode": "none", "ReadonlyRootfs": true, "CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges:true"}, "PidsLimit": 128, "Memory": int64(1024 * 1024 * 1024), "NanoCpus": int64(2_000_000_000), "Tmpfs": map[string]string{"/tmp": "rw,nosuid,nodev,size=268435456,mode=1777"}, "LogConfig": map[string]any{"Type": "json-file", "Config": map[string]string{"max-size": "1m", "max-file": "1"}}}}
	var created struct {
		ID string `json:"Id"`
	}
	if err := c.request(ctx, "POST", "/containers/create", conf, &created); err != nil {
		return nil, err
	}
	life, cancel := context.WithTimeout(context.WithoutCancel(ctx), spec.Lifetime)
	process := &Process{client: c, ID: created.ID, done: make(chan struct{}), cancel: cancel}
	ok := false
	defer func() {
		if !ok {
			process.Stop()
		}
	}()
	if spec.Tty {
		connection, err := (&net.Dialer{}).DialContext(ctx, "unix", c.socket)
		if err != nil {
			return nil, ErrUnavailable
		}
		process.connection = connection
		process.reader = bufio.NewReader(connection)
		req, _ := http.NewRequest("POST", "http://docker/v1.47/containers/"+created.ID+"/attach?stream=1&stdin=1&stdout=1&stderr=1", nil)
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "tcp")
		if err := req.Write(connection); err != nil {
			return nil, ErrUnavailable
		}
		response, err := http.ReadResponse(process.reader, req)
		if err != nil {
			return nil, ErrUnavailable
		}
		if response.StatusCode != 101 {
			return nil, ErrUnavailable
		}
	}
	if err := c.request(ctx, "POST", "/containers/"+created.ID+"/start", nil, nil); err != nil {
		return nil, err
	}
	ok = true
	go func() {
		defer close(process.done)
		_ = c.request(life, "POST", "/containers/"+created.ID+"/wait?condition=not-running", nil, &struct{}{})
		process.Stop()
	}()
	return process, nil
}
func (p *Process) Done() <-chan struct{} { return p.done }
func (p *Process) Read(buffer []byte) (int, error) {
	if p.reader == nil {
		return 0, io.EOF
	}
	return p.reader.Read(buffer)
}
func (p *Process) Write(buffer []byte) (int, error) {
	if p.connection == nil {
		return 0, ErrUnavailable
	}
	_ = p.connection.SetWriteDeadline(time.Now().Add(3 * time.Second))
	return p.connection.Write(buffer)
}
func (p *Process) Resize(ctx context.Context, columns, rows int32) error {
	return p.client.request(ctx, "POST", fmt.Sprintf("/containers/%s/resize?w=%d&h=%d", p.ID, columns, rows), nil, nil)
}
func (p *Process) Stop() {
	p.once.Do(func() {
		p.cancel()
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = p.client.request(cleanup, "DELETE", "/containers/"+p.ID+"?force=true&v=true", nil, nil)
		if p.connection != nil {
			_ = p.connection.Close()
		}
	})
}

func (c *Client) Available(ctx context.Context) error {
	return c.request(ctx, "GET", "/images/"+url.PathEscape(c.image)+"/json", nil, &struct{}{})
}

// Reconcile is called once at Runtime startup, before it accepts workloads.
// Rows from the previous process are terminalized by their owning modules;
// their daemon-owned processes must be reaped too.
func (c *Client) Reconcile(ctx context.Context) error {
	for _, owner := range []string{"runtime-interactive", "runtime-workspace"} {
		filters, _ := json.Marshal(map[string][]string{"label": {"workos.owner=" + owner, "workos.runtime=" + c.namespace}})
		var containers []struct {
			ID string `json:"Id"`
		}
		if err := c.request(ctx, "GET", "/containers/json?all=1&filters="+url.QueryEscape(string(filters)), nil, &containers); err != nil {
			return err
		}
		for _, container := range containers {
			if err := c.request(ctx, "DELETE", "/containers/"+container.ID+"?force=true&v=true", nil, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// Namespace separates deployments sharing a Docker daemon. It must remain
// stable across restarts, and a separate test stack must supply its own value.
func Namespace() string {
	if value := os.Getenv("WORKOS_RUNTIME_CONTAINER_NAMESPACE"); value != "" {
		return value
	}
	return "workos"
}
