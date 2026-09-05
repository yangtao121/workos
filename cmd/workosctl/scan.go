package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/yangtao121/workos/internal/platform/transportprovider"
	"github.com/yangtao121/workos/internal/platform/transportprovider/lansd"
)

// deviceScan implements `workosctl device scan`: the client side of the LAN
// discovery UX (ADR-0019). The pairing ticket (or its QR) already pins the
// TLS leaf fingerprint; the scan browses the local segment over mDNS and
// lists only instances whose advertised fingerprint matches that
// expectation, so a forged responder can never redirect pairing. The
// printed origins are the input to the existing pairing flow.
func deviceScan(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("device scan", flag.ContinueOnError)
	fingerprint := fs.String("fingerprint", "", "pinned TLS fingerprint from the pairing ticket (sha256:<64 hex>)")
	timeout := fs.Duration("timeout", 5*time.Second, "browse budget")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !transportprovider.ValidFingerprint(*fingerprint) {
		return errors.New("device scan requires --fingerprint sha256:<64 hex> from the pairing ticket")
	}
	if *timeout <= 0 || *timeout > 30*time.Second {
		return errors.New("device scan timeout must be between 1ms and 30s")
	}
	browseCtx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	discovery := lansd.NewDiscovery()
	defer discovery.Close() //nolint:errcheck
	instances, err := discovery.Browse(browseCtx, *fingerprint)
	if err != nil {
		return fmt.Errorf("browse LAN: %w", err)
	}
	if len(instances) == 0 {
		return errors.New("no WorkOS instance on the LAN matches this pairing fingerprint")
	}
	for _, instance := range instances {
		fmt.Fprintf(os.Stdout, "origin: %s\n", instance.Origin)
	}
	return nil
}
