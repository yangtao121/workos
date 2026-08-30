package application

import (
	"sync"
	"time"

	"github.com/yangtao121/workos/internal/gateway/auth/ports"
)

// RateLimiter is the bounded process-local limiter layered on top of the
// durable per-object attempt budgets. Keys are remote addresses (never
// X-Forwarded-For); the entry map has a hard capacity with deterministic
// eviction so attacker-controlled keys cannot grow memory without bound.
type RateLimiter struct {
	mu      sync.Mutex
	entries map[string]*rateWindow
	limit   int
	window  time.Duration
	maxKeys int
	clock   ports.Clock
}

type rateWindow struct {
	start time.Time
	count int
}

// NewRateLimiter builds the limiter; limit is the per-window request budget,
// maxKeys the hard map capacity.
func NewRateLimiter(limit int, window time.Duration, maxKeys int, clock ports.Clock) *RateLimiter {
	if limit < 1 {
		limit = 1
	}
	if window <= 0 {
		window = time.Minute
	}
	if maxKeys < 1 {
		maxKeys = 1
	}
	return &RateLimiter{
		entries: make(map[string]*rateWindow, 16),
		limit:   limit,
		window:  window,
		maxKeys: maxKeys,
		clock:   clock,
	}
}

// Allow reports whether key may proceed in the current fixed window. When
// the map is full, expired windows are evicted first; if none expired, the
// oldest window is evicted so the limiter never grows unbounded.
func (l *RateLimiter) Allow(key string) bool {
	now := l.clock.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.entries[key]
	if !ok || now.Sub(entry.start) >= l.window {
		if !ok && len(l.entries) >= l.maxKeys {
			l.evict(now)
		}
		entry = &rateWindow{start: now, count: 0}
		l.entries[key] = entry
	}
	entry.count++
	return entry.count <= l.limit
}

// evict drops expired windows first, then the oldest starts, while holding
// the lock. Bounded work: one pass over at most maxKeys entries.
func (l *RateLimiter) evict(now time.Time) {
	oldestKey := ""
	var oldest time.Time
	for key, entry := range l.entries {
		if now.Sub(entry.start) >= l.window {
			delete(l.entries, key)
			continue
		}
		if oldestKey == "" || entry.start.Before(oldest) {
			oldestKey = key
			oldest = entry.start
		}
	}
	if len(l.entries) >= l.maxKeys && oldestKey != "" {
		delete(l.entries, oldestKey)
	}
}
