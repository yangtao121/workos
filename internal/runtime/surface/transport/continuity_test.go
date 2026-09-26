package transport

import (
	"testing"

	"github.com/yangtao121/workos/internal/runtime/surface/ports"
)

func TestContinuitySessionProjectsWorkloadGeneration(t *testing.T) {
	workload := ports.InteractiveWorkload{
		WorkloadID: "01999999-9999-7999-8999-000000000001",
		ProjectID:  "01999999-9999-7999-8999-000000000002",
		Kind:       ports.WorkloadKindNative,
		Generation: 3,
	}
	if got := continuitySessionProto(workload).GetWorkloadGeneration(); got != 3 {
		t.Fatalf("AttachSurface must return the native workload generation, got %d", got)
	}
	workload.Generation = 0 // Older stored sessions normalize to generation one.
	if got := continuitySessionProto(workload).GetWorkloadGeneration(); got != 1 {
		t.Fatalf("legacy workload generation must normalize to one, got %d", got)
	}
}
