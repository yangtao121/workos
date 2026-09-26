package transport

import (
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/runtime/surface/application"
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

func TestContinuityWorkloadViewDisplaysApplicationName(t *testing.T) {
	started := time.Date(2026, time.September, 26, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name     string
		workload ports.InteractiveWorkload
		want     string
	}{
		{"Code", ports.InteractiveWorkload{Kind: ports.WorkloadKindNative, DisplayName: "WorkOS Code"}, "WorkOS Code"},
		{"Text Editor", ports.InteractiveWorkload{Kind: ports.WorkloadKindNative, DisplayName: "Text Editor"}, "Text Editor"},
		{"legacy Native", ports.InteractiveWorkload{Kind: ports.WorkloadKindNative}, "Native display"},
		{"PTY", ports.InteractiveWorkload{Kind: ports.WorkloadKindPty}, "Terminal"},
		{"installed app", ports.InteractiveWorkload{Kind: ports.WorkloadKindApp, AppID: "notes-app"}, "notes-app"},
	} {
		t.Run(test.name, func(t *testing.T) {
			workload := test.workload
			workload.CreatedAt = started
			view := workloadViewProto(application.ContinuitySummary{Workload: workload, Generation: 1})
			if got := view.GetDisplayName(); got != test.want {
				t.Fatalf("display name = %q, want %q", got, test.want)
			}
		})
	}
}
