package transport

import (
	"connectrpc.com/connect"
	"fmt"
	"github.com/yangtao121/workos/internal/indexer/ports"
	"testing"
)

func TestModelFailureIsSanitizedUnavailable(t *testing.T) {
	err := mapError(fmt.Errorf("private model process detail: %w", ports.ErrEmbeddingUnavailable))
	if connect.CodeOf(err) != connect.CodeUnavailable || err.Error() != "unavailable: index service is temporarily unavailable" {
		t.Fatalf("model failure response: %v", err)
	}
}
