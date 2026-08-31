// Package privatetls builds mutually authenticated TLS configurations for
// WorkOS private service-to-service listeners. Both peers present distinct
// leaf identities issued by one explicit private CA, and each side verifies
// the exact peer process identity through a fixed URI SAN — never a bare
// CN, never "any certificate signed by any CA", and never
// InsecureSkipVerify. Key material is loaded only from absolute, regular,
// non-symlink files with strict permissions; the CA private key never
// reaches a resident process.
package privatetls

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// Well-known WorkOS process identities carried in certificate URI SANs.
const (
	IdentityCore         = "urn:workos:core"
	IdentityHarnessHost  = "urn:workos:harness-host"
	identityURISchemeApp = "urn"
)

// Identity describes one peer's material on disk.
type Identity struct {
	CAFile   string // the private CA certificate (trust anchor), PEM
	CertFile string // this peer's leaf certificate chain, PEM
	KeyFile  string // this peer's leaf private key, PEM
	// PeerIdentity is the exact URI SAN the other side must present.
	PeerIdentity string
}

// ServerConfig returns the TLS configuration for the private listener side:
// TLS 1.3 only, client certificates required and verified against the
// configured CA, and the client leaf's URI SAN must equal PeerIdentity.
func ServerConfig(identity Identity) (*tls.Config, error) {
	certificate, rootPool, err := loadIdentity(identity, IdentityCore)
	if err != nil {
		return nil, err
	}
	peer := identity.PeerIdentity
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certificate},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    rootPool,
		VerifyPeerCertificate: func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
			return verifyPeerIdentity(verifiedChains, peer)
		},
	}, nil
}

// ClientConfig returns the TLS configuration for the private dial side: the
// server certificate must chain to the configured CA and present the exact
// server URI SAN.
func ClientConfig(identity Identity) (*tls.Config, error) {
	certificate, rootPool, err := loadIdentity(identity, IdentityHarnessHost)
	if err != nil {
		return nil, err
	}
	peer := identity.PeerIdentity
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certificate},
		RootCAs:      rootPool,
		VerifyPeerCertificate: func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
			return verifyPeerIdentity(verifiedChains, peer)
		},
	}, nil
}

func loadIdentity(identity Identity, selfIdentity string) (tls.Certificate, *x509.CertPool, error) {
	if identity.PeerIdentity == "" {
		return tls.Certificate{}, nil, errors.New("private TLS identity requires the exact peer URI SAN")
	}
	caPEM, err := readMaterial(identity.CAFile, false)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("CA file: %w", err)
	}
	certPEM, err := readMaterial(identity.CertFile, false)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("certificate file: %w", err)
	}
	keyPEM, err := readMaterial(identity.KeyFile, true)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("private key file: %w", err)
	}
	defer overwrite(keyPEM)
	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("load private TLS identity: %w", err)
	}
	if len(certificate.Certificate) == 0 {
		return tls.Certificate{}, nil, errors.New("private TLS identity contains no leaf certificate")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return tls.Certificate{}, nil, errors.New("private TLS leaf certificate is invalid")
	}
	if err := verifyExactURIIdentity(leaf, selfIdentity); err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("private TLS local identity: %w", err)
	}
	certificate.Leaf = leaf
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return tls.Certificate{}, nil, errors.New("CA file contains no usable certificate")
	}
	return certificate, pool, nil
}

// readMaterial opens one deployment file without following symlinks, then
// validates the opened descriptor before reading a bounded payload. This
// avoids the Lstat/open race that would otherwise let a path change between
// validation and use.
func readMaterial(path string, privateKey bool) ([]byte, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("path must be absolute and cleaned")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("material is unavailable: %w", err)
	}
	defer file.Close() //nolint:errcheck -- read-only handle
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect material: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("path must be a regular file, not a symlink")
	}
	if privateKey && info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("private key must not be group or world accessible")
	}
	if privateKey {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Geteuid()) {
			return nil, errors.New("private key must be owned by this process user")
		}
	}
	const maximumMaterialBytes = 1 << 20
	material, err := io.ReadAll(io.LimitReader(file, maximumMaterialBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read material: %w", err)
	}
	if len(material) == 0 || len(material) > maximumMaterialBytes {
		overwrite(material)
		return nil, errors.New("material size is invalid")
	}
	return material, nil
}

// verifyPeerIdentity re-checks the leaf of every chain the TLS stack already
// verified: the exact peer URI SAN must be present. Certificate expiry,
// signature, and usage validation stay with crypto/tls and crypto/x509.
func verifyPeerIdentity(verifiedChains [][]*x509.Certificate, peer string) error {
	for _, chain := range verifiedChains {
		if len(chain) == 0 {
			continue
		}
		if err := verifyExactURIIdentity(chain[0], peer); err == nil {
			return nil
		}
	}
	return fmt.Errorf("peer identity is not %q", peer)
}

func verifyExactURIIdentity(certificate *x509.Certificate, expected string) error {
	if certificate == nil || len(certificate.URIs) != 1 || certificate.URIs[0] == nil ||
		certificate.URIs[0].Scheme != identityURISchemeApp || certificate.URIs[0].String() != expected {
		return fmt.Errorf("identity is not exactly %q", expected)
	}
	return nil
}

func overwrite(buffer []byte) {
	for index := range buffer {
		buffer[index] = 0
	}
}
