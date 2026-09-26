package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/term"

	authv1 "github.com/yangtao121/workos/gen/go/workos/auth/v1"
	"github.com/yangtao121/workos/gen/go/workos/auth/v1/authv1connect"
	"github.com/yangtao121/workos/internal/gateway/auth/domain"
	"github.com/yangtao121/workos/internal/platform/config"
)

// Read one byte at a time from the TTY. A buffered username reader can read
// ahead into the next pasted line, which term.ReadPassword would then miss.
func readTerminalLine(reader io.Reader, maxBytes int) (string, error) {
	line := make([]byte, 0, 80)
	for len(line) <= maxBytes {
		var one [1]byte
		if _, err := io.ReadFull(reader, one[:]); err != nil {
			return "", err
		}
		if one[0] == '\n' {
			return string(bytes.TrimSuffix(line, []byte{'\r'})), nil
		}
		line = append(line, one[0])
	}
	return "", errors.New("terminal line too long")
}

// setPassword reads secrets only from an interactive terminal, then sends
// them to the Gateway-owned private Unix socket. Shell history, arguments,
// environment, public TLS endpoints, and stdout never carry the password.
func setPassword(ctx context.Context, cfg config.Config) error {
	if cfg.Auth.DevBypass || cfg.Auth.Mode != "password" {
		return errors.New("password setup requires production password auth mode")
	}
	if cfg.Auth.AdminSocketPath == "" {
		return errors.New("WORKOS_AUTH_ADMIN_SOCKET is required")
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return errors.New("password setup requires an interactive terminal")
	}
	fmt.Fprint(os.Stderr, "Username: ")
	username, err := readTerminalLine(os.Stdin, 512)
	if err != nil {
		return errors.New("could not read username")
	}
	username, err = domain.NormalizeLoginUsername(username)
	if err != nil {
		return errors.New("username must contain 1–80 characters after trimming and no control characters")
	}
	fmt.Fprint(os.Stderr, "Password (12–1024 UTF-8 bytes): ")
	first, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return errors.New("could not read password")
	}
	defer clear(first)
	if err := domain.ValidateOwnerPassword(string(first)); err != nil {
		return errors.New("password must contain 12–1024 valid UTF-8 bytes")
	}
	fmt.Fprint(os.Stderr, "Confirm password: ")
	second, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return errors.New("could not confirm password")
	}
	defer clear(second)
	if !bytes.Equal(first, second) {
		return errors.New("passwords do not match")
	}
	client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", cfg.Auth.AdminSocketPath)
		},
	}}
	admin := authv1connect.NewDeviceAuthAdminServiceClient(client, "http://unix")
	_, err = admin.SetPassword(ctx, connect.NewRequest(&authv1.SetPasswordRequest{Username: username, Password: string(first)}))
	if err != nil {
		return fmt.Errorf("set password: %w", err)
	}
	fmt.Fprintln(os.Stdout, "Password set. Previous password sessions have been revoked.")
	return nil
}
