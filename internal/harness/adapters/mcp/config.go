package mcp

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	ProviderID     = "mcp"
	AdapterVersion = "1.0.0"

	// protocolVersion pins the MCP-shaped stdio contract (ADR-0015). The
	// server rejects any other version during initialize.
	protocolVersion = "workos.mcp-server/v1"

	DefaultTimeout = 2 * time.Minute
	MaximumTimeout = 10 * time.Minute

	// taskToolName is the single fixture tool the adapter consumes; a
	// server without it stays honestly unavailable per run.
	taskToolName = "task"

	maximumGoalBytes = 1 << 20
)

// Config is the process-local adapter configuration. MCP servers run locally
// from an operator-provisioned absolute path and receive no credential
// material: the adapter declares no lease requirement (ADR-0015).
type Config struct {
	Enabled   bool
	Server    string
	Timeout   time.Duration
	Arguments []string

	// fixtureMode and fixtureEnv exist solely for package tests; production
	// starts the server with no caller-controlled extra environment.
	fixtureMode string
	fixtureEnv  []string
}

func normalizeConfig(config Config) Config {
	if config.Timeout == 0 {
		config.Timeout = DefaultTimeout
	}
	return config
}

func validateConfig(config Config) error {
	if !config.Enabled {
		return errors.New("mcp harness adapter is not enabled")
	}
	if config.Server == "" || !filepath.IsAbs(config.Server) || filepath.Clean(config.Server) != config.Server {
		return errors.New("mcp server must be an absolute allowlisted path")
	}
	if info, err := os.Stat(config.Server); err != nil || info.IsDir() {
		return errors.New("mcp server is unavailable")
	}
	for _, argument := range config.Arguments {
		if argument == "" || len(argument) > 4096 || strings.ContainsAny(argument, "\x00\r\n") {
			return errors.New("mcp server arguments must be bounded control-free strings")
		}
	}
	if config.Timeout < time.Second || config.Timeout > MaximumTimeout {
		return fmt.Errorf("mcp timeout must be between 1s and %s", MaximumTimeout)
	}
	return nil
}
