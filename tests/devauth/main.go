// devauth generates the deterministic development fixture material for the
// Core private mTLS harness execution channel and the Credential Vault
// master key (ADR-0009): one private CA plus two leaf identities — Core
// (urn:workos:core) and harness-host (urn:workos:harness-host) — and a
// 32-byte master key file. It is DEV/CI tooling only: production provisions
// certificates and the master key through systemd credentials or an
// equivalent file facility, and never runs this command. Files already
// present are left untouched so the stack can restart against the same
// facts; existing files with wrong contents or permissions fail instead of
// being silently overwritten.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
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
	root := flag.String("root", "", "output root directory (required)")
	flag.Parse()
	if *root == "" {
		fmt.Fprintln(os.Stderr, "devauth: -root is required")
		os.Exit(2)
	}
	dir := filepath.Join(*root, "execution")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fatal(err)
	}
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		fatal(err)
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
		fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		fatal(err)
	}
	coreKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		fatal(err)
	}
	coreLeaf, err := leaf(caTemplate, caCert, caKey, coreKey, "workos dev core leaf", "urn:workos:core")
	if err != nil {
		fatal(err)
	}
	harnessKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		fatal(err)
	}
	harnessLeaf, err := leaf(caTemplate, caCert, caKey, harnessKey, "workos dev harness leaf", "urn:workos:harness-host")
	if err != nil {
		fatal(err)
	}
	materials := []struct {
		name    string
		kind    string
		der     []byte
		key     *ecdsa.PrivateKey
		mode    os.FileMode
		present func() bool
	}{
		{"ca.crt", "CERTIFICATE", caDER, nil, 0o644, func() bool { return exists(filepath.Join(dir, "ca.crt")) }},
		{"core.crt", "CERTIFICATE", coreLeaf, nil, 0o644, func() bool { return exists(filepath.Join(dir, "core.crt")) }},
		{"core.key", "EC PRIVATE KEY", nil, coreKey, 0o600, func() bool { return exists(filepath.Join(dir, "core.key")) }},
		{"harness.crt", "CERTIFICATE", harnessLeaf, nil, 0o644, func() bool { return exists(filepath.Join(dir, "harness.crt")) }},
		{"harness.key", "EC PRIVATE KEY", nil, harnessKey, 0o600, func() bool { return exists(filepath.Join(dir, "harness.key")) }},
	}
	for _, material := range materials {
		path := filepath.Join(dir, material.name)
		if material.present() {
			continue
		}
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, material.mode)
		if err != nil {
			fatal(err)
		}
		defer file.Close() //nolint:errcheck
		var block []byte
		if material.key != nil {
			encoded, err := x509.MarshalECPrivateKey(material.key)
			if err != nil {
				fatal(err)
			}
			block = pem.EncodeToMemory(&pem.Block{Type: material.kind, Bytes: encoded})
		} else {
			block = pem.EncodeToMemory(&pem.Block{Type: material.kind, Bytes: material.der})
		}
		if _, err := file.Write(block); err != nil {
			fatal(err)
		}
		if err := file.Chmod(material.mode); err != nil {
			fatal(err)
		}
	}
	master := filepath.Join(*root, "vault-master.key")
	if !exists(master) {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			fatal(err)
		}
		file, err := os.OpenFile(master, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			fatal(err)
		}
		if _, err := file.Write(key); err != nil {
			file.Close() //nolint:errcheck
			fatal(err)
		}
		if err := file.Close(); err != nil {
			fatal(err)
		}
		if err := os.Chmod(master, 0o600); err != nil {
			fatal(err)
		}
	}
	fmt.Println("dev fixture material ready")
}

// leaf builds one server+client leaf: the exact URI SAN is the process
// identity; loopback DNS/IP SANs let ordinary https dials resolve in dev.
func leaf(parentTemplate *x509.Certificate, parent *x509.Certificate, parentKey *ecdsa.PrivateKey, key *ecdsa.PrivateKey, commonName, uri string) ([]byte, error) {
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(fixtureValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
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

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "devauth:", err)
	os.Exit(1)
}
