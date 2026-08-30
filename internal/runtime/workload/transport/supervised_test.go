package transport

import (
	"errors"
	"testing"

	"connectrpc.com/connect"

	"github.com/yangtao121/workos/internal/runtime/workload/domain"
)

func TestControlErrorsKeepUnsupportedSeparateFromRestartBudget(t *testing.T) {
	if code := connect.CodeOf(mapError(domain.ErrUnsupported)); code != connect.CodeFailedPrecondition {
		t.Fatalf("unsupported code %v, want failed precondition", code)
	}
	if code := connect.CodeOf(mapError(domain.ErrRestartLimitExhausted)); code != connect.CodeResourceExhausted {
		t.Fatalf("restart limit code %v, want resource exhausted", code)
	}
	if code := connect.CodeOf(mapError(errors.New("unknown"))); code != connect.CodeInternal {
		t.Fatalf("unknown code %v, want internal", code)
	}
}
