package domain

import (
	"strings"
	"testing"
	"time"
)

func TestValidConsumerID(t *testing.T) {
	for _, valid := range []string{"deepseek", "generic-cli", "a", "provider.v2_x"} {
		if !ValidConsumerID(valid) {
			t.Fatalf("consumer %q rejected", valid)
		}
	}
	for _, invalid := range []string{"", "DeepSeek", "with space", "ünicode", string(make([]byte, 129))} {
		if ValidConsumerID(invalid) {
			t.Fatalf("consumer %q accepted", invalid)
		}
	}
}

func TestValidCredentialStoredMetadata(t *testing.T) {
	now := CanonicalUTCTime(time.Now())
	valid := Credential{
		ID: "0198d7ea-2110-7c42-b659-c5e4d73bc337", OwnerUserID: "0198d7ea-2110-7c42-b659-c5e4d73bc338",
		ConsumerID: "deepseek", Purpose: PurposeProviderAPIKeyV1, Label: "production",
		Revision: 1, Status: StatusActive, CreatedAt: now, UpdatedAt: now,
	}
	if !ValidCredential(valid) {
		t.Fatal("honest stored credential rejected")
	}
	for name, mutate := range map[string]func(*Credential){
		"foreign-shaped owner": func(c *Credential) { c.OwnerUserID = "not-an-owner" },
		"unknown status":       func(c *Credential) { c.Status = "pending" },
		"noncanonical time":    func(c *Credential) { c.UpdatedAt = c.UpdatedAt.Add(time.Nanosecond) },
		"time reversal":        func(c *Credential) { c.UpdatedAt = c.CreatedAt.Add(-time.Microsecond) },
	} {
		candidate := valid
		mutate(&candidate)
		if ValidCredential(candidate) {
			t.Fatalf("%s was accepted", name)
		}
	}
}

func TestValidSecret(t *testing.T) {
	maximum := make([]byte, MaxSecretBytes)
	for index := range maximum {
		maximum[index] = 'x'
	}
	if !ValidSecret([]byte("k")) || !ValidSecret(maximum) {
		t.Fatal("bounded secrets rejected")
	}
	oversize := make([]byte, MaxSecretBytes+1)
	for index := range oversize {
		oversize[index] = 'x'
	}
	if ValidSecret(nil) || ValidSecret(oversize) {
		t.Fatal("unbounded secrets accepted")
	}
	if ValidSecret([]byte("with\nnewline")) || ValidSecret([]byte("with\rcarriage")) || ValidSecret([]byte("with\x00nul")) {
		t.Fatal("control-bearing secret accepted")
	}
}

func TestValidLabelAndIdempotencyKey(t *testing.T) {
	if !ValidLabel("") || !ValidLabel("production key") {
		t.Fatal("honest labels rejected")
	}
	if ValidLabel(string([]rune{'a', 0x00})) {
		t.Fatal("NUL-bearing label accepted")
	}
	long := make([]rune, 81)
	for index := range long {
		long[index] = 'a'
	}
	_ = long
	if ValidLabel(string(long)) {
		t.Fatal("81-code-point label accepted")
	}
	if !ValidIdempotencyKey("0198d7ea-2110-7c42-b659-c5e4d73bc337") {
		t.Fatal("honest key rejected")
	}
	if ValidIdempotencyKey("") || ValidIdempotencyKey(strings.Repeat("a", 129)) {
		t.Fatal("unbounded keys accepted")
	}
}

func TestValidWorkerID(t *testing.T) {
	for _, valid := range []string{"harness-host-local", "worker.1", "worker_二"} {
		if !ValidWorkerID(valid) {
			t.Fatalf("worker %q rejected", valid)
		}
	}
	for _, invalid := range []string{"", " leading", "trailing ", "line\nbreak", strings.Repeat("w", 129)} {
		if ValidWorkerID(invalid) {
			t.Fatalf("worker %q accepted", invalid)
		}
	}
}

func TestValidCredentialIDUUIDv7(t *testing.T) {
	if !ValidCredentialID("0198d7ea-2110-7c42-b659-c5e4d73bc337") {
		t.Fatal("canonical UUIDv7 rejected")
	}
	for _, invalid := range []string{
		"", "not-a-uuid", "0198D7EA-2110-7C42-B659-C5E4D73BC337",
		"0198d7ea-2110-1c42-b659-c5e4d73bc337", // version 1, not 7
		"0198d7ea21107c42b659c5e4d73bc337",     // unhyphenated
	} {
		if ValidCredentialID(invalid) {
			t.Fatalf("invalid credential id %q accepted", invalid)
		}
	}
}
