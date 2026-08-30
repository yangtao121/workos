package transport

import (
	"fmt"
	"testing"

	"connectrpc.com/connect"

	"github.com/yangtao121/workos/internal/gateway/auth/domain"
)

func TestCorruptionVerdictWinsOverNestedClientVerdicts(t *testing.T) {
	t.Parallel()
	// A parser used on stored material may carry an InvalidRequest cause.
	// ErrAuthCorrupt must still win so the wire result is Internal, not 400.
	err := fmt.Errorf("%w: nested parser: %w", domain.ErrAuthCorrupt, domain.ErrInvalidRequest)
	if code := connect.CodeOf(verdict(err)); code != connect.CodeInternal {
		t.Fatalf("corruption mapped to %s", code)
	}
}
