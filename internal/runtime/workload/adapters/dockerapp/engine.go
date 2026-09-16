// Package dockerapp is the digest-pinned Docker formal App runner (ADR-0033).
// Containers join an internal network with no published ports; the host
// reaches them via the bridge IPv4. The profile does not claim rootless
// isolation and does not claim memory.high.
package dockerapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/yangtao121/workos/internal/platform/appbundle"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/yangtao121/workos/internal/platform/containerprocess"
	"github.com/yangtao121/workos/internal/runtime/workload/domain"
	"github.com/yangtao121/workos/internal/runtime/workload/ports"
)

const (
	apiVersion   = "v1.47"
	networkName  = "workos-app-internal"
	tmpfsSize    = "size=33554432"
	purposeLabel = "runtime-app"
)

// BundleOpener returns the verified on-disk tar of one ready bundle.
type BundleOpener interface {
	OpenForLaunch(ctx context.Context, owner, digest string) (string, error)
}

type Config struct {
	Socket     string
	UnpackRoot string
}

type Engine struct {
	client     *http.Client
	baseURL    string
	unpackRoot string
	bundles    BundleOpener
}

func New(config Config, bundles BundleOpener) (*Engine, error) {
	if config.Socket == "" || config.UnpackRoot == "" || bundles == nil {
		return nil, errors.New("docker app engine requires socket, unpack root, and bundle opener")
	}
	if err := os.MkdirAll(config.UnpackRoot, 0o700); err != nil {
		return nil, err
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", config.Socket)
		},
	}
	return &Engine{
		client:     &http.Client{Transport: transport, Timeout: 2 * time.Minute},
		baseURL:    "http://docker/" + apiVersion,
		unpackRoot: config.UnpackRoot,
		bundles:    bundles,
	}, nil
}

func (e *Engine) Probe(ctx context.Context) (ports.Capability, error) {
	var info struct {
		CgroupVersion   string
		ServerVersion   string
		SecurityOptions []string
	}
	if err := e.call(ctx, "GET", "/info", nil, &info); err != nil || info.CgroupVersion != "2" {
		return ports.Capability{Available: false, Reason: "docker cgroup v2 facts unavailable"}, nil
	}
	return ports.Capability{
		Available:           true,
		Rootless:            false,
		CgroupV2:            true,
		EngineVersion:       info.ServerVersion,
		AllowBridgeEndpoint: true,
		SkipMemoryHigh:      true,
		HostCgroup:          true,
		Reason:              "docker formal app runner (not rootless; memory.high unavailable)",
	}, nil
}

func (e *Engine) ImageExists(ctx context.Context, image string) (bool, error) {
	if !domain.ValidImage(image) {
		return false, domain.ErrInvalid
	}
	_, err := e.imageID(ctx, image)
	if err == nil {
		return true, nil
	}
	var apiErr *apiError
	if errors.As(err, &apiErr) && apiErr.notFound() {
		return false, nil
	}
	return false, ports.ErrEngineUnavailable
}

func (e *Engine) CreateContainer(ctx context.Context, spec ports.ContainerSpec) (string, error) {
	if !safeName(spec.Name) || !domain.ValidImage(spec.Image) || !domain.ValidCommand(spec.Command) ||
		spec.Port < 1 || spec.Port > 65535 {
		return "", domain.ErrInvalid
	}
	if _, err := e.imageID(ctx, spec.Image); err != nil {
		return "", domain.ErrImageMissing
	}
	if spec.ArtifactDigest == "" || spec.ArtifactID == "" {
		return "", domain.ErrInvalid
	}
	if err := e.ensureInternalNetwork(ctx); err != nil {
		return "", err
	}
	binds := []string{}
	if spec.ArtifactDigest != "" {
		mount, err := e.unpack(ctx, spec)
		if err != nil {
			return "", err
		}
		binds = append(binds, mount+":/app:ro")
	}
	labels := map[string]string{}
	for key, value := range spec.Labels {
		labels[key] = value
	}
	labels["workos.purpose"] = purposeLabel
	labels["workos.runtime"] = containerprocess.Namespace()
	labels["workos.container.port"] = strconv.FormatInt(spec.Port, 10)
	if spec.ArtifactDigest != "" {
		labels["workos.artifact.digest"] = spec.ArtifactDigest
		labels["workos.artifact.id"] = spec.ArtifactID
	}
	configuration := map[string]any{
		"Image":      spec.Image,
		"User":       "65532:65532",
		"Entrypoint": spec.Command,
		"Cmd":        []string{},
		"WorkingDir": "/app",
		"Labels":     labels,
		"HostConfig": map[string]any{
			"Binds":           binds,
			"NetworkMode":     e.network(),
			"ReadonlyRootfs":  true,
			"CapDrop":         []string{"ALL"},
			"SecurityOpt":     []string{"no-new-privileges:true"},
			"PidsLimit":       spec.Policy.PidsMax,
			"Memory":          spec.Policy.MemoryMaxBytes,
			"MemorySwap":      spec.Policy.MemoryMaxBytes,
			"NanoCpus":        spec.Policy.CPUQuotaUSec * 1_000_000_000 / domain.CPUPeriodUSec,
			"Tmpfs":           map[string]string{"/tmp": "rw,nosuid,nodev," + tmpfsSize + ",noexec"},
			"RestartPolicy":   map[string]string{"Name": "no"},
			"Privileged":      false,
			"AutoRemove":      false,
			"PublishAllPorts": false,
			"LogConfig":       map[string]any{"Type": "json-file", "Config": map[string]string{"max-size": "1m", "max-file": "1"}},
		},
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := e.call(ctx, "POST", "/containers/create?name="+spec.Name, configuration, &created); err != nil {
		var apiErr *apiError
		if errors.As(err, &apiErr) && apiErr.notFound() {
			return "", domain.ErrImageMissing
		}
		if errors.As(err, &apiErr) && apiErr.status == 409 {
			return "", ports.ErrContainerAlreadyExists
		}
		return "", err
	}
	return created.ID, nil
}

func (e *Engine) unpack(ctx context.Context, spec ports.ContainerSpec) (string, error) {
	tarPath, err := e.bundles.OpenForLaunch(ctx, spec.OwnerUserID, spec.ArtifactDigest)
	if err != nil {
		return "", err
	}
	dest := filepath.Join(e.unpackRoot, spec.Name)
	verify := func() (string, error) {
		info, err := os.Lstat(dest)
		if err != nil || !info.IsDir() {
			return "", domain.ErrInvalid
		}
		stats, err := appbundle.EncodeDirectory(dest, io.Discard)
		if err != nil || stats.Digest != spec.ArtifactDigest {
			return "", domain.ErrInvalid
		}
		return dest, nil
	}
	if _, err := os.Lstat(dest); err == nil {
		return verify()
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	// Publish only a complete tree. A concurrent ensure/reconcile must never
	// inspect a partially unpacked directory or replace a live mount source.
	staging, err := os.MkdirTemp(e.unpackRoot, ".unpack-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staging)
	handle, err := os.Open(tarPath)
	if err != nil {
		return "", err
	}
	defer handle.Close()
	if _, err := appbundle.Unpack(handle, spec.ArtifactDigest, staging); err != nil {
		return "", err
	}
	if err := os.Chmod(staging, 0755); err != nil {
		return "", err
	}
	if err := unix.Renameat2(unix.AT_FDCWD, staging, unix.AT_FDCWD, dest, unix.RENAME_NOREPLACE); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return verify()
		}
		return "", err
	}
	return dest, nil
}

func (e *Engine) StartContainer(ctx context.Context, nameOrID string) error {
	err := e.call(ctx, "POST", "/containers/"+nameOrID+"/start", nil, nil)
	if err == nil {
		return nil
	}
	var apiErr *apiError
	if errors.As(err, &apiErr) && apiErr.status == http.StatusNotModified {
		return nil
	}
	if errors.As(err, &apiErr) && apiErr.notFound() {
		return ports.ErrContainerNotFound
	}
	return err
}

func (e *Engine) StopContainer(ctx context.Context, nameOrID string, timeout time.Duration) error {
	seconds := int(timeout.Seconds())
	if seconds < 1 {
		seconds = 1
	}
	err := e.call(ctx, "POST", "/containers/"+nameOrID+"/stop?t="+strconv.Itoa(seconds), nil, nil)
	var apiErr *apiError
	if errors.As(err, &apiErr) && apiErr.status == http.StatusNotModified {
		return nil
	}
	if errors.As(err, &apiErr) && apiErr.notFound() {
		return ports.ErrContainerNotFound
	}
	return err
}

func (e *Engine) RemoveContainer(ctx context.Context, nameOrID string) error {
	var document inspectDocument
	if err := e.call(ctx, "GET", "/containers/"+nameOrID+"/json", nil, &document); err != nil {
		return err
	}
	name := strings.TrimPrefix(document.Name, "/")
	if !safeName(name) || document.Config.Labels["workos.runtime"] != containerprocess.Namespace() || document.Config.Labels["workos.purpose"] != purposeLabel {
		return ports.ErrEngineUnavailable
	}
	err := e.call(ctx, "DELETE", "/containers/"+nameOrID+"?force=true&v=true", nil, nil)
	var apiErr *apiError
	if errors.As(err, &apiErr) && apiErr.status == http.StatusNotModified {
		return nil
	}
	if errors.As(err, &apiErr) && apiErr.notFound() {
		return ports.ErrContainerNotFound
	}
	if err == nil {
		err = os.RemoveAll(filepath.Join(e.unpackRoot, name))
	}
	return err
}

func (e *Engine) InspectContainer(ctx context.Context, nameOrID string) (ports.ContainerFacts, error) {
	var document inspectDocument
	if err := e.call(ctx, "GET", "/containers/"+nameOrID+"/json", nil, &document); err != nil {
		var apiErr *apiError
		if errors.As(err, &apiErr) && apiErr.notFound() {
			return ports.ContainerFacts{}, ports.ErrContainerNotFound
		}
		return ports.ContainerFacts{}, err
	}
	facts, err := factsFromInspect(document)
	if err != nil {
		return facts, err
	}
	// Labels are claims. Verify image config ID, mounted bytes and the actual
	// network object independently before asserting container identity.
	imageID, imageErr := e.imageID(ctx, document.Config.Image)
	networkErr := e.verifyNetwork(ctx)
	facts.InternalNetwork = networkErr == nil && document.HostConfig.NetworkMode == e.network()
	facts.IdentityVerified = false
	if !safeName(facts.Name) || facts.AppMountSource != filepath.Join(e.unpackRoot, facts.Name) {
		return facts, nil
	}
	stats, bundleErr := appbundle.EncodeDirectory(facts.AppMountSource, io.Discard)
	facts.IdentityVerified = imageErr == nil && imageID == document.Image && bundleErr == nil && stats.Digest == facts.ArtifactDigest &&
		document.Config.Labels["workos.runtime"] == containerprocess.Namespace() && document.Config.Labels["workos.purpose"] == purposeLabel &&
		facts.NoNewPrivileges && facts.ReadOnly && !facts.Privileged && facts.CapabilitiesAdded == 0 &&
		facts.EffectiveCapabilities == 0 && facts.BoundingCapabilities == 0 && facts.UnexpectedSecurityOpts == 0 &&
		facts.PublishedPorts == 0 && facts.InternalNetwork && facts.ConnectedNetworks == 1 && facts.AppMountRO &&
		facts.UnexpectedMounts == 0 && facts.BindMounts == 1 && facts.Devices == 0 && !facts.AutoRemove && facts.RestartPolicy == "no"
	return facts, nil
}

func (e *Engine) ListManagedContainers(ctx context.Context) ([]ports.ContainerFacts, error) {
	var items []struct {
		ID string `json:"Id"`
	}
	if err := e.call(ctx, "GET", "/containers/json?all=1&filters="+urlQuery(`{"label":["workos.purpose=runtime-app","workos.runtime=`+containerprocess.Namespace()+`"]}`), nil, &items); err != nil {
		return nil, err
	}
	out := make([]ports.ContainerFacts, 0, len(items))
	for _, item := range items {
		facts, err := e.InspectContainer(ctx, item.ID)
		if err != nil {
			continue
		}
		out = append(out, facts)
	}
	return out, nil
}

func urlQuery(raw string) string {
	return url.QueryEscape(raw)
}

func (e *Engine) network() string { return networkName + "-" + containerprocess.Namespace() }

func (e *Engine) verifyNetwork(ctx context.Context) error {
	var network struct {
		Name     string
		Internal bool
		Driver   string
		Labels   map[string]string
	}
	if err := e.call(ctx, "GET", "/networks/"+e.network(), nil, &network); err != nil {
		return err
	}
	if network.Name != e.network() || !network.Internal || network.Driver != "bridge" ||
		network.Labels["workos.runtime"] != containerprocess.Namespace() || network.Labels["workos.purpose"] != "runtime-app-net" {
		return ports.ErrEngineUnavailable
	}
	return nil
}
func (e *Engine) ensureInternalNetwork(ctx context.Context) error {
	err := e.verifyNetwork(ctx)
	if err == nil {
		return nil
	}
	var apiErr *apiError
	if !errors.As(err, &apiErr) || !apiErr.notFound() {
		return err
	}
	if err := e.call(ctx, "POST", "/networks/create", map[string]any{
		"Name": e.network(), "Internal": true, "Driver": "bridge",
		"Labels": map[string]string{"workos.purpose": "runtime-app-net", "workos.runtime": containerprocess.Namespace()},
	}, nil); err != nil {
		return err
	}
	return e.verifyNetwork(ctx)
}
func safeName(name string) bool {
	return regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`).MatchString(name)
}
func (e *Engine) imageID(ctx context.Context, ref string) (string, error) {
	if !domain.ValidImage(ref) {
		return "", domain.ErrInvalid
	}
	var image struct {
		ID          string `json:"Id"`
		RepoDigests []string
	}
	if err := e.call(ctx, "GET", "/images/"+url.PathEscape(ref)+"/json", nil, &image); err != nil {
		return "", err
	}
	if !slices.Contains(image.RepoDigests, ref) || !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(image.ID) {
		return "", ports.ErrEngineUnavailable
	}
	return image.ID, nil
}

type inspectDocument struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Image  string `json:"Image"`
	Path   string `json:"Path"`
	Args   []string
	Config struct {
		Image      string            `json:"Image"`
		Labels     map[string]string `json:"Labels"`
		Entrypoint []string          `json:"Entrypoint"`
		Cmd        []string          `json:"Cmd"`
	} `json:"Config"`
	State struct {
		Running   bool `json:"Running"`
		Pid       int  `json:"Pid"`
		ExitCode  int  `json:"ExitCode"`
		OOMKilled bool `json:"OOMKilled"`
	} `json:"State"`
	HostConfig struct {
		ReadonlyRootfs bool   `json:"ReadonlyRootfs"`
		Privileged     bool   `json:"Privileged"`
		AutoRemove     bool   `json:"AutoRemove"`
		NetworkMode    string `json:"NetworkMode"`
		CapDrop        []string
		CapAdd         []string          `json:"CapAdd"`
		SecurityOpt    []string          `json:"SecurityOpt"`
		Binds          []string          `json:"Binds"`
		Devices        []any             `json:"Devices"`
		Tmpfs          map[string]string `json:"Tmpfs"`
		RestartPolicy  struct {
			Name string `json:"Name"`
		} `json:"RestartPolicy"`
		Memory    int64 `json:"Memory"`
		PidsLimit int64 `json:"PidsLimit"`
		NanoCpus  int64 `json:"NanoCpus"`
	} `json:"HostConfig"`
	NetworkSettings struct {
		Networks map[string]struct {
			IPAddress string `json:"IPAddress"`
		} `json:"Networks"`
		Ports map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"Ports"`
	} `json:"NetworkSettings"`
	Mounts []struct {
		Type        string `json:"Type"`
		Destination string `json:"Destination"`
		Source      string `json:"Source"`
		RW          bool   `json:"RW"`
	} `json:"Mounts"`
}

func factsFromInspect(document inspectDocument) (ports.ContainerFacts, error) {
	name := strings.TrimPrefix(document.Name, "/")
	command := append([]string(nil), document.Config.Entrypoint...)
	if len(command) == 0 {
		command = append(command, document.Config.Cmd...)
	}
	facts := ports.ContainerFacts{
		ID: document.ID, Name: name,
		Running: document.State.Running, ExitCode: document.State.ExitCode,
		PID: document.State.Pid, OOMKilled: document.State.OOMKilled,
		Labels: document.Config.Labels, Image: document.Config.Image,
		Command: command, ReadOnly: document.HostConfig.ReadonlyRootfs,
		Privileged: document.HostConfig.Privileged, AutoRemove: document.HostConfig.AutoRemove,
		CapabilitiesAdded: len(document.HostConfig.CapAdd),
		NetworkMode:       document.HostConfig.NetworkMode,
		RestartPolicy:     document.HostConfig.RestartPolicy.Name,
		BindMounts:        len(document.HostConfig.Binds),
		Devices:           len(document.HostConfig.Devices),
		Tmpfs:             document.HostConfig.Tmpfs,
		ImageDigest:       document.Image,
	}
	if portLabel := document.Config.Labels["workos.container.port"]; portLabel != "" {
		if parsed, err := strconv.Atoi(portLabel); err == nil {
			facts.HostPort = int32(parsed)
			facts.ContainerPort = int32(parsed)
		}
	}
	facts.ArtifactDigest = document.Config.Labels["workos.artifact.digest"]
	// Network internal flag requires an independent Engine API readback.
	facts.ConnectedNetworks = len(document.NetworkSettings.Networks)
	if net, ok := document.NetworkSettings.Networks[document.HostConfig.NetworkMode]; ok {
		facts.HostIP = net.IPAddress
	}
	for _, option := range document.HostConfig.SecurityOpt {
		if option == "no-new-privileges" || option == "no-new-privileges:true" || option == "no-new-privileges=true" {
			facts.NoNewPrivileges = true
			continue
		}
		facts.UnexpectedSecurityOpts++
	}
	for _, mount := range document.Mounts {
		if mount.Type == "bind" && mount.Destination == "/app" && !mount.RW {
			facts.AppMountRO = true
			facts.AppMountSource = mount.Source
			continue
		}
		if mount.Type == "tmpfs" && mount.Destination == "/tmp" {
			continue
		}
		facts.UnexpectedMounts++
	}
	for _, bindings := range document.NetworkSettings.Ports {
		facts.PublishedPorts += len(bindings)
	}
	// Missing drop-all is unknown/unsafe, never an all-zero capability claim.
	if !slices.Contains(document.HostConfig.CapDrop, "ALL") {
		facts.EffectiveCapabilities = 1
		facts.BoundingCapabilities = 1
	}
	return facts, nil
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
		return ports.ErrEngineUnavailable
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
