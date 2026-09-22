package domain

import "time"

// LifecycleMode is explicit; zero-valued legacy requests remain bounded.
type LifecycleMode int32

const (
	LifecycleBounded    LifecycleMode = 1
	LifecycleManualStop LifecycleMode = 2
)

func NormalizeLifecycle(modes ...LifecycleMode) (LifecycleMode, error) {
	mode := LifecycleBounded
	if len(modes) > 0 && modes[0] != 0 {
		mode = modes[0]
	}
	if mode != LifecycleBounded && mode != LifecycleManualStop {
		return 0, ErrInvalid
	}
	return mode, nil
}
func (m LifecycleMode) Expiry(now time.Time) time.Time {
	if m == LifecycleManualStop {
		return time.Time{}
	}
	return now.Add(PreviewTTL)
}
func (m LifecycleMode) Expired(expiry, now time.Time) bool {
	return m != LifecycleManualStop && !expiry.After(now)
}
