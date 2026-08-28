// Package dbtransient classifies PostgreSQL driver failures that represent a
// temporarily unreachable or overloaded dependency, as opposed to a violated
// invariant. Adapters call it at the port boundary to wrap such errors with
// their module's store-unavailable sentinel; transports map that sentinel to
// a sanitized Unavailable. Classification inspects Go error types and
// PostgreSQL error classes only — never constraint names or message text —
// so business verdicts never depend on wording.
package dbtransient

import (
	"context"
	"errors"
	"net"

	"github.com/jackc/pgx/v5/pgconn"
)

// IsTransient reports whether err is a connection, network, cancellation-
// timeout, or server resource failure: the caller's retry later may succeed.
// Ordinary server-rejected SQL (constraint violations, malformed queries)
// and driver programming errors are not transient.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	// A request that timed out on the caller side still points at a
	// dependency that did not answer in time.
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	// Dial and authentication-phase failures surface as ConnectError.
	var connectErr *pgconn.ConnectError
	if errors.As(err, &connectErr) {
		return true
	}
	// Established-connection failures (reset, broken pipe, keepalive
	// timeouts) surface through the net error tree.
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	// Server-reported failures with a protocol error code: classify by the
	// SQLSTATE class only — connection exceptions (08), insufficient
	// resources (53), operator intervention (57: admin shutdown, crash
	// recovery, statement timeout), and system I/O errors (58). Malformed or
	// truncated codes (empty, shorter than the class prefix) are never
	// transient: an availability verdict must never depend on guessing from
	// a malformed value.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if len(pgErr.Code) < 2 {
			return false
		}
		switch pgErr.Code[:2] {
		case "08", "53", "57", "58":
			return true
		}
	}
	return false
}
