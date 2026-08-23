package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	"github.com/yangtao121/workos/gen/go/workos/artifact/v1/artifactv1connect"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	"github.com/yangtao121/workos/gen/go/workos/common/v1/commonv1connect"
	"github.com/yangtao121/workos/gen/go/workos/harness/v1/harnessv1connect"
	projectconnect "github.com/yangtao121/workos/gen/go/workos/project/v1/projectv1connect"
	"github.com/yangtao121/workos/gen/go/workos/taskexecution/v1/taskexecutionv1connect"
	agentpostgres "github.com/yangtao121/workos/internal/core/agent/adapters/postgres"
	agentapp "github.com/yangtao121/workos/internal/core/agent/application"
	agenttransport "github.com/yangtao121/workos/internal/core/agent/transport"
	manifestvalidator "github.com/yangtao121/workos/internal/core/appregistry/adapters/manifestvalidator"
	appregistrypostgres "github.com/yangtao121/workos/internal/core/appregistry/adapters/postgres"
	appregistryapp "github.com/yangtao121/workos/internal/core/appregistry/application"
	appregistrytransport "github.com/yangtao121/workos/internal/core/appregistry/transport"
	cataloghost "github.com/yangtao121/workos/internal/core/harnesscatalog/adapters/harnesshost"
	catalogapp "github.com/yangtao121/workos/internal/core/harnesscatalog/application"
	catalogtransport "github.com/yangtao121/workos/internal/core/harnesscatalog/transport"
	"github.com/yangtao121/workos/internal/core/orchestration"
	orchestrationtransport "github.com/yangtao121/workos/internal/core/orchestration/transport"
	projectpostgres "github.com/yangtao121/workos/internal/core/project/adapters/postgres"
	projectapp "github.com/yangtao121/workos/internal/core/project/application"
	projecttransport "github.com/yangtao121/workos/internal/core/project/transport"
	"github.com/yangtao121/workos/internal/platform/config"
	"github.com/yangtao121/workos/internal/platform/database"
	"github.com/yangtao121/workos/internal/platform/httpserver"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/logging"
	"github.com/yangtao121/workos/internal/platform/systemhandler"
	"github.com/yangtao121/workos/internal/platform/telemetry"
)

func main() {
	logger := logging.New("workos-core")
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
	if err := cfg.ValidateCore(); err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	ready := func(ctx context.Context) error { return pool.Ping(ctx) }
	mux := httpserver.NewMux("workos-core", ready)

	generator := ids.UUIDv7{}
	projectService := projectapp.New(projectpostgres.New(pool), generator)
	projectPath, projectHandler := projectconnect.NewProjectServiceHandler(projecttransport.New(projectService))
	mux.Handle(projectPath, identity.Middleware(projectHandler))

	privateHarnessClient := harnessv1connect.NewHarnessHostServiceClient(telemetry.HTTPClient(), cfg.Services.Harness)
	catalogSource, err := cataloghost.New(privateHarnessClient, cfg.Agent.CatalogTimeout)
	if err != nil {
		return err
	}
	catalogService, err := catalogapp.New(catalogSource, cfg.Agent.DefaultProvider)
	if err != nil {
		return err
	}
	catalogPath, catalogHandler := harnessv1connect.NewHarnessCatalogServiceHandler(catalogtransport.New(catalogService))
	mux.Handle(catalogPath, identity.Middleware(catalogHandler))

	binder, err := orchestration.NewProjectHarnessBinder(projectService, catalogService, orchestration.BindingPreset{
		InstancePolicy: cfg.Agent.ProjectBinding.InstancePolicy, ProfileID: cfg.Agent.ProjectBinding.ProfileID,
		ResourcePolicyID: cfg.Agent.ProjectBinding.ResourcePolicyID,
	})
	if err != nil {
		return err
	}
	bindingPath, bindingHandler := projectconnect.NewProjectHarnessBindingServiceHandler(orchestrationtransport.New(binder))
	mux.Handle(bindingPath, identity.Middleware(bindingHandler))

	agentService := agentapp.New(agentpostgres.New(pool), generator)
	taskRouter, err := orchestration.NewTaskRouter(agentService, projectService, cfg.Agent.DefaultProvider)
	if err != nil {
		return err
	}
	agentPath, agentHandler := agentv1connect.NewAgentTaskServiceHandler(agenttransport.New(agentService, taskRouter))
	mux.Handle(agentPath, identity.Middleware(agentHandler))
	executionPath, executionHandler := taskexecutionv1connect.NewTaskExecutionServiceHandler(agenttransport.NewExecution(agentService))
	mux.Handle(executionPath, executionHandler)

	manifestValidator, err := manifestvalidator.New()
	if err != nil {
		return err
	}
	projectDirectory, err := orchestration.NewProjectDirectory(projectService)
	if err != nil {
		return err
	}
	appService, err := appregistryapp.New(appregistrypostgres.New(pool), manifestValidator, projectDirectory, generator)
	if err != nil {
		return err
	}
	appPath, appHandler := appregistrytransport.NewConnectHandler(appService)
	mux.Handle(appPath, identity.Middleware(appHandler))
	artifactPath, artifactHandler := artifactv1connect.NewArtifactServiceHandler(artifactv1connect.UnimplementedArtifactServiceHandler{})
	mux.Handle(artifactPath, artifactHandler)
	systemPath, systemHandler := commonv1connect.NewSystemServiceHandler(systemhandler.New("workos-core", commonv1.HealthState_HEALTH_STATE_HEALTHY,
		&commonv1.FeatureCapability{Id: "project", Available: true},
		&commonv1.FeatureCapability{Id: "agent-task", Available: true},
		&commonv1.FeatureCapability{Id: "harness-catalog", Available: true},
		&commonv1.FeatureCapability{Id: "app-registry", Available: true},
		&commonv1.FeatureCapability{Id: "artifact", Available: false, Reason: "contract only"},
	))
	mux.Handle(systemPath, systemHandler)
	return httpserver.Run("workos-core", cfg.HTTP.Address, mux, logger, "", "", cfg.Telemetry.OTLPEndpoint)
}
