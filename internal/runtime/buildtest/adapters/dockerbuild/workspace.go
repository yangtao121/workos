package dockerbuild

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yangtao121/workos/internal/platform/bundleformat"
	"github.com/yangtao121/workos/internal/platform/containerprocess"
	"github.com/yangtao121/workos/internal/runtime/buildtest/ports"
)

// workspace uses a Docker-managed, size-limited tmpfs volume. The keeper holds
// the mount across build/test containers; no untrusted command can write a host
// bind mount. Its lifetime is bounded even if runtime-host dies.
func (e *Engine) workspace(ctx context.Context, spec ports.RunSpec) (string, string, func(), error) {
	suffix := make([]byte, 12)
	if _, err := rand.Read(suffix); err != nil {
		return "", "", nil, err
	}
	volume := "workos-build-" + hex.EncodeToString(suffix)
	labels := map[string]string{"workos.purpose": "runtime-build", "workos.runtime": containerprocess.Namespace()}
	if err := e.call(ctx, "POST", "/volumes/create", map[string]any{
		"Name": volume, "Driver": "local", "Labels": labels,
		"DriverOpts": map[string]string{"type": "tmpfs", "device": "tmpfs", "o": "size=1073741824,exec,mode=0700,uid=" + strconv.Itoa(os.Getuid()) + ",gid=" + strconv.Itoa(os.Getgid())},
	}, nil); err != nil {
		return "", "", nil, err
	}
	keeper := ""
	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if keeper != "" {
			_ = e.call(cleanupCtx, "DELETE", "/containers/"+keeper+"?force=true&v=true", nil, nil)
		}
		_ = e.call(cleanupCtx, "DELETE", "/volumes/"+volume, nil, nil)
	}
	var created struct {
		ID string `json:"Id"`
	}
	err := e.call(ctx, "POST", "/containers/create", map[string]any{
		"Image": spec.BaseImage, "Entrypoint": []string{"/bin/sleep"}, "Cmd": []string{strconv.Itoa(int(spec.Timeout.Seconds()) + 60)},
		"User": strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()), "Labels": labels,
		"HostConfig": map[string]any{
			"Binds": []string{volume + ":/src:rw"}, "NetworkMode": "none", "ReadonlyRootfs": true,
			"CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges:true"},
			"Memory": int64(64 << 20), "MemorySwap": int64(64 << 20), "PidsLimit": 16, "NanoCpus": int64(100_000_000),
			"LogConfig": map[string]string{"Type": "none"},
		},
	}, &created)
	if err != nil {
		cleanup()
		return "", "", nil, err
	}
	keeper = created.ID
	if err := e.call(ctx, "POST", "/containers/"+keeper+"/start", nil, nil); err != nil {
		cleanup()
		return "", "", nil, err
	}
	var buffer bytes.Buffer
	archive := tar.NewWriter(&buffer)
	dirs := map[string]bool{}
	for _, file := range spec.Files {
		components := strings.Split(path.Dir(file.Path), "/")
		parent := ""
		for _, part := range components {
			if part == "." {
				continue
			}
			parent = path.Join(parent, part)
			if !dirs[parent] {
				dirs[parent] = true
				if err := archive.WriteHeader(&tar.Header{Name: parent + "/", Mode: 0700, Typeflag: tar.TypeDir, Uid: os.Getuid(), Gid: os.Getgid()}); err != nil {
					cleanup()
					return "", "", nil, err
				}
			}
		}
		mode := int64(0600)
		if file.Executable {
			mode = 0700
		}
		if err := archive.WriteHeader(&tar.Header{Name: file.Path, Size: int64(len(file.Content)), Mode: mode, Typeflag: tar.TypeReg, Uid: os.Getuid(), Gid: os.Getgid()}); err != nil {
			cleanup()
			return "", "", nil, err
		}
		if _, err := archive.Write(file.Content); err != nil {
			cleanup()
			return "", "", nil, err
		}
	}
	if err := archive.Close(); err != nil {
		cleanup()
		return "", "", nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "PUT", e.baseURL+"/containers/"+keeper+"/archive?path=/src", &buffer)
	if err != nil {
		cleanup()
		return "", "", nil, err
	}
	req.Header.Set("Content-Type", "application/x-tar")
	response, err := e.client.Do(req)
	if err != nil {
		cleanup()
		return "", "", nil, err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		cleanup()
		return "", "", nil, fmt.Errorf("source materialization status %d", response.StatusCode)
	}
	return volume, keeper, cleanup, nil
}

// freezeOutput accepts only bounded regular files and directories from Docker's
// archive API. Links, sparse data, devices, duplicate entries and path escape
// are rejected before the package can become a build result.
func (e *Engine) freezeOutput(ctx context.Context, keeper, output, dest string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", e.baseURL+"/containers/"+keeper+"/archive?path="+url.QueryEscape("/src/"+output), nil)
	if err != nil {
		return err
	}
	response, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("build output is unavailable")
	}
	if err := os.Mkdir(dest, 0700); err != nil {
		return err
	}
	reader := tar.NewReader(io.LimitReader(response.Body, bundleformat.MaxEncodedBundleBytes+1))
	prefix := path.Base(output)
	seen := map[string]bool{}
	var total int64
	count := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(header.Name, "/")
		if name == prefix && header.Typeflag == tar.TypeDir {
			continue
		}
		if !strings.HasPrefix(name, prefix+"/") {
			return errors.New("output path outside requested directory")
		}
		name = strings.TrimPrefix(name, prefix+"/")
		if err := bundleformat.ValidBundlePath(name); err != nil {
			return err
		}
		if seen[name] {
			return errors.New("duplicate output path")
		}
		seen[name] = true
		count++
		total += header.Size
		if count > bundleformat.MaxBundleFiles || header.Size < 0 || header.Size > bundleformat.MaxFileBytes || total > bundleformat.MaxTotalContentBytes {
			return bundleformat.ErrBundleTooLarge
		}
		for key := range header.PAXRecords {
			if strings.Contains(key, "sparse") {
				return bundleformat.ErrBundleInvalid
			}
		}
		target := filepath.Join(dest, name)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			mode := os.FileMode(0644)
			if header.Mode&0111 != 0 {
				mode = 0755
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			_, err = io.CopyN(f, reader, header.Size)
			closeErr := f.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return bundleformat.ErrBundleInvalid
		}
	}
	if count == 0 {
		return bundleformat.ErrBundleInvalid
	}
	return nil
}
