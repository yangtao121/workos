// The at-least-once ingestion worker (ADR-0013 §4): bounded claims, resolve,
// local commit, then Core complete — with bounded backoff that honours
// context cancellation. Shutdown cancels new claims and lets the in-flight
// effect settle; no in-process "completed" state ever precedes the database
// commit.
package application

import (
	"context"
	"errors"
	"time"

	"github.com/yangtao121/workos/internal/indexer/domain"
	"github.com/yangtao121/workos/internal/indexer/ports"
)

// WorkerConfig bounds the poll loop. Values are clamped so a hostile config
// can neither spin the loop nor stall recovery.
type WorkerConfig struct {
	WorkerID     string
	BatchSize    int
	Lease        time.Duration
	PollInterval time.Duration
	ErrorBackoff time.Duration
	MaxBackoff   time.Duration
}

const (
	defaultBatchSize = 8
	maxBatchSize     = 16
	minLease         = 5 * time.Second
	maxLease         = 5 * time.Minute
	minPollInterval  = 200 * time.Millisecond
	maxPollInterval  = 30 * time.Second
	minErrorBackoff  = 500 * time.Millisecond
	maxErrorBackoff  = 2 * time.Minute
)

func (c WorkerConfig) clamped() WorkerConfig {
	if c.BatchSize <= 0 {
		c.BatchSize = defaultBatchSize
	}
	if c.BatchSize > maxBatchSize {
		c.BatchSize = maxBatchSize
	}
	if c.Lease < minLease {
		c.Lease = minLease
	}
	if c.Lease > maxLease {
		c.Lease = maxLease
	}
	if c.PollInterval < minPollInterval {
		c.PollInterval = minPollInterval
	}
	if c.PollInterval > maxPollInterval {
		c.PollInterval = maxPollInterval
	}
	if c.ErrorBackoff < minErrorBackoff {
		c.ErrorBackoff = minErrorBackoff
	}
	if c.ErrorBackoff > maxErrorBackoff {
		c.ErrorBackoff = maxErrorBackoff
	}
	return c
}

// Worker runs the ingestion loop. Exactly one instance per process; the
// database arbitrates concurrent instances through the Core lease.
type Worker struct {
	ingestion  *IngestionService
	feed       ports.CoreFeedClient
	projection ports.ProjectionRepository
	config     WorkerConfig
	backoff    time.Duration
}

func NewWorker(ingestion *IngestionService, feed ports.CoreFeedClient, projection ports.ProjectionRepository, config WorkerConfig) (*Worker, error) {
	if ingestion == nil || feed == nil || projection == nil {
		return nil, errors.New("worker requires ingestion service, feed client, and projection")
	}
	if !domain.ValidWorkerID(config.WorkerID) {
		return nil, errors.New("worker requires a valid worker id")
	}
	return &Worker{
		ingestion: ingestion, feed: feed, projection: projection,
		config: config.clamped(), backoff: minErrorBackoff,
	}, nil
}

// Run drives the loop until the context is cancelled. Every claim batch is
// bounded; a drained batch waits one poll interval, a failure backs off
// with a bounded exponential ceiling.
func (w *Worker) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		claimed, err := w.feed.Claim(ctx, w.config.WorkerID, w.config.BatchSize, w.config.Lease)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !errors.Is(err, ports.ErrCoreUnavailable) {
				return err
			}
			if !w.wait(ctx, w.backoff) {
				return ctx.Err()
			}
			w.growBackoff()
			continue
		}
		for _, item := range claimed {
			outcome, err := w.ingestion.IngestOne(ctx, item)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if !outcome.Retryable {
					return err
				}
				if !w.wait(ctx, w.backoff) {
					return ctx.Err()
				}
				w.growBackoff()
				break
			}
			w.backoff = minErrorBackoff
		}
		if len(claimed) < w.config.BatchSize {
			if !w.wait(ctx, w.config.PollInterval) {
				return ctx.Err()
			}
		}
	}
}

func (w *Worker) wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (w *Worker) growBackoff() {
	doubled := w.backoff * 2
	if doubled > maxErrorBackoff {
		doubled = maxErrorBackoff
	}
	w.backoff = doubled
}
