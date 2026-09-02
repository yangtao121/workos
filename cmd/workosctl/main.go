package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/skip2/go-qrcode"

	authv1 "github.com/yangtao121/workos/gen/go/workos/auth/v1"
	"github.com/yangtao121/workos/gen/go/workos/auth/v1/authv1connect"
	"github.com/yangtao121/workos/internal/platform/config"
	"github.com/yangtao121/workos/internal/platform/database"
	"github.com/yangtao121/workos/internal/platform/migrations"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "workosctl:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: workosctl bootstrap | db migrate | owner init | device pair | credential put|rotate|revoke|list | index status|rebuild|job | doctor")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	switch {
	case len(args) == 1 && args[0] == "bootstrap":
		if err := migrations.Run(ctx, cfg.DatabaseURL); err != nil {
			return err
		}
		return initializeOwner(ctx, cfg)
	case len(args) == 2 && args[0] == "db" && args[1] == "migrate":
		return migrations.Run(ctx, cfg.DatabaseURL)
	case len(args) == 2 && args[0] == "owner" && args[1] == "init":
		return initializeOwner(ctx, cfg)
	case len(args) == 2 && args[0] == "device" && args[1] == "pair":
		return devicePair(ctx, cfg)
	case len(args) >= 1 && args[0] == "index":
		return runIndex(ctx, cfg, args[1:])
	case len(args) >= 2 && args[0] == "credential":
		return runCredential(ctx, cfg, args[1:])
	case len(args) == 1 && args[0] == "doctor":
		return doctor(ctx, cfg)
	default:
		return errors.New("usage: workosctl bootstrap | db migrate | owner init | device pair | credential put|rotate|revoke|list | index status|rebuild|job | doctor")
	}
}

// devicePair rotates one short-lived pairing ticket through the Gateway's
// private admin Unix socket and prints the one-time pairing URL plus a
// terminal QR. The CLI never opens the database and never sees any Gateway
// auth table: the raw ticket secret exists only in this RPC response.
func devicePair(ctx context.Context, cfg config.Config) error {
	if cfg.Auth.DevBypass {
		return errors.New("device pairing requires production auth mode; unset WORKOS_DEV_AUTH_BYPASS and configure the gateway admin socket")
	}
	if cfg.Auth.AdminSocketPath == "" {
		return errors.New("WORKOS_AUTH_ADMIN_SOCKET is not configured; the gateway admin socket path is required")
	}
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				dialer := &net.Dialer{Timeout: 3 * time.Second}
				return dialer.DialContext(ctx, "unix", cfg.Auth.AdminSocketPath)
			},
		},
	}
	admin := authv1connect.NewDeviceAuthAdminServiceClient(client, "http://unix")
	response, err := admin.RotatePairingTicket(ctx, connect.NewRequest(&authv1.DeviceAuthAdminServiceRotatePairingTicketRequest{}))
	if err != nil {
		// Connect errors are sanitized by the gateway; the raw ticket never
		// appears in a failure.
		return fmt.Errorf("rotate pairing ticket: %w", err)
	}
	ticket := response.Msg.GetTicket()
	if ticket == nil || ticket.GetPairingUrl() == "" {
		return errors.New("gateway returned an empty pairing ticket")
	}
	fmt.Println("Pairing ticket rotated. Any previously displayed QR code is now invalid.")
	fmt.Printf("Expires at: %s\n", ticket.GetExpiresAt().AsTime().UTC().Format(time.RFC3339))
	fmt.Printf("Public origin: %s\n", ticket.GetPublicOrigin())
	fmt.Printf("TLS fingerprint: %s\n", ticket.GetTlsFingerprint())
	fmt.Println("Pairing URL (also encoded in the QR below):")
	fmt.Println(ticket.GetPairingUrl())
	qr, err := qrcode.New(ticket.GetPairingUrl(), qrcode.Low)
	if err != nil {
		return fmt.Errorf("render pairing QR: %w", err)
	}
	fmt.Println()
	fmt.Println(qr.ToSmallString(false))
	return nil
}

func initializeOwner(ctx context.Context, cfg config.Config) error {
	conn, err := pgx.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer conn.Close(ctx) //nolint:errcheck
	now := time.Now().UTC()
	if _, err := conn.Exec(ctx, `INSERT INTO workos_core.users(id,kind,display_name,created_at)
		VALUES ($1,'owner','WorkOS Owner',$2) ON CONFLICT (id) DO NOTHING`, cfg.Auth.OwnerID, now); err != nil {
		return fmt.Errorf("initialize owner: %w", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO workos_core.devices(id,user_id,name,created_at)
		VALUES ($1,$2,'Development Device',$3) ON CONFLICT (id) DO NOTHING`, cfg.Auth.DeviceID, cfg.Auth.OwnerID, now); err != nil {
		return fmt.Errorf("initialize device: %w", err)
	}
	return nil
}

func doctor(ctx context.Context, cfg config.Config) error {
	result := map[string]any{}
	failures := 0
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err == nil {
		result["database"] = "ok"
		defer pool.Close()
	} else {
		result["database"] = "unavailable"
		failures++
	}
	_, cgroupErr := os.Stat("/sys/fs/cgroup/cgroup.controllers")
	result["cgroupV2"] = cgroupErr == nil
	_, podmanErr := exec.LookPath("podman")
	result["rootlessPodmanAvailable"] = podmanErr == nil

	services := map[string]string{
		"core": cfg.Services.Core, "harness": cfg.Services.Harness, "runtime": cfg.Services.Runtime,
		"reliability": cfg.Services.Reliability, "indexer": cfg.Services.Indexer,
	}
	serviceStatus := make(map[string]string, len(services))
	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			Proxy:       nil,
			DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
		},
	}
	var mu sync.Mutex
	var wait sync.WaitGroup
	for name, baseURL := range services {
		name, baseURL := name, baseURL
		wait.Add(1)
		go func() {
			defer wait.Done()
			status := "ok"
			if err := probeReadiness(ctx, client, baseURL); err != nil {
				status = "unavailable"
			}
			mu.Lock()
			serviceStatus[name] = status
			mu.Unlock()
		}()
	}
	wait.Wait()
	for _, status := range serviceStatus {
		if status != "ok" {
			failures++
		}
	}
	result["services"] = serviceStatus
	result["gatewayAddress"] = cfg.HTTP.Address
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		return fmt.Errorf("write doctor report: %w", err)
	}
	if failures > 0 {
		return fmt.Errorf("doctor found %d unavailable required dependencies", failures)
	}
	return nil
}

func probeReadiness(ctx context.Context, client *http.Client, baseURL string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/readyz", nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("readiness returned %s", response.Status)
	}
	return nil
}
