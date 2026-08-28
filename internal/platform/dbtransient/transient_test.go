package dbtransient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// testNetError is a minimal real net.Error: a dial timeout against a closed
// port, the shape pgx surfaces when the database process is unreachable.
func testNetError() error {
	_, err := net.DialTimeout("tcp", "127.0.0.1:1", 50*time.Millisecond)
	if err == nil {
		panic("127.0.0.1:1 is expected to refuse connections")
	}
	return err
}

func TestIsTransientClassificationMatrix(t *testing.T) {
	t.Parallel()
	pgError := func(code string) error {
		return &pgconn.PgError{Code: code, Message: "synthetic"}
	}
	transient := map[string]error{
		// Well-formed SQLSTATE classes that mean the dependency failed.
		"08 connection exception":       pgError("08006"),
		"53 insufficient resources":     pgError("53300"),
		"57 operator intervention":      pgError("57P01"),
		"57 statement timeout":          pgError("57014"),
		"58 system error":               pgError("58030"),
		"wrapped 08 through fmt":        fmt.Errorf("read bundle asset: %w", pgError("08003")),
		"context deadline exceeded":     fmt.Errorf("query: %w", context.DeadlineExceeded),
		"real dial timeout (net.Error)": testNetError(),
		"wrapped net.OpError":           fmt.Errorf("begin: %w", testNetError()),
	}
	for name, err := range transient {
		if !IsTransient(err) {
			t.Errorf("%s: expected transient, got false (%v)", name, err)
		}
	}

	notTransient := map[string]error{
		// Nothing to classify: nil must return false, never panic.
		"nil":              nil,
		"plain error":      errors.New("pool exhausted"),
		"wrapped plain":    fmt.Errorf("insert artifact: %w", errors.New("pool exhausted")),
		"context canceled": context.Canceled,
		// Constraint violations and programming errors are server-rejected
		// facts, not outages.
		"23 integrity violation":   pgError("23505"),
		"42 syntax/privilege":      pgError("42601"),
		"00 successful completion": pgError("00000"),
		// Malformed or truncated SQLSTATE codes must fail closed: an
		// availability verdict never guesses from a malformed value.
		"empty code":    pgError(""),
		"one-char code": pgError("0"),
		"unknown class": pgError("ZZ999"),
	}
	for name, err := range notTransient {
		if IsTransient(err) {
			t.Errorf("%s: expected not transient, got true (%v)", name, err)
		}
	}
}

// TestIsTransientShortSQLSTATECodesNeverPanic pins the fail-closed contract
// for malformed protocol codes: empty and one-character SQLSTATE values —
// constructible by a broken proxy or test double — return false instead of
// panicking on the class slice.
func TestIsTransientShortSQLSTATECodesNeverPanic(t *testing.T) {
	t.Parallel()
	for _, code := range []string{"", "0"} {
		err := &pgconn.PgError{Code: code, Message: "malformed synthetic"}
		if IsTransient(fmt.Errorf("wrapped: %w", err)) {
			t.Errorf("code %q classified transient", code)
		}
		if IsTransient(err) {
			t.Errorf("code %q (direct) classified transient", code)
		}
	}
}
