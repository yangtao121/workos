package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	"github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	artifactv1 "github.com/yangtao121/workos/gen/go/workos/artifact/v1"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	"github.com/yangtao121/workos/gen/go/workos/common/v1/commonv1connect"
	"github.com/yangtao121/workos/gen/go/workos/harness/v1/harnessv1connect"
	projectconnect "github.com/yangtao121/workos/gen/go/workos/project/v1/projectv1connect"
	agentpostgres "github.com/yangtao121/workos/internal/core/agent/adapters/postgres"
	agentapp "github.com/yangtao121/workos/internal/core/agent/application"
	agenttransport "github.com/yangtao121/workos/internal/core/agent/transport"
	manifestvalidator "github.com/yangtao121/workos/internal/core/appregistry/adapters/manifestvalidator"
	appregistrypostgres "github.com/yangtao121/workos/internal/core/appregistry/adapters/postgres"
	appregistryapp "github.com/yangtao121/workos/internal/core/appregistry/application"
	appregistrytransport "github.com/yangtao121/workos/internal/core/appregistry/transport"
	artifactpostgres "github.com/yangtao121/workos/internal/core/artifact/adapters/postgres"
	artifactapp "github.com/yangtao121/workos/internal/core/artifact/application"
	artifacttransport "github.com/yangtao121/workos/internal/core/artifact/transport"
	credentialcipher "github.com/yangtao121/workos/internal/core/credential/adapters/cipher"
	credentialpostgres "github.com/yangtao121/workos/internal/core/credential/adapters/postgres"
	credentialapp "github.com/yangtao121/workos/internal/core/credential/application"
	credentialports "github.com/yangtao121/workos/internal/core/credential/ports"
	credentialtransport "github.com/yangtao121/workos/internal/core/credential/transport"
	cataloghost "github.com/yangtao121/workos/internal/core/harnesscatalog/adapters/harnesshost"
	catalogapp "github.com/yangtao121/workos/internal/core/harnesscatalog/application"
	catalogtransport "github.com/yangtao121/workos/internal/core/harnesscatalog/transport"
	indexfeedpostgres "github.com/yangtao121/workos/internal/core/indexfeed/adapters/postgres"
	indexfeedapp "github.com/yangtao121/workos/internal/core/indexfeed/application"
	indexfeedtransport "github.com/yangtao121/workos/internal/core/indexfeed/transport"
	notificationpostgres "github.com/yangtao121/workos/internal/core/notification/adapters/postgres"
	notificationapp "github.com/yangtao121/workos/internal/core/notification/application"
	notificationtransport "github.com/yangtao121/workos/internal/core/notification/transport"
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
	"github.com/yangtao121/workos/internal/platform/privatetls"
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
	// Index publication feed (ADR-0013): the Core-side durable authority the
	// indexer consumes. The tx-scoped sink joins the artifact materialization
	// and project archive transactions, so a publication commits exactly when
	// its source fact commits.
	feedRepository := indexfeedpostgres.New(pool)
	// The durable notification authority (ADR-0014): owner-scoped facts, the
	// monotonic change stream, and read state. Constructed before every
	// producer so the tx-scoped sink is a hard requirement of their source
	// transactions.
	notificationRepository := notificationpostgres.New(pool)
	projectRepository, err := projectpostgres.NewWithFeed(pool, feedRepository)
	if err != nil {
		return err
	}
	projectService := projectapp.New(projectRepository, generator)
	projectPath, projectHandler := projecttransport.NewProjectConnectHandler(projectService)
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

	// Credential Vault (ADR-0009): the only durable authority for long-lived
	// provider credential material. When the master key + admin socket are
	// not configured the vault stays unavailable — every credential-bearing
	// provider fails closed while Project, Agent, Artifact, and all other
	// non-credential functions start normally.
	var credentialService *credentialapp.Service
	if cfg.Credential.MasterKeyFile != "" {
		ciph, err := credentialcipher.Load(cfg.Credential.MasterKeyFile)
		if err != nil {
			return err
		}
		credentialService, err = credentialapp.New(credentialpostgres.New(pool), ciph)
		if err != nil {
			return err
		}
	}

	// The public catalog is owner-aware: providers that require a task
	// credential lease are projected unavailable to owners without one.
	if _, err := catalogService.WithCredentialAvailability(orchestration.NewCredentialAvailability(credentialService)); err != nil {
		return err
	}
	catalogPath, catalogHandler := harnessv1connect.NewHarnessCatalogServiceHandler(catalogtransport.New(catalogService))
	mux.Handle(catalogPath, identity.Middleware(catalogHandler))

	binder, err := orchestration.NewProjectHarnessBinder(projectService, catalogService, orchestration.NewCredentialSnapshots(credentialService), orchestration.BindingPreset{
		InstancePolicy: cfg.Agent.ProjectBinding.InstancePolicy, ProfileID: cfg.Agent.ProjectBinding.ProfileID,
		ResourcePolicyID: cfg.Agent.ProjectBinding.ResourcePolicyID,
	})
	if err != nil {
		return err
	}
	bindingPath, bindingHandler := projectconnect.NewProjectHarnessBindingServiceHandler(orchestrationtransport.New(binder))
	mux.Handle(bindingPath, identity.Middleware(bindingHandler))

	agentRepository, err := agentpostgres.NewWithNotificationSink(pool, notificationRepository)
	if err != nil {
		return err
	}
	agentService := agentapp.New(agentRepository, generator)
	artifactRepository := artifactpostgres.New(pool)
	artifactService, err := artifactapp.New(artifactRepository, generator)
	if err != nil {
		return err
	}
	// Project-scoped review reads verify the project through the neutral
	// scope port; the Artifact module never imports Project adapters or SQL.
	artifactProjectScope, err := orchestration.NewArtifactProjectScope(projectService)
	if err != nil {
		return err
	}
	if _, err := artifactService.WithProjectScope(artifactProjectScope); err != nil {
		return err
	}
	// The lease-bound materialization coordinator composes the Agent and
	// Artifact modules through their transaction-scoped ports: one shared
	// transaction adjudicates the provider output, persists the immutable
	// artifact, and publishes exactly one Core-minted timeline event. Only
	// harness-host reaches this private RPC; it never enters the gateway
	// allowlist.
	artifactMaterializer, err := orchestration.NewTaskArtifactMaterializer(
		pool, agentRepository, artifactRepository, artifactService, feedRepository, notificationRepository, generator,
	)
	if err != nil {
		return err
	}

	// The credential lease issuer composes the Agent task-lease authority
	// with the vault inside one transaction: owner, provider, task, and the
	// exact credential snapshot are always derived from the active task
	// lease, never from the caller (ADR-0009). A nil vault still constructs
	// the listener — acquire fails closed until the vault is configured.
	credentialIssuer, err := orchestration.NewCredentialLeaseIssuer(
		pool, agentRepository, credentialpostgres.New(pool), credentialCipherOrNil(credentialService), generator,
	)
	if err != nil {
		return err
	}

	// The private execution listener is the ONLY place TaskExecution and
	// CredentialLease RPCs exist. It requires mutually authenticated TLS
	// with exact WorkOS process identities; the Gateway and every public
	// surface deterministically cannot route here.
	executionTLS, err := privatetls.ServerConfig(privatetls.Identity{
		CAFile:       cfg.Execution.CAFile,
		CertFile:     cfg.Execution.CertFile,
		KeyFile:      cfg.Execution.KeyFile,
		PeerIdentity: privatetls.IdentityHarnessHost,
	})
	if err != nil {
		return err
	}
	// The lease-bound context resolver composes the Agent task-lease
	// authority with the Artifact module's transaction-scoped read: identity,
	// project binding, subtype, and the exact pinned digest are revalidated
	// from the claimed lease inside one transaction before any byte leaves
	// Core (ADR-0010).
	taskContextResolver, err := orchestration.NewTaskContextResolver(pool, agentRepository, artifactRepository, generator)
	if err != nil {
		return err
	}
	executionMux := httpserver.NewMux("workos-core-execution", ready)
	// The composition layer adapts the orchestrator's batch method to the
	// transport's narrow interface (transport-local output type), keeping
	// both modules free of each other's imports.
	batchMaterializer := batchMaterializerAdapter{m: artifactMaterializer}
	executionPath, executionHandler := agenttransport.NewExecutionConnectHandler(agentService, batchMaterializer, taskContextResolver)
	executionMux.Handle(executionPath, executionHandler)
	leasePath, leaseHandler := credentialtransport.NewLeaseConnectHandler(credentialIssuer)
	executionMux.Handle(leasePath, leaseHandler)

	manifestValidator, err := manifestvalidator.New()
	if err != nil {
		return err
	}
	projectDirectory, err := orchestration.NewProjectDirectory(projectService)
	if err != nil {
		return err
	}
	artifactDirectory, err := orchestration.NewArtifactDirectory(artifactService)
	if err != nil {
		return err
	}
	appService, err := appregistryapp.New(appregistrypostgres.New(pool), manifestValidator, projectDirectory, artifactDirectory, generator)
	if err != nil {
		return err
	}
	appPath, appHandler := appregistrytransport.NewConnectHandler(appService)
	mux.Handle(appPath, identity.Middleware(appHandler))

	appCatalog, err := orchestration.NewAppCatalog(appService)
	if err != nil {
		return err
	}
	installationService, err := projectapp.NewInstallationService(projectpostgres.New(pool), appCatalog, generator)
	if err != nil {
		return err
	}
	installationPath, installationHandler := projecttransport.NewInstallationConnectHandler(installationService)
	mux.Handle(installationPath, identity.Middleware(installationHandler))

	// The Agent policy/approval/usage facts revalidate installation liveness
	// through the neutral facts adapter above; they never touch Project
	// tables. The three public services are owner-only surfaces behind the
	// Gateway identity.
	installationFactsAdapter, err := orchestration.NewInstallationFacts(installationService)
	if err != nil {
		return err
	}
	providerCapabilitiesAdapter, err := orchestration.NewProviderCapabilities(catalogService)
	if err != nil {
		return err
	}
	policyService, err := agentapp.NewPolicyService(agentRepository, installationFactsAdapter, generator)
	if err != nil {
		return err
	}
	approvalService, err := agentapp.NewApprovalService(agentRepository, installationFactsAdapter, providerCapabilitiesAdapter, orchestration.NewCredentialSnapshotVerifier(credentialService))
	if err != nil {
		return err
	}
	usageService, err := agentapp.NewUsageService(agentRepository, installationFactsAdapter)
	if err != nil {
		return err
	}
	artifactContextVerifier, err := orchestration.NewArtifactContextVerifier(artifactService)
	if err != nil {
		return err
	}
	taskRouter, err := orchestration.NewTaskRouter(agentService, projectService, policyService, providerCapabilitiesAdapter, orchestration.NewCredentialSnapshots(credentialService), artifactContextVerifier, cfg.Agent.DefaultProvider)
	if err != nil {
		return err
	}
	agentPath, agentHandler := agentv1connect.NewAgentTaskServiceHandler(agenttransport.New(agentService, taskRouter))
	mux.Handle(agentPath, identity.Middleware(agentHandler))
	policyPath, policyHandler := agenttransport.NewPolicyConnectHandler(policyService)
	mux.Handle(policyPath, identity.Middleware(policyHandler))
	approvalPath, approvalHandler := agenttransport.NewApprovalConnectHandler(approvalService)
	mux.Handle(approvalPath, identity.Middleware(approvalHandler))
	usagePath, usageHandler := agenttransport.NewUsageConnectHandler(usageService)
	mux.Handle(usagePath, identity.Middleware(usageHandler))

	// The private installed-instance resolver composes the three authoritative
	// module services; only runtime-host reaches it on the private listener.
	launchResolver, err := orchestration.NewSurfaceLaunchResolver(installationService, appService, artifactService)
	if err != nil {
		return err
	}
	resolverPath, resolverHandler := orchestrationtransport.NewSurfaceResolverConnectHandler(launchResolver)
	mux.Handle(resolverPath, identity.Middleware(resolverHandler))

	// The private App Agent service composes installation authority with the
	// Task Router: every bridge call re-validates the active installation and
	// its grant snapshot before any task is created or watched. Only
	// runtime-host reaches it on the private listener; it never enters the
	// gateway allowlist.
	appAgentService, err := orchestration.NewAppAgentService(installationService, taskRouter)
	if err != nil {
		return err
	}
	appAgentPath, appAgentHandler := orchestrationtransport.NewAppAgentConnectHandler(appAgentService)
	mux.Handle(appAgentPath, identity.Middleware(appAgentHandler))

	// The private index publication source service composes the feed claim
	// store with the neutral source authority (Artifact + Project modules).
	// Only the indexer reaches it on the internal network; it never enters
	// the gateway allowlist (ADR-0013).
	indexSourceAuthority, err := orchestration.NewIndexSourceAuthority(artifactService, projectService, projectpostgres.New(pool))
	if err != nil {
		return err
	}
	indexFeedService, err := indexfeedapp.NewService(feedRepository, indexSourceAuthority, pool)
	if err != nil {
		return err
	}
	indexFeedPath, indexFeedHandler := indexfeedtransport.NewConnectHandler(indexFeedService)
	mux.Handle(indexFeedPath, identity.Middleware(indexFeedHandler))

	// The public notification surface is registered here; its store and
	// producer sink were constructed above (ADR-0014).
	notificationService, err := notificationapp.New(notificationRepository, pool, generator)
	if err != nil {
		return err
	}
	notificationPath, notificationHandler := notificationtransport.NewConnectHandler(notificationService)
	mux.Handle(notificationPath, identity.Middleware(notificationHandler))
	// Bounded housekeeping: old read facts are swept and the owner sweep
	// watermark advances, so stream-gap detection stays authoritative.
	// Correctness never relies on this loop; every failure is observable.
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := notificationService.Sweep(ctx); err != nil {
					logger.Warn("notification sweep failed", "error", err)
				}
			}
		}
	}()

	artifactPath, artifactHandler := artifacttransport.NewConnectHandler(artifactService)
	mux.Handle(artifactPath, identity.Middleware(artifactHandler))
	capabilities := []*commonv1.FeatureCapability{
		&commonv1.FeatureCapability{Id: "project", Available: true},
		&commonv1.FeatureCapability{Id: "agent-task", Available: true},
		&commonv1.FeatureCapability{Id: "harness-catalog", Available: true},
		&commonv1.FeatureCapability{Id: "app-registry", Available: true},
		&commonv1.FeatureCapability{Id: "app-installation", Available: true},
		&commonv1.FeatureCapability{Id: "artifact", Available: true, Reason: "web bundle subtype only"},
		&commonv1.FeatureCapability{Id: "surface-launch-resolution", Available: true},
	}
	if credentialService != nil {
		capabilities = append(capabilities, &commonv1.FeatureCapability{Id: "credential-vault", Available: true,
			Reason: "provider API-key encrypted store + local admin socket + task-bound leases only"})
	} else {
		capabilities = append(capabilities, &commonv1.FeatureCapability{Id: "credential-vault", Available: false,
			Reason: "credential master key and admin socket are not configured"})
	}
	systemPath, systemHandler := commonv1connect.NewSystemServiceHandler(systemhandler.New("workos-core", commonv1.HealthState_HEALTH_STATE_HEALTHY, capabilities...))
	mux.Handle(systemPath, systemHandler)

	// The private execution listener and the credential admin socket share
	// the Core lifecycle: a failure on either stops the whole process rather
	// than serving an edge whose security path is broken.
	executionErr := make(chan error, 1)
	go func() {
		executionErr <- httpserver.RunWithTLSConfigContext(ctx, "workos-core-execution", cfg.Execution.Address, executionMux, logger, executionTLS, cfg.Telemetry.OTLPEndpoint)
	}()
	var adminSocket *credentialtransport.AdminSocket
	var adminErr chan error
	if credentialService != nil {
		_, adminHandler := credentialtransport.NewAdminConnectHandler(credentialService, cfg.Auth.OwnerID)
		wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			adminHandler.ServeHTTP(w, r)
		})
		adminSocket, err = credentialtransport.ListenAdminSocket(cfg.Credential.AdminSocketPath, wrapped, logger)
		if err != nil {
			return err
		}
		adminErr = make(chan error, 1)
		go func() { adminErr <- adminSocket.Serve() }()
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = adminSocket.Close(shutdownCtx)
		}()
		logger.Info("credential admin socket listening")
		// Bounded housekeeping: expired credential leases are marked expired.
		// Every read fails closed independently, so correctness never relies
		// on this loop.
		go func() {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if _, err := credentialService.SweepExpiredLeases(ctx); err != nil {
						logger.Warn("credential lease sweep failed", "error", err)
					}
				}
			}
		}()
	}

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- httpserver.Run("workos-core", cfg.HTTP.Address, mux, logger, "", "", cfg.Telemetry.OTLPEndpoint)
	}()
	select {
	case err := <-serverErr:
		return err
	case err := <-executionErr:
		if err == nil {
			err = errors.New("harness execution listener stopped unexpectedly")
		}
		return fmt.Errorf("harness execution listener failed: %w", err)
	case err := <-adminErr:
		if err == nil {
			err = errors.New("credential admin socket stopped unexpectedly")
		}
		return fmt.Errorf("credential admin socket failed: %w", err)
	}
}

// credentialCipherOrNil exposes the vault cipher to the lease issuer without
// widening the application service. A nil vault keeps the issuer constructed
// but strictly fail-closed (acquire answers unavailable).
func credentialCipherOrNil(service *credentialapp.Service) credentialports.Cipher {
	if service == nil {
		return nil
	}
	return service.Cipher()
}

// batchMaterializerAdapter converts the transport-local batch output type to
// the orchestration coordinator's input (see ADR-0011).
type batchMaterializerAdapter struct {
	m *orchestration.TaskArtifactMaterializer
}

func (a batchMaterializerAdapter) MaterializeTaskArtifact(ctx context.Context, leaseID, workerID, outputKey, title, artifactType string, content []byte) (*artifactv1.Artifact, *agentv1.AgentEvent, error) {
	return a.m.MaterializeTaskArtifact(ctx, leaseID, workerID, outputKey, title, artifactType, content)
}

func (a batchMaterializerAdapter) MaterializeTaskArtifactBatch(ctx context.Context, leaseID, workerID string, outputs []agenttransport.BatchOutput) ([]*artifactv1.Artifact, []*agentv1.AgentEvent, error) {
	batch := make([]orchestration.BatchOutput, 0, len(outputs))
	for _, output := range outputs {
		batch = append(batch, orchestration.BatchOutput{Key: output.Key, Title: output.Title, Type: output.Type, Content: output.Content})
	}
	return a.m.MaterializeTaskArtifactBatch(ctx, leaseID, workerID, batch)
}
