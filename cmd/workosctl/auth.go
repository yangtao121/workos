package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/term"

	authv1 "github.com/yangtao121/workos/gen/go/workos/auth/v1"
	"github.com/yangtao121/workos/gen/go/workos/auth/v1/authv1connect"
	"github.com/yangtao121/workos/internal/platform/config"
)

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
	username, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return errors.New("could not read username")
	}
	username = strings.TrimSpace(username)
	fmt.Fprint(os.Stderr, "Password (at least 12 bytes): ")
	first, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return errors.New("could not read password")
	}
	fmt.Fprint(os.Stderr, "Confirm password: ")
	second, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return errors.New("could not confirm password")
	}
	if string(first) != string(second) {
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
