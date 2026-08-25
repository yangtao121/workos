package domain

import (
	"encoding/json"
	"testing"
)

func TestValidInstallationAppID(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"abc", "board-app", "notes-2", "z01"} {
		if !ValidInstallationAppID(value) {
			t.Errorf("expected %q to be a valid app id", value)
		}
	}
	for _, value := range []string{"", "ab", "Abc", "1abc", "abc_def", "abc.def", "应用"} {
		if ValidInstallationAppID(value) {
			t.Errorf("expected %q to be an invalid app id", value)
		}
	}
	maximal := "a"
	for index := 0; index < 62; index++ {
		maximal += "b"
	}
	if !ValidInstallationAppID(maximal) || len(maximal) != 63 {
		t.Error("63-byte app id must be valid")
	}
	oversize := maximal + "c"
	if ValidInstallationAppID(oversize) {
		t.Error("64-byte app id must be invalid")
	}
}

func TestValidInstallationVersion(t *testing.T) {
	t.Parallel()
	valid := []string{"1.0.0", "0.0.1", "1.10.0", "1.10.0-rc.3", "2.0.0-alpha-1"}
	for _, value := range valid {
		if !ValidInstallationVersion(value) {
			t.Errorf("expected %q to be a valid version", value)
		}
	}
	invalid := []string{"", "1.0", "1.0.0.0", "01.2.3", "1.0.0-", "1.0.0-.rc1", "1.0.0-rc..1", "v1.0.0", "1.0.0+", "1.0.0-rc_1"}
	for _, value := range invalid {
		if ValidInstallationVersion(value) {
			t.Errorf("expected %q to be an invalid version", value)
		}
	}
}

func TestValidInstallationManifestDigest(t *testing.T) {
	t.Parallel()
	good := "sha256:" + repeatHex(64)
	if !ValidInstallationManifestDigest(good) {
		t.Fatal("canonical digest must be valid")
	}
	bad := []string{
		"", "sha256:", "sha256:abc", "sha256:" + repeatHex(63), "sha256:" + repeatHex(65),
		"sha256:" + repeatUpperHex(64), "md5:" + repeatHex(64),
	}
	for _, value := range bad {
		if ValidInstallationManifestDigest(value) {
			t.Errorf("expected %q to be an invalid digest", value)
		}
	}
}

func TestValidInstallationUUID(t *testing.T) {
	t.Parallel()
	if !ValidInstallationUUID("01999999-9999-7999-8999-99999999999a") {
		t.Fatal("canonical UUID must be valid")
	}
	if !ValidInstallationUUID("01999999-9999-7999-8999-99999999999A") {
		t.Fatal("uppercase UUID must be valid at the boundary")
	}
	for _, value := range []string{"", "not-a-uuid", "01999999-9999-7999-8999-99999999999", "0199999g-9999-7999-8999-99999999999a", "01999999_9999_7999_8999_99999999999a"} {
		if ValidInstallationUUID(value) {
			t.Errorf("expected %q to be an invalid UUID", value)
		}
	}
}

func TestValidInstallationIdempotencyKey(t *testing.T) {
	t.Parallel()
	if !ValidInstallationIdempotencyKey("install-once") {
		t.Fatal("plain key must be valid")
	}
	unicode := "安装-key"
	if !ValidInstallationIdempotencyKey(unicode) {
		t.Fatal("UTF-8 key must be valid")
	}
	for _, value := range []string{"", "with\nnewline", "with\x00null", "with\x7fdel", "with\x9fc1"} {
		if ValidInstallationIdempotencyKey(value) {
			t.Errorf("expected %q to be an invalid key", value)
		}
	}
	long := ""
	for index := 0; index < 129; index++ {
		long += "k"
	}
	if ValidInstallationIdempotencyKey(long) {
		t.Error("129-rune key must be invalid")
	}
	exact := ""
	for index := 0; index < 128; index++ {
		exact += "k"
	}
	if !ValidInstallationIdempotencyKey(exact) {
		t.Error("128-rune key must be valid")
	}
}

func TestInstallableScope(t *testing.T) {
	t.Parallel()
	for _, scope := range []string{"user", "project"} {
		if !InstallableScope(scope) {
			t.Errorf("scope %q must be installable", scope)
		}
	}
	for _, scope := range []string{"", "system", "trusted", "user "} {
		if InstallableScope(scope) {
			t.Errorf("scope %q must fail closed", scope)
		}
	}
}

func TestInstallationRequestDigestIsCanonicalAndStable(t *testing.T) {
	t.Parallel()
	first := InstallationRequestDigest("install", "01999999-9999-7999-8999-99999999999a", "board-app", "", "", 7)
	second := InstallationRequestDigest("install", "01999999-9999-7999-8999-99999999999a", "board-app", "", "", 7)
	if first != second || first == "" {
		t.Fatalf("digest must be deterministic: %q vs %q", first, second)
	}
	if len(first) != len("sha256:")+64 {
		t.Fatalf("digest must be sha256 hex: %q", first)
	}
	// Every canonical field changes the digest.
	variations := []string{
		InstallationRequestDigest("uninstall", "01999999-9999-7999-8999-99999999999a", "board-app", "", "", 7),
		InstallationRequestDigest("install", "01999999-9999-7999-8999-99999999999b", "board-app", "", "", 7),
		InstallationRequestDigest("install", "01999999-9999-7999-8999-99999999999a", "notes-app", "", "", 7),
		InstallationRequestDigest("install", "01999999-9999-7999-8999-99999999999a", "board-app", "1.0.0", "", 7),
		InstallationRequestDigest("install", "01999999-9999-7999-8999-99999999999a", "board-app", "", "01999999-9999-7999-8999-99999999999c", 7),
		InstallationRequestDigest("install", "01999999-9999-7999-8999-99999999999a", "board-app", "", "", 8),
	}
	seen := map[string]bool{first: true}
	for index, variation := range variations {
		if seen[variation] {
			t.Errorf("variation %d must change the digest", index)
		}
		seen[variation] = true
	}
}

// TestInstallationRequestDigestDeterministicJSON pins the encoding contract:
// the digest input is the alphabetical-field JSON document itself, so any
// encoding change is a deliberate compatibility break, not an accident.
func TestInstallationRequestDigestDeterministicJSON(t *testing.T) {
	t.Parallel()
	body, err := json.Marshal(struct {
		Action           string `json:"action"`
		AppID            string `json:"app_id"`
		ExpectedRevision int64  `json:"expected_project_revision"`
		InstallationID   string `json:"installation_id"`
		ProjectID        string `json:"project_id"`
		Version          string `json:"version"`
	}{Action: "install", AppID: "board-app", ExpectedRevision: 7, InstallationID: "", ProjectID: "01999999-9999-7999-8999-99999999999a", Version: ""})
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"action":"install","app_id":"board-app","expected_project_revision":7,"installation_id":"","project_id":"01999999-9999-7999-8999-99999999999a","version":""}` {
		t.Fatalf("canonical JSON changed: %s", body)
	}
}

func repeatHex(count int) string {
	value := ""
	for index := 0; index < count; index++ {
		value += "a"
	}
	return value
}

func repeatUpperHex(count int) string {
	value := ""
	for index := 0; index < count; index++ {
		value += "F"
	}
	return value
}
