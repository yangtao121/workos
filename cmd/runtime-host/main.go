package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"

	agentv1connect "github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	"github.com/yangtao121/workos/gen/go/workos/common/v1/commonv1connect"
	surfacev1connect "github.com/yangtao121/workos/gen/go/workos/surface/v1/surfacev1connect"
	"github.com/yangtao121/workos/gen/go/workos/workload/v1/workloadv1connect"
	"github.com/yangtao121/workos/internal/platform/config"
	"github.com/yangtao121/workos/internal/platform/database"
	"github.com/yangtao121/workos/internal/platform/httpserver"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/logging"
	"github.com/yangtao121/workos/internal/platform/systemhandler"
	"github.com/yangtao121/workos/internal/platform/telemetry"
	surfacecoreclient "github.com/yangtao121/workos/internal/runtime/surface/adapters/coreclient"
	surfacepostgres "github.com/yangtao121/workos/internal/runtime/surface/adapters/postgres"
	surfaceapp "github.com/yangtao121/workos/internal/runtime/surface/application"
	surfacetransport "github.com/yangtao121/workos/internal/runtime/surface/transport"
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
	if err := cfg.ValidateRuntimeHost(); err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	ready := func(ctx context.Context) error { return pool.Ping(ctx) }
	mux := httpserver.NewMux("runtime-host", ready)

	workloadPath, workloadHandler := workloadv1connect.NewWorkloadServiceHandler(runtimetransport.NewWorkloadHandler())
	mux.Handle(workloadPath, workloadHandler)

	// The Surface Broker owns durable sessions in runtime-owned tables and
	// resolves installed instances through the private Core resolver on every
	// request. Identity arrives only via the gateway-injected trusted headers.
	privateCoreResolver := surfacev1connect.NewSurfaceLaunchResolverServiceClient(telemetry.HTTPClient(), cfg.Services.Core)
	resolverClient, err := surfacecoreclient.New(privateCoreResolver)
	if err != nil {
		return err
	}
	privateCoreAppAgent := agentv1connect.NewAppAgentServiceClient(telemetry.HTTPClient(), cfg.Services.Core)
	appAgentClient, err := surfacecoreclient.NewAppAgent(privateCoreAppAgent)
	if err != nil {
		return err
	}
	generator := ids.UUIDv7{}
	sessionStore := surfacepostgres.New(pool)
	surfaceService, err := surfaceapp.New(sessionStore, resolverClient, generator, cfg.Surface.SessionTTL)
	if err != nil {
		return err
	}
	surfacePath, surfaceHandler := surfacetransport.NewConnectHandler(surfaceService)
	mux.Handle(surfacePath, identity.Middleware(surfaceHandler))
	// The public App Bridge validates the ephemeral bridge token against the
	// stored session facts, gates each method on the effective capability
	// list, and forwards to the private Core App Agent service — which
	// re-validates the active installation and its grant again.
	bridgeService, err := surfaceapp.NewBridgeService(sessionStore, appAgentClient)
	if err != nil {
		return err
	}
	bridgePath, bridgeHandler := surfacetransport.NewBridgeConnectHandler(bridgeService)
	mux.Handle(bridgePath, identity.Middleware(bridgeHandler))
	// The asset route is served ahead of the ServeMux: mux path cleaning
	// would redirect traversal-shaped requests instead of letting the asset
	// policy fail closed on the raw path.
	assetHandler := identity.Middleware(surfacetransport.NewAssetHandler(surfaceService, logger))
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/surfaces/") {
			assetHandler.ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})

	systemPath, systemHandler := commonv1connect.NewSystemServiceHandler(systemhandler.New("runtime-host", commonv1.HealthState_HEALTH_STATE_HEALTHY,
		&commonv1.FeatureCapability{Id: "node-inspection", Available: true},
		&commonv1.FeatureCapability{Id: "container-runner", Available: false, Reason: "not implemented"},
		&commonv1.FeatureCapability{Id: "native-runner", Available: false, Reason: "not implemented"},
		&commonv1.FeatureCapability{Id: "surface-broker", Available: true, Reason: "web bundle surfaces only"},
		&commonv1.FeatureCapability{Id: "app-bridge", Available: true, Reason: "agent.task.run and agent.event.watch only"},
	))
	mux.Handle(systemPath, systemHandler)
	return httpserver.Run("runtime-host", cfg.HTTP.Address, root, logger, "", "", cfg.Telemetry.OTLPEndpoint)
}
