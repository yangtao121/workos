package main

import (
	"log/slog"
	"os"

	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	"github.com/yangtao121/workos/gen/go/workos/common/v1/commonv1connect"
	"github.com/yangtao121/workos/gen/go/workos/surface/v1/surfacev1connect"
	"github.com/yangtao121/workos/gen/go/workos/workload/v1/workloadv1connect"
	"github.com/yangtao121/workos/internal/platform/config"
	"github.com/yangtao121/workos/internal/platform/httpserver"
	"github.com/yangtao121/workos/internal/platform/logging"
	"github.com/yangtao121/workos/internal/platform/systemhandler"
	runtimetransport "github.com/yangtao121/workos/internal/runtime/transport"
)

func main() {
	logger := logging.New("runtime-host")
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
	mux := httpserver.NewMux("runtime-host", nil)
	workloadPath, workloadHandler := workloadv1connect.NewWorkloadServiceHandler(runtimetransport.NewWorkloadHandler())
	mux.Handle(workloadPath, workloadHandler)
	surfacePath, surfaceHandler := surfacev1connect.NewSurfaceServiceHandler(surfacev1connect.UnimplementedSurfaceServiceHandler{})
	mux.Handle(surfacePath, surfaceHandler)
	systemPath, systemHandler := commonv1connect.NewSystemServiceHandler(systemhandler.New("runtime-host", commonv1.HealthState_HEALTH_STATE_DEGRADED,
		&commonv1.FeatureCapability{Id: "node-inspection", Available: true},
		&commonv1.FeatureCapability{Id: "container-runner", Available: false, Reason: "not implemented"},
		&commonv1.FeatureCapability{Id: "surface-broker", Available: false, Reason: "contract only"},
	))
	mux.Handle(systemPath, systemHandler)
	return httpserver.Run("runtime-host", cfg.HTTP.Address, mux, logger, "", "", cfg.Telemetry.OTLPEndpoint)
}
