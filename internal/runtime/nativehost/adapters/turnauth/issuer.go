// Package turnauth implements coturn-compatible ephemeral transport credentials.
package turnauth

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/runtime/nativehost/ports"
)

const Lifetime = 90 * time.Second

type Issuer struct {
	mode   string
	urls   []string
	secret []byte
	now    func() time.Time
}

var errConfiguration = errors.New("native relay requires valid TURN URLs and an owner-only secret file")

func New(mode, urls, secretFile string) (*Issuer, error) {
	if mode == "" {
		mode = "loopback"
	}
	issuer := &Issuer{mode: mode, now: time.Now}
	if mode == "loopback" || mode == "lan" {
		if urls != "" || secretFile != "" {
			return nil, errConfiguration
		}
		return issuer, nil
	}
	if mode != "relay" || !filepath.IsAbs(secretFile) {
		return nil, errConfiguration
	}
	for _, raw := range strings.Split(urls, ",") {
		value := strings.TrimSpace(raw)
		// This deployment profile supports TURN UDP/TCP and TURN over TLS,
		// with explicit ports; no userinfo, paths, fragments or arbitrary URLs.
		parts := strings.SplitN(value, ":", 2)
		if len(parts) != 2 || (parts[0] != "turn" && parts[0] != "turns") {
			return nil, errConfiguration
		}
		address := strings.SplitN(parts[1], "?", 2)
		if len(address) == 2 && address[1] != "transport=udp" && address[1] != "transport=tcp" {
			return nil, errConfiguration
		}
		if parts[0] == "turns" && len(address) == 2 && address[1] != "transport=tcp" {
			return nil, errConfiguration
		}
		host, port, err := net.SplitHostPort(address[0])
		number, portErr := strconv.Atoi(port)
		if err != nil || portErr != nil || number < 1 || number > 65535 || host == "" || strings.ContainsAny(host, "@/#? \\%\t\n\r") {
			return nil, errConfiguration
		}
		issuer.urls = append(issuer.urls, value)
	}
	if len(issuer.urls) == 0 || len(issuer.urls) > 4 {
		return nil, errConfiguration
	}
	// Open once without following the leaf symlink, then inspect that same fd.
	fd, err := syscall.Open(secretFile, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errConfiguration
	}
	file := os.NewFile(uintptr(fd), "native-turn-secret")
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() < 32 || info.Size() > 257 {
		return nil, errConfiguration
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return nil, errConfiguration
	}
	secret, err := io.ReadAll(io.LimitReader(file, 258))
	if err != nil {
		return nil, errConfiguration
	}
	secret = []byte(strings.TrimSuffix(string(secret), "\n"))
	if len(secret) < 32 || len(secret) > 256 {
		return nil, errConfiguration
	}
	for _, b := range secret {
		if b < 33 || b > 126 {
			return nil, errConfiguration
		}
	}
	issuer.secret = secret
	return issuer, nil
}

func (i *Issuer) Issue() (ports.Connectivity, error) {
	expires := i.now().UTC().Add(Lifetime).Truncate(time.Second)
	result := ports.Connectivity{Mode: i.mode, RelayOnly: i.mode == "relay", ExpiresAt: expires}
	if !result.RelayOnly {
		return result, nil
	}
	if len(i.secret) < 32 || len(i.urls) == 0 {
		return ports.Connectivity{}, errConfiguration
	}
	username := strconv.FormatInt(expires.Unix(), 10) + ":" + (ids.UUIDv7{}).New()
	mac := hmac.New(sha1.New, i.secret) // coturn REST credential protocol, not password hashing.
	_, _ = mac.Write([]byte(username))
	result.Servers = []ports.IceServer{{URLs: append([]string(nil), i.urls...), Username: username, Credential: base64.StdEncoding.EncodeToString(mac.Sum(nil))}}
	return result, nil
}
