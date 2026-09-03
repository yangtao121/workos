package fakefixture

import (
	"context"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"

	workloadports "github.com/yangtao121/workos/internal/runtime/workload/ports"
)

// CgroupReader reports the fixture engine's recorded effective policy for
// verification reads and derives bounded deterministic counters for
// observation. It simulates NO real resource control (ADR-0016).
type CgroupReader struct{ state *State }

// NewCgroupReader is unused; the paired reader comes from New().
func newCgroupReader(state *State) *CgroupReader { return &CgroupReader{state: state} }

// SelfSubtree returns the fixture subtree marker.
func (r *CgroupReader) SelfSubtree() (string, error) {
	return "/workos/fixtures", nil
}

// CgroupPathForPID maps the fixture PID space onto a stable path.
func (r *CgroupReader) CgroupPathForPID(pid int) (string, error) {
	if pid <= 0 {
		return "", fmt.Errorf("fixture cgroup: pid %d is not addressable", pid)
	}
	return "/workos/fixtures/pid-" + strconv.Itoa(pid), nil
}

func (r *CgroupReader) ReadEffective(ctx context.Context, path string) (workloadports.EffectiveFacts, error) {
	// The path embeds the fixture PID; resolve the recorded policy.
	if pid, ok := pidFromPath(path); ok {
		if facts, known := r.state.resolvePolicy(pid); known {
			return facts, nil
		}
	}
	return workloadports.EffectiveFacts{}, fmt.Errorf("fixture cgroup: no recorded policy for %q", path)
}

// pidFromPath parses "/workos/fixtures/pid-<n>".
func pidFromPath(path string) (int, bool) {
	const marker = "/pid-"
	index := strings.LastIndex(path, marker)
	if index < 0 {
		return 0, false
	}
	pid, err := strconv.Atoi(path[index+len(marker):])
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

func (r *CgroupReader) ReadCounters(ctx context.Context, path string) (workloadports.CgroupCounters, error) {
	return workloadports.CgroupCounters{
		CPUUsageUSec:    fixtureNumber(path, "usage"),
		MemoryCurrent:   fixtureNumber(path, "mem"),
		MemoryPeak:      fixtureNumber(path, "peak"),
		MemoryOOMs:      fixtureNumber(path, "ooms") % 4,
		PIDsCurrent:     fixtureNumber(path, "pidscur") % 64,
		PIDsLimitEvents: fixtureNumber(path, "pidev") % 4,
	}, nil
}

// fixtureNumber derives a stable bounded number from the path and salt.
func fixtureNumber(path, salt string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(salt + ":" + path))
	return h.Sum64()
}
