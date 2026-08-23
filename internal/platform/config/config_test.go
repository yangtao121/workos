package config

import "testing"

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
