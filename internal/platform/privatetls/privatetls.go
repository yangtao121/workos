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
	"os"
	"path/filepath"
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
	certificate, rootPool, err := loadIdentity(identity)
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
	certificate, rootPool, err := loadIdentity(identity)
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

func loadIdentity(identity Identity) (tls.Certificate, *x509.CertPool, error) {
	if identity.PeerIdentity == "" {
		return tls.Certificate{}, nil, errors.New("private TLS identity requires the exact peer URI SAN")
	}
	if err := validateMaterialPath(identity.CAFile, false); err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("CA file: %w", err)
	}
	if err := validateMaterialPath(identity.CertFile, false); err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("certificate file: %w", err)
	}
	if err := validateMaterialPath(identity.KeyFile, true); err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("private key file: %w", err)
	}
	certificate, err := tls.LoadX509KeyPair(identity.CertFile, identity.KeyFile)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("load private TLS identity: %w", err)
	}
	caPEM, err := os.ReadFile(identity.CAFile)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("read CA file: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return tls.Certificate{}, nil, errors.New("CA file contains no usable certificate")
	}
	return certificate, pool, nil
}

// validateMaterialPath enforces the deployment grammar: absolute cleaned
// path, regular non-symlink file readable by this process, and for private
// keys no group/world permission bits. Ownership is proven by the successful
// open under this process's credentials.
func validateMaterialPath(path string, privateKey bool) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("path must be absolute and cleaned")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("material is unavailable: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("path must be a regular file, not a symlink")
	}
	if privateKey && info.Mode().Perm()&0o077 != 0 {
		return errors.New("private key must not be group or world accessible")
	}
	return nil
}

// verifyPeerIdentity re-checks the leaf of every chain the TLS stack already
// verified: the exact peer URI SAN must be present. Certificate expiry,
// signature, and usage validation stay with crypto/tls and crypto/x509.
func verifyPeerIdentity(verifiedChains [][]*x509.Certificate, peer string) error {
	for _, chain := range verifiedChains {
		if len(chain) == 0 {
			continue
		}
		for _, uri := range chain[0].URIs {
			if uri != nil && uri.Scheme == identityURISchemeApp && uri.String() == peer {
				return nil
			}
		}
	}
	return fmt.Errorf("peer identity is not %q", peer)
}
