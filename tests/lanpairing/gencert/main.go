// gencert generates the deterministic throwaway TLS fixture for
// make test-lan-pairing (ADR-0007): a fresh CA + leaf keypair per run, in a
// host temp directory. The material never enters Git, logs, or screenshots;
// the caller deletes the directory afterwards. Test certificates are not
// evidence of certificate management or browser trust.
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
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	out := flag.String("out", "", "output directory (required)")
	hosts := flag.String("hosts", "localhost,127.0.0.1", "comma-separated SAN DNS names and IPs")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "gencert: -out is required")
		os.Exit(2)
	}
	if err := os.MkdirAll(*out, 0o700); err != nil {
		fatal(err)
	}

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "workos lan-pairing test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
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

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "workos lan-pairing test leaf"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, host := range strings.Split(*hosts, ",") {
		host = strings.TrimSpace(host)
		if host == "" {
			continue
		}
		if ip := net.ParseIP(host); ip != nil {
			leafTemplate.IPAddresses = append(leafTemplate.IPAddresses, ip)
		} else {
			leafTemplate.DNSNames = append(leafTemplate.DNSNames, host)
		}
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		fatal(err)
	}

	// 0644 so the non-root container user can read the mounted fixtures;
	// the host temp directory itself is 0700.
	if err := writePEM(filepath.Join(*out, "ca.crt"), "CERTIFICATE", caDER, 0o644); err != nil {
		fatal(err)
	}
	if err := writePEM(filepath.Join(*out, "leaf.crt"), "CERTIFICATE", leafDER, 0o644); err != nil {
		fatal(err)
	}
	leafKeyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		fatal(err)
	}
	if err := writePEM(filepath.Join(*out, "leaf.key"), "EC PRIVATE KEY", leafKeyDER, 0o644); err != nil {
		fatal(err)
	}
	fmt.Println("tls fixture generated in", *out)
}

func writePEM(path, blockType string, der []byte, mode os.FileMode) error {
	return os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}), mode)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "gencert:", err)
	os.Exit(1)
}
