package main

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestFixtureMaterialIsSeparatedByProcess(t *testing.T) {
	root := t.TempDir()
	coreDir := filepath.Join(root, "core")
	harnessDir := filepath.Join(root, "harness")
	vaultDir := filepath.Join(root, "vault")
	for _, dir := range []string{coreDir, harnessDir, vaultDir} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := generateTLS(coreDir, harnessDir); err != nil {
		t.Fatal(err)
	}
	if err := ensureMasterKey(filepath.Join(vaultDir, "vault-master.key")); err != nil {
		t.Fatal(err)
	}
	if err := validateExistingTLS(coreDir, harnessDir); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		filepath.Join(coreDir, "harness.key"), filepath.Join(coreDir, "vault-master.key"),
		filepath.Join(harnessDir, "core.key"), filepath.Join(harnessDir, "vault-master.key"),
		filepath.Join(vaultDir, "core.key"), filepath.Join(vaultDir, "harness.key"),
	} {
		if _, err := os.Lstat(forbidden); !os.IsNotExist(err) {
			t.Fatalf("forbidden cross-process material exists: %s", forbidden)
		}
	}
	assertLeafUsage(t, filepath.Join(coreDir, "core.crt"), x509.ExtKeyUsageServerAuth)
	assertLeafUsage(t, filepath.Join(harnessDir, "harness.crt"), x509.ExtKeyUsageClientAuth)
}

func assertLeafUsage(t *testing.T, path string, expected x509.ExtKeyUsage) {
	t.Helper()
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(encoded)
	if block == nil {
		t.Fatalf("certificate %s is not PEM", path)
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(certificate.ExtKeyUsage) != 1 || certificate.ExtKeyUsage[0] != expected {
		t.Fatalf("certificate %s has unexpected usages: %#v", path, certificate.ExtKeyUsage)
	}
}
