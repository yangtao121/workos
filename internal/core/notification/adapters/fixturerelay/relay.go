// Package fixturerelay is the working stand-in push relay (ADR-0018): it
// records every dispatch with its full payload so the whitelist boundary is
// assertable, and it honors injected failure so the chain's error paths stay
// real. Real APNs/FCM senders remain unavailable until provider credentials
// exist; the whitelist payload shape is identical.
package fixturerelay

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/yangtao121/workos/internal/core/notification/domain"
)

// ErrRelayDown is the injected relay outage.
var ErrRelayDown = errors.New("fixture push relay is down")

// Recorded is one relay-visible dispatch fact.
type Recorded struct {
	DeviceID       string
	Platform       string
	PayloadJSON    string
	NotificationID string
}

// Relay is the fixture sender.
type Relay struct {
	mu        sync.Mutex
	down      bool
	delivered []Recorded
}

// New builds the fixture relay.
func New() *Relay { return &Relay{} }

// SetDown flips the injected outage.
func (r *Relay) SetDown(down bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.down = down
}

// Deliver sends one whitelist payload. The JSON body is produced here from
// the domain whitelist struct alone, so a field can only reach the wire by
// joining the whitelist.
func (r *Relay) Deliver(ctx context.Context, subscription domain.PushSubscription, payload domain.PushPayload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.down {
		return ErrRelayDown
	}
	r.delivered = append(r.delivered, Recorded{
		DeviceID: subscription.DeviceID, Platform: subscription.Platform,
		PayloadJSON: string(body), NotificationID: payload.NotificationID,
	})
	return nil
}

// Delivered returns the recorded dispatches for the given owner-independent
// assertion surface.
func (r *Relay) Delivered() []Recorded {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Recorded(nil), r.delivered...)
}

// Reset clears the recorded dispatches.
func (r *Relay) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.delivered = nil
}
