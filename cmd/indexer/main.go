package main

import (
	"log/slog"
	"os"

	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	"github.com/yangtao121/workos/gen/go/workos/common/v1/commonv1connect"
	"github.com/yangtao121/workos/gen/go/workos/index/v1/indexv1connect"
	"github.com/yangtao121/workos/internal/platform/config"
	"github.com/yangtao121/workos/internal/platform/httpserver"
	"github.com/yangtao121/workos/internal/platform/logging"
	"github.com/yangtao121/workos/internal/platform/systemhandler"
)

func main() {
	logger := logging.New("indexer")
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
	mux := httpserver.NewMux("indexer", nil)
	indexPath, indexHandler := indexv1connect.NewIndexServiceHandler(indexv1connect.UnimplementedIndexServiceHandler{})
	mux.Handle(indexPath, indexHandler)
	systemPath, systemHandler := commonv1connect.NewSystemServiceHandler(systemhandler.New("indexer", commonv1.HealthState_HEALTH_STATE_DEGRADED,
		&commonv1.FeatureCapability{Id: "archive", Available: false, Reason: "contract only"},
		&commonv1.FeatureCapability{Id: "rag", Available: false, Reason: "contract only"},
	))
	mux.Handle(systemPath, systemHandler)
	return httpserver.Run("indexer", cfg.HTTP.Address, mux, logger, "", "", cfg.Telemetry.OTLPEndpoint)
}
