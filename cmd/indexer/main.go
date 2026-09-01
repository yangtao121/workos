// The indexer composition root (ADR-0013): database/migration readiness, the
// real at-least-once ingestion worker, bounded startup + periodic
// reconciliation, the public owner-facing IndexService (Search + repair
// jobs), and honest system capabilities. Generic archive and semantic
// RAG/embedding stay unavailable with fixed reasons.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	"github.com/yangtao121/workos/gen/go/workos/common/v1/commonv1connect"
	"github.com/yangtao121/workos/internal/indexer/adapters/coreclient"
	indexerpostgres "github.com/yangtao121/workos/internal/indexer/adapters/postgres"
	indexerapp "github.com/yangtao121/workos/internal/indexer/application"
	indexertransport "github.com/yangtao121/workos/internal/indexer/transport"
	"github.com/yangtao121/workos/internal/platform/config"
	"github.com/yangtao121/workos/internal/platform/database"
	"github.com/yangtao121/workos/internal/platform/httpserver"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/logging"
	"github.com/yangtao121/workos/internal/platform/migrations"
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
	if cfg.Services.Core == "" {
		return errors.New("indexer requires WORKOS_CORE_URL")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The projection is disposable; the migration chain that creates it is
	// not. Running the shared forward-only migrations keeps a fresh volume
	// bootable; 001-027 are byte-identical everywhere.
	if err := migrations.Run(ctx, cfg.DatabaseURL); err != nil {
		return err
	}
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	generator := ids.UUIDv7{}
	projection, err := indexerpostgres.New(pool, generator)
	if err != nil {
		return err
	}
	if _, err := projection.EnsureBootstrapGeneration(ctx, time.Now().UTC()); err != nil {
		return err
	}
	jobStore, err := indexerpostgres.NewJobStore(pool, generator)
	if err != nil {
		return err
	}
	feed, err := coreclient.NewFeedClient(cfg.Services.Core, cfg.Auth.OwnerID, cfg.Auth.DeviceID, 15*time.Second)
	if err != nil {
		return err
	}
	ingestion, err := indexerapp.NewIngestionService(feed, projection, "indexer-worker-1")
	if err != nil {
		return err
	}
	repair, err := indexerapp.NewRepairService(jobStore, feed, projection, generator)
	if err != nil {
		return err
	}
	search, err := indexerapp.NewSearchService(projection, ingestion)
	if err != nil {
		return err
	}

	worker, err := indexerapp.NewWorker(ingestion, feed, projection, indexerapp.WorkerConfig{
		WorkerID: "indexer-worker-1", BatchSize: 8,
		Lease: 60 * time.Second, PollInterval: 1 * time.Second, ErrorBackoff: 1 * time.Second,
	})
	if err != nil {
		return err
	}
	workerErr := make(chan error, 1)
	go func() { workerErr <- worker.Run(ctx) }()

	// Bounded startup reconciliation + periodic reconciliation: additive
	// digest repair and archived-project tombstones over authoritative
	// pages. It never blocks readiness.
	go func() {
		runReconciliation := func() {
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			if err := indexerapp.Reconcile(ctx, feed, projection, generator, 100); err != nil {
				logger.Warn("index reconciliation pass failed", "error", err)
			}
		}
		runReconciliation()
		ticker := time.NewTicker(2 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runReconciliation()
			}
		}
	}()

	// The rebuild driver composes the feed (Core authority pages, drain
	// barrier, digest-pinned content) with the projection's active pointer.
	rebuildDriver := indexerapp.NewRebuildDriver(&rebuildFeed{FeedClient: feed, proj: projection}, generator)
	rebuildStore, err := indexerpostgres.NewRebuildStore(pool, generator)
	if err != nil {
		return err
	}
	executor, err := indexerapp.NewRebuildExecutor(rebuildStore, rebuildDriver, 100)
	if err != nil {
		return err
	}
	admin, err := indexerapp.NewAdminService(executor, ingestion, projection)
	if err != nil {
		return err
	}
	// The rebuild loop advances the durable state machine; passes are bounded
	// and a crash resumes from the stored phase and cursor.
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				passCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				if _, err := executor.RunPass(passCtx); err != nil && passCtx.Err() == nil {
					logger.Warn("rebuild pass failed", "error", err)
				}
				cancel()
			}
		}
	}()

	ready := func(ctx context.Context) error { return pool.Ping(ctx) }
	mux := httpserver.NewMux("indexer", ready)
	service := compositeIndexService{search: search, repair: repair}
	indexPath, indexHandler := indexertransport.NewConnectHandler(service)
	mux.Handle(indexPath, identity.Middleware(indexHandler))
	systemPath, systemHandler := commonv1connect.NewSystemServiceHandler(systemhandler.New("indexer",
		commonv1.HealthState_HEALTH_STATE_HEALTHY,
		&commonv1.FeatureCapability{Id: "project-review-index", Available: true,
			Reason: "durable review-artifact lexical projection (evidence limited to review artifacts)"},
		&commonv1.FeatureCapability{Id: "project-knowledge-search", Available: true,
			Reason: "bounded deterministic lexical search over review artifacts"},
		&commonv1.FeatureCapability{Id: "archive", Available: false,
			Reason: "generic archive and object storage are not implemented"},
		&commonv1.FeatureCapability{Id: "rag", Available: false,
			Reason: "semantic RAG, embeddings, and pgvector are not implemented; lexical search only"},
	))
	mux.Handle(systemPath, systemHandler)

	// The local admin socket (ADR-0013 §8): owner-verified Unix socket only,
	// never the gateway or any TCP listener. A failure stops the process.
	var adminErr chan error
	var adminSock *indexertransport.AdminSocket
	if cfg.Indexer.AdminSocketPath != "" {
		_, adminHandler := indexertransport.NewAdminConnectHandler(adminServiceSurface{admin})
		adminSock, err = indexertransport.ListenAdminSocket(cfg.Indexer.AdminSocketPath, adminHandler, logger)
		if err != nil {
			return err
		}
		adminErr = make(chan error, 1)
		go func() { adminErr <- adminSock.Serve() }()
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = adminSock.Close(shutdownCtx)
		}()
		logger.Info("indexer admin socket listening")
	}

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- httpserver.Run("indexer", cfg.HTTP.Address, mux, logger, "", "", cfg.Telemetry.OTLPEndpoint)
	}()
	select {
	case err := <-serverErr:
		cancel()
		return err
	case err := <-workerErr:
		if err == nil || errors.Is(err, context.Canceled) {
			// A cancelled worker context follows a server failure; keep the
			// server verdict.
			err = <-serverErr
			return err
		}
		return err
	case err := <-adminErr:
		if err == nil {
			err = errors.New("indexer admin socket stopped unexpectedly")
		}
		return err
	}
}

// adminServiceSurface adapts the AdminService to the transport interface.
type adminServiceSurface struct {
	admin *indexerapp.AdminService
}

func (a adminServiceSurface) Status(ctx context.Context) (indexerapp.IndexStatus, error) {
	return a.admin.Status(ctx)
}

func (a adminServiceSurface) StartRebuild(ctx context.Context, request indexerapp.RebuildRequest) (indexerapp.RebuildJobView, bool, error) {
	return a.admin.StartRebuild(ctx, request)
}

func (a adminServiceSurface) GetRebuildJob(ctx context.Context, jobID string) (indexerapp.RebuildJobView, error) {
	return a.admin.GetRebuildJob(ctx, jobID)
}

func (a adminServiceSurface) CancelRebuildJob(ctx context.Context, jobID string) (bool, error) {
	return a.admin.CancelRebuildJob(ctx, jobID)
}

// rebuildFeed composes the Core feed with the projection's active pointer
// for the rebuild driver: the feed methods delegate; only the active
// generation pointer comes from the projection.
type rebuildFeed struct {
	*coreclient.FeedClient
	proj *indexerpostgres.Repository
}

func (r *rebuildFeed) ActiveGenerationID(ctx context.Context) (string, error) {
	return r.proj.ActiveGenerationID(ctx)
}

// compositeIndexService binds the search and repair use cases onto the one
// public application contract the transport serves.
type compositeIndexService struct {
	search *indexerapp.SearchService
	repair *indexerapp.RepairService
}

func (c compositeIndexService) Search(ctx context.Context, input indexerapp.SearchInput) (indexerapp.SearchResult, error) {
	return c.search.Search(ctx, input)
}

func (c compositeIndexService) CreateRepairJob(ctx context.Context, input indexerapp.JobRequestInput) (indexerapp.JobView, bool, error) {
	return c.repair.CreateJob(ctx, input)
}

func (c compositeIndexService) GetRepairJob(ctx context.Context, ownerUserID, jobID string) (indexerapp.JobView, []indexerapp.JobSourceView, error) {
	return c.repair.GetJob(ctx, ownerUserID, jobID)
}
