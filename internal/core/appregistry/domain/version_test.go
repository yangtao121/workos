package domain

import "testing"

func TestParseVersionAcceptsSchemaSubset(t *testing.T) {
	t.Parallel()
	valid := []string{
		"0.0.1", "1.0.0", "1.10.0", "10.20.30", "1.0.0-rc.1", "1.0.0-0", "1.0.0-alpha", "1.0.0-alpha.1",
		"1.0.0-x.7.z.92", "20260823.0.0", "1.0.0-rc-1",
	}
	for _, value := range valid {
		if _, ok := ParseVersion(value); !ok {
			t.Errorf("ParseVersion(%q) rejected a schema-compatible version", value)
		}
	}
	invalid := []string{
		"", "1.0", "1.0.0.0", "01.0.0", "1.0.00", "v1.0.0", "1.0.0-", "1.0.0-.rc1", "1.0.0-rc..1",
		"1.0.0-rc.01", "1.0.0-rc.1+", "1.0.0+b1", "1.0.0_rc", "a.b.c", "18446744073709551616.0.0",
	}
	for _, value := range invalid {
		if _, ok := ParseVersion(value); ok {
			t.Errorf("ParseVersion(%q) accepted an invalid version", value)
		}
	}
}

func TestCompareVersionFollowsSemVerPrecedence(t *testing.T) {
	t.Parallel()
	// Ordered ascending per the SemVer specification, including the classic
	// multi-digit cases the registry must not get wrong.
	ascending := []string{
		"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta",
		"1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0",
		"1.0.1", "1.9.0", "1.10.0-rc.1", "1.10.0-rc.2", "1.10.0-rc.10", "1.10.0", "1.10.1", "2.0.0",
	}
	parsed := make([]Version, 0, len(ascending))
	for _, value := range ascending {
		version, ok := ParseVersion(value)
		if !ok {
			t.Fatalf("ParseVersion(%q) failed", value)
		}
		parsed = append(parsed, version)
	}
	for i := 0; i < len(parsed)-1; i++ {
		if CompareVersion(parsed[i], parsed[i+1]) >= 0 {
			t.Errorf("expected %s < %s", ascending[i], ascending[i+1])
		}
		if CompareVersion(parsed[i+1], parsed[i]) <= 0 {
			t.Errorf("expected %s > %s (reverse)", ascending[i+1], ascending[i])
		}
	}
	if CompareVersion(parsed[0], parsed[0]) != 0 {
		t.Error("identical versions must compare equal")
	}
}

func TestCurrentVersionSelectsHighestPrecedence(t *testing.T) {
	t.Parallel()
	versions := []AppVersion{
		{AppID: "notes", Version: "1.9.0"}, {AppID: "notes", Version: "1.10.0"},
		{AppID: "notes", Version: "2.0.0-rc.1"},
	}
	// SemVer precedence is major-first: 2.0.0-rc.1 outranks 1.10.0 even
	// though it is a prerelease of its own 2.0.0 release.
	current, ok := CurrentVersion(versions)
	if !ok || current.Version != "2.0.0-rc.1" {
		t.Fatalf("unexpected current version: %#v ok=%v", current, ok)
	}
	release, _ := ParseVersion("2.0.0")
	if CompareVersion(ParseVersionOrFatal(t, "2.0.0-rc.1"), release) >= 0 {
		t.Fatal("release must outrank its own prerelease")
	}
	if _, ok := CurrentVersion(nil); ok {
		t.Fatal("empty version list must not yield a current version")
	}
	if _, ok := CurrentVersion([]AppVersion{{AppID: "notes", Version: "not-semver"}}); ok {
		t.Fatal("unparseable stored version must fail closed")
	}
}

func ParseVersionOrFatal(t *testing.T, value string) Version {
	t.Helper()
	version, ok := ParseVersion(value)
	if !ok {
		t.Fatalf("ParseVersion(%q) failed", value)
	}
	return version
}
