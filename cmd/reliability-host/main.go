package main

import (
	"log/slog"
	"os"

	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	"github.com/yangtao121/workos/gen/go/workos/common/v1/commonv1connect"
	"github.com/yangtao121/workos/gen/go/workos/incident/v1/incidentv1connect"
	"github.com/yangtao121/workos/internal/platform/config"
	"github.com/yangtao121/workos/internal/platform/httpserver"
	"github.com/yangtao121/workos/internal/platform/logging"
	"github.com/yangtao121/workos/internal/platform/systemhandler"
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
	mux := httpserver.NewMux("reliability-host", nil)
	incidentPath, incidentHandler := incidentv1connect.NewIncidentServiceHandler(incidentv1connect.UnimplementedIncidentServiceHandler{})
	mux.Handle(incidentPath, incidentHandler)
	systemPath, systemHandler := commonv1connect.NewSystemServiceHandler(systemhandler.New("reliability-host", commonv1.HealthState_HEALTH_STATE_DEGRADED,
		&commonv1.FeatureCapability{Id: "supervisor", Available: false, Reason: "cgroup enforcement not implemented"},
		&commonv1.FeatureCapability{Id: "incident-manager", Available: false, Reason: "contract only"},
		&commonv1.FeatureCapability{Id: "repair-orchestrator", Available: false, Reason: "contract only"},
	))
	mux.Handle(systemPath, systemHandler)
	return httpserver.Run("reliability-host", cfg.HTTP.Address, mux, logger, "", "", cfg.Telemetry.OTLPEndpoint)
}
