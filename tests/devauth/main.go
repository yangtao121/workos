// devauth generates deterministic-shape development fixture material for the
// Core private mTLS harness execution channel and the Credential Vault master
// key (ADR-0009). Core, harness-host, and vault outputs go to three distinct
// roots so Compose can mount each resident process's minimum material only.
// It is DEV/CI tooling only: production provisions certificates and the
// master key through systemd credentials or an equivalent file facility and
// never runs this command. A complete existing fixture is preserved across
// restarts; a partial fixture fails instead of mixing identities from two CAs.
package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

const fixtureValidity = 30 * 24 * time.Hour

func main() {
	coreDir := flag.String("core-dir", "", "Core execution identity directory (required)")
	harnessDir := flag.String("harness-dir", "", "harness-host execution identity directory (required)")
	vaultDir := flag.String("vault-dir", "", "Core vault key directory (required)")
	flag.Parse()
	if !validOutputDir(*coreDir) || !validOutputDir(*harnessDir) || !validOutputDir(*vaultDir) ||
		*coreDir == *harnessDir || *coreDir == *vaultDir || *harnessDir == *vaultDir {
		fmt.Fprintln(os.Stderr, "devauth: three distinct absolute cleaned output directories are required")
		os.Exit(2)
	}
	for _, dir := range []string{*coreDir, *harnessDir, *vaultDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			fatal(err)
		}
	}
	tlsPaths := []string{
		filepath.Join(*coreDir, "ca.crt"), filepath.Join(*coreDir, "core.crt"), filepath.Join(*coreDir, "core.key"),
		filepath.Join(*harnessDir, "ca.crt"), filepath.Join(*harnessDir, "harness.crt"), filepath.Join(*harnessDir, "harness.key"),
	}
	present := 0
	for _, path := range tlsPaths {
		if pathExists(path) {
			present++
		}
	}
	if present != 0 && present != len(tlsPaths) {
		fatal(fmt.Errorf("private execution fixture is partial (%d/%d files)", present, len(tlsPaths)))
	}
	if present == len(tlsPaths) {
		if err := validateExistingTLS(*coreDir, *harnessDir); err != nil {
			fatal(err)
		}
	} else {
		if err := generateTLS(*coreDir, *harnessDir); err != nil {
			fatal(err)
		}
	}
	if err := ensureMasterKey(filepath.Join(*vaultDir, "vault-master.key")); err != nil {
		fatal(err)
	}
	fmt.Println("dev fixture material ready")
}

func generateTLS(coreDir, harnessDir string) error {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "workos dev execution CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(fixtureValidity),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return err
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return err
	}
	coreKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	coreLeaf, err := leaf(caTemplate, caCert, caKey, coreKey, "workos dev core leaf", "urn:workos:core", x509.ExtKeyUsageServerAuth)
	if err != nil {
		return err
	}
	harnessKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	harnessLeaf, err := leaf(caTemplate, caCert, caKey, harnessKey, "workos dev harness leaf", "urn:workos:harness-host", x509.ExtKeyUsageClientAuth)
	if err != nil {
		return err
	}
	materials := []struct {
		path string
		kind string
		der  []byte
		key  *ecdsa.PrivateKey
		mode os.FileMode
	}{
		{filepath.Join(coreDir, "ca.crt"), "CERTIFICATE", caDER, nil, 0o644},
		{filepath.Join(coreDir, "core.crt"), "CERTIFICATE", coreLeaf, nil, 0o644},
		{filepath.Join(coreDir, "core.key"), "EC PRIVATE KEY", nil, coreKey, 0o600},
		{filepath.Join(harnessDir, "ca.crt"), "CERTIFICATE", caDER, nil, 0o644},
		{filepath.Join(harnessDir, "harness.crt"), "CERTIFICATE", harnessLeaf, nil, 0o644},
		{filepath.Join(harnessDir, "harness.key"), "EC PRIVATE KEY", nil, harnessKey, 0o600},
	}
	for _, material := range materials {
		file, err := os.OpenFile(material.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, material.mode)
		if err != nil {
			return err
		}
		var block []byte
		if material.key != nil {
			encoded, err := x509.MarshalECPrivateKey(material.key)
			if err != nil {
				file.Close() //nolint:errcheck
				return err
			}
			block = pem.EncodeToMemory(&pem.Block{Type: material.kind, Bytes: encoded})
		} else {
			block = pem.EncodeToMemory(&pem.Block{Type: material.kind, Bytes: material.der})
		}
		if _, err := file.Write(block); err != nil {
			file.Close() //nolint:errcheck
			return err
		}
		if err := file.Chmod(material.mode); err != nil {
			file.Close() //nolint:errcheck
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
	}
	return nil
}

func ensureMasterKey(master string) error {
	if !pathExists(master) {
		key := make([]byte, 32)
		defer overwrite(key)
		if _, err := rand.Read(key); err != nil {
			return err
		}
		file, err := os.OpenFile(master, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		if _, err := file.Write(key); err != nil {
			file.Close() //nolint:errcheck
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		if err := os.Chmod(master, 0o600); err != nil {
			return err
		}
	}
	info, err := os.Lstat(master)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 || info.Size() != 32 {
		return errors.New("existing vault master key must be a 0600 regular non-symlink file of exactly 32 bytes")
	}
	return nil
}

// leaf builds one role-specific leaf: the exact URI SAN is the process
// identity; loopback DNS/IP SANs let ordinary https dials resolve in dev.
func leaf(parentTemplate *x509.Certificate, parent *x509.Certificate, parentKey *ecdsa.PrivateKey, key *ecdsa.PrivateKey, commonName, uri string, usage x509.ExtKeyUsage) ([]byte, error) {
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(fixtureValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
		URIs:         mustParseURI(uri),
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	return x509.CreateCertificate(rand.Reader, template, parent, &key.PublicKey, parentKey)
}

func mustParseURI(raw string) []*url.URL {
	parsed, err := url.Parse(raw)
	if err != nil {
		fatal(err)
	}
	return []*url.URL{parsed}
}

func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func validOutputDir(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path
}

func validateExistingTLS(coreDir, harnessDir string) error {
	coreCA, err := os.ReadFile(filepath.Join(coreDir, "ca.crt"))
	if err != nil {
		return err
	}
	harnessCA, err := os.ReadFile(filepath.Join(harnessDir, "ca.crt"))
	if err != nil {
		return err
	}
	if !bytes.Equal(coreCA, harnessCA) {
		return errors.New("Core and harness fixture trust anchors differ")
	}
	for _, material := range []struct {
		dir, name, identity string
	}{
		{coreDir, "core", "urn:workos:core"},
		{harnessDir, "harness", "urn:workos:harness-host"},
	} {
		certPath := filepath.Join(material.dir, material.name+".crt")
		keyPath := filepath.Join(material.dir, material.name+".key")
		keyInfo, err := os.Lstat(keyPath)
		if err != nil || !keyInfo.Mode().IsRegular() || keyInfo.Mode()&os.ModeSymlink != 0 || keyInfo.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("%s fixture private key has unsafe metadata", material.name)
		}
		pair, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil || len(pair.Certificate) == 0 {
			return fmt.Errorf("%s fixture key pair is invalid", material.name)
		}
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil || len(leaf.URIs) != 1 || leaf.URIs[0].String() != material.identity {
			return fmt.Errorf("%s fixture URI identity is invalid", material.name)
		}
	}
	return nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "devauth:", err)
	os.Exit(1)
}

func overwrite(buffer []byte) {
	for index := range buffer {
		buffer[index] = 0
	}
}
