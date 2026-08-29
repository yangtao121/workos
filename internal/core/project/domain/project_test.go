package domain

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestNormalizeName(t *testing.T) {
	t.Parallel()
	if value, err := NormalizeName("  Mission Control  "); err != nil || value != "Mission Control" {
		t.Fatalf("unexpected normalized name %q: %v", value, err)
	}
	if _, err := NormalizeName("   "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected blank name to fail, got %v", err)
	}
	// The limit is Unicode code points, not bytes: 120 two-byte runes are
	// legal while 240 bytes.
	multibyte := strings.Repeat("◈", MaxNameRunes)
	if _, err := NormalizeName(multibyte); err != nil {
		t.Fatalf("120 code-point name must be legal: %v", err)
	}
	if _, err := NormalizeName(multibyte + "◈"); !errors.Is(err, ErrInvalid) {
		t.Fatal("121 code-point name must be rejected")
	}
}

func TestHarnessBindingRequiresExplicitPolicy(t *testing.T) {
	t.Parallel()
	valid := &HarnessBinding{ProviderID: "fake", InstancePolicy: "ephemeral", ResourcePolicyID: "foundation"}
	if err := ValidateBinding(valid); err != nil {
		t.Fatal(err)
	}
	invalid := *valid
	invalid.InstancePolicy = "magic"
	if err := ValidateBinding(&invalid); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected unknown instance policy to fail, got %v", err)
	}
}

func TestValidateBindingFieldLimits(t *testing.T) {
	t.Parallel()
	base := &HarnessBinding{ProviderID: "fake", InstancePolicy: "lazy", ResourcePolicyID: "project-no-tools"}

	over := func(value string) string { return value + strings.Repeat("a", 300) }
	for name, mutate := range map[string]func(*HarnessBinding){
		"provider missing":      func(b *HarnessBinding) { b.ProviderID = "" },
		"provider over limit":   func(b *HarnessBinding) { b.ProviderID = over(b.ProviderID) },
		"policy id missing":     func(b *HarnessBinding) { b.ResourcePolicyID = "" },
		"policy id over limit":  func(b *HarnessBinding) { b.ResourcePolicyID = over(b.ResourcePolicyID) },
		"profile over limit":    func(b *HarnessBinding) { b.ProfileID = over("p") },
		"credential over limit": func(b *HarnessBinding) { b.CredentialRef = over("c") },
		"provider control char": func(b *HarnessBinding) { b.ProviderID = "fa\x00ke" },
		"credential control":    func(b *HarnessBinding) { b.CredentialRef = "ref\nline" },
		"invalid utf8 profile":  func(b *HarnessBinding) { b.ProfileID = "\xff\xfe" },
	} {
		candidate := *base
		mutate(&candidate)
		if err := ValidateBinding(&candidate); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: expected ErrInvalid, got %v", name, err)
		}
	}
	// A bounded opaque credential reference is legal; raw credentials are
	// not a validation concern here, but the reference must stay bounded.
	withRef := *base
	withRef.CredentialRef = strings.Repeat("r", MaxCredentialRefRunes)
	if err := ValidateBinding(&withRef); err != nil {
		t.Fatalf("maximal legal credential ref rejected: %v", err)
	}
}

func TestValidateIcon(t *testing.T) {
	t.Parallel()
	if err := ValidateIcon(""); err != nil {
		t.Fatalf("empty icon is legal: %v", err)
	}
	if err := ValidateIcon(strings.Repeat("◈", MaxIconRunes)); err != nil {
		t.Fatalf("maximal icon must be legal: %v", err)
	}
	for name, value := range map[string]string{
		"over limit":   strings.Repeat("◈", MaxIconRunes+1),
		"control char": "icon\nname",
		"invalid utf8": "\xff\xfe",
		"c1 control":   "icon\x9f",
	} {
		if err := ValidateIcon(value); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: expected ErrInvalid, got %v", name, err)
		}
	}
}

func TestValidateWorkspaceRefs(t *testing.T) {
	t.Parallel()
	validRef := func(id string) WorkspaceRef {
		return WorkspaceRef{ID: id, Kind: "WORKSPACE_KIND_LOCAL_GIT", URI: "file:///repos/" + id}
	}
	if err := ValidateWorkspaceRefs(nil); err != nil {
		t.Fatalf("empty ref list is legal: %v", err)
	}
	if err := ValidateWorkspaceRefs([]WorkspaceRef{validRef("r1"), validRef("r2")}); err != nil {
		t.Fatalf("legal refs rejected: %v", err)
	}

	many := make([]WorkspaceRef, 0, MaxWorkspaceRefs+1)
	for index := 0; index <= MaxWorkspaceRefs; index++ {
		many = append(many, validRef(fmt.Sprintf("ref-%02d", index)))
	}
	if err := ValidateWorkspaceRefs(many[:MaxWorkspaceRefs]); err != nil {
		t.Fatalf("maximal ref count must be legal: %v", err)
	}
	if err := ValidateWorkspaceRefs(many); !errors.Is(err, ErrInvalid) {
		t.Fatal("over-limit ref count must be rejected")
	}

	for name, ref := range map[string]WorkspaceRef{
		"unspecified kind": {ID: "r1", Kind: "WORKSPACE_KIND_UNSPECIFIED", URI: "file:///x"},
		"unknown kind":     {ID: "r1", Kind: "99", URI: "file:///x"},
		"empty id":         {ID: "", Kind: "WORKSPACE_KIND_DATASET", URI: "file:///x"},
		"empty uri":        {ID: "r1", Kind: "WORKSPACE_KIND_DATASET"},
		"id over limit":    {ID: strings.Repeat("i", MaxWorkspaceRefIDRunes+1), Kind: "WORKSPACE_KIND_DATASET", URI: "file:///x"},
		"uri over limit":   {ID: "r1", Kind: "WORKSPACE_KIND_DATASET", URI: strings.Repeat("u", MaxWorkspaceRefURIRunes+1)},
		"id control char":  {ID: "r\x01", Kind: "WORKSPACE_KIND_DATASET", URI: "file:///x"},
		"uri invalid utf8": {ID: "r1", Kind: "WORKSPACE_KIND_DATASET", URI: "\xff"},
		"nil-derived zero": {Kind: "WORKSPACE_KIND_UNSPECIFIED"},
	} {
		if err := ValidateWorkspaceRefs([]WorkspaceRef{ref}); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: expected ErrInvalid, got %v", name, err)
		}
	}

	if err := ValidateWorkspaceRefs([]WorkspaceRef{validRef("dup"), validRef("dup")}); !errors.Is(err, ErrInvalid) {
		t.Fatal("duplicate ref ids must be rejected")
	}
	if err := ValidateWorkspaceRefs([]WorkspaceRef{
		{ID: "a", Kind: "WORKSPACE_KIND_NAS", URI: "nfs:///a", LogicalMount: "share"},
		{ID: "b", Kind: "WORKSPACE_KIND_NAS", URI: "nfs:///b", LogicalMount: "share"},
	}); !errors.Is(err, ErrInvalid) {
		t.Fatal("ambiguous duplicate logical mounts must be rejected")
	}
	if err := ValidateWorkspaceRefs([]WorkspaceRef{
		{ID: "a", Kind: "WORKSPACE_KIND_NAS", URI: "nfs:///a", LogicalMount: "share-a"},
		{ID: "b", Kind: "WORKSPACE_KIND_NAS", URI: "nfs:///b", LogicalMount: "share-b"},
	}); err != nil {
		t.Fatalf("distinct logical mounts must be legal: %v", err)
	}
}

func TestValidProjectUUID(t *testing.T) {
	t.Parallel()
	valid := "01999999-9999-7999-8999-999999999991"
	if !ValidProjectUUID(valid) {
		t.Fatal("canonical UUIDv7 must be accepted")
	}
	for name, value := range map[string]string{
		"uppercase":     "01999999-9999-7999-8999-99999999999A",
		"uuid v4":       "01999999-9999-4999-8999-999999999991",
		"no hyphens":    "01999999999979998999999999999991",
		"wrong variant": "01999999-9999-7999-c999-999999999991",
		"braced":        "{01999999-9999-7999-8999-999999999991}",
		"empty":         "",
		"too long":      valid + "0",
		"non hex":       "01999999-9999-7999-8999-9999999999gg",
	} {
		if ValidProjectUUID(value) {
			t.Errorf("%s must be rejected", name)
		}
	}
}

func TestValidIdempotencyKey(t *testing.T) {
	t.Parallel()
	if !ValidIdempotencyKey("k") {
		t.Fatal("single rune key must be accepted")
	}
	if !ValidIdempotencyKey(strings.Repeat("◈", MaxIdempotencyKeyRunes)) {
		t.Fatal("128 code-point key must be accepted")
	}
	for name, value := range map[string]string{
		"empty":        "",
		"over limit":   strings.Repeat("k", MaxIdempotencyKeyRunes+1),
		"invalid utf8": "\xff",
		"c0 control":   "key\n",
		"c1 control":   "key\x85",
		"del":          "key\x7f",
		"nul":          "ke\x00y",
	} {
		if ValidIdempotencyKey(value) {
			t.Errorf("%s key must be rejected", name)
		}
	}
}

func TestCreateRequestDigestStability(t *testing.T) {
	t.Parallel()
	refs := []WorkspaceRef{
		{ID: "r1", Kind: "WORKSPACE_KIND_LOCAL_GIT", URI: "file:///repos/a", ReadOnly: true},
		{ID: "r2", Kind: "WORKSPACE_KIND_DATASET", URI: "file:///data"},
	}
	binding := &HarnessBinding{ProviderID: "fake", InstancePolicy: "lazy", ProfileID: "p", CredentialRef: "c", ResourcePolicyID: "r"}

	base := CreateRequestDigest("Mission Control", "◈", refs, binding)
	if base != CreateRequestDigest("Mission Control", "◈", refs, binding) {
		t.Fatal("identical requests must digest identically")
	}
	if len(base) != len("sha256:")+64 || base[:7] != "sha256:" {
		t.Fatalf("digest must be sha256 hex: %q", base)
	}
	different := map[string]string{
		"name": CreateRequestDigest("Mission Control ", "◈", refs, binding),
		"icon": CreateRequestDigest("Mission Control", "", refs, binding),
	}
	mutatedRefs := append([]WorkspaceRef(nil), refs...)
	mutatedRefs[0].ReadOnly = false
	different["ref field"] = CreateRequestDigest("Mission Control", "◈", mutatedRefs, binding)
	different["ref order"] = CreateRequestDigest("Mission Control", "◈", []WorkspaceRef{refs[1], refs[0]}, binding)
	different["binding presence"] = CreateRequestDigest("Mission Control", "◈", refs, nil)
	mutatedBinding := *binding
	mutatedBinding.ProfileID = "other"
	different["binding field"] = CreateRequestDigest("Mission Control", "◈", refs, &mutatedBinding)
	for name, digest := range different {
		if digest == base {
			t.Fatalf("%s must change the digest", name)
		}
	}
}
