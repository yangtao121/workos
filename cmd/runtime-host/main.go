package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/yangtao121/workos/gen/go/workos/project/v1/projectv1connect"
	workspacecoreclient "github.com/yangtao121/workos/internal/runtime/workspacehost/adapters/coreclient"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	agentv1connect "github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	artifactv1connect "github.com/yangtao121/workos/gen/go/workos/artifact/v1/artifactv1connect"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	"github.com/yangtao121/workos/gen/go/workos/common/v1/commonv1connect"
	notificationv1connect "github.com/yangtao121/workos/gen/go/workos/notification/v1/notificationv1connect"
	surfacev1connect "github.com/yangtao121/workos/gen/go/workos/surface/v1/surfacev1connect"
	"github.com/yangtao121/workos/gen/go/workos/workload/v1/workloadv1connect"
	"github.com/yangtao121/workos/internal/platform/config"
	"github.com/yangtao121/workos/internal/platform/containerprocess"
	"github.com/yangtao121/workos/internal/platform/database"
	"github.com/yangtao121/workos/internal/platform/httpserver"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/logging"
	"github.com/yangtao121/workos/internal/platform/systemhandler"
	"github.com/yangtao121/workos/internal/platform/telemetry"
	artifactfiles "github.com/yangtao121/workos/internal/runtime/artifactstore/adapters/files"
	artifactpostgres "github.com/yangtao121/workos/internal/runtime/artifactstore/adapters/postgres"
	artifactapp "github.com/yangtao121/workos/internal/runtime/artifactstore/application"
	artifacttransport "github.com/yangtao121/workos/internal/runtime/artifactstore/transport"
	chromiumengine "github.com/yangtao121/workos/internal/runtime/browserpool/adapters/chromiumengine"
	browserpoolpostgres "github.com/yangtao121/workos/internal/runtime/browserpool/adapters/postgres"
	browserpoolapp "github.com/yangtao121/workos/internal/runtime/browserpool/application"
	browserpooltransport "github.com/yangtao121/workos/internal/runtime/browserpool/transport"
	"github.com/yangtao121/workos/internal/runtime/buildtest/adapters/dockerbuild"
	buildtestpostgres "github.com/yangtao121/workos/internal/runtime/buildtest/adapters/postgres"
	"github.com/yangtao121/workos/internal/runtime/buildtest/adapters/processexec"
	buildtestapp "github.com/yangtao121/workos/internal/runtime/buildtest/application"
	buildtestports "github.com/yangtao121/workos/internal/runtime/buildtest/ports"
	buildtesttransport "github.com/yangtao121/workos/internal/runtime/buildtest/transport"
	nativehostpostgres "github.com/yangtao121/workos/internal/runtime/nativehost/adapters/postgres"
	xvfbengine "github.com/yangtao121/workos/internal/runtime/nativehost/adapters/xvfbengine"
	nativehostapp "github.com/yangtao121/workos/internal/runtime/nativehost/application"
	nativehosttransport "github.com/yangtao121/workos/internal/runtime/nativehost/transport"
	previewdocker "github.com/yangtao121/workos/internal/runtime/previewhost/adapters/dockerpreview"
	previewhostpostgres "github.com/yangtao121/workos/internal/runtime/previewhost/adapters/postgres"
	previewhostapp "github.com/yangtao121/workos/internal/runtime/previewhost/application"
	previewhosttransport "github.com/yangtao121/workos/internal/runtime/previewhost/transport"
	"github.com/yangtao121/workos/internal/runtime/ptyhost/adapters/dockerpty"
	ptyhostpostgres "github.com/yangtao121/workos/internal/runtime/ptyhost/adapters/postgres"
	shellexec "github.com/yangtao121/workos/internal/runtime/ptyhost/adapters/shellexec"
	ptyhostapp "github.com/yangtao121/workos/internal/runtime/ptyhost/application"
	ptyengineports "github.com/yangtao121/workos/internal/runtime/ptyhost/ports"
	ptyhosttransport "github.com/yangtao121/workos/internal/runtime/ptyhost/transport"
	surfacecoreclient "github.com/yangtao121/workos/internal/runtime/surface/adapters/coreclient"
	indexerclient "github.com/yangtao121/workos/internal/runtime/surface/adapters/indexerclient"
	surfacepostgres "github.com/yangtao121/workos/internal/runtime/surface/adapters/postgres"
	workspaceadapter "github.com/yangtao121/workos/internal/runtime/surface/adapters/workspace"
	surfaceapp "github.com/yangtao121/workos/internal/runtime/surface/application"
	surfacetransport "github.com/yangtao121/workos/internal/runtime/surface/transport"
	runtimetransport "github.com/yangtao121/workos/internal/runtime/transport"
	"github.com/yangtao121/workos/internal/runtime/workload/adapters/dockerapp"
	fakefixture "github.com/yangtao121/workos/internal/runtime/workload/adapters/fakefixture"
	workloadpodman "github.com/yangtao121/workos/internal/runtime/workload/adapters/podman"
	workloadpostgres "github.com/yangtao121/workos/internal/runtime/workload/adapters/postgres"
	workloadapp "github.com/yangtao121/workos/internal/runtime/workload/application"
	workloadports "github.com/yangtao121/workos/internal/runtime/workload/ports"
	workloadtransport "github.com/yangtao121/workos/internal/runtime/workload/transport"
	workspacehostdocker "github.com/yangtao121/workos/internal/runtime/workspacehost/adapters/dockerexec"
	workspacehostfiles "github.com/yangtao121/workos/internal/runtime/workspacehost/adapters/localfs"
	workspacehostpostgres "github.com/yangtao121/workos/internal/runtime/workspacehost/adapters/postgres"
	workspacehostapp "github.com/yangtao121/workos/internal/runtime/workspacehost/application"
	workspacehostports "github.com/yangtao121/workos/internal/runtime/workspacehost/ports"
	workspacehosttransport "github.com/yangtao121/workos/internal/runtime/workspacehost/transport"
)

func main() {
	logger := logging.New("runtime-host")
	if err := run(logger); err != nil {
		logger.Error("service stopped", "error", err)
		os.Exit(1)
	}
}

// rootlessRunnerReason keeps the rootless verdict honest even when the
// fixture engine is operational: simulated lifecycle is not a container
// runtime (ADR-0016 §2).
func rootlessRunnerReason(engine string, capability workloadports.Capability) string {
	if engine == "fake-fixture" {
		return "the fixture engine simulates the container lifecycle; rootless isolation is unproven on this host"
	}
	if capability.Reason == "" {
		return "verified rootless capability unavailable"
	}
	return capability.Reason
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

// artifactFactsFor avoids a typed-nil interface: a nil service must leave
// the handler's bundle queries honestly Unimplemented.
func artifactFactsFor(service *artifactapp.Service) buildtesttransport.ArtifactFacts {
	if service == nil {
		return nil
	}
	return service
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
	// The private app notification ingest surface (ADR-0014): the runtime
	// forwards only bounded app text plus session-derived scope facts; Core
	// re-verifies installation, grant epoch, idempotency, and quota.
	privateNotificationIngest := notificationv1connect.NewAppNotificationIngestServiceClient(telemetry.HTTPClient(), cfg.Services.Core)
	appAgentClient, err := surfacecoreclient.NewAppAgent(privateCoreAppAgent, privateNotificationIngest)
	if err != nil {
		return err
	}
	generator := ids.UUIDv7{}

	// The runtime-owned release bundle repository (ADR-0033): 0700 private
	// root, content-addressed bytes, PostgreSQL metadata. Empty root keeps
	// bundle features honestly unavailable. Startup reconciliation runs
	// before the admin socket can serve any import.
	var artifactService *artifactapp.Service
	if strings.TrimSpace(cfg.Runtime.ArtifactRoot) != "" {
		bundleFiles, filesErr := artifactfiles.New(cfg.Runtime.ArtifactRoot)
		if filesErr != nil {
			return filesErr
		}
		artifactService = artifactapp.New(artifactpostgres.New(pool), bundleFiles).WithBuildAuthority(buildArtifactAuthority(buildtestpostgres.New(pool)))
		if err := artifactService.Reconcile(ctx); err != nil {
			return fmt.Errorf("reconcile artifact store: %w", err)
		}
		if strings.TrimSpace(cfg.Runtime.ArtifactAdminSocket) != "" {
			adminPath, adminHandler := artifacttransport.NewArtifactAdminHandler(artifactService)
			listener, adminServer, listenErr := artifacttransport.ListenAdminSocket(cfg.Runtime.ArtifactAdminSocket, adminHandler, logger)
			if listenErr != nil {
				return listenErr
			}
			defer func() {
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = adminServer.Shutdown(shutdownCtx)
				cancel()
				_ = listener.Close()
			}()
			go func() {
				if serveErr := adminServer.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
					logger.Error("runtime admin socket failed", "error", serveErr)
				}
			}()
			logger.Info("runtime artifact admin socket serving", "path", adminPath)
		}
	}

	// The private Build/Test executor (ADR-0026 + ADR-0033): durable jobs and
	// the configured engine tier — "process" keeps the kernel-rlimit sandbox
	// and its old gate semantics; "docker" runs each stage in a container
	// from a digest-pinned toolchain image and freezes verified build output
	// into the release bundle repository. Empty scratch config disables the
	// service honestly.
	if strings.TrimSpace(cfg.Runtime.BuildTestScratch) != "" {
		var buildEngine buildtestports.BuildEngine
		switch cfg.Runtime.BuildTestEngine {
		case "docker":
			dockerEngine, engineErr := dockerbuild.New(dockerbuild.Config{Socket: cfg.Runtime.DockerSocket})
			if engineErr != nil {
				return engineErr
			}
			buildEngine = dockerEngine
			logger.Info("build test engine selected", "engine", "docker", "socket", cfg.Runtime.DockerSocket)
		case "process":
			processEngine, engineErr := processexec.New(processexec.Config{ScratchRoot: cfg.Runtime.BuildTestScratch, Processes: cfg.Runtime.BuildTestProcessLimit})
			if engineErr != nil {
				return engineErr
			}
			buildEngine = processEngine
		default:
			return fmt.Errorf("WORKOS_RUNTIME_BUILDTEST_ENGINE must be process or docker, got %q", cfg.Runtime.BuildTestEngine)
		}
		buildStore := buildtestpostgres.New(pool)
		buildService, serviceErr := buildtestapp.NewService(buildStore, buildEngine, artifactService, generator, cfg.Runtime.InstanceName, cfg.Runtime.BuildTestScratch, cfg.Runtime.BuildTestTimeout, cfg.Runtime.BuildTestLeaseTTL)
		if serviceErr != nil {
			return serviceErr
		}
		buildPath, buildHandler := buildtesttransport.NewBuildTestHandler(buildService, artifactFactsFor(artifactService))
		mux.Handle(buildPath, buildHandler)
		buildStop := make(chan struct{})
		defer close(buildStop)
		go func() {
			ticker := time.NewTicker(cfg.Runtime.BuildTestInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-buildStop:
					return
				case <-ticker.C:
					// The service gives each claimed job its own timeout. A
					// batch-wide deadline spends later jobs' budget on earlier
					// builds and then prevents their lease/verdict writes.
					if _, err := buildService.RunPass(ctx, time.Now().UTC()); err != nil {
						logger.Info("build test pass pending", "error", err)
					}
				}
			}
		}()
	}
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
	switch cfg.Runtime.WorkloadEngine {
	case "fake-fixture":
		// ADR-0016 §2: the bounded in-process simulator proves the
		// supervision software chain on hosts without rootless Podman. It
		// never claims the container capability and is never a production
		// fallback. It is not a P3 formal runner.
		fixtureEngine, fixtureReader := fakefixture.New(cfg.Runtime.FixtureScenarioFile)
		engine = fixtureEngine
		cgroupReader = fixtureReader
	case "docker":
		if artifactService == nil {
			return errors.New("WORKOS_RUNTIME_WORKLOAD_ENGINE=docker requires WORKOS_RUNTIME_ARTIFACT_ROOT")
		}
		socket := cfg.Runtime.DockerSocket
		if socket == "" {
			socket = "/var/run/docker.sock"
		}
		dockerEngine, engineErr := dockerapp.New(dockerapp.Config{
			Socket: socket, UnpackRoot: filepath.Join(cfg.Runtime.ArtifactRoot, "unpack"),
		}, artifactService)
		if engineErr != nil {
			return engineErr
		}
		engine = dockerEngine
		reader, readerErr := workloadpodman.NewCgroupReader()
		if readerErr != nil {
			return fmt.Errorf("docker workload cgroup reader: %w", readerErr)
		}
		cgroupReader = reader
		logger.Info("workload engine selected", "engine", "docker", "socket", socket)
	default:
		podmanEngine, engineErr := workloadpodman.New(cfg.Runtime.PodmanBin)
		if engineErr == nil {
			reader, readerErr := workloadpodman.NewCgroupReader()
			if readerErr == nil {
				engine = podmanEngine
				cgroupReader = reader
			} else {
				engine = workloadpodman.NewUnavailableEngine("cgroup v2 is not available")
				cgroupReader = workloadpodman.NewUnavailableCgroupReader()
			}
		} else {
			engine = workloadpodman.NewUnavailableEngine("podman executable is not available")
			cgroupReader = workloadpodman.NewUnavailableCgroupReader()
		}
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
		logger.Info("container capability verified", "rootless", capability.Rootless, "cgroup_v2", capability.CgroupV2)
	} else {
		logger.Warn("verified container capability unavailable", "reason", capability.Reason)
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
	// Knowledge search pipeline (ADR-0013): configured only when the runtime
	// holds an indexer upstream. Without it knowledge.search is never
	// negotiated into new sessions and the runtime serves every other app
	// normally.
	var knowledgePipeline *surfaceapp.KnowledgeSearchPipeline
	if strings.TrimSpace(cfg.Runtime.IndexerURL) != "" {
		knowledgeIndexer, err := indexerclient.NewKnowledgeSearch(cfg.Runtime.IndexerURL, cfg.Runtime.DeviceID)
		if err != nil {
			return err
		}
		knowledgePipeline, err = surfaceapp.NewKnowledgeSearchPipeline(appAgentClient, knowledgeIndexer)
		if err != nil {
			return err
		}
		surfaceService = surfaceService.WithKnowledgeConfigured()
	}
	bridgeService, err := surfaceapp.NewBridgeService(sessionStore, appAgentClient, knowledgePipeline, resolverClient)
	if err != nil {
		return err
	}

	bridgeService.WithArtifacts(surfacecoreclient.AppArtifacts{Client: artifactv1connect.NewAppArtifactServiceClient(telemetry.HTTPClient(), cfg.Services.Core)})
	surfaceService.WithArtifactsConfigured()
	mounts := make([]workspaceadapter.Mount, 0, len(cfg.Runtime.WorkspaceMounts))
	for _, mount := range cfg.Runtime.WorkspaceMounts {
		mounts = append(mounts, workspaceadapter.Mount{OwnerUserID: mount.OwnerUserID, ProjectID: mount.ProjectID, Path: mount.RootPath, ReadOnly: mount.ReadOnly})
	}
	workspace, workspaceErr := workspaceadapter.New(mounts)
	if workspaceErr != nil {
		logger.Warn("project workspace bindings unavailable")
	} else {
		defer workspace.Close()
	}
	// The workspace host service (ADR-0030): operator-registered sources and
	// prepared execution environments for terminals, native runners, and the
	// harness. Private to the runtime listener; never on the gateway
	// allowlist.
	workspaceAuthorizer := workspacecoreclient.Authorization{Client: projectv1connect.NewWorkspaceExecutionAuthorizationServiceClient(telemetry.HTTPClient(), cfg.Services.Core)}
	workspaceOperations := &workspaceOperationRouter{authorization: workspaceAuthorizer}
	var workspaceHost *workspacehostapp.Service
	if workspaceHostService, workspaceHostErr := workspacehostapp.New(time.Now().UTC(), cfg.Runtime.WorkspaceMounts); workspaceHostErr != nil {
		logger.Warn("workspace host service unavailable", "error", workspaceHostErr)
	} else {
		workspaceHost = workspaceHostService
		var commandEngine workspacehostports.Executor
		if socket, image := os.Getenv("WORKOS_WORKSPACE_DOCKER_SOCKET"), os.Getenv("WORKOS_WORKSPACE_EXECUTION_IMAGE"); socket != "" && image != "" {
			if err := containerprocess.New(socket, image).Reconcile(ctx); err != nil {
				return fmt.Errorf("workspace container recovery unavailable: %w", err)
			}
			commandEngine = workspacehostdocker.New(socket, image)
		}
		executionService := workspacehostapp.NewExecution(workspaceHost, &workspacehostfiles.Files{}, commandEngine, workspacehostpostgres.New(pool)).WithAuthorization(workspaceAuthorizer)
		workspaceOperations.files = executionService
		workspaceOperations.host = workspaceHost
		executionPath, executionHandler := workspacehosttransport.NewExecutionHandler(workspaceOperations)
		mux.Handle(executionPath, executionHandler)

		workspaceHostPath, workspaceHostHandler := workspacehosttransport.NewWorkspaceHostHandler(workspaceHost, time.Now)
		mux.Handle(workspaceHostPath, identity.Middleware(workspaceHostHandler))
	}

	if workspaceErr == nil && workspaceHost != nil {
		guarded := authorizedAppWorkspace{files: workspace, authorization: previewWorkspaceAuthorization{workspaceHost, workspaceAuthorizer}}
		surfaceService.WithWorkspace(guarded)
		bridgeService.WithWorkspace(guarded)
	}

	// Development servers live in isolated containers and expose only a
	// capability-scoped HTTP bridge; they receive no network or Runtime socket.
	previewService, err := previewhostapp.New(previewhostpostgres.New(pool),
		previewWorkspaceAuthorization{workspaceHost, workspaceAuthorizer},
		previewdocker.New(os.Getenv("WORKOS_WORKSPACE_DOCKER_SOCKET"), os.Getenv("WORKOS_WORKSPACE_EXECUTION_IMAGE"), os.Getenv("WORKOS_PREVIEW_BRIDGE_ROOT")), generator)
	if err != nil {
		return err
	}
	workspaceOperations.previews = previewService
	previewPath, previewHandler := previewhosttransport.NewPreviewHandler(previewService)
	mux.Handle(previewPath, identity.Middleware(previewHandler))
	defer previewService.Close()
	if err := previewService.Sweep(ctx); err != nil {
		return err
	}
	previewServing := previewhosttransport.NewServingHandler(previewService, logger)
	previewStop := make(chan struct{})
	defer close(previewStop)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-previewStop:
				return
			case <-ticker.C:
				if err := previewService.Sweep(ctx); err != nil {
					logger.Info("workspace preview sweep pending", "error", err)
				}
			}
		}
	}()
	bridgePath, bridgeHandler := surfacetransport.NewBridgeConnectHandler(bridgeService)
	mux.Handle(bridgePath, identity.Middleware(bridgeHandler))
	// The asset routes are served ahead of the ServeMux: mux path cleaning
	// would redirect traversal-shaped requests instead of letting the asset
	// policy fail closed on the raw path.
	assetHandler := identity.Middleware(surfacetransport.NewAssetHandler(surfaceService, logger))
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/surfaces/") {
			assetHandler.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/previews/") {
			previewServing.ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})

	// The Remote Browser Pool (ADR-0027): real Chromium workers behind the
	// owner-identity gate. Without a configured binary the capability stays
	// honestly unavailable and the handler reports it.
	if strings.TrimSpace(cfg.Runtime.BrowserBinary) != "" {
		browserEngine, engineErr := chromiumengine.New(cfg.Runtime.BrowserBinary, cfg.Runtime.BrowserScratch)
		if engineErr != nil {
			return engineErr
		}
		browserService, serviceErr := browserpoolapp.NewService(browserpoolpostgres.New(pool), browserEngine, browserEngine, generator, cfg.Runtime.InstanceName, logger)
		if serviceErr != nil {
			return serviceErr
		}
		browserPath, browserHandler := browserpooltransport.NewBrowserPoolHandler(browserService)
		mux.Handle(browserPath, identity.Middleware(browserHandler))
		browserStop := make(chan struct{})
		defer close(browserStop)
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-browserStop:
					return
				case <-ticker.C:
					if err := browserService.Sweep(ctx); err != nil {
						logger.Info("browser pool sweep pending", "error", err)
					}
				}
			}
		}()
	}

	// Supervised terminal sessions (ADR-0028): real login shells behind the
	// owner-identity gate; without a configured shell the capability stays
	// honestly unavailable.
	var ptyService *ptyhostapp.Service
	if strings.TrimSpace(cfg.Runtime.PtyShell) != "" {
		localEngine, engineErr := shellexec.New(cfg.Runtime.PtyShell)
		var ptyEngine ptyengineports.Engine = localEngine
		if socket, image := os.Getenv("WORKOS_WORKSPACE_DOCKER_SOCKET"), os.Getenv("WORKOS_WORKSPACE_EXECUTION_IMAGE"); socket != "" {
			if image == "" {
				return errors.New("workspace image required with Docker socket")
			}
			ptyEngine = dockerpty.New(socket, image)
		}
		if engineErr != nil {
			return engineErr
		}
		ptyService, engineErr = ptyhostapp.NewService(ptyhostpostgres.New(pool), ptyEngine, generator, logger)
		if engineErr != nil {
			return engineErr
		}
		ptyService.WithWorkspaceAuthorization(ptyWorkspaceAuthorization{workspaceHost, workspaceAuthorizer})
		// Startup reconcile (A13): finalize durable rows whose child died
		// with a previous runtime host (setsid+Pdeathsig) before serving,
		// so the continuity discovery view never reports a dead program as
		// running. Mirrors the native runner's startup sweep.
		if err := ptyService.Reconcile(ctx); err != nil {
			return err
		}
		ptyPath, ptyHandler := ptyhosttransport.NewPtyHandler(ptyService)
		mux.Handle(ptyPath, identity.Middleware(ptyHandler))
		ptyStop := make(chan struct{})
		defer close(ptyStop)
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ptyStop:
					return
				case <-ticker.C:
					if err := ptyService.Sweep(ctx); err != nil {
						logger.Info("pty sweep pending", "error", err)
					}
				}
			}
		}()
	}

	// The virtual-display native runner (ADR-0029): real Xvfb displays with
	// WebRTC video and data-channel input behind the owner-identity gate.
	// Without the X11 toolchain the capability stays honestly unavailable.
	nativeConfigured := strings.TrimSpace(cfg.Runtime.NativeDisplay) != ""
	var nativeService *nativehostapp.Service
	if nativeConfigured {
		nativeEngine, engineErr := xvfbengine.New(cfg.Runtime.NativeDisplay, cfg.Runtime.NativeClient, cfg.Runtime.NativeFFmpeg, cfg.Runtime.NativeXdotool, cfg.Runtime.NativeScratch, cfg.Runtime.NativeCandidates)
		if engineErr != nil {
			return engineErr
		}
		if err := nativeEngine.WithLAN(os.Getenv("WORKOS_NATIVE_LAN_CIDRS"), os.Getenv("WORKOS_NATIVE_UDP_PORTS")); err != nil {
			return err
		}
		if socket := os.Getenv("WORKOS_WORKSPACE_DOCKER_SOCKET"); socket != "" {
			nativeEngine.WithContainers(socket, os.Getenv("WORKOS_WORKSPACE_EXECUTION_IMAGE"), os.Getenv("WORKOS_NATIVE_X11_HOST_DIRECTORY"))
		}
		var serviceErr error
		nativeService, serviceErr = nativehostapp.NewService(nativehostpostgres.New(pool), nativeEngine, generator, logger)
		if serviceErr != nil {
			return serviceErr
		}
		nativeService.WithWorkspaceAuthorization(nativeWorkspaceAuthorization{workspaceHost, workspaceAuthorizer})
		if err := nativeService.Sweep(ctx); err != nil {
			return err
		}
		defer nativeService.Shutdown()
		nativePath, nativeHandler := nativehosttransport.NewNativeHandler(nativeService)
		mux.Handle(nativePath, identity.Middleware(nativeHandler))
		nativeStop := make(chan struct{})
		defer close(nativeStop)
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-nativeStop:
					return
				case <-ticker.C:
					if err := nativeService.Sweep(ctx); err != nil {
						logger.Info("native runner sweep pending", "error", err)
					}
				}
			}
		}()
	}

	// The surface continuity service (ADR-0031): program execution and device
	// access are separate lifecycles. The interactive workload runtime
	// adapts the PTY and native runner services to the continuity ports; the
	// single-controller lease gates the input paths of both runners at the
	// application layer, and the bounded attachment sweep rides the same
	// 30-second maintenance cadence as the session sweeps.
	continuityRuntime := &surfaceInteractiveRuntime{pty: ptyService, native: nativeService}
	continuityService, err := surfaceapp.NewContinuityService(surfacepostgres.NewContinuity(pool), continuityRuntime, generator, 30*time.Minute)
	if err != nil {
		return err
	}
	if ptyService != nil {
		ptyService.WithControlAuthorization(continuityAuthorization{service: continuityService})
	}
	if nativeService != nil {
		nativeService.WithControlAuthorization(continuityAuthorization{service: continuityService})
	}
	continuityPath, continuityHandler := surfacetransport.NewContinuityHandler(continuityService)
	mux.Handle(continuityPath, identity.Middleware(continuityHandler))
	continuityStop := make(chan struct{})
	defer close(continuityStop)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-continuityStop:
				return
			case <-ticker.C:
				if err := continuityService.Sweep(ctx); err != nil {
					logger.Info("surface continuity sweep pending", "error", err)
				}
			}
		}
	}()

	systemPath, systemHandler := commonv1connect.NewSystemServiceHandler(systemhandler.New("runtime-host", commonv1.HealthState_HEALTH_STATE_HEALTHY,
		&commonv1.FeatureCapability{Id: "node-inspection", Available: true},
		workloadCapability(capability, "container-runner"),
		&commonv1.FeatureCapability{Id: "rootless-container-runner", Available: cfg.Runtime.WorkloadEngine != "fake-fixture" && capability.Available && capability.Rootless, Reason: rootlessRunnerReason(cfg.Runtime.WorkloadEngine, capability)},
		&commonv1.FeatureCapability{Id: "native-runner", Available: strings.TrimSpace(cfg.Runtime.PtyShell) != "", Reason: "supervised PTY sessions require a configured login shell (ADR-0028)"},
		&commonv1.FeatureCapability{Id: "virtual-display-native-runner", Available: nativeConfigured, Reason: "virtual-display WebRTC sessions require the Xvfb/ffmpeg/xdotool toolchain (ADR-0029); loopback host candidates only"},
		&commonv1.FeatureCapability{Id: "surface-broker", Available: true, Reason: "web bundle and supervised web service surfaces"},
		&commonv1.FeatureCapability{Id: "app-bridge", Available: true, Reason: "grant-checked agent, knowledge, notifications, project and own-window methods; files require explicit workspace bindings"},
		&commonv1.FeatureCapability{Id: "workspace-files", Available: workspaceErr == nil && len(mounts) > 0, Reason: "requires usable owner-bound workspace directories and explicit files.read/files.write grants"},
		&commonv1.FeatureCapability{Id: "app-knowledge-search", Available: knowledgePipeline != nil, Reason: "scoped read-only project knowledge search over the indexer"},
	))
	mux.Handle(systemPath, systemHandler)
	return httpserver.Run("runtime-host", cfg.HTTP.Address, root, logger, "", "", cfg.Telemetry.OTLPEndpoint)
}
