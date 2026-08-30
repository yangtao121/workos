package podman

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yangtao121/workos/internal/runtime/workload/domain"
	"github.com/yangtao121/workos/internal/runtime/workload/ports"
)

// maxReadBytes bounds every cgroup file read; cgroup values are tiny by
// construction, so any larger content is a hostile or corrupt filesystem.
const maxReadBytes = 64 * 1024

// CgroupReader reads the real cgroup v2 facts of the workload cgroups the
// engine reports. Every read is bounded; every path is validated by the
// domain before use.
type CgroupReader struct {
	mountPoint string
}

func NewCgroupReader() (*CgroupReader, error) {
	mount, err := cgroupV2MountPoint()
	if err != nil {
		return nil, err
	}
	return &CgroupReader{mountPoint: mount}, nil
}

// cgroupV2MountPoint locates the cgroup2 mount. It verifies the filesystem
// type marker file exists so a cgroup v1 host fails honestly.
func cgroupV2MountPoint() (string, error) {
	const unified = "/sys/fs/cgroup"
	if _, err := os.Stat(filepath.Join(unified, "cgroup.controllers")); err != nil {
		return "", fmt.Errorf("cgroup v2 is not available: %w", err)
	}
	return unified, nil
}

// SelfSubtree returns this process's delegated cgroup v2 subtree as an
// absolute path under the unified mount point: the single `0::/path` entry
// of /proc/self/cgroup joined onto the cgroup2 mount. Every workload cgroup
// path must live under this subtree.
func SelfSubtree() (string, error) {
	return absoluteSubtree("/sys/fs/cgroup", "/proc/self/cgroup")
}

// absoluteSubtree reads one /proc/<pid>/cgroup file and joins the unified
// `0::/path` entry onto mountPoint. The kernel reports the path relative to
// the hierarchy root; every consumer (validation, reads) requires the
// absolute location, so the join happens here — exactly once.
func absoluteSubtree(mountPoint, procPath string) (string, error) {
	content, err := os.ReadFile(procPath)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(content), "\n") {
		hydrated, path, found := strings.Cut(line, "::")
		if !found || hydrated != "0" {
			continue
		}
		if path == "" || path == "/" {
			return strings.TrimSuffix(mountPoint, "/"), nil
		}
		return filepath.Join(mountPoint, path), nil
	}
	return "", errors.New("process has no cgroup v2 entry")
}

// CgroupPathForPID resolves the absolute host cgroup v2 path of one process.
func (r *CgroupReader) CgroupPathForPID(pid int) (string, error) {
	if pid <= 0 {
		return "", errors.New("invalid pid")
	}
	return absoluteSubtree(r.mountPoint, fmt.Sprintf("/proc/%d/cgroup", pid))
}

// SelfSubtree returns this reader's mount-absolute delegated subtree.
func (r *CgroupReader) SelfSubtree() (string, error) {
	return absoluteSubtree(r.mountPoint, "/proc/self/cgroup")
}

func (r *CgroupReader) read(path, file string) (string, error) {
	mountPoint := filepath.Clean(r.mountPoint)
	cleanPath := filepath.Clean(path)
	relative, err := filepath.Rel(mountPoint, cleanPath)
	if err != nil || cleanPath != path || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("cgroup path escapes the mount point")
	}
	handle, err := os.Open(filepath.Join(cleanPath, file))
	if err != nil {
		return "", err
	}
	defer func() { _ = handle.Close() }()
	content, err := io.ReadAll(io.LimitReader(handle, maxReadBytes+1))
	if err != nil {
		return "", err
	}
	text := string(content)
	if len(text) > maxReadBytes {
		return "", errors.New("cgroup file exceeds the bounded read")
	}
	return strings.TrimSpace(text), nil
}

// ReadEffective reads the enforced limits: cpu.max (quota period),
// memory.high, memory.max, pids.max. The "max" sentinel (no limit) maps to 0
// and always fails the caller's equality check against the finite effective
// policy.
func (r *CgroupReader) ReadEffective(ctx context.Context, path string) (ports.EffectiveFacts, error) {
	facts := ports.EffectiveFacts{}
	if err := ctx.Err(); err != nil {
		return facts, err
	}
	cpuMax, err := r.read(path, "cpu.max")
	if err != nil {
		return facts, err
	}
	cpuFields := strings.Fields(cpuMax)
	if len(cpuFields) != 2 {
		return facts, errors.New("cpu.max is malformed")
	}
	if cpuFields[0] == "max" {
		facts.CPUMaxUSec = 0
	} else {
		number, err := strconv.ParseInt(cpuFields[0], 10, 64)
		if err != nil || number <= 0 {
			return facts, errors.New("cpu.max quota is malformed")
		}
		facts.CPUMaxUSec = number
	}
	period, err := strconv.ParseInt(cpuFields[1], 10, 64)
	if err != nil || period <= 0 {
		return facts, errors.New("cpu.max period is malformed")
	}
	facts.CPUPeriodUSec = period
	high, err := r.read(path, "memory.high")
	if err != nil {
		return facts, err
	}
	facts.MemoryHigh, err = parseLimit(high)
	if err != nil {
		return facts, errors.New("memory.high is malformed")
	}
	maximum, err := r.read(path, "memory.max")
	if err != nil {
		return facts, err
	}
	facts.MemoryMax, err = parseLimit(maximum)
	if err != nil {
		return facts, errors.New("memory.max is malformed")
	}
	pids, err := r.read(path, "pids.max")
	if err != nil {
		return facts, err
	}
	if pids == "max" {
		facts.PIDsMax = 0
	} else {
		number, err := strconv.ParseInt(pids, 10, 64)
		if err != nil || number <= 0 {
			return facts, errors.New("pids.max is malformed")
		}
		facts.PIDsMax = number
	}
	return facts, nil
}

func parseLimit(value string) (int64, error) {
	if value == "max" {
		return 0, nil
	}
	number, err := strconv.ParseInt(value, 10, 64)
	if err != nil || number <= 0 {
		return 0, errors.New("cgroup limit is malformed")
	}
	return number, nil
}

// ReadCounters reads the bounded numeric counters: cpu.stat usage_usec,
// memory.current/peak, memory.events oom, pids.current, and pids.events max.
func (r *CgroupReader) ReadCounters(ctx context.Context, path string) (ports.CgroupCounters, error) {
	counters := ports.CgroupCounters{}
	if err := ctx.Err(); err != nil {
		return counters, err
	}
	usage, err := r.read(path, "cpu.stat")
	if err != nil {
		return counters, err
	}
	if counters.CPUUsageUSec, err = requiredCounterValue(usage, "usage_usec"); err != nil {
		return counters, err
	}
	current, err := r.read(path, "memory.current")
	if err != nil {
		return counters, err
	}
	if counters.MemoryCurrent, err = parseCounter(current); err != nil {
		return counters, errors.New("memory.current is malformed")
	}
	peak, err := r.read(path, "memory.peak")
	if err != nil {
		return counters, err
	}
	if counters.MemoryPeak, err = parseCounter(peak); err != nil {
		return counters, errors.New("memory.peak is malformed")
	}
	events, err := r.read(path, "memory.events")
	if err != nil {
		return counters, err
	}
	oom, err := requiredCounterValue(events, "oom")
	if err != nil {
		return counters, err
	}
	oomKill, err := requiredCounterValue(events, "oom_kill")
	if err != nil || ^uint64(0)-oom < oomKill {
		return counters, errors.New("memory.events is malformed")
	}
	counters.MemoryOOMs = oom + oomKill
	pidsCurrent, err := r.read(path, "pids.current")
	if err != nil {
		return counters, err
	}
	if counters.PIDsCurrent, err = parseCounter(pidsCurrent); err != nil {
		return counters, errors.New("pids.current is malformed")
	}
	pidsEvents, err := r.read(path, "pids.events")
	if err != nil {
		return counters, err
	}
	if counters.PIDsLimitEvents, err = requiredCounterValue(pidsEvents, "max"); err != nil {
		return counters, err
	}
	return counters, nil
}

func parseCounter(value string) (uint64, error) {
	number, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, errors.New("cgroup counter is malformed")
	}
	return number, nil
}

func requiredCounterValue(content, key string) (uint64, error) {
	found := false
	var result uint64
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != key {
			continue
		}
		if found {
			return 0, errors.New("cgroup counter is duplicated")
		}
		number, err := parseCounter(fields[1])
		if err != nil {
			return 0, err
		}
		found = true
		result = number
	}
	if !found {
		return 0, errors.New("required cgroup counter is missing")
	}
	return result, nil
}

// Prober probes the workload's HTTP health endpoint. It follows no redirects
// and never reads the body beyond draining it bounded.
type Prober struct{}

func NewProber() *Prober { return &Prober{} }

func (p *Prober) Probe(ctx context.Context, endpoint, httpPath string, timeout time.Duration) (ports.HealthResult, error) {
	if !domain.ValidLoopbackEndpoint(endpoint) {
		return ports.HealthResult{Verdict: domain.HealthFailing}, domain.ErrInvalid
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	target := "http://" + endpoint + httpPath
	status, err := headStatus(probeCtx, target)
	if err != nil || status < 200 || status >= 400 {
		return ports.HealthResult{Verdict: domain.HealthFailing}, nil
	}
	return ports.HealthResult{Verdict: domain.HealthOK}, nil
}

// headStatus performs a GET with no redirect following and drains at most a
// small bounded tail of the body.
func headStatus(ctx context.Context, target string) (int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, err
	}
	response, err := probeClient.Do(request)
	if err != nil {
		return 0, err
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.CopyN(io.Discard, response.Body, 4*1024)
	return response.StatusCode, nil
}

// probeClient never follows redirects: the workload's health endpoint is a
// plain server-rendered probe, and a 3xx is not "ok".
var probeClient = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
	Timeout: 5 * time.Second,
}

// UnavailableCgroupReader is the honest no-reader for hosts without cgroup
// v2. It refuses every read; the capability probe already reported the
// runner unavailable, so this is never consulted on a launch path.
type UnavailableCgroupReader struct{}

func NewUnavailableCgroupReader() *UnavailableCgroupReader { return &UnavailableCgroupReader{} }

func (r *UnavailableCgroupReader) SelfSubtree() (string, error) {
	return "", errors.New("cgroup v2 is not available")
}

func (r *UnavailableCgroupReader) CgroupPathForPID(int) (string, error) {
	return "", errors.New("cgroup v2 is not available")
}

func (r *UnavailableCgroupReader) ReadEffective(context.Context, string) (ports.EffectiveFacts, error) {
	return ports.EffectiveFacts{}, errors.New("cgroup v2 is not available")
}

func (r *UnavailableCgroupReader) ReadCounters(context.Context, string) (ports.CgroupCounters, error) {
	return ports.CgroupCounters{}, errors.New("cgroup v2 is not available")
}
