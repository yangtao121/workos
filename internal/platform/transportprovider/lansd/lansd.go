// Package lansd is the LAN service-discovery provider (ADR-0019): an mDNS
// announcer for the local WorkOS gateway and a browsing discovery client.
// Advertised facts are the public origin and the pinned TLS fingerprint —
// never secrets, tokens, or pairing URLs.
package lansd

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/hashicorp/mdns"

	"github.com/yangtao121/workos/internal/platform/transportprovider"
)

// ServiceName is the mDNS service tag WorkOS instances advertise.
const ServiceName = "workos"

// Announcer advertises one instance over mDNS with the pinned fingerprint.
type Announcer struct {
	server *mdns.MDNSService
	mdns   *mdns.Server
}

// NewAnnouncer builds the responder for one origin + fingerprint pair.
func NewAnnouncer(instance, origin, fingerprint string) (*Announcer, error) {
	if !transportprovider.ValidFingerprint(fingerprint) {
		return nil, transportprovider.ErrDiscoveryInvalid
	}
	ips := []net.IP{net.ParseIP("127.0.0.1")}
	server, err := mdns.NewMDNSService(instance, ServiceName, "", "", 5353, ips, []string{
		"origin=" + origin,
		"fp=" + fingerprint,
	})
	if err != nil {
		return nil, fmt.Errorf("build mdns service: %w", err)
	}
	mdnsServer, err := mdns.NewServer(&mdns.Config{Zone: server})
	if err != nil {
		return nil, fmt.Errorf("start mdns responder: %w", err)
	}
	return &Announcer{server: server, mdns: mdnsServer}, nil
}

// Announce starts answering queries.
func (a *Announcer) Announce() error { return nil }

// Close shuts the responder down.
func (a *Announcer) Close() error { return a.mdns.Shutdown() }

// Discovery browses the local segment for WorkOS instances and returns only
// well-formed ones whose advertised fingerprint matches the pairing
// expectation. A mismatched advertisement is rejected, never surfaced.
type Discovery struct{}

// NewDiscovery builds the browsing client.
func NewDiscovery() *Discovery { return &Discovery{} }

// Browse runs one bounded mDNS lookup.
func (d *Discovery) Browse(ctx context.Context, expectedFingerprint string) ([]transportprovider.DiscoveredInstance, error) {
	if !transportprovider.ValidFingerprint(expectedFingerprint) {
		return nil, transportprovider.ErrDiscoveryInvalid
	}
	entries := make(chan *mdns.ServiceEntry, 16)
	var instances []transportprovider.DiscoveredInstance
	done := make(chan struct{})
	go func() {
		defer close(done)
		for entry := range entries {
			origin, fingerprint := "", ""
			for _, text := range strings.Split(entry.Info, "|") {
				switch {
				case strings.HasPrefix(text, "origin="):
					origin = strings.TrimPrefix(text, "origin=")
				case strings.HasPrefix(text, "fp="):
					fingerprint = strings.TrimPrefix(text, "fp=")
				}
			}
			instance := transportprovider.DiscoveredInstance{
				Origin: origin, Fingerprint: fingerprint,
				Host: fmt.Sprintf("%s:%d", entry.AddrV4, entry.Port),
			}
			if !instance.Valid() {
				continue
			}
			if transportprovider.VerifyFingerprint(expectedFingerprint, fingerprint) != nil {
				// A forged or foreign advertisement never reaches the caller.
				continue
			}
			instances = append(instances, instance)
		}
	}()
	if err := mdns.Lookup(ServiceName, entries); err != nil {
		return nil, fmt.Errorf("browse mdns: %w", err)
	}
	close(entries)
	<-done
	return instances, nil
}

// Close releases the browse resources (no persistent state today).
func (d *Discovery) Close() error { return nil }
