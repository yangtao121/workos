package config

import (
	"os"
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
		cfg.Services.Runtime = target
		if err := cfg.ValidateGateway(); err == nil {
			t.Errorf("invalid runtime URL %q accepted", target)
		}
		cfg = defaults()
		cfg.Services.Core = target
		if err := cfg.ValidateGateway(); err == nil {
			t.Errorf("invalid core URL %q accepted", target)
		}
	}
	for _, target := range []string{"http://127.0.0.1:8083", "https://runtime.internal:8443"} {
		cfg := defaults()
		cfg.Services.Runtime = target
		if err := cfg.ValidateGateway(); err != nil {
			t.Errorf("valid runtime URL %q rejected: %v", target, err)
		}
	}
	if err := defaults().ValidateGateway(); err != nil {
		t.Fatalf("safe defaults rejected: %v", err)
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
