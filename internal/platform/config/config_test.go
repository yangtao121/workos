package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDevBypassRejectsPublicBind(t *testing.T) {
	t.Parallel()
	cfg := defaults()
	cfg.HTTP.Address = "0.0.0.0:8080"
	cfg.Auth.DevBypass = true
	if err := cfg.ValidateGateway(); err == nil {
		t.Fatal("expected public development bypass to be rejected")
	}
}

func TestDevBypassAllowsLoopback(t *testing.T) {
	t.Parallel()
	cfg := defaults()
	cfg.Auth.DevBypass = true
	if err := cfg.ValidateGateway(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestDeepSeekDefaultsAreDisabledAndSecretFree(t *testing.T) {
	t.Parallel()
	cfg := defaults()
	if cfg.Harness.DeepSeek.Enabled || cfg.Harness.DeepSeek.APIKey != "" {
		t.Fatalf("unsafe DeepSeek defaults: %#v", cfg.Harness.DeepSeek)
	}
	if cfg.Harness.DeepSeek.Model != "deepseek-v4-flash" || cfg.Harness.DeepSeek.Timeout != 2*time.Minute {
		t.Fatalf("unexpected DeepSeek defaults: %#v", cfg.Harness.DeepSeek)
	}
}

func TestCatalogAndBindingDefaultsAreSafeAndSecretFree(t *testing.T) {
	t.Parallel()
	cfg := defaults()
	if cfg.Services.Harness != "http://127.0.0.1:8082" || cfg.Agent.CatalogTimeout != 2*time.Second || cfg.Agent.ProjectBinding.InstancePolicy != "ephemeral" || cfg.Agent.ProjectBinding.ProfileID != "" || cfg.Agent.ProjectBinding.ResourcePolicyID != "project-no-tools" {
		t.Fatalf("unexpected Catalog/binding defaults: services=%#v agent=%#v", cfg.Services, cfg.Agent)
	}
}

func TestLoadCatalogAndBindingEnvironment(t *testing.T) {
	t.Setenv("WORKOS_CONFIG_FILE", "")
	_ = os.Unsetenv("WORKOS_CONFIG_FILE")
	t.Setenv("WORKOS_HARNESS_URL", "http://harness.internal:8082")
	t.Setenv("WORKOS_AGENT_CATALOG_TIMEOUT", "3s")
	t.Setenv("WORKOS_PROJECT_HARNESS_INSTANCE_POLICY", "lazy")
	t.Setenv("WORKOS_PROJECT_HARNESS_PROFILE_ID", "general")
	t.Setenv("WORKOS_PROJECT_HARNESS_RESOURCE_POLICY_ID", "project-safe")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Services.Harness != "http://harness.internal:8082" || cfg.Agent.CatalogTimeout != 3*time.Second || cfg.Agent.ProjectBinding.InstancePolicy != "lazy" || cfg.Agent.ProjectBinding.ProfileID != "general" || cfg.Agent.ProjectBinding.ResourcePolicyID != "project-safe" {
		t.Fatalf("unexpected Catalog/binding environment mapping: services=%#v agent=%#v", cfg.Services, cfg.Agent)
	}
}

func TestCoreRejectsInvalidCatalogAndBindingConfiguration(t *testing.T) {
	t.Parallel()
	for _, mutate := range []func(*Config){
		func(cfg *Config) { cfg.Services.Harness = "relative-host" },
		func(cfg *Config) { cfg.Agent.DefaultProvider = " " },
		func(cfg *Config) { cfg.Agent.CatalogTimeout = 31 * time.Second },
		func(cfg *Config) { cfg.Agent.ProjectBinding.InstancePolicy = "magic" },
		func(cfg *Config) { cfg.Agent.ProjectBinding.ResourcePolicyID = "" },
	} {
		cfg := defaults()
		mutate(&cfg)
		if err := cfg.ValidateCore(); err == nil {
			t.Fatalf("invalid Core configuration was accepted: %#v", cfg)
		}
	}
	if err := defaults().ValidateCore(); err != nil {
		t.Fatalf("safe Core defaults were rejected: %v", err)
	}
}

func TestLoadDeepSeekEnvironment(t *testing.T) {
	for _, key := range []string{
		"WORKOS_CONFIG_FILE", "WORKOS_DEEPSEEK_ENABLED", "DEEPSEEK_API_KEY", "WORKOS_DEEPSEEK_BASE_URL",
		"WORKOS_DEEPSEEK_MODEL", "WORKOS_DEEPSEEK_TIMEOUT", "WORKOS_DEEPSEEK_RUNTIME_PATH", "WORKOS_DEEPSEEK_CORDIS_CONFIG",
	} {
		t.Setenv(key, "")
		_ = os.Unsetenv(key)
	}
	t.Setenv("WORKOS_DEEPSEEK_ENABLED", "true")
	t.Setenv("DEEPSEEK_API_KEY", "fixture-secret")
	t.Setenv("WORKOS_DEEPSEEK_BASE_URL", "https://fixture.invalid")
	t.Setenv("WORKOS_DEEPSEEK_MODEL", "deepseek-v4-pro")
	t.Setenv("WORKOS_DEEPSEEK_TIMEOUT", "45s")
	t.Setenv("WORKOS_DEEPSEEK_RUNTIME_PATH", "/opt/dsh")
	t.Setenv("WORKOS_DEEPSEEK_CORDIS_CONFIG", "/opt/cordis.yml")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Harness.DeepSeek
	if !got.Enabled || got.APIKey != "fixture-secret" || got.BaseURL != "https://fixture.invalid" || got.Model != "deepseek-v4-pro" || got.Timeout != 45*time.Second || got.RuntimePath != "/opt/dsh" || got.CordisConfigPath != "/opt/cordis.yml" || got.ConfigurationIssue != "" {
		t.Fatalf("unexpected DeepSeek environment mapping: %#v", got)
	}
}

func TestInvalidDeepSeekEnvironmentDoesNotCrashHost(t *testing.T) {
	t.Setenv("WORKOS_CONFIG_FILE", "")
	_ = os.Unsetenv("WORKOS_CONFIG_FILE")
	t.Setenv("WORKOS_DEEPSEEK_ENABLED", "sometimes")
	t.Setenv("WORKOS_DEEPSEEK_TIMEOUT", "tomorrow")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Harness.DeepSeek.ConfigurationIssue == "" {
		t.Fatal("expected invalid adapter configuration to be retained as an unavailable reason")
	}
}

func TestDeepSeekKeyIsRejectedFromYAML(t *testing.T) {
	path := t.TempDir() + "/workos.yml"
	if err := os.WriteFile(path, []byte("harness:\n  deepseek:\n    enabled: true\n    api_key: must-not-live-here\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKOS_CONFIG_FILE", path)
	if _, err := Load(); err == nil {
		t.Fatal("expected YAML credential field to be rejected")
	}
}

func TestSurfaceSessionTTLEnvironment(t *testing.T) {
	t.Setenv("WORKOS_SURFACE_SESSION_TTL", "45m")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Surface.SessionTTL != 45*time.Minute {
		t.Fatalf("ttl not applied: %v", cfg.Surface.SessionTTL)
	}
	t.Setenv("WORKOS_SURFACE_SESSION_TTL", "-5m")
	if _, err := Load(); err == nil {
		t.Fatal("negative TTL accepted")
	}
	t.Setenv("WORKOS_SURFACE_SESSION_TTL", "garbage")
	if _, err := Load(); err == nil {
		t.Fatal("malformed TTL accepted")
	}
}

func TestValidateRuntimeHost(t *testing.T) {
	cfg := defaults()
	if err := cfg.ValidateRuntimeHost(); err != nil {
		t.Fatalf("defaults rejected: %v", err)
	}
	cfg.Services.Core = "ftp://core"
	if err := cfg.ValidateRuntimeHost(); err == nil {
		t.Fatal("non-http core URL accepted")
	}
	cfg = defaults()
	cfg.Surface.SessionTTL = 30 * time.Second
	if err := cfg.ValidateRuntimeHost(); err == nil {
		t.Fatal("undersized TTL accepted")
	}
	cfg.Surface.SessionTTL = 25 * time.Hour
	if err := cfg.ValidateRuntimeHost(); err == nil {
		t.Fatal("oversized TTL accepted")
	}
}

func TestValidateGatewayRequiresRuntimeURL(t *testing.T) {
	cfg := defaults()
	cfg.HTTP.Address = "127.0.0.1:8080"
	cfg.Auth.DevBypass = true
	cfg.Services.Core = "http://127.0.0.1:8081"
	cfg.Services.Runtime = "::not-a-url::"
	if err := cfg.ValidateGateway(); err == nil {
		t.Fatal("malformed runtime URL accepted")
	}
	cfg.Services.Runtime = "http://127.0.0.1:8083"
	if err := cfg.ValidateGateway(); err != nil {
		t.Fatalf("valid runtime URL rejected: %v", err)
	}
}

// TestValidateGatewayRejectsUnusableUpstreams pins the startup fail-fast
// contract: both upstreams must be absolute http(s) URLs with a host, so a
// bad value can never survive startup and fail lazily on first request.
func TestValidateGatewayRejectsUnusableUpstreams(t *testing.T) {
	t.Parallel()
	invalid := []string{
		"",
		"surfaces/local-relative",
		"127.0.0.1:8083",
		"ftp://127.0.0.1:8083",
		"http://",
		"https://",
		"//127.0.0.1:8083",
	}
	for _, target := range invalid {
		cfg := defaults()
		cfg.Auth.DevBypass = true
		cfg.Services.Runtime = target
		if err := cfg.ValidateGateway(); err == nil {
			t.Errorf("invalid runtime URL %q accepted", target)
		}
		cfg = defaults()
		cfg.Auth.DevBypass = true
		cfg.Services.Core = target
		if err := cfg.ValidateGateway(); err == nil {
			t.Errorf("invalid core URL %q accepted", target)
		}
	}
	for _, target := range []string{"http://127.0.0.1:8083", "https://runtime.internal:8443"} {
		cfg := defaults()
		cfg.Auth.DevBypass = true
		cfg.Services.Runtime = target
		if err := cfg.ValidateGateway(); err != nil {
			t.Errorf("valid runtime URL %q rejected: %v", target, err)
		}
	}
	cfg := defaults()
	cfg.Auth.DevBypass = true
	if err := cfg.ValidateGateway(); err != nil {
		t.Fatalf("safe dev-bypass defaults rejected: %v", err)
	}
}

// TestValidateGatewayProductionRequiresFullAuthDeployment pins the fail-
// closed production grammar: without the development bypass the Gateway
// must terminate TLS, publish a canonical https origin, own a canonical
// UUIDv7 owner, a postgres URL, and a secure admin socket path — no matter
// whether the bind address is loopback.
func TestValidateGatewayProductionRequiresFullAuthDeployment(t *testing.T) {
	t.Parallel()
	socketRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(socketRoot, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	base := func() Config {
		cfg := defaults()
		cfg.HTTP.Address = "127.0.0.1:8443"
		cfg.HTTP.TLSCertFile = "/tmp/gateway.crt"
		cfg.HTTP.TLSKeyFile = "/tmp/gateway.key"
		cfg.Auth.PublicOrigin = "https://workos.example"
		cfg.Auth.AdminSocketPath = filepath.Join(socketRoot, "run", "gateway-admin.sock")
		return cfg
	}
	// The default owner is a canonical UUIDv7; the production base passes.
	cfg := base()
	if err := cfg.ValidateGateway(); err != nil {
		t.Fatalf("valid production config rejected: %v", err)
	}
	for name, mutate := range map[string]func(Config) Config{
		"missing TLS certificate": func(c Config) Config { c.HTTP.TLSCertFile = ""; return c },
		"missing TLS key":         func(c Config) Config { c.HTTP.TLSKeyFile = ""; return c },
		"empty origin":            func(c Config) Config { c.Auth.PublicOrigin = ""; return c },
		"http origin":             func(c Config) Config { c.Auth.PublicOrigin = "http://workos.example"; return c },
		"origin with path":        func(c Config) Config { c.Auth.PublicOrigin = "https://workos.example/app/"; return c },
		"origin with query":       func(c Config) Config { c.Auth.PublicOrigin = "https://workos.example/?x=1"; return c },
		"origin with fragment":    func(c Config) Config { c.Auth.PublicOrigin = "https://workos.example/#frag"; return c },
		"origin with userinfo":    func(c Config) Config { c.Auth.PublicOrigin = "https://user@workos.example"; return c },
		"owner not uuid":          func(c Config) Config { c.Auth.OwnerID = "owner-1"; return c },
		"owner not v7":            func(c Config) Config { c.Auth.OwnerID = "0198d7ea-2110-6c42-b659-c5e4d73bc337"; return c },
		"owner not canonical":     func(c Config) Config { c.Auth.OwnerID = "0198D7EA-2110-7C42-B659-C5E4D73BC337"; return c },
		"database not postgres":   func(c Config) Config { c.DatabaseURL = "mysql://db"; return c },
		"admin socket relative":   func(c Config) Config { c.Auth.AdminSocketPath = "run/workos.sock"; return c },
		"admin socket dirty":      func(c Config) Config { c.Auth.AdminSocketPath = "/run/workos/../workos/gateway-admin.sock"; return c },
		"oversized ticket ttl":    func(c Config) Config { c.Auth.TicketTTL = 16 * time.Minute; return c },
		"undersized ticket ttl":   func(c Config) Config { c.Auth.TicketTTL = 30 * time.Second; return c },
		"oversized challenge ttl": func(c Config) Config { c.Auth.ChallengeTTL = 6 * time.Minute; return c },
		"oversized session ttl":   func(c Config) Config { c.Auth.SessionTTL = 31 * 24 * time.Hour; return c },
	} {
		if err := mutate(base()).ValidateGateway(); err == nil {
			t.Errorf("invalid production config accepted: %s", name)
		}
	}
	// The dev bypass stays loopback-only even alongside production values.
	cfg = base()
	cfg.Auth.DevBypass = true
	cfg.HTTP.Address = "0.0.0.0:8080"
	if err := cfg.ValidateGateway(); err == nil {
		t.Fatal("dev bypass accepted a non-loopback bind")
	}
}

// TestValidateGatewayAdminSocketRejectsUnsafePaths pins the socket path
// grammar against the real filesystem: missing parents, symlinked parents,
// regular files, and directories all fail startup.
func TestValidateGatewayAdminSocketRejectsUnsafePaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	base := func() Config {
		cfg := defaults()
		cfg.HTTP.TLSCertFile = "/tmp/gateway.crt"
		cfg.HTTP.TLSKeyFile = "/tmp/gateway.key"
		cfg.Auth.PublicOrigin = "https://workos.example"
		return cfg
	}
	cfg := base()
	cfg.Auth.AdminSocketPath = filepath.Join(root, "missing", "gateway-admin.sock")
	if err := cfg.ValidateGateway(); err == nil {
		t.Fatal("admin socket under a missing parent accepted")
	}
	if err := os.Symlink(root, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	cfg = base()
	cfg.Auth.AdminSocketPath = filepath.Join(root, "link", "gateway-admin.sock")
	if err := cfg.ValidateGateway(); err == nil {
		t.Fatal("admin socket through a symlinked parent accepted")
	}
	regular := filepath.Join(root, "regular.txt")
	if err := os.WriteFile(regular, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg = base()
	cfg.Auth.AdminSocketPath = regular
	if err := cfg.ValidateGateway(); err == nil {
		t.Fatal("admin socket over a regular file accepted")
	}
	directory := filepath.Join(root, "dir")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg = base()
	cfg.Auth.AdminSocketPath = directory
	if err := cfg.ValidateGateway(); err == nil {
		t.Fatal("admin socket over a directory accepted")
	}
	if err := os.Mkdir(filepath.Join(root, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg = base()
	cfg.Auth.AdminSocketPath = filepath.Join(root, "run", "gateway-admin.sock")
	if err := cfg.ValidateGateway(); err != nil {
		t.Fatalf("fresh socket path in a real directory rejected: %v", err)
	}
}

func TestRuntimeURLEnvironment(t *testing.T) {
	t.Setenv("WORKOS_RUNTIME_URL", "http://127.0.0.1:9099")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Services.Runtime != "http://127.0.0.1:9099" {
		t.Fatalf("runtime URL not applied: %v", cfg.Services.Runtime)
	}
}
