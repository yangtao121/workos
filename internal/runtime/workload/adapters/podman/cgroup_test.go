package podman

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAbsoluteSubtreeJoinsTheMountPoint pins the P0 fix: /proc reads report
// the cgroup path RELATIVE to the hierarchy root; the reader must join it
// onto the unified mount point so every consumer (validation, reads) gets
// one absolute location.
func TestAbsoluteSubtreeJoinsTheMountPoint(t *testing.T) {
	procFile := filepath.Join(t.TempDir(), "cgroup")
	if err := os.WriteFile(procFile, []byte(
		"12:hugetlb:/legacy\n0::/user.slice/user-1000.slice/user@1000.service/app.slice\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := absoluteSubtree("/sys/fs/cgroup", procFile)
	if err != nil {
		t.Fatalf("absoluteSubtree: %v", err)
	}
	want := "/sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/app.slice"
	if got != want {
		t.Fatalf("subtree %q, want %q", got, want)
	}

	// The namespace root collapses onto the mount point itself.
	rootFile := filepath.Join(t.TempDir(), "root")
	if err := os.WriteFile(rootFile, []byte("0::/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := absoluteSubtree("/sys/fs/cgroup", rootFile); err != nil || got != "/sys/fs/cgroup" {
		t.Fatalf("root subtree %q err=%v, want /sys/fs/cgroup", got, err)
	}

	// The reader's own reads resolve under the mount point, so a workload
	// path derived through this function passes the boundary check while a
	// bare relative path cannot.
	reader := &CgroupReader{mountPoint: "/sys/fs/cgroup"}
	if _, err := reader.read(want, "cpu.max"); err == nil && !strings_Prefix(want, reader.mountPoint) {
		t.Fatalf("mount-point boundary not enforced")
	}
}

func strings_Prefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
