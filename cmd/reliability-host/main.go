// reliability-host observes supervised workloads through the private,
// versioned runtime contract, persists its own Incident facts, and applies
// bounded, idempotent restart/stop decisions (ADR-0006). It never queries
// the runtime schema and never runs the engine itself.
package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	"github.com/yangtao121/workos/gen/go/workos/common/v1/commonv1connect"
	workloadv1connect "github.com/yangtao121/workos/gen/go/workos/workload/v1/workloadv1connect"
	"github.com/yangtao121/workos/internal/platform/config"
	"github.com/yangtao121/workos/internal/platform/database"
	"github.com/yangtao121/workos/internal/platform/httpserver"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/logging"
	"github.com/yangtao121/workos/internal/platform/systemhandler"
	"github.com/yangtao121/workos/internal/platform/telemetry"
	"github.com/yangtao121/workos/internal/reliability/adapters/postgres"
	"github.com/yangtao121/workos/internal/reliability/application"
	"github.com/yangtao121/workos/internal/reliability/transport"
)

func main() {
	logger := logging.New("reliability-host")
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
	if err := cfg.ValidateReliabilityHost(); err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	ready := func(ctx context.Context) error { return pool.Ping(ctx) }
	mux := httpserver.NewMux("reliability-host", ready)

	repository, err := postgres.New(pool)
	if err != nil {
		return err
	}
	incidentService, err := application.NewIncidentService(repository)
	if err != nil {
		return err
	}
	incidentPath, incidentHandler := transport.NewIncidentConnectHandler(incidentService)
	mux.Handle(incidentPath, identity.Middleware(incidentHandler))

	// The runtime client observes and controls supervised workloads over the
	// private, versioned contract. It is a hard dependency of the loop but
	// never of the public incident RPCs: the UI degrades, the process lives.
	runtimeClient, err := transport.NewRuntimeClient(
		workloadv1connect.NewSupervisedWorkloadServiceClient(telemetry.HTTPClient(), cfg.Services.Runtime))
	if err != nil {
		return err
	}
	supervisor, err := application.NewSupervisor(runtimeClient, runtimeClient, repository,
		ids.UUIDv7{}, application.Config{
			StablePollsToResolve: cfg.Reliability.StablePollsToResolve,
			MaxIncidentsPerPoll:  cfg.Reliability.MaxIncidentsPerPoll,
		}, logger)
	if err != nil {
		return err
	}
	supervisorStop := make(chan struct{})
	defer close(supervisorStop)
	go func() {
		ticker := time.NewTicker(cfg.Reliability.PollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-supervisorStop:
				return
			case <-ticker.C:
				pollCtx, cancel := context.WithTimeout(ctx, cfg.Reliability.PollTimeout)
				if err := supervisor.Poll(pollCtx); err != nil {
					logger.Warn("supervision poll pending", "error", err)
				}
				cancel()
			}
		}
	}()

	systemPath, systemHandler := commonv1connect.NewSystemServiceHandler(systemhandler.New("reliability-host", commonv1.HealthState_HEALTH_STATE_DEGRADED,
		&commonv1.FeatureCapability{Id: "supervisor", Available: true, Reason: "observation, incident, and bounded restart/stop loop"},
		&commonv1.FeatureCapability{Id: "incident-manager", Available: true, Reason: "owner-scoped incident list and acknowledge"},
		&commonv1.FeatureCapability{Id: "repair-orchestrator", Available: false, Reason: "contract only"},
		&commonv1.FeatureCapability{Id: "deployment-controller", Available: false, Reason: "contract only"},
	))
	mux.Handle(systemPath, systemHandler)
	return httpserver.Run("reliability-host", cfg.HTTP.Address, mux, logger, "", "", cfg.Telemetry.OTLPEndpoint)
}
