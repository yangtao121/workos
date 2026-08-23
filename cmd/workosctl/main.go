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

	"github.com/jackc/pgx/v5"

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
		return errors.New("usage: workosctl bootstrap | db migrate | owner init | doctor")
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
	case len(args) == 1 && args[0] == "doctor":
		return doctor(ctx, cfg)
	default:
		return errors.New("usage: workosctl bootstrap | db migrate | owner init | doctor")
	}
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
