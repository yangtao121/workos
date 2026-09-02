// Reconciliation paging facts for the index feed (ADR-0013): a stable
// (created_at, id) ordered walk over this module's immutable review
// artifacts, exposing only identity facts — never content bytes.
package domain

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

// ReconcileSource is one authoritative review artifact identity fact.
type ReconcileSource struct {
	OwnerUserID  string
	ProjectID    string
	ArtifactID   string
	ArtifactType string
	Digest       string
	CreatedAt    time.Time
}

// ReconcileCursorVersion prefixes the opaque continuation token so a future
// ordering change can never decode an old token into a wrong boundary.
const ReconcileCursorVersion = "v1"

// ErrReconcileCursor marks a malformed reconciliation cursor. It is an
// invalid-input verdict, never a silent restart from the beginning.
var ErrReconcileCursor = errors.New("reconciliation cursor is invalid")

// EncodeReconcileCursor renders the versioned continuation token.
func EncodeReconcileCursor(at time.Time, id string) string {
	return ReconcileCursorVersion + ":" + strconv.FormatInt(at.UnixMicro(), 10) + ":" + id
}

// DecodeReconcileCursor parses a versioned continuation token. Zero time and
// empty id mean "first page".
func DecodeReconcileCursor(value string) (time.Time, string, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 3 || parts[0] != ReconcileCursorVersion {
		return time.Time{}, "", ErrReconcileCursor
	}
	micros, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || micros < 0 {
		return time.Time{}, "", ErrReconcileCursor
	}
	id := parts[2]
	if id != "" && !ValidArtifactUUID(id) {
		return time.Time{}, "", ErrReconcileCursor
	}
	return time.UnixMicro(micros).UTC(), id, nil
}
