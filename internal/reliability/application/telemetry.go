// Telemetry aggregation (ADR-0016 §4): the sanitized per-service summary
// derived from the collector's export feed. It is a bounded numeric
// projection — no spans, log bodies, attribute maps, or user content survive
// the aggregation.
package application

import (
	"context"
	"sort"
	"sync"
	"time"
)

// TelemetryServiceStats is one service's bounded span statistics.
type TelemetryServiceStats struct {
	Service           string
	SpanCount         uint64
	ErrorCount        uint64
	AvgDurationMS     float64
	MaxDurationMS     float64
	AttributesDropped uint64
	// TotalDurationMS backs AvgDurationMS and never leaves the process.
	TotalDurationMS float64
}

// RecordSpan folds one observed span into the bucket.
func (s *TelemetryServiceStats) RecordSpan(durationMS float64, isError bool) {
	s.SpanCount++
	s.TotalDurationMS += durationMS
	if durationMS > s.MaxDurationMS {
		s.MaxDurationMS = durationMS
	}
	if isError {
		s.ErrorCount++
	}
}

// TelemetrySnapshot is one aggregation pass over the export tail.
type TelemetrySnapshot struct {
	Services          []*TelemetryServiceStats
	SpansObserved     uint64
	AttributesDropped uint64
	GeneratedAt       time.Time
}

// ServiceStats returns the mutable bucket for one service, bounded by
// maxServices (extra services fold into the "other" bucket).
func (s *TelemetrySnapshot) ServiceStats(service string, maxServices int) *TelemetryServiceStats {
	for _, stats := range s.Services {
		if stats.Service == service {
			return stats
		}
	}
	if len(s.Services) >= maxServices {
		for _, stats := range s.Services {
			if stats.Service == "other" {
				return stats
			}
		}
		service = "other"
	}
	stats := &TelemetryServiceStats{Service: service}
	s.Services = append(s.Services, stats)
	return stats
}

// Finalize sorts the services and derives the averages.
func (s *TelemetrySnapshot) Finalize() {
	for _, stats := range s.Services {
		if stats.SpanCount > 0 {
			stats.AvgDurationMS = stats.TotalDurationMS / float64(stats.SpanCount)
		}
	}
	sort.Slice(s.Services, func(i, j int) bool {
		if s.Services[i].SpanCount != s.Services[j].SpanCount {
			return s.Services[i].SpanCount > s.Services[j].SpanCount
		}
		return s.Services[i].Service < s.Services[j].Service
	})
}

// TelemetrySource is the collector component's refresh port.
type TelemetrySource interface {
	Refresh(ctx context.Context) (TelemetrySnapshot, error)
	Available() bool
}

// TelemetryAggregator caches the last fresh snapshot behind a bounded
// refresh: concurrent readers share the most recent pass, one refresher
// recomputes it, and an unavailable source yields an empty snapshot.
type TelemetryAggregator struct {
	mu       sync.RWMutex
	snapshot TelemetrySnapshot
	source   TelemetrySource
	minAge   time.Duration
	last     time.Time
}

// NewTelemetryAggregator wires the source. minAge throttles re-reads of the
// export file under polling load.
func NewTelemetryAggregator(source TelemetrySource, minAge time.Duration) *TelemetryAggregator {
	return &TelemetryAggregator{source: source, minAge: minAge}
}

// Available reports whether a telemetry source is configured at all.
func (a *TelemetryAggregator) Available() bool { return a.source != nil && a.source.Available() }

// Summary returns the aggregated snapshot, refreshing it when the cached
// pass is older than minAge. Source failures keep the last good snapshot.
func (a *TelemetryAggregator) Summary(ctx context.Context) TelemetrySnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.source == nil || !a.source.Available() {
		return TelemetrySnapshot{GeneratedAt: time.Now().UTC()}
	}
	if time.Since(a.last) >= a.minAge {
		if snapshot, err := a.source.Refresh(ctx); err == nil {
			snapshot.Finalize()
			a.snapshot = snapshot
			a.last = time.Now()
		}
	}
	return a.snapshot
}
