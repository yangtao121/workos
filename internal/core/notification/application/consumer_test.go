package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/platform/dbtx"
)

type readinessSource struct {
	claimErr    error
	completeErr error
}

type inertTxSource struct{}

func (inertTxSource) Begin(context.Context) (dbtx.Tx, error) {
	return nil, errors.New("unexpected transaction")
}

func (s *readinessSource) ClaimIncidentPublications(context.Context, string, int32, int32) ([]IncidentPublication, error) {
	return nil, s.claimErr
}

func (s *readinessSource) CompleteIncidentPublications(context.Context, string, string, []string) error {
	return s.completeErr
}

func TestIncidentConsumerReadinessTracksUpstreamResults(t *testing.T) {
	source := &readinessSource{}
	consumer := &IncidentConsumer{source: source, workerID: "worker"}
	if consumer.Ready() {
		t.Fatal("consumer started ready before an upstream result")
	}
	if _, err := consumer.ClaimBatch(context.Background()); err != nil {
		t.Fatalf("successful claim: %v", err)
	}
	if !consumer.Ready() {
		t.Fatal("successful claim did not mark source ready")
	}
	source.completeErr = errors.New("source unavailable")
	if err := consumer.CompleteClaims(context.Background(), []IncidentPublication{{PublicationID: "p", LeaseToken: "l"}}); err == nil {
		t.Fatal("completion failure was not returned")
	}
	if consumer.Ready() {
		t.Fatal("completion failure left source ready")
	}
	source.completeErr = nil
	if err := consumer.CompleteClaims(context.Background(), nil); err != nil {
		t.Fatalf("successful completion: %v", err)
	}
	if !consumer.Ready() {
		t.Fatal("successful completion did not restore readiness")
	}
	source.claimErr = errors.New("source unavailable")
	if _, err := consumer.ClaimBatch(context.Background()); err == nil {
		t.Fatal("claim failure was not returned")
	}
	if consumer.Ready() {
		t.Fatal("claim failure left source ready")
	}
}

func TestIncidentConsumerLocalProjectionFailureDoesNotMisclassifyUpstream(t *testing.T) {
	consumer := &IncidentConsumer{
		source:   &readinessSource{},
		pool:     inertTxSource{},
		workerID: "worker",
	}
	consumer.ready.Store(true)
	if err := consumer.ApplyClaims(context.Background(), []IncidentPublication{{
		PublicationID: "01990575-4a80-7000-8000-000000000001",
		OwnerUserID:   "01990575-4a80-7000-8000-000000000002",
		ProjectID:     "01990575-4a80-7000-8000-000000000003",
		IncidentID:    "01990575-4a80-7000-8000-000000000004",
		Severity:      "warning",
		ActionOutcome: "failed",
		OccurredAt:    time.Date(2026, 9, 3, 1, 2, 3, 0, time.UTC),
		Digest:        "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}); err == nil {
		t.Fatal("local projection failure was not returned")
	}
	if !consumer.Ready() {
		t.Fatal("local projection failure misclassified the upstream source as unavailable")
	}
}
