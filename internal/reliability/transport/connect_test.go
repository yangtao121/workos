package transport

import (
	"errors"
	"testing"

	"connectrpc.com/connect"

	"github.com/yangtao121/workos/internal/reliability/ports"
)

func TestControlErrorDoesNotFabricateRestartLimit(t *testing.T) {
	cases := []struct {
		code connect.Code
		want ports.ControlOutcome
	}{
		{connect.CodeResourceExhausted, ports.ControlLimitExhausted},
		{connect.CodeFailedPrecondition, ports.ControlUnsupported},
		{connect.CodeAborted, ports.ControlConflict},
		{connect.CodeUnavailable, ports.ControlUnavailable},
	}
	for _, testCase := range cases {
		result, err := controlError(connect.NewError(testCase.code, errors.New("sanitized")))
		if err != nil || result.Outcome != testCase.want {
			t.Fatalf("code %v: result=%+v err=%v, want %v", testCase.code, result, err, testCase.want)
		}
	}
}
