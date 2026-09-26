package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestOwnerPasswordSetupGrammar(t *testing.T) {
	if _, err := NormalizeLoginUsername("  owner  "); err != nil {
		t.Fatal("ordinary username rejected")
	}
	for _, candidate := range []string{"", "  ", "owner\x00name", strings.Repeat("u", 81), string([]byte{0xff})} {
		if _, err := NormalizeLoginUsername(candidate); !errors.Is(err, ErrInvalidRequest) {
			t.Fatal("invalid username accepted")
		}
	}
	for _, candidate := range []string{"", strings.Repeat("x", 11), strings.Repeat("x", 1025), string([]byte{0xff})} {
		if err := ValidateOwnerPassword(candidate); !errors.Is(err, ErrInvalidRequest) {
			t.Fatal("invalid password accepted")
		}
	}
	for _, candidate := range []string{strings.Repeat("x", 12), strings.Repeat("x", 1024), strings.Repeat("界", 4)} {
		if err := ValidateOwnerPassword(candidate); err != nil {
			t.Fatal("valid password boundary rejected")
		}
	}
}
