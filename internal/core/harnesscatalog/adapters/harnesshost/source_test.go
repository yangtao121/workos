package harnesshost

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	harnessv1 "github.com/yangtao121/workos/gen/go/workos/harness/v1"
	"github.com/yangtao121/workos/internal/core/harnesscatalog/domain"
)

type describeClientFake struct {
	describe func(context.Context) (*connect.Response[harnessv1.DescribeProvidersResponse], error)
}

func (f describeClientFake) DescribeProviders(ctx context.Context, _ *connect.Request[harnessv1.DescribeProvidersRequest]) (*connect.Response[harnessv1.DescribeProvidersResponse], error) {
	return f.describe(ctx)
}

func TestSourceMapsEveryCanonicalCapabilityAndHealth(t *testing.T) {
	t.Parallel()
	client := describeClientFake{describe: func(context.Context) (*connect.Response[harnessv1.DescribeProvidersResponse], error) {
		return connect.NewResponse(&harnessv1.DescribeProvidersResponse{Providers: []*harnessv1.HarnessProviderInfo{{
			Id: "all", DisplayName: "All", AdapterVersion: "1", Health: commonv1.HealthState_HEALTH_STATE_DEGRADED,
			UnavailableReason: "private provider detail",
			Capabilities: &harnessv1.HarnessCapabilities{
				Streaming: true, PersistentSessions: true, Resume: true, SteerDuringRun: true,
				Approvals: true, ToolRegistration: true, Mcp: true, Subagents: true,
				WorkspaceMount: true, StructuredArtifacts: true, UsageReporting: true,
			},
		}, {Id: "illegal-health", Health: commonv1.HealthState(99)}}}), nil
	}}
	source, err := New(client, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	providers, err := source.ListProviders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	all := providers[0]
	if all.Health != domain.HealthDegraded || all.UnavailableReason != "" || !all.Capabilities.Streaming || !all.Capabilities.PersistentSessions || !all.Capabilities.Resume || !all.Capabilities.SteerDuringRun || !all.Capabilities.Approvals || !all.Capabilities.ToolRegistration || !all.Capabilities.MCP || !all.Capabilities.Subagents || !all.Capabilities.WorkspaceMount || !all.Capabilities.StructuredArtifacts || !all.Capabilities.UsageReporting {
		t.Fatalf("canonical provider was not fully mapped: %#v", all)
	}
	if providers[1].Health != domain.HealthUnknown {
		t.Fatalf("illegal health was not mapped to unknown: %#v", providers[1])
	}
}

func TestSourcePreservesEveryCanonicalHealthState(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		proto commonv1.HealthState
		want  domain.Health
	}{
		{commonv1.HealthState_HEALTH_STATE_UNSPECIFIED, domain.HealthUnknown},
		{commonv1.HealthState_HEALTH_STATE_STARTING, domain.HealthStarting},
		{commonv1.HealthState_HEALTH_STATE_HEALTHY, domain.HealthHealthy},
		{commonv1.HealthState_HEALTH_STATE_DEGRADED, domain.HealthDegraded},
		{commonv1.HealthState_HEALTH_STATE_UNAVAILABLE, domain.HealthUnavailable},
		{commonv1.HealthState(99), domain.HealthUnknown},
	} {
		if got := healthFromProto(test.proto); got != test.want {
			t.Errorf("health %d mapped to %q, want %q", test.proto, got, test.want)
		}
	}
}

func TestSourceMapsRawTransportFailureToSafeSentinel(t *testing.T) {
	t.Parallel()
	raw := errors.New("dial tcp 127.0.0.1:8082: Authorization Bearer should-not-escape")
	source, err := New(describeClientFake{describe: func(context.Context) (*connect.Response[harnessv1.DescribeProvidersResponse], error) {
		return nil, raw
	}}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.ListProviders(context.Background())
	if !errors.Is(err, domain.ErrUnavailable) || strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), "Bearer") {
		t.Fatalf("raw transport failure escaped: %v", err)
	}
}

func TestSourcePropagatesCancellationAndOwnDeadline(t *testing.T) {
	t.Parallel()
	blocking := describeClientFake{describe: func(ctx context.Context) (*connect.Response[harnessv1.DescribeProvidersResponse], error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source, err := New(blocking, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.ListProviders(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}

	source, err = New(blocking, 5*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.ListProviders(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected source deadline, got %v", err)
	}
}
