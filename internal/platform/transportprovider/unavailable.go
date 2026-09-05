package transportprovider

import "context"

// UnavailableProvider is the shared honest implementation for providers
// whose infrastructure does not exist yet (Relay, Overlay — ADR-0019).
// They keep the interface slot and never pretend to browse.
type UnavailableProvider struct {
	name string
}

// NewUnavailable builds one named unavailable provider.
func NewUnavailable(name string) *UnavailableProvider { return &UnavailableProvider{name: name} }

// Name reports the provider id.
func (p *UnavailableProvider) Name() string { return p.name }

// Status is always unavailable — by design, never by accident.
func (p *UnavailableProvider) Status() string { return StatusUnavailable }

// Browse always fails with the sanitized unavailable verdict.
func (p *UnavailableProvider) Browse(ctx context.Context, expectedFingerprint string) ([]DiscoveredInstance, error) {
	return nil, ErrProviderUnavailable
}

// Close is a no-op.
func (p *UnavailableProvider) Close() error { return nil }
