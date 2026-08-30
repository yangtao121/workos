package domain

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestParseRevocationSnapshotRejectsEveryCorruptStoredFact(t *testing.T) {
	t.Parallel()
	valid := RevocationSnapshot{
		ResultVersion: "v1", DeviceID: "0198d7ea-2110-7c42-b659-c5e4d73bc301",
		Name: "Revoked phone", Class: "phone", Revision: 2,
		CreatedAt: "2026-08-30T10:00:00Z", LastAuthenticatedAt: "2026-08-30T11:00:00Z",
		RevokedAt: "2026-08-30T12:00:00Z",
	}
	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseRevocationSnapshot(encoded); err != nil {
		t.Fatalf("valid snapshot rejected: %v", err)
	}
	for name, raw := range map[string]json.RawMessage{
		"unknown field":         append(encoded[:len(encoded)-1], []byte(`,"extra":true}`)...),
		"non-v7 id":             []byte(`{"result_version":"v1","device_id":"0198d7ea-2110-6c42-b659-c5e4d73bc301","name":"D","device_class":"desktop","revision":2,"created_at":"2026-08-30T10:00:00Z","last_authenticated_at":"2026-08-30T11:00:00Z","revoked_at":"2026-08-30T12:00:00Z"}`),
		"bad class":             []byte(`{"result_version":"v1","device_id":"0198d7ea-2110-7c42-b659-c5e4d73bc301","name":"D","device_class":"watch","revision":2,"created_at":"2026-08-30T10:00:00Z","last_authenticated_at":"2026-08-30T11:00:00Z","revoked_at":"2026-08-30T12:00:00Z"}`),
		"bad revision":          []byte(`{"result_version":"v1","device_id":"0198d7ea-2110-7c42-b659-c5e4d73bc301","name":"D","device_class":"desktop","revision":1,"created_at":"2026-08-30T10:00:00Z","last_authenticated_at":"2026-08-30T11:00:00Z","revoked_at":"2026-08-30T12:00:00Z"}`),
		"missing created at":    []byte(`{"result_version":"v1","device_id":"0198d7ea-2110-7c42-b659-c5e4d73bc301","name":"D","device_class":"desktop","revision":2,"last_authenticated_at":"2026-08-30T11:00:00Z","revoked_at":"2026-08-30T12:00:00Z"}`),
		"non-UTC timestamp":     []byte(`{"result_version":"v1","device_id":"0198d7ea-2110-7c42-b659-c5e4d73bc301","name":"D","device_class":"desktop","revision":2,"created_at":"2026-08-30T11:00:00+01:00","last_authenticated_at":"2026-08-30T11:00:00Z","revoked_at":"2026-08-30T12:00:00Z"}`),
		"time order":            []byte(`{"result_version":"v1","device_id":"0198d7ea-2110-7c42-b659-c5e4d73bc301","name":"D","device_class":"desktop","revision":2,"created_at":"2026-08-30T10:00:00Z","last_authenticated_at":"2026-08-30T13:00:00Z","revoked_at":"2026-08-30T12:00:00Z"}`),
		"bad revoked timestamp": []byte(`{"result_version":"v1","device_id":"0198d7ea-2110-7c42-b659-c5e4d73bc301","name":"D","device_class":"desktop","revision":2,"created_at":"2026-08-30T10:00:00Z","last_authenticated_at":"2026-08-30T11:00:00Z","revoked_at":"yesterday"}`),
	} {
		if _, err := ParseRevocationSnapshot(raw); !errors.Is(err, ErrAuthCorrupt) {
			t.Errorf("%s verdict = %v", name, err)
		}
	}
}

func TestValidateDeviceNameRejectsInvalidUTF8(t *testing.T) {
	t.Parallel()
	if _, err := ValidateDeviceName(string([]byte{0xff})); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid UTF-8 verdict = %v", err)
	}
}
