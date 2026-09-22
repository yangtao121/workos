package application

import (
	"context"
	"errors"
	"github.com/yangtao121/workos/internal/runtime/surface/domain"
	"testing"
)

func TestExactGenerationRejectsBeforeAttachmentAndTerminalFactsRemainReadable(t *testing.T) {
	service, runtime, store := newContinuityFixture(t)
	ctx := context.Background()
	const workload = "01999999-9999-7999-8999-000000000c01"
	row := runtime.workloads[workload]
	row.Generation = 3
	row.LifecycleMode = 2
	runtime.workloads[workload] = row
	if _, err := service.AttachSurface(ctx, continuityOwner, deviceA, workload, "old-generation", 2); !errors.Is(err, domain.ErrWorkloadNotRunning) {
		t.Fatalf("stale generation accepted: %v", err)
	}
	counts, err := store.CountLiveAttachments(ctx, continuityOwner, continuityProject)
	if err != nil || counts[workload] != 0 {
		t.Fatal("stale attachment mutated state")
	}
	result, err := service.AttachSurface(ctx, continuityOwner, deviceA, workload, "current-generation", 3)
	if err != nil || !result.Attachment.Controls {
		t.Fatalf("current generation refused: %v", err)
	}
	row.Terminal = true
	row.State = "failed"
	runtime.workloads[workload] = row
	facts, err := service.GetSurfaceWorkload(ctx, continuityOwner, workload)
	if err != nil || !facts.Workload.Terminal || facts.KeepAliveSeconds != 0 || facts.Generation != 3 {
		t.Fatalf("terminal policy facts lost: %#v %v", facts, err)
	}
	if len(runtime.stopped) != 0 {
		t.Fatal("reading terminal facts mutated workload")
	}
}
