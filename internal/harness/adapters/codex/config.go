package codex

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	ProviderID     = "codex"
	AdapterVersion = "1.0.0"

	// purpose is the canonical credential kind this adapter consumes; it is
	// the exact lease purpose every run requires (ports.PurposeCodexAuthV1).
	purpose = "codex-auth.v1"

	// protocolVersion pins the app-server contract (ADR-0015). The child
	// rejects any other version during initialize, so a drifted runtime can
	// never half-serve a task.
	protocolVersion = "workos.codex.app-server/v1"

	DefaultMaxTokens = int64(4096)
	MaximumMaxTokens = int64(8192)
	DefaultTimeout   = 2 * time.Minute
	MaximumTimeout   = 10 * time.Minute

	// credentialEnv is the only channel the lease secret may travel through:
	// one allowlisted child environment variable for exactly this task. It
	// is never an argv value, a config field, or a log line.
	credentialEnv = "WORKOS_CODEX_AUTH_TOKEN"

	maximumGoalBytes = 1 << 20
)

// Config is the process-local adapter configuration. Like every provider
// adapter it has no credential field: the secret arrives only as a
// short-lived, task-bound lease (ADR-0009/0015).
type Config struct {
	Enabled     bool
	AppServer   string
	Timeout     time.Duration
	Environment string

	// fixtureMode and fixtureEnv exist solely for package tests; production
	// starts the app server with no caller-controlled extra environment.
	fixtureMode string
	fixtureEnv  []string
}

func normalizeConfig(config Config) Config {
	config.Environment = strings.TrimSpace(config.Environment)
	if config.Environment == "" {
		config.Environment = "production"
	}
	if config.Timeout == 0 {
		config.Timeout = DefaultTimeout
	}
	if config.AppServer == "" && config.Environment == "development" {
		config.AppServer = "/usr/local/libexec/workos/codex-app-server-fixture"
	}
	return config
}

func validateConfig(config Config) error {
	if !config.Enabled {
		return errors.New("codex harness adapter is not enabled")
	}
	if config.AppServer == "" || !filepath.IsAbs(config.AppServer) || filepath.Clean(config.AppServer) != config.AppServer {
		return errors.New("codex app server must be an absolute allowlisted path")
	}
	if info, err := os.Stat(config.AppServer); err != nil || info.IsDir() {
		return errors.New("codex app server is unavailable")
	}
	if config.Timeout < time.Second || config.Timeout > MaximumTimeout {
		return fmt.Errorf("codex timeout must be between 1s and %s", MaximumTimeout)
	}
	return nil
}
