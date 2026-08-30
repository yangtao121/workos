package httpserver

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yangtao121/workos/internal/platform/telemetry"
)

func NewMux(service string, ready func(context.Context) error) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"service":%q,"status":"alive"}`, service)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if ready != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if err := ready(ctx); err != nil {
				http.Error(w, "dependency unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"service":%q,"status":"ready"}`, service)
	})
	return mux
}

func Run(service, address string, handler http.Handler, logger *slog.Logger, tlsCert, tlsKey, telemetryEndpoint string) error {
	var tlsConfig *tls.Config
	if tlsCert != "" && tlsKey != "" {
		certificate, err := tls.LoadX509KeyPair(tlsCert, tlsKey)
		if err != nil {
			return fmt.Errorf("load TLS key pair: %w", err)
		}
		tlsConfig = &tls.Config{Certificates: []tls.Certificate{certificate}}
	}
	return RunWithTLSConfig(service, address, handler, logger, tlsConfig, telemetryEndpoint)
}

// RunWithTLSConfig serves the handler with an explicit TLS configuration;
// a nil configuration serves plain HTTP. The platform default minimum is
// TLS 1.2; callers that require TLS 1.3 pass it in their configuration.
func RunWithTLSConfig(service, address string, handler http.Handler, logger *slog.Logger, tlsConfig *tls.Config, telemetryEndpoint string) error {
	shutdownTelemetry, err := telemetry.Setup(context.Background(), service, telemetryEndpoint)
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTelemetry(shutdownCtx); err != nil {
			logger.Warn("telemetry shutdown failed", "error", err)
		}
	}()
	server := &http.Server{
		Addr: address, Handler: telemetry.Handler(service, handler),
		TLSConfig:         tlsConfig,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 0, IdleTimeout: 90 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("service listening", "address", address)
		var err error
		if tlsConfig != nil {
			err = server.ListenAndServeTLS("", "")
		} else {
			err = server.ListenAndServe()
		}
		errCh <- err
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}
