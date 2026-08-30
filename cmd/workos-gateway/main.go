package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/yangtao121/workos/gen/go/workos/auth/v1/authv1connect"
	"github.com/yangtao121/workos/internal/gateway"
	authpostgres "github.com/yangtao121/workos/internal/gateway/auth/adapters/postgres"
	"github.com/yangtao121/workos/internal/gateway/auth/adapters/randsource"
	"github.com/yangtao121/workos/internal/gateway/auth/application"
	authtransport "github.com/yangtao121/workos/internal/gateway/auth/transport"
	"github.com/yangtao121/workos/internal/platform/config"
	"github.com/yangtao121/workos/internal/platform/database"
	"github.com/yangtao121/workos/internal/platform/httpserver"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/logging"
	"github.com/yangtao121/workos/internal/platform/telemetry"
)

func main() {
	logger := logging.New("workos-gateway")
	if err := run(logger); err != nil {
		logger.Error("service stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := cfg.ValidateGateway(); err != nil {
		return err
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	var authStack *gateway.AuthStack
	var tlsConfig *tls.Config
	var adminHandler http.Handler
	var authApp *application.Service
	if !cfg.Auth.DevBypass {
		// Production mode: the Gateway terminates its own TLS 1.3 listener
		// and the ticket snapshots pin the leaf certificate it actually
		// serves.
		certificate, err := tls.LoadX509KeyPair(cfg.HTTP.TLSCertFile, cfg.HTTP.TLSKeyFile)
		if err != nil {
			return fmt.Errorf("load TLS key pair: %w", err)
		}
		if len(certificate.Certificate) == 0 {
			return errors.New("TLS certificate chain is empty")
		}
		digest := sha256.Sum256(certificate.Certificate[0])
		fingerprint := "sha256:" + hex.EncodeToString(digest[:])
		tlsConfig = &tls.Config{
			MinVersion:   tls.VersionTLS13,
			Certificates: []tls.Certificate{certificate},
		}
		pool, err := database.Open(ctx, cfg.DatabaseURL)
		if err != nil {
			return err
		}
		defer pool.Close()
		authApp, err = application.New(
			authpostgres.New(pool),
			application.Config{
				OwnerID:        cfg.Auth.OwnerID,
				PublicOrigin:   cfg.Auth.PublicOrigin,
				TLSFingerprint: fingerprint,
				TicketTTL:      authTTL(cfg.Auth.TicketTTL, 5*time.Minute),
				ChallengeTTL:   authTTL(cfg.Auth.ChallengeTTL, 2*time.Minute),
				SessionTTL:     authTTL(cfg.Auth.SessionTTL, 24*time.Hour),
			},
			randsource.Clock{}, randsource.Entropy{}, ids.UUIDv7{},
			application.NewRateLimiter(60, time.Minute, 4096, randsource.Clock{}),
		)
		if err != nil {
			return err
		}
		now := func() time.Time { return randsource.Clock{}.Now() }
		_, pairingConnect := authv1connect.NewDevicePairingServiceHandler(
			authtransport.NewPairingHandler(authApp, now))
		_, deviceConnect := authv1connect.NewDeviceServiceHandler(
			authtransport.NewDeviceHandler(authApp, now))
		_, adminConnect := authv1connect.NewDeviceAuthAdminServiceHandler(
			authtransport.NewAdminHandler(authApp, cfg.Auth.OwnerID))
		adminHandler = adminConnect
		authStack = &gateway.AuthStack{
			Service: authApp,
			Pairing: pairingConnect,
			Device:  deviceConnect,
			Limiter: application.NewRateLimiter(60, time.Minute, 4096, randsource.Clock{}),
		}
	}

	handler, err := gateway.New(cfg, logger, authStack)
	if err != nil {
		return err
	}
	client := telemetry.HTTPClient()
	ready := func(ctx context.Context) error {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.Services.Core+"/readyz", nil)
		if err != nil {
			return err
		}
		response, err := client.Do(request)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("core readiness returned %s", response.Status)
		}
		// Production readiness also covers the Gateway-owned auth store:
		// an outage degrades readiness instead of silently falling back to
		// stale identity material.
		if authApp != nil {
			return authApp.Ready(ctx)
		}
		return nil
	}
	mux := httpserver.NewMux("workos-gateway", ready)
	// Only health endpoints route through the ServeMux: its path cleaning
	// would redirect dot-segment or double-slash surface requests before the
	// gateway's fail-closed /surfaces/ policy can reject them.
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/healthz") || strings.HasPrefix(r.URL.Path, "/readyz") {
			mux.ServeHTTP(w, r)
			return
		}
		handler.ServeHTTP(w, r)
	})

	// The private admin socket exists only in production pairing mode: it
	// is owned by this process alone and never registered on the TCP mux.
	if adminHandler != nil {
		adminSocket, err := gateway.ListenAdminSocket(cfg.Auth.AdminSocketPath, adminHandler, logger)
		if err != nil {
			return err
		}
		adminErr := make(chan error, 1)
		go func() { adminErr <- adminSocket.Serve() }()
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = adminSocket.Close(shutdownCtx)
		}()
		go func() {
			select {
			case err := <-adminErr:
				if err != nil {
					logger.Error("admin socket failed", "error", err)
					stop()
				}
			case <-ctx.Done():
			}
		}()
		logger.Info("gateway admin socket listening")
	}
	return httpserver.RunWithTLSConfig("workos-gateway", cfg.HTTP.Address, root, logger, tlsConfig, cfg.Telemetry.OTLPEndpoint)
}

func authTTL(value, fallback time.Duration) time.Duration {
	if value == 0 {
		return fallback
	}
	return value
}
