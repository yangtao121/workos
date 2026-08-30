package postgres

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/gateway/auth/adapters/postgres/gatewayauthdb"
	"github.com/yangtao121/workos/internal/gateway/auth/domain"
)

func TestStoredDeviceRejectsCorruptSPKIAsInternal(t *testing.T) {
	t.Parallel()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	spki, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	_, hash, err := domain.ParseP256SPKI(spki)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	row := gatewayauthdb.WorkosGatewayDeviceCredential{
		ID: "0198d7ea-2110-7c42-b659-c5e4d73bc301", OwnerUserID: "0198d7ea-2110-7c42-b659-c5e4d73bc302",
		Name: "Stored device", DeviceClass: "desktop", PublicKeySpki: spki, PublicKeyHash: hash,
		Revision: 1, CreatedAt: now, LastAuthenticatedAt: now,
	}
	if _, err := deviceFromRow(row); err != nil {
		t.Fatalf("valid device rejected: %v", err)
	}
	row.PublicKeySpki = []byte{1, 2, 3}
	_, err = deviceFromRow(row)
	if !errors.Is(err, domain.ErrAuthCorrupt) || errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("corrupt stored SPKI verdict = %v", err)
	}
}

func TestStoredTicketRejectsIncoherentState(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	claimed := created.Add(time.Second)
	row := gatewayauthdb.WorkosGatewayPairingTicket{
		ID: "0198d7ea-2110-7c42-b659-c5e4d73bc301", OwnerUserID: "0198d7ea-2110-7c42-b659-c5e4d73bc302",
		SecretHash: `sha256:` + repeatHex("aa", 32), PublicOrigin: "https://workos.example",
		TlsFingerprint: `sha256:` + repeatHex("bb", 32), State: "claimed",
		DeviceID:      uuidOrNil("0198d7ea-2110-7c42-b659-c5e4d73bc303"),
		PublicKeyHash: textOrNil(`sha256:` + repeatHex("cc", 32)), ClaimedName: textOrNil("Claimed device"),
		ClaimedClass: textOrNil("phone"), ExpiresAt: created.Add(5 * time.Minute), CreatedAt: created,
		ClaimedAt: &claimed,
	}
	if _, err := ticketFromRow(row); err != nil {
		t.Fatalf("valid claimed ticket rejected: %v", err)
	}
	row.ClaimedAt = nil
	if _, err := ticketFromRow(row); !errors.Is(err, domain.ErrAuthCorrupt) {
		t.Fatalf("incoherent stored ticket verdict = %v", err)
	}
}

func repeatHex(value string, count int) string {
	out := ""
	for range count {
		out += value
	}
	return out
}
