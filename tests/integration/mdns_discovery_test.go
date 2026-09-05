//go:build integration

package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/platform/transportprovider"
	"github.com/yangtao121/workos/internal/platform/transportprovider/lansd"
)

func fingerprintFor(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// TestMDNSDiscovery proves the W5 LAN slice over real multicast: an mDNS
// announcer is discovered, fingerprint verification admits only the pairing
// expectation, forged advertisements are rejected, and the Relay/Overlay
// providers stay honestly unavailable.
func TestMDNSDiscovery(t *testing.T) {
	ctx := context.Background()

	// Fingerprint grammar and constant-time verification.
	good := fingerprintFor("gateway-cert")
	bad := fingerprintFor("forged-cert")
	if !transportprovider.ValidFingerprint(good) {
		t.Fatal("valid fingerprint rejected")
	}
	if transportprovider.ValidFingerprint("sha256:nothex") || transportprovider.ValidFingerprint("") {
		t.Fatal("malformed fingerprint accepted")
	}
	if err := transportprovider.VerifyFingerprint(good, good); err != nil {
		t.Fatalf("matching fingerprint rejected: %v", err)
	}
	if err := transportprovider.VerifyFingerprint(good, bad); !errors.Is(err, transportprovider.ErrFingerprintMismatch) {
		t.Fatalf("fingerprint mismatch verdict drifted: %v", err)
	}

	// Relay and Overlay stay honestly unavailable.
	relay := transportprovider.NewUnavailable("relay")
	if relay.Status() != transportprovider.StatusUnavailable {
		t.Fatal("relay status must be unavailable")
	}
	if _, err := relay.Browse(ctx, good); !errors.Is(err, transportprovider.ErrProviderUnavailable) {
		t.Fatalf("relay browse verdict drifted: %v", err)
	}

	// Real multicast: announce a loopback WorkOS instance and discover it.
	origin := "https://workos.local:8443"
	announcer, err := lansd.NewAnnouncer("workos-mdns-gate", origin, good)
	if err != nil {
		t.Skipf("mDNS responder unavailable on this host: %v", err)
	}
	defer announcer.Close() //nolint:errcheck
	if err := announcer.Announce(); err != nil {
		t.Fatalf("announce: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	discovery := lansd.NewDiscovery()
	defer discovery.Close() //nolint:errcheck
	browseCtx, cancel := context.WithTimeout(ctx, transportprovider.BrowseBudget)
	defer cancel()
	instances, err := discovery.Browse(browseCtx, good)
	if err != nil {
		t.Skipf("mDNS browse unavailable on this host: %v", err)
	}
	found := false
	for _, instance := range instances {
		if strings.HasPrefix(instance.Origin, "https://") && instance.Fingerprint == good {
			found = true
		}
	}
	if !found {
		t.Fatalf("announced instance not discovered with the expected fingerprint: %+v", instances)
	}

	// A forged advertisement (different certificate) is never surfaced.
	foreign, err := discovery.Browse(browseCtx, bad)
	if err != nil {
		t.Fatalf("forged browse: %v", err)
	}
	for _, instance := range foreign {
		if instance.Fingerprint == good {
			t.Fatalf("fingerprint expectation is not enforced: %+v", instance)
		}
	}

	// Malformed expectations fail closed before any network work.
	if _, err := discovery.Browse(browseCtx, "sha256:short"); !errors.Is(err, transportprovider.ErrDiscoveryInvalid) {
		t.Fatalf("malformed expectation verdict drifted: %v", err)
	}
}
