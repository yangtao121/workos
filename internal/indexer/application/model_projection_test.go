package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/indexer/ports"
	"github.com/yangtao121/workos/tests/fixtures/embedding"
)

type retryModel struct {
	embedding.Model
	unavailable bool
}

func (m *retryModel) Document(ctx context.Context, text string) ([]float32, error) {
	if m.unavailable {
		return nil, ports.ErrEmbeddingUnavailable
	}
	return m.Model.Document(ctx, text)
}

type modelStoreSpy struct {
	ModelProjectionStore
	writes int
}

func (*modelStoreSpy) ModelFingerprint() string { return embedding.Fingerprint }
func (s *modelStoreSpy) ApplyResolvedSource(_ context.Context, source ports.ResolvedSource, _, _ string, _ time.Time) error {
	if !source.Embedding.Valid() {
		return errors.New("missing derived model vector")
	}
	s.writes++
	return nil
}

type modelFeedSpy struct {
	ports.CoreFeedClient
	acknowledged int
}

func (*modelFeedSpy) Resolve(context.Context, string, string, string) (ports.ResolvedSource, error) {
	return resolvedSourceFixture(), nil
}
func (f *modelFeedSpy) Complete(_ context.Context, _ string, results []ports.ConsumptionResult) ([]bool, error) {
	f.acknowledged += len(results)
	return []bool{true}, nil
}

func TestModelOutageRetriesWithoutCommittingOrAcknowledging(t *testing.T) {
	ctx := context.Background()
	model := &retryModel{unavailable: true}
	store, feed := &modelStoreSpy{}, &modelFeedSpy{}
	projection, err := NewModelProjection(store, model)
	if err != nil {
		t.Fatal(err)
	}
	ingestion, err := NewIngestionService(feed, projection, "model-retry-fixture")
	if err != nil {
		t.Fatal(err)
	}
	claim := ports.ClaimedPublication{PublicationID: resolvedSourceFixture().PublicationID, LeaseToken: "fixture-lease"}
	outcome, err := ingestion.IngestOne(ctx, claim)
	if !errors.Is(err, ports.ErrEmbeddingUnavailable) || !outcome.Retryable || store.writes != 0 || feed.acknowledged != 0 {
		t.Fatalf("outage consumed publication: %+v %v", outcome, err)
	}
	model.unavailable = false
	outcome, err = ingestion.IngestOne(ctx, claim)
	if err != nil || !outcome.Acked || store.writes != 1 || feed.acknowledged != 1 {
		t.Fatalf("recovery failed: %+v %v", outcome, err)
	}
}
