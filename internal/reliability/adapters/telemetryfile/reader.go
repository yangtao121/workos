// Package telemetryfile reads the OpenTelemetry collector's file export —
// the bounded JSONL feed of already-trimmed spans — and aggregates it into
// the sanitized per-service telemetry summary the public IncidentService
// serves (ADR-0016 §4). Raw spans never leave this package: only counts,
// error counts, and durations survive.
package telemetryfile

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yangtao121/workos/internal/reliability/application"
)

const (
	// maxFileBytes bounds how much of the export file is read per refresh
	// (the tail of the feed; the collector rotates or the operator prunes).
	maxFileBytes = 8 << 20
	// maxLines bounds the parsed lines per refresh.
	maxLines = 20000
	// maxServices bounds the aggregate service keyspace.
	maxServices = 64
	// droppedAttributesKey carries the in-process trim marker (ADR-0016).
	droppedAttributesKey = "workos.telemetry.attributes_dropped"
)

// Reader is the collector component's file source.
type Reader struct {
	path string
	mu   sync.Mutex
}

// New constructs the reader for one export file path. An empty path is a
// valid configuration: telemetry aggregation stays unavailable.
func New(path string) *Reader { return &Reader{path: path} }

// Available reports whether a source file is configured.
func (r *Reader) Available() bool { return r != nil && r.path != "" }

// attribute is one OTel JSON attribute (string or int value).
type attribute struct {
	Key   string `json:"key"`
	Value struct {
		StringValue string `json:"stringValue"`
		IntValue    string `json:"intValue"`
	} `json:"value"`
}

type oneSpan struct {
	Name       string      `json:"name"`
	Attributes []attribute `json:"attributes"`
	Status     struct {
		Code string `json:"code"`
	} `json:"status"`
	StartTimeUnixNano string `json:"startTimeUnixNano"`
	EndTimeUnixNano   string `json:"endTimeUnixNano"`
}

type resourceSpans struct {
	Resource struct {
		Attributes []attribute `json:"attributes"`
	} `json:"resource"`
	ScopeSpans []struct {
		Spans []oneSpan `json:"spans"`
	} `json:"scopeSpans"`
}

type exportBatch struct {
	ResourceSpans []resourceSpans `json:"resourceSpans"`
}

// Refresh re-reads the export tail and aggregates it into a fresh summary.
// A missing file is an empty (not failed) summary: telemetry may simply not
// have been produced yet.
func (r *Reader) Refresh(ctx context.Context) (application.TelemetrySnapshot, error) {
	snapshot := application.TelemetrySnapshot{GeneratedAt: time.Now().UTC()}
	if r == nil || !r.Available() {
		return snapshot, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	file, err := os.Open(r.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return snapshot, nil
		}
		return snapshot, fmt.Errorf("open telemetry export: %w", err)
	}
	defer file.Close() //nolint:errcheck -- read-only handle
	info, err := file.Stat()
	if err != nil {
		return snapshot, fmt.Errorf("stat telemetry export: %w", err)
	}
	if size := info.Size(); size > maxFileBytes {
		if _, err := file.Seek(size-maxFileBytes, 0); err != nil {
			return snapshot, fmt.Errorf("seek telemetry export: %w", err)
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), 4<<20)
		scanner.Scan() // drop the partial line the seek landed in
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	lines := 0
	for scanner.Scan() && lines < maxLines {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		lines++
		var batch exportBatch
		if err := json.Unmarshal([]byte(line), &batch); err != nil {
			// One malformed line never fails the whole refresh.
			continue
		}
		aggregateBatch(&snapshot, &batch)
	}
	sort.Slice(snapshot.Services, func(i, j int) bool {
		if snapshot.Services[i].SpanCount != snapshot.Services[j].SpanCount {
			return snapshot.Services[i].SpanCount > snapshot.Services[j].SpanCount
		}
		return snapshot.Services[i].Service < snapshot.Services[j].Service
	})
	return snapshot, nil
}

func aggregateBatch(snapshot *application.TelemetrySnapshot, batch *exportBatch) {
	for _, resourceSpans := range batch.ResourceSpans {
		service := attributeString(resourceSpans.Resource.Attributes, "service.name")
		if strings.TrimSpace(service) == "" {
			service = "unknown"
		}
		stats := snapshot.ServiceStats(service, maxServices)
		for _, scope := range resourceSpans.ScopeSpans {
			for _, span := range scope.Spans {
				snapshot.SpansObserved++
				durationMS := spanDurationMS(span)
				isError := span.Status.Code == "STATUS_CODE_ERROR" || span.Status.Code == "2"
				stats.RecordSpan(durationMS, isError)
				for _, attribute := range span.Attributes {
					if attribute.Key == droppedAttributesKey {
						if dropped, err := strconv.ParseUint(attribute.Value.IntValue, 10, 64); err == nil {
							snapshot.AttributesDropped += dropped
						}
					}
				}
			}
		}
	}
}

func attributeString(attributes []attribute, key string) string {
	for _, attribute := range attributes {
		if attribute.Key == key {
			return attribute.Value.StringValue
		}
	}
	return ""
}

func spanDurationMS(span oneSpan) float64 {
	start, startErr := strconv.ParseInt(span.StartTimeUnixNano, 10, 64)
	end, endErr := strconv.ParseInt(span.EndTimeUnixNano, 10, 64)
	if startErr != nil || endErr != nil || end <= start {
		return 0
	}
	return float64(end-start) / float64(time.Millisecond)
}
