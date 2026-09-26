package resident

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yangtao121/workos/internal/platform/containerprocess"
	"github.com/yangtao121/workos/internal/runtime/nativehost/domain"
	"github.com/yangtao121/workos/internal/runtime/nativehost/ports"
)

const childPurpose = "greenfield-resident"

type dockerAPI struct{ client *http.Client }

func newDockerAPI(socket string) *dockerAPI {
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
	}}
	return &dockerAPI{client: &http.Client{Transport: transport, Timeout: 30 * time.Second}}
}

type apiStatusError int

func (e apiStatusError) Error() string { return fmt.Sprintf("docker status %d", int(e)) }

func (d *dockerAPI) call(ctx context.Context, method, endpoint string, input, output any) error {
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker/v1.47"+endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := d.client.Do(req)
	if err != nil {
		return domain.ErrEngineUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// Docker error bodies may quote host paths or supplied environment. Never
		// expose them through Runtime logs or public Connect errors.
		return apiStatusError(response.StatusCode)
	}
	if output != nil {
		if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(output); err != nil {
			return domain.ErrEngineUnavailable
		}
	}
	return nil
}

func (d *dockerAPI) imageID(ctx context.Context, ref string) (string, error) {
	var image struct {
		ID string `json:"Id"`
	}
	if err := d.call(ctx, http.MethodGet, "/images/"+url.PathEscape(ref)+"/json", nil, &image); err != nil {
		return "", domain.ErrEngineUnavailable
	}
	if !strings.HasPrefix(image.ID, "sha256:") || len(image.ID) != len("sha256:")+64 {
		return "", domain.ErrEngineUnavailable
	}
	return image.ID, nil
}

func (d *dockerAPI) hasRuntime(ctx context.Context, name string) bool {
	var info struct {
		Runtimes map[string]json.RawMessage `json:"Runtimes"`
	}
	return d.call(ctx, http.MethodGet, "/info", nil, &info) == nil && info.Runtimes[name] != nil
}

func (d *dockerAPI) waitStopped(ctx context.Context, id string) error {
	var result struct {
		StatusCode int `json:"StatusCode"`
	}
	if err := d.call(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/wait?condition=not-running", nil, &result); err != nil || result.StatusCode != 0 {
		return domain.ErrEngineUnavailable
	}
	return nil
}

type childDevice struct {
	PathOnHost        string `json:"PathOnHost"`
	PathInContainer   string `json:"PathInContainer"`
	CgroupPermissions string `json:"CgroupPermissions"`
}
type deviceRequest struct {
	Driver       string     `json:"Driver"`
	Count        int        `json:"Count"`
	Capabilities [][]string `json:"Capabilities"`
}

type childInspect struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Image  string `json:"Image"`
	Config struct {
		Image      string            `json:"Image"`
		User       string            `json:"User"`
		WorkingDir string            `json:"WorkingDir"`
		Entrypoint []string          `json:"Entrypoint"`
		Cmd        []string          `json:"Cmd"`
		Env        []string          `json:"Env"`
		Labels     map[string]string `json:"Labels"`
	} `json:"Config"`
	State struct {
		Running   bool `json:"Running"`
		PID       int  `json:"Pid"`
		OOMKilled bool `json:"OOMKilled"`
	} `json:"State"`
	HostConfig struct {
		Binds           []string          `json:"Binds"`
		NetworkMode     string            `json:"NetworkMode"`
		ReadonlyRootfs  bool              `json:"ReadonlyRootfs"`
		Privileged      bool              `json:"Privileged"`
		CapDrop         []string          `json:"CapDrop"`
		CapAdd          []string          `json:"CapAdd"`
		SecurityOpt     []string          `json:"SecurityOpt"`
		PidsLimit       int64             `json:"PidsLimit"`
		Memory          int64             `json:"Memory"`
		MemorySwap      int64             `json:"MemorySwap"`
		NanoCpus        int64             `json:"NanoCpus"`
		Tmpfs           map[string]string `json:"Tmpfs"`
		ShmSize         int64             `json:"ShmSize"`
		GroupAdd        []string          `json:"GroupAdd"`
		Devices         []childDevice     `json:"Devices"`
		DeviceRequests  []deviceRequest   `json:"DeviceRequests"`
		AutoRemove      bool              `json:"AutoRemove"`
		PublishAllPorts bool              `json:"PublishAllPorts"`
		RestartPolicy   struct {
			Name string `json:"Name"`
		} `json:"RestartPolicy"`
	} `json:"HostConfig"`
	NetworkSettings struct {
		Ports    map[string][]any `json:"Ports"`
		Networks map[string]any   `json:"Networks"`
	} `json:"NetworkSettings"`
	Mounts []struct {
		Type        string `json:"Type"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
		RW          bool   `json:"RW"`
	} `json:"Mounts"`
}

func (d *dockerAPI) inspect(ctx context.Context, nameOrID string) (childInspect, error) {
	var inspected childInspect
	err := d.call(ctx, http.MethodGet, "/containers/"+url.PathEscape(nameOrID)+"/json", nil, &inspected)
	if errors.Is(err, apiStatusError(http.StatusNotFound)) {
		return childInspect{}, domain.ErrResidentChildNotFound
	}
	if err != nil {
		return childInspect{}, domain.ErrEngineUnavailable
	}
	return inspected, nil
}

func (d *dockerAPI) remove(ctx context.Context, id string) error {
	err := d.call(ctx, http.MethodDelete, "/containers/"+url.PathEscape(id)+"?force=true&v=true", nil, nil)
	if errors.Is(err, apiStatusError(http.StatusNotFound)) {
		return nil
	}
	if err != nil {
		return domain.ErrEngineUnavailable
	}
	if _, err := d.inspect(ctx, id); !errors.Is(err, domain.ErrResidentChildNotFound) {
		return domain.ErrEngineUnavailable
	}
	return nil
}

func (d *dockerAPI) create(ctx context.Context, name string, config map[string]any) (string, error) {
	var created struct {
		ID string `json:"Id"`
	}
	err := d.call(ctx, http.MethodPost, "/containers/create?name="+url.QueryEscape(name), config, &created)
	if err != nil {
		return "", err
	}
	if len(created.ID) != 64 {
		return "", domain.ErrEngineUnavailable
	}
	return created.ID, nil
}

func (d *dockerAPI) start(ctx context.Context, id string) error {
	err := d.call(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/start", nil, nil)
	if err != nil {
		return domain.ErrEngineUnavailable
	}
	return nil
}

func childName(session domain.Session) string {
	return "workos-gf-" + strings.ReplaceAll(session.SessionID, "-", "") + "-g" + strconv.FormatInt(session.Generation, 10)
}

func childLabels(session domain.Session) map[string]string {
	labels := map[string]string{
		"workos.purpose":    childPurpose,
		"workos.runtime":    containerprocess.Namespace(),
		"workos.session":    session.SessionID,
		"workos.owner":      session.OwnerUserID,
		"workos.generation": strconv.FormatInt(session.Generation, 10),
	}
	if session.Application == domain.ApplicationTextEditor {
		labels["workos.application"] = string(session.Application)
	}
	return labels
}

func childArgs(spec ports.ResidentLaunch, renderDevice string) []string {
	args := []string{"--socket", "/run/workos/greenfield/bridge.sock", "--session-id", spec.Session.SessionID,
		"--generation", strconv.FormatInt(spec.Session.Generation, 10), "--width", strconv.Itoa(int(spec.Session.Width)),
		"--height", strconv.Itoa(int(spec.Session.Height)), "--render-device", renderDevice}
	if spec.Session.Application == domain.ApplicationTextEditor {
		args = append(args, "--application", string(spec.Session.Application))
	}
	if spec.Workspace.Directory != "" {
		args = append(args, "--workspace", "/workspace")
	}
	return args
}

func childBinds(spec ports.ResidentLaunch, ipcDir string) []string {
	binds := []string{ipcDir + ":/run/workos/greenfield:rw"}
	if spec.Workspace.Directory != "" {
		mode := "rw"
		if spec.Workspace.ReadOnly {
			mode = "ro"
		}
		binds = append(binds, spec.Workspace.Directory+":/workspace:"+mode)
	}
	return binds
}

func childEnv() []string {
	return []string{
		"HOME=/tmp", "PATH=/usr/local/bin:/usr/bin:/bin", "XDG_RUNTIME_DIR=/tmp/xdg",
		"XDG_CACHE_HOME=/tmp/cache", "XDG_CONFIG_HOME=/tmp/config", "LANG=C.UTF-8",
		"NVIDIA_VISIBLE_DEVICES=all", "NVIDIA_DRIVER_CAPABILITIES=graphics,display,utility,compute",
	}
}

func (e *Engine) childConfig(spec ports.ResidentLaunch, ipcDir string) map[string]any {
	return map[string]any{
		"Image":      e.config.Image,
		"User":       "10001:10001",
		"WorkingDir": "/opt/workos/greenfield-child",
		"Entrypoint": []string{"node", "/opt/workos/greenfield-child/bridge.mjs"},
		"Cmd":        childArgs(spec, e.config.RenderDevice),
		"Env":        childEnv(),
		"Labels":     childLabels(spec.Session),
		"HostConfig": map[string]any{
			"Binds": childBinds(spec, ipcDir), "NetworkMode": "none", "ReadonlyRootfs": true,
			"Privileged": false, "CapDrop": []string{"ALL"}, "CapAdd": []string{},
			"SecurityOpt": []string{"no-new-privileges:true"}, "PidsLimit": int64(512),
			"Memory": int64(4 << 30), "MemorySwap": int64(4 << 30), "NanoCpus": int64(4_000_000_000),
			"Tmpfs":   map[string]string{"/tmp": "rw,nosuid,nodev,size=2147483648,mode=1777"},
			"ShmSize": int64(1 << 30), "GroupAdd": []string{e.config.RenderGID},
			"Devices":        []childDevice{{PathOnHost: e.config.RenderDevice, PathInContainer: e.config.RenderDevice, CgroupPermissions: "rwm"}},
			"DeviceRequests": []deviceRequest{{Driver: e.config.GPUDriver, Count: -1, Capabilities: [][]string{{"gpu"}}}},
			"AutoRemove":     false, "PublishAllPorts": false, "RestartPolicy": map[string]string{"Name": "no"},
			"LogConfig": map[string]any{"Type": "json-file", "Config": map[string]string{"max-size": "1m", "max-file": "2"}},
		},
	}
}

func (e *Engine) verifyChild(spec ports.ResidentLaunch, ipcDir, imageID string, child childInspect) error {
	return e.verifyChildProfile(spec, ipcDir, imageID, child, true)
}

func (e *Engine) verifyChildProfile(spec ports.ResidentLaunch, ipcDir, imageID string, child childInspect, requireRunning bool) error {
	if child.ID == "" || child.Image != imageID || strings.TrimPrefix(child.Name, "/") != childName(spec.Session) ||
		child.Config.User != "10001:10001" || child.Config.WorkingDir != "/opt/workos/greenfield-child" ||
		!reflect.DeepEqual(child.Config.Entrypoint, []string{"node", "/opt/workos/greenfield-child/bridge.mjs"}) ||
		!reflect.DeepEqual(child.Config.Cmd, childArgs(spec, e.config.RenderDevice)) {
		return domain.ErrEngineUnavailable
	}
	for key, value := range childLabels(spec.Session) {
		if child.Config.Labels[key] != value {
			return domain.ErrEngineUnavailable
		}
	}
	if (requireRunning && (!child.State.Running || child.State.PID <= 0 || child.State.OOMKilled)) ||
		child.HostConfig.NetworkMode != "none" || !child.HostConfig.ReadonlyRootfs || child.HostConfig.Privileged ||
		child.HostConfig.AutoRemove || child.HostConfig.PublishAllPorts || child.HostConfig.RestartPolicy.Name != "no" ||
		!slices.Contains(child.HostConfig.CapDrop, "ALL") || len(child.HostConfig.CapAdd) != 0 ||
		!slices.ContainsFunc(child.HostConfig.SecurityOpt, func(s string) bool { return s == "no-new-privileges" || s == "no-new-privileges:true" }) ||
		child.HostConfig.PidsLimit != 512 || child.HostConfig.Memory != int64(4<<30) ||
		child.HostConfig.MemorySwap != int64(4<<30) || child.HostConfig.NanoCpus != int64(4_000_000_000) ||
		child.HostConfig.ShmSize != int64(1<<30) || !slices.Contains(child.HostConfig.GroupAdd, e.config.RenderGID) ||
		!reflect.DeepEqual(child.HostConfig.Devices, []childDevice{{PathOnHost: e.config.RenderDevice, PathInContainer: e.config.RenderDevice, CgroupPermissions: "rwm"}}) ||
		!reflect.DeepEqual(child.HostConfig.DeviceRequests, []deviceRequest{{Driver: e.config.GPUDriver, Count: -1, Capabilities: [][]string{{"gpu"}}}}) {
		return domain.ErrEngineUnavailable
	}
	if !sameStrings(child.HostConfig.Binds, childBinds(spec, ipcDir)) || len(child.HostConfig.Tmpfs) != 1 || child.HostConfig.Tmpfs["/tmp"] != "rw,nosuid,nodev,size=2147483648,mode=1777" {
		return domain.ErrEngineUnavailable
	}
	for _, bindings := range child.NetworkSettings.Ports {
		if len(bindings) != 0 {
			return domain.ErrEngineUnavailable
		}
	}
	if len(child.NetworkSettings.Networks) > 1 || (len(child.NetworkSettings.Networks) == 1 && child.NetworkSettings.Networks["none"] == nil) {
		return domain.ErrEngineUnavailable
	}
	expectedMounts := map[string]struct {
		source string
		rw     bool
	}{"/run/workos/greenfield": {source: ipcDir, rw: true}}
	if spec.Workspace.Directory != "" {
		expectedMounts["/workspace"] = struct {
			source string
			rw     bool
		}{source: spec.Workspace.Directory, rw: !spec.Workspace.ReadOnly}
	}
	seenMounts := map[string]bool{}
	for _, mount := range child.Mounts {
		if mount.Type == "tmpfs" && mount.Destination == "/tmp" {
			continue
		}
		expected, allowed := expectedMounts[mount.Destination]
		if !allowed || seenMounts[mount.Destination] || mount.Type != "bind" || mount.Source != expected.source || mount.RW != expected.rw {
			return domain.ErrEngineUnavailable
		}
		seenMounts[mount.Destination] = true
	}
	if len(seenMounts) != len(expectedMounts) {
		return domain.ErrEngineUnavailable
	}
	for _, item := range child.Config.Env {
		name, _, ok := strings.Cut(item, "=")
		if !ok || !slices.Contains([]string{"HOME", "PATH", "XDG_RUNTIME_DIR", "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "LANG", "NVIDIA_VISIBLE_DEVICES", "NVIDIA_DRIVER_CAPABILITIES", "NODE_VERSION", "YARN_VERSION", "YARN_NPM_REGISTRY_SERVER", "ELECTRON_OZONE_PLATFORM_HINT", "PLAYWRIGHT_BROWSERS_PATH"}, name) {
			return domain.ErrEngineUnavailable
		}
	}
	for _, required := range childEnv() {
		if !slices.Contains(child.Config.Env, required) {
			return domain.ErrEngineUnavailable
		}
	}
	return nil
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, value := range a {
		if !slices.Contains(b, value) {
			return false
		}
	}
	return true
}

func cleanIPCDir(root string, session domain.Session) string {
	return filepath.Join(root, childName(session))
}
