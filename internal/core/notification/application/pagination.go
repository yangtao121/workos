// Pagination token codec for owner notification lists. The token is a
// keyset boundary (created_at, id) bound to the owner and a canonical
// filter fingerprint, so a token minted under one filter can never silently
// page another filter and there is never a bare offset (ADR-0014).
package application

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/yangtao121/workos/internal/core/notification/domain"
	"github.com/yangtao121/workos/internal/core/notification/ports"
)

type pageToken struct {
	Version     int    `json:"v"`
	CreatedUs   int64  `json:"t"`
	ID          string `json:"i"`
	Owner       string `json:"o"`
	Fingerprint string `json:"f"`
	Watermark   int64  `json:"w"`
}

func encodePageToken(cursor ports.Cursor, owner string, filter ports.Filter, watermark int64) (string, error) {
	if cursor.CreatedAt.IsZero() || cursor.ID == "" || watermark < 0 {
		return "", ErrInvalid
	}
	blob, err := json.Marshal(pageToken{
		Version: 2, CreatedUs: cursor.CreatedAt.UnixMicro(), ID: cursor.ID,
		Owner: owner, Fingerprint: filterFingerprint(filter), Watermark: watermark,
	})
	if err != nil {
		return "", fmt.Errorf("encode notification page token: %w", domain.ErrInvalid)
	}
	return base64.RawURLEncoding.EncodeToString(blob), nil
}

func decodePageToken(token, owner string, filter ports.Filter) (ports.Cursor, int64, error) {
	if token == "" {
		return ports.Cursor{}, 0, nil
	}
	blob, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return ports.Cursor{}, 0, ErrInvalid
	}
	var decoded pageToken
	if err := json.Unmarshal(blob, &decoded); err != nil {
		return ports.Cursor{}, 0, ErrInvalid
	}
	if decoded.Version != 2 || decoded.Owner != owner || decoded.Fingerprint != filterFingerprint(filter) {
		return ports.Cursor{}, 0, ErrInvalid
	}
	if !domain.ValidUUID(decoded.ID) || decoded.CreatedUs <= 0 || decoded.Watermark < 0 {
		return ports.Cursor{}, 0, ErrInvalid
	}
	created := time.UnixMicro(decoded.CreatedUs).UTC()
	return ports.Cursor{CreatedAt: domain.CanonicalUTCTime(created), ID: decoded.ID}, decoded.Watermark, nil
}

// filterFingerprint is the canonical fingerprint of the list filter; it
// binds tokens to the exact filter snapshot they were minted under.
func filterFingerprint(filter ports.Filter) string {
	kind := strconv.Itoa(len(filter.Kind))
	if filter.Kind != "" {
		kind = filter.Kind
	}
	canonical := strings.Join([]string{
		"workos.notification-list.v1", filter.ProjectID,
		strconv.FormatBool(filter.UnreadOnly), kind,
	}, "|")
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:8])
}

func sha256Sum(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
