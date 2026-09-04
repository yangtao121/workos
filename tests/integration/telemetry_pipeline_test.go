//go:build integration && telemetryfixture

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	"github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	incidentv1 "github.com/yangtao121/workos/gen/go/workos/incident/v1"
	"github.com/yangtao121/workos/gen/go/workos/incident/v1/incidentv1connect"
	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	"github.com/yangtao121/workos/gen/go/workos/project/v1/projectv1connect"
)

// TestTelemetryPipeline proves the ADR-0016 §4 chain end to end: real RPC
// traffic produces real spans, the in-process budget bounds them before
// export, the collector's file export feeds the reliability collector
// component, and the sanitized summary the public IncidentService serves
// contains only bounded numeric facts — never the submitted goal text.
func TestTelemetryPipeline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	client := &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext}}
	baseURL := "http://127.0.0.1:8080"
	projects := projectv1connect.NewProjectServiceClient(client, baseURL)
	tasks := agentv1connect.NewAgentTaskServiceClient(client, baseURL)
	incidents := incidentv1connect.NewIncidentServiceClient(client, baseURL)

	// Generate real traffic: a project plus a fake-harness task run.
	key := fmt.Sprintf("telemetry-%d", time.Now().UnixNano())
	created, err := projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: key, Name: "Telemetry Pipeline",
	}))
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	submitted, err := tasks.SubmitTask(ctx, connect.NewRequest(&agentv1.SubmitTaskRequest{
		IdempotencyKey: "task-" + key,
		Input: &agentv1.AgentTaskInput{
			TargetScope: &agentv1.TargetScope{Scope: &agentv1.TargetScope_ProjectId{ProjectId: created.Msg.GetProject().GetId()}},
			Role:        "general", Goal: "telemetry-pipeline-probe-goal-marker",
		},
	}))
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}
	stream, err := tasks.WatchTaskEvents(ctx, connect.NewRequest(&agentv1.WatchTaskEventsRequest{TaskId: submitted.Msg.GetTask().GetId()}))
	if err != nil {
		t.Fatalf("watch task: %v", err)
	}
	for stream.Receive() {
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("task stream: %v", err)
	}

	// Poll the sanitized summary until the collector feed carries the real
	// spans of the traffic above.
	var (
		summary   *incidentv1.GetTelemetrySummaryResponse
		sawCore   bool
		sawHarnes bool
	)
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		response, err := incidents.GetTelemetrySummary(ctx, connect.NewRequest(&incidentv1.GetTelemetrySummaryRequest{}))
		if err == nil {
			summary = response.Msg
			for _, service := range response.Msg.GetServices() {
				// The core process's service name is the resource of its
				// first-registered listener (workos-core-execution) under
				// the shared idempotent provider (ADR-0016).
				if (service.GetService() == "workos-core" || service.GetService() == "workos-core-execution") && service.GetSpanCount() > 0 {
					sawCore = true
				}
				if service.GetService() == "harness-host" && service.GetSpanCount() > 0 {
					sawHarnes = true
				}
			}
			if sawCore && sawHarnes {
				break
			}
		}
		time.Sleep(2 * time.Second)
	}
	if summary == nil || !sawCore || !sawHarnes {
		t.Fatalf("telemetry summary never carried the real traffic: core=%v harness=%v summary=%v",
			sawCore, sawHarnes, summary)
	}
	if summary.GetSpansObserved() < summary.GetServices()[0].GetSpanCount() {
		t.Fatal("observed span total is inconsistent with the service buckets")
	}

	// Sanitized projection whitelist: the wire facts are counts, durations,
	// service names, and timestamps only. The submitted goal marker must
	// never appear in any field.
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("encode summary: %v", err)
	}
	if strings.Contains(string(encoded), "telemetry-pipeline-probe-goal-marker") {
		t.Fatal("raw goal content leaked into the telemetry summary")
	}
	allowed := map[string]bool{
		"services": true, "service": true, "span_count": true, "error_count": true,
		"avg_duration_ms": true, "max_duration_ms": true, "attributes_dropped": true,
		"spans_observed": true, "generated_at": true,
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	for name := range fields {
		if !allowed[name] {
			t.Fatalf("unexpected summary field %q", name)
		}
	}
}
