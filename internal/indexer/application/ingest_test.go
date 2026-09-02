package application

import (
	"strings"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/indexer/domain"
	"github.com/yangtao121/workos/internal/indexer/ports"
)

func resolvedSourceFixture() ports.ResolvedSource {
	return ports.ResolvedSource{
		Verdict: "resolved", Operation: "review-artifact.upsert",
		OwnerUserID:  "01999999-9999-7999-8999-000000000031",
		ProjectID:    "01999999-9999-7999-8999-000000000032",
		ArtifactID:   "01999999-9999-7999-8999-000000000033",
		SourceTaskID: "01999999-9999-7999-8999-000000000034",
		ArtifactType: "document.markdown.v1", Digest: "sha256:" + strings.Repeat("b", 64),
		Title: "Bounded title", Content: []byte("bounded body"), CreatedAt: time.Unix(1, 0).UTC(),
		PublicationID: "01999999-9999-7999-8999-000000000035", OccurredAt: time.Unix(2, 0).UTC(),
	}
}

func TestClassifyValidatesResolvedSourceBeforeAdapter(t *testing.T) {
	t.Parallel()
	service := &IngestionService{}
	if outcome, err := service.classify(resolvedSourceFixture()); err != nil || outcome != domain.OutcomeApplied {
		t.Fatalf("outcome=%q error=%v", outcome, err)
	}
	for name, mutate := range map[string]func(*ports.ResolvedSource){
		"publication":   func(s *ports.ResolvedSource) { s.PublicationID = strings.Repeat("x", 36) },
		"operation":     func(s *ports.ResolvedSource) { s.Operation = "unknown" },
		"title control": func(s *ports.ResolvedSource) { s.Title = "bad\nname" },
		"content bound": func(s *ports.ResolvedSource) { s.Content = []byte(strings.Repeat("x", 512*1024+1)) },
		"occurred":      func(s *ports.ResolvedSource) { s.OccurredAt = time.Time{} },
	} {
		source := resolvedSourceFixture()
		mutate(&source)
		if _, err := service.classify(source); err != domain.ErrCorrupt {
			t.Fatalf("%s: error=%v", name, err)
		}
	}
}
