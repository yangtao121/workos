// workos-mdns-announce (ADR-0019): the LAN discovery responder. It
// advertises exactly two facts over mDNS — the public origin and the pinned
// TLS leaf fingerprint — so LAN clients can discover and verify the pairing
// target. Secrets, tokens, and pairing URLs never enter the broadcast.
package main

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/yangtao121/workos/internal/platform/transportprovider"
	"github.com/yangtao121/workos/internal/platform/transportprovider/lansd"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	origin := os.Getenv("WORKOS_MDNS_ORIGIN")
	if origin == "" {
		logger.Error("WORKOS_MDNS_ORIGIN is required")
		os.Exit(2)
	}
	certFile := os.Getenv("WORKOS_HTTP_TLS_CERT_FILE")
	if certFile == "" {
		logger.Error("WORKOS_HTTP_TLS_CERT_FILE is required")
		os.Exit(2)
	}
	instance := os.Getenv("WORKOS_MDNS_INSTANCE")
	if instance == "" {
		instance = "workos-gateway"
	}

	pemBytes, err := os.ReadFile(certFile)
	if err != nil {
		logger.Error("read TLS certificate", "error", err)
		os.Exit(1)
	}
	var der []byte
	for len(pemBytes) > 0 {
		var block *pem.Block
		block, pemBytes = pem.Decode(pemBytes)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			logger.Error("parse TLS leaf certificate", "error", err)
			os.Exit(1)
		}
		// The gateway pins the FIRST certificate of the served chain, so the
		// first parsed leaf is the advertised fingerprint source.
		der = certificate.Raw
		break
	}
	if len(der) == 0 {
		logger.Error("no certificate found in the TLS certificate file")
		os.Exit(1)
	}
	digest := sha256.Sum256(der)
	fingerprint := "sha256:" + hex.EncodeToString(digest[:])
	if !transportprovider.ValidFingerprint(fingerprint) {
		logger.Error("computed fingerprint failed the grammar")
		os.Exit(1)
	}

	announcer, err := lansd.NewAnnouncer(instance, origin, fingerprint)
	if err != nil {
		logger.Error("build mdns announcer", "error", err)
		os.Exit(1)
	}
	defer announcer.Close() //nolint:errcheck
	if err := announcer.Announce(); err != nil {
		logger.Error("announce", "error", err)
		os.Exit(1)
	}
	logger.Info("mdns responder announcing", "origin", origin)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	sig := <-signals
	logger.Info("mdns responder stopping", "signal", sig.String())
}
