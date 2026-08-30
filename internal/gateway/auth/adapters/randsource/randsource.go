// Package randsource provides the process adapters for the injected time
// and CSPRNG dependencies of the device auth application service.
package randsource

import (
	"crypto/rand"
	"fmt"
	"time"
)

// Clock is the real-time UTC clock.
type Clock struct{}

func (Clock) Now() time.Time { return time.Now().UTC() }

// Entropy reads the platform CSPRNG; failure fails closed.
type Entropy struct{}

func (Entropy) Random(n int) ([]byte, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("read crypto/rand entropy: %w", err)
	}
	return raw, nil
}
