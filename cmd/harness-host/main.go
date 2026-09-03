package main

import (
	"context"
	"log/slog"
	"os"

	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	"github.com/yangtao121/workos/gen/go/workos/common/v1/commonv1connect"
	"github.com/yangtao121/workos/gen/go/workos/credential/v1/credentialv1connect"
	"github.com/yangtao121/workos/gen/go/workos/harness/v1/harnessv1connect"
	"github.com/yangtao121/workos/internal/harness/adapters/codex"
	"github.com/yangtao121/workos/internal/harness/adapters/deepseek"
	"github.com/yangtao121/workos/internal/harness/adapters/fake"
	"github.com/yangtao121/workos/internal/harness/adapters/genericcli"
	"github.com/yangtao121/workos/internal/harness/adapters/mcp"
	"github.com/yangtao121/workos/internal/harness/broker"
	"github.com/yangtao121/workos/internal/harness/ports"
	harnesstransport "github.com/yangtao121/workos/internal/harness/transport"
	"github.com/yangtao121/workos/internal/harness/worker"
	"github.com/yangtao121/workos/internal/platform/config"
	"github.com/yangtao121/workos/internal/platform/httpserver"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/logging"
	"github.com/yangtao121/workos/internal/platform/privatetls"
	"github.com/yangtao121/workos/internal/platform/systemhandler"
	"github.com/yangtao121/workos/internal/platform/telemetry"
)

func main() {
	logger := logging.New("harness-host")
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
	// The private execution channel is the only path to TaskExecution and
	// CredentialLease RPCs: without its client identity this process cannot
	// claim tasks at all, so startup fails instead of polling in the dark.
	if err := cfg.ValidateHarness(); err != nil {
		return err
	}
	clientTLS, err := privatetls.ClientConfig(privatetls.Identity{
		CAFile:       cfg.Harness.ExecutionCAFile,
		CertFile:     cfg.Harness.ExecutionCertFile,
		KeyFile:      cfg.Harness.ExecutionKeyFile,
		PeerIdentity: privatetls.IdentityCore,
	})
	if err != nil {
		return err
	}
	executionClient := telemetry.HTTPClientWithTLS(clientTLS)
	// DeepSeek has no long-lived API key anymore: it runs only with a
	// task-bound credential lease derived from the active task lease
	// (ADR-0009). A legacy DEEPSEEK_API_KEY surfaces as a configuration
	// issue and keeps the provider honestly unavailable.
	deepSeekProvider := deepseek.New(deepseek.Config{
		Enabled: cfg.Harness.DeepSeek.Enabled, Environment: cfg.Environment,
		BaseURL: cfg.Harness.DeepSeek.BaseURL, Model: cfg.Harness.DeepSeek.Model, Timeout: cfg.Harness.DeepSeek.Timeout,
		RuntimePath: cfg.Harness.DeepSeek.RuntimePath, CordisConfigPath: cfg.Harness.DeepSeek.CordisConfigPath,
		ConfigurationIssue: cfg.Harness.DeepSeek.ConfigurationIssue,
	}, ids.UUIDv7{})
	providers := []ports.Provider{fake.New(ids.UUIDv7{}), deepSeekProvider}
	if cfg.Harness.Generic.Enabled {
		provider, err := genericcli.New(genericcli.Config{Executable: cfg.Harness.Generic.Executable, Args: cfg.Harness.Generic.Args, Timeout: cfg.Harness.Generic.Timeout})
		if err != nil {
			return err
		}
		providers = append(providers, provider)
	}
	// Codex (ADR-0015) holds no long-lived material either: every run needs a
	// task-bound codex-auth.v1 lease, and an unavailable app server keeps the
	// provider honestly unavailable instead of silently absent.
	codexProvider := codex.New(codex.Config{
		Enabled: cfg.Harness.Codex.Enabled, AppServer: cfg.Harness.Codex.AppServer, Timeout: cfg.Harness.Codex.Timeout,
	}, ids.UUIDv7{})
	if cfg.Harness.Codex.Enabled {
		providers = append(providers, codexProvider)
	}
	// MCP (ADR-0015) declares the honest degraded subset: no streaming, no
	// usage, no budgets, and no credential path at all.
	mcpProvider := mcp.New(mcp.Config{
		Enabled: cfg.Harness.MCP.Enabled, Server: cfg.Harness.MCP.Server,
		Timeout: cfg.Harness.MCP.Timeout, Arguments: cfg.Harness.MCP.Arguments,
	})
	if cfg.Harness.MCP.Enabled {
		providers = append(providers, mcpProvider)
	}
	value := broker.New(providers...)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	credentialClient := credentialv1connect.NewCredentialLeaseServiceClient(executionClient, cfg.Harness.ExecutionURL)
	go worker.New(cfg.Harness.WorkerID, cfg.Harness.ExecutionURL, cfg.Harness.PollInterval, value, logger, credentialClient, executionClient).Run(ctx)

	mux := httpserver.NewMux("harness-host", nil)
	harnessPath, harnessHandler := harnessv1connect.NewHarnessHostServiceHandler(harnesstransport.New(value))
	mux.Handle(harnessPath, harnessHandler)
	systemPath, systemHandler := commonv1connect.NewSystemServiceHandler(systemhandler.New("harness-host", commonv1.HealthState_HEALTH_STATE_HEALTHY,
		&commonv1.FeatureCapability{Id: "fake", Available: true},
		&commonv1.FeatureCapability{Id: "generic-cli", Available: cfg.Harness.Generic.Enabled, Reason: "requires an absolute allowlisted executable"},
		&commonv1.FeatureCapability{Id: "codex", Available: cfg.Harness.Codex.Enabled && codexProvider.Describe().GetHealth() == commonv1.HealthState_HEALTH_STATE_HEALTHY, Reason: codexProvider.Describe().GetUnavailableReason()},
		&commonv1.FeatureCapability{Id: "mcp", Available: cfg.Harness.MCP.Enabled && mcpProvider.Describe().GetHealth() == commonv1.HealthState_HEALTH_STATE_HEALTHY, Reason: mcpProvider.Describe().GetUnavailableReason()},
		&commonv1.FeatureCapability{Id: "deepseek", Available: deepSeekProvider.Describe().GetHealth() == commonv1.HealthState_HEALTH_STATE_HEALTHY, Reason: deepSeekProvider.Describe().GetUnavailableReason()},
	))
	mux.Handle(systemPath, systemHandler)
	return httpserver.Run("harness-host", cfg.HTTP.Address, mux, logger, "", "", cfg.Telemetry.OTLPEndpoint)
}
