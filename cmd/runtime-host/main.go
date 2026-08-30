package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

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
	workloadpodman "github.com/yangtao121/workos/internal/runtime/workload/adapters/podman"
	workloadpostgres "github.com/yangtao121/workos/internal/runtime/workload/adapters/postgres"
	workloadapp "github.com/yangtao121/workos/internal/runtime/workload/application"
	workloadports "github.com/yangtao121/workos/internal/runtime/workload/ports"
	workloadtransport "github.com/yangtao121/workos/internal/runtime/workload/transport"
)

func main() {
	logger := logging.New("runtime-host")
	if err := run(logger); err != nil {
		logger.Error("service stopped", "error", err)
		os.Exit(1)
	}
}

// workloadCapability projects the verified runner capability honestly: the
// reason carries the fixed probe verdict, never engine internals.
func workloadCapability(capability workloadports.Capability, id string) *commonv1.FeatureCapability {
	if capability.Available {
		return &commonv1.FeatureCapability{Id: id, Available: true}
	}
	reason := capability.Reason
	if reason == "" {
		reason = "verified rootless capability unavailable"
	}
	return &commonv1.FeatureCapability{Id: id, Available: false, Reason: reason}
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

	// The Workload Manager owns supervised containers in runtime-owned
	// tables. The Podman adapter is constructed eagerly but the capability
	// verdict always comes from its bounded probe: a host without verified
	// rootless Podman + cgroup v2 reports the runner unavailable and every
	// container launch refuses — there is no fallback engine (ADR-0006 §4).
	workloadStore, err := workloadpostgres.New(pool)
	if err != nil {
		return err
	}
	var engine workloadports.Engine
	var cgroupReader workloadports.CgroupReader
	podmanEngine, engineErr := workloadpodman.New(cfg.Runtime.PodmanBin)
	if engineErr == nil {
		reader, readerErr := workloadpodman.NewCgroupReader()
		if readerErr == nil {
			engine = podmanEngine
			cgroupReader = reader
		} else {
			// Podman without a readable cgroup v2 hierarchy is an unavailable
			// combined runner capability, not a reason to take down runtime-host's
			// DB-backed Surface and Workload fact services.
			engine = workloadpodman.NewUnavailableEngine("cgroup v2 is not available")
			cgroupReader = workloadpodman.NewUnavailableCgroupReader()
		}
	} else {
		engine = workloadpodman.NewUnavailableEngine("podman executable is not available")
		cgroupReader = workloadpodman.NewUnavailableCgroupReader()
	}
	verifier := &coreInstallationVerifier{resolver: resolverClient}
	references := &surfaceReferenceSource{sessions: sessionStore}
	manager, err := workloadapp.New(workloadStore, engine, cgroupReader,
		workloadpodman.NewProber(), verifier, references, generator, workloadapp.Config{
			ReconcileInterval: cfg.Runtime.ReconcileInterval,
			IdleTTL:           cfg.Runtime.IdleTTL,
			OperationTimeout:  cfg.Runtime.OperationTimeout,
			CoreGrace:         cfg.Runtime.CoreGrace,
			LeaseTTL:          cfg.Runtime.LeaseTTL,
			InstanceName:      cfg.Runtime.InstanceName,
			VerifyDeviceID:    cfg.Runtime.DeviceID,
		}, logger)
	if err != nil {
		return err
	}
	capability, _ := manager.ProbeRunner(ctx)
	if capability.Available {
		logger.Info("rootless container capability verified")
	} else {
		logger.Warn("verified rootless container capability unavailable", "reason", capability.Reason)
	}
	// The reconcile loop converges every crash window between the database
	// and the engine, re-validates installations through Core, and enforces
	// the idle TTL. Deterministic code; no Harness or model involvement.
	reconcileStop := make(chan struct{})
	defer close(reconcileStop)
	go func() {
		ticker := time.NewTicker(cfg.Runtime.ReconcileInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-reconcileStop:
				return
			case <-ticker.C:
				reconcileCtx, cancel := context.WithTimeout(ctx, cfg.Runtime.OperationTimeout)
				if err := manager.Reconcile(reconcileCtx); err != nil {
					logger.Info("workload reconcile pending", "error", err)
				}
				cancel()
			}
		}
	}()

	// The private supervised-workload control service: reached by the
	// trusted private-network reliability host only. It is never registered
	// on the gateway allowlist.
	supervisedPath, supervisedHandler := workloadtransport.NewSupervisedWorkloadHandler(
		workloadtransport.ApplicationManager(manager))
	mux.Handle(supervisedPath, supervisedHandler)

	surfaceService, err := surfaceapp.NewWithWorkloads(sessionStore, resolverClient,
		&surfaceWorkloadLauncher{manager: manager}, generator, cfg.Surface.SessionTTL)
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
		workloadCapability(capability, "container-runner"),
		&commonv1.FeatureCapability{Id: "native-runner", Available: false, Reason: "not implemented"},
		&commonv1.FeatureCapability{Id: "surface-broker", Available: true, Reason: "web bundle and supervised web service surfaces"},
		&commonv1.FeatureCapability{Id: "app-bridge", Available: true, Reason: "agent.task.run and agent.event.watch only"},
	))
	mux.Handle(systemPath, systemHandler)
	return httpserver.Run("runtime-host", cfg.HTTP.Address, root, logger, "", "", cfg.Telemetry.OTLPEndpoint)
}
