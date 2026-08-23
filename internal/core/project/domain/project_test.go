package domain

import (
	"errors"
	"testing"
)

func TestNormalizeName(t *testing.T) {
	t.Parallel()
	if value, err := NormalizeName("  Mission Control  "); err != nil || value != "Mission Control" {
		t.Fatalf("unexpected normalized name %q: %v", value, err)
	}
	if _, err := NormalizeName("   "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected blank name to fail, got %v", err)
	}
}

func TestHarnessBindingRequiresExplicitPolicy(t *testing.T) {
	t.Parallel()
	valid := &HarnessBinding{ProviderID: "fake", InstancePolicy: "ephemeral", ResourcePolicyID: "foundation"}
	if err := ValidateBinding(valid); err != nil {
		t.Fatal(err)
	}
	invalid := *valid
	invalid.InstancePolicy = "magic"
	if err := ValidateBinding(&invalid); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected unknown instance policy to fail, got %v", err)
	}
}
