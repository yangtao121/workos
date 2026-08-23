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
