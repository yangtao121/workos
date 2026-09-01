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
	"github.com/yangtao121/workos/internal/indexer/transport"
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

	ready := func(ctx context.Context) error { return pool.Ping(ctx) }
	mux := httpserver.NewMux("indexer", ready)
	service := compositeIndexService{search: search, repair: repair}
	indexPath, indexHandler := transport.NewConnectHandler(service)
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
	}
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
