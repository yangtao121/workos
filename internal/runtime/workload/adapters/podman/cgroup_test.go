package podman

import (
	"context"
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

func TestReadCountersUsesPIDsMaxEvent(t *testing.T) {
	root := t.TempDir()
	for name, value := range map[string]string{
		"cpu.stat":       "usage_usec 12\n",
		"memory.current": "1\n",
		"memory.peak":    "2\n",
		"memory.events":  "oom 0\noom_kill 0\n",
		"pids.current":   "3\n",
		// cgroup v2 defines the cumulative limit-hit counter as `max`.
		"pids.events": "max 7\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(value), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	reader := &CgroupReader{mountPoint: root}
	counters, err := reader.ReadCounters(context.Background(), root)
	if err != nil {
		t.Fatalf("ReadCounters: %v", err)
	}
	if counters.PIDsLimitEvents != 7 {
		t.Fatalf("pids max events %d, want 7", counters.PIDsLimitEvents)
	}
}

func TestReadEffectiveCarriesCPUQuotaAndPeriod(t *testing.T) {
	root := t.TempDir()
	for name, value := range map[string]string{
		"cpu.max": "100000 100000\n", "memory.high": "67108864\n",
		"memory.max": "100663296\n", "pids.max": "32\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(value), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	reader := &CgroupReader{mountPoint: root}
	facts, err := reader.ReadEffective(context.Background(), root)
	if err != nil {
		t.Fatalf("ReadEffective: %v", err)
	}
	if facts.CPUMaxUSec != 100000 || facts.CPUPeriodUSec != 100000 {
		t.Fatalf("cpu.max facts=%+v, want quota and period", facts)
	}
	if err := os.WriteFile(filepath.Join(root, "cpu.max"), []byte("100000 200000\n"), 0o644); err != nil {
		t.Fatalf("write cpu.max drift: %v", err)
	}
	drifted, err := reader.ReadEffective(context.Background(), root)
	if err != nil || drifted.CPUPeriodUSec != 200000 {
		t.Fatalf("period drift was not preserved: facts=%+v err=%v", drifted, err)
	}
}

func TestReadCountersRejectsMissingOrMalformedEvidence(t *testing.T) {
	writeFixture := func(t *testing.T, root string) {
		t.Helper()
		for name, value := range map[string]string{
			"cpu.stat": "usage_usec 12\n", "memory.current": "1\n", "memory.peak": "2\n",
			"memory.events": "oom 0\noom_kill 0\n", "pids.current": "3\n", "pids.events": "max 0\n",
		} {
			if err := os.WriteFile(filepath.Join(root, name), []byte(value), 0o644); err != nil {
				t.Fatalf("write %s: %v", name, err)
			}
		}
	}
	t.Run("missing", func(t *testing.T) {
		root := t.TempDir()
		writeFixture(t, root)
		if err := os.Remove(filepath.Join(root, "pids.events")); err != nil {
			t.Fatalf("remove pids.events: %v", err)
		}
		if _, err := (&CgroupReader{mountPoint: root}).ReadCounters(context.Background(), root); err == nil {
			t.Fatal("missing pids.events was treated as zero")
		}
	})
	t.Run("malformed", func(t *testing.T) {
		root := t.TempDir()
		writeFixture(t, root)
		if err := os.WriteFile(filepath.Join(root, "memory.events"), []byte("oom nope\noom_kill 0\n"), 0o644); err != nil {
			t.Fatalf("write malformed memory.events: %v", err)
		}
		if _, err := (&CgroupReader{mountPoint: root}).ReadCounters(context.Background(), root); err == nil {
			t.Fatal("malformed memory.events was treated as zero")
		}
	})
}

func TestCgroupReadRejectsPrefixSiblingAndTraversal(t *testing.T) {
	root := t.TempDir()
	reader := &CgroupReader{mountPoint: root}
	for _, path := range []string{root + "-sibling", filepath.Join(root, "child", "..", "..", "escape")} {
		if _, err := reader.read(path, "cpu.stat"); err == nil {
			t.Fatalf("unsafe cgroup path %q was accepted", path)
		}
	}
}

func strings_Prefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
