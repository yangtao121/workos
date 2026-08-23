package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/yangtao121/workos/internal/gateway"
	"github.com/yangtao121/workos/internal/platform/config"
	"github.com/yangtao121/workos/internal/platform/httpserver"
	"github.com/yangtao121/workos/internal/platform/logging"
	"github.com/yangtao121/workos/internal/platform/telemetry"
)

func main() {
	logger := logging.New("workos-gateway")
	if err := run(logger); err != nil {
		logger.Error("service stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := cfg.ValidateGateway(); err != nil {
		return err
	}
	handler, err := gateway.New(cfg, logger)
	if err != nil {
		return err
	}
	client := telemetry.HTTPClient()
	ready := func(ctx context.Context) error {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.Services.Core+"/readyz", nil)
		if err != nil {
			return err
		}
		response, err := client.Do(request)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("core readiness returned %s", response.Status)
		}
		return nil
	}
	mux := httpserver.NewMux("workos-gateway", ready)
	mux.Handle("/", handler)
	return httpserver.Run("workos-gateway", cfg.HTTP.Address, mux, logger, cfg.HTTP.TLSCertFile, cfg.HTTP.TLSKeyFile, cfg.Telemetry.OTLPEndpoint)
}
