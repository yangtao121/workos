package turnauth

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEphemeralCredentialsAndPrivateConfiguration(t *testing.T) {
	key := strings.Repeat("fixture-only-", 4)
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(key+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	issuer, err := New("relay", "turn:relay.fixture:3478?transport=udp,turns:relay.fixture:5349?transport=tcp", path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	issuer.now = func() time.Time { return now }
	first, err := issuer.Issue()
	if err != nil {
		t.Fatal(err)
	}
	second, err := issuer.Issue()
	if err != nil || !first.RelayOnly || len(first.Servers) != 1 || first.ExpiresAt.Sub(now) != Lifetime {
		t.Fatalf("invalid capability: %v", err)
	}
	server := first.Servers[0]
	if server.Username == second.Servers[0].Username || !strings.HasPrefix(server.Username, "1789948890:") {
		t.Fatal("capability identity or expiry is not fresh")
	}
	mac := hmac.New(sha1.New, []byte(key))
	mac.Write([]byte(server.Username))
	if !hmac.Equal([]byte(base64.StdEncoding.EncodeToString(mac.Sum(nil))), []byte(server.Credential)) {
		t.Fatal("coturn REST HMAC mismatch")
	}
	first.Servers[0].URLs[0] = "turn:changed:1"
	third, _ := issuer.Issue()
	if third.Servers[0].URLs[0] != second.Servers[0].URLs[0] {
		t.Fatal("caller mutated operator configuration")
	}
	if strings.Contains(server.Credential, key) {
		t.Fatal("shared secret exposed")
	}
}

func TestRelayConfigurationFailsClosed(t *testing.T) {
	root := t.TempDir()
	key := filepath.Join(root, "secret")
	if err := os.WriteFile(key, []byte(strings.Repeat("a", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	for _, url := range []string{"", "https://host:3478", "turn:user@host:3478", "turn:host:0", "turn:host:3478/path", "turn:host:3478?transport=sctp", "turns:host:5349?transport=udp"} {
		if _, err := New("relay", url, key); err == nil {
			t.Fatalf("invalid TURN URL accepted: %q", url)
		}
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(key, link); err != nil {
		t.Fatal(err)
	}
	if _, err := New("relay", "turn:host:3478", link); err == nil {
		t.Fatal("symlink secret accepted")
	}
	if err := os.Chmod(key, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := New("relay", "turn:host:3478", key); err == nil {
		t.Fatal("publicly readable secret accepted")
	}
	if _, err := New("lan", "turn:host:3478", key); err == nil {
		t.Fatal("LAN mode widened to relay")
	}
	issuer, err := New("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	capability, err := issuer.Issue()
	if err != nil || capability.Mode != "loopback" || capability.RelayOnly || len(capability.Servers) != 0 {
		t.Fatal("default policy changed")
	}
}
