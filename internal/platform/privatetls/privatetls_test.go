package privatetls

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type testPKI struct {
	dir       string
	caPEM     string
	issuer    *x509.Certificate
	issuerKey *ecdsa.PrivateKey
}

func newPKI(t *testing.T) *testPKI {
	t.Helper()
	dir := t.TempDir()
	issuerKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "privatetls test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &issuerKey.PublicKey, issuerKey)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	caPEM := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(caPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	return &testPKI{dir: dir, caPEM: caPEM, issuer: issuer, issuerKey: issuerKey}
}

func (p *testPKI) writeLeaf(t *testing.T, name, uri string, mode os.FileMode) Identity {
	return p.writeLeafURIs(t, name, []string{uri}, mode)
}

func (p *testPKI) writeLeafURIs(t *testing.T, name string, uris []string, mode os.FileMode) Identity {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	parsedURIs := make([]*url.URL, 0, len(uris))
	for _, uri := range uris {
		parsedURIs = append(parsedURIs, mustURL(t, uri))
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "privatetls test leaf"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		URIs:         parsedURIs,
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, p.issuer, &key.PublicKey, p.issuerKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(p.dir, name+".crt")
	keyPath := filepath.Join(p.dir, name+".key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), mode); err != nil {
		t.Fatal(err)
	}
	return Identity{CAFile: p.caPEM, CertFile: certPath, KeyFile: keyPath}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func startServer(t *testing.T, serverIdentity Identity, peer string) string {
	t.Helper()
	config, err := ServerConfig(Identity{
		CAFile: serverIdentity.CAFile, CertFile: serverIdentity.CertFile,
		KeyFile: serverIdentity.KeyFile, PeerIdentity: peer,
	})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})}
	go server.Serve(listener) //nolint:errcheck
	return listener.Addr().String()
}

// roundTrip completes an HTTP request over the TLS connection: under TLS 1.3
// a server-side client-certificate rejection surfaces on the first read, not
// necessarily during the client handshake.
func roundTrip(t *testing.T, address string, config *tls.Config) error {
	t.Helper()
	conn, err := tls.Dial("tcp", address, config)
	if err != nil {
		return err
	}
	defer conn.Close()
	request, err := http.NewRequest(http.MethodGet, "https://"+address+"/", nil)
	if err != nil {
		return err
	}
	if err := request.Write(conn); err != nil {
		return err
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), request)
	if err != nil {
		return err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("unexpected status")
	}
	return nil
}

func TestMutualTLSExactIdentity(t *testing.T) {
	pki := newPKI(t)
	serverLeaf := pki.writeLeaf(t, "core", IdentityCore, 0o600)
	clientLeaf := pki.writeLeaf(t, "harness", IdentityHarnessHost, 0o600)
	address := startServer(t, serverLeaf, IdentityHarnessHost)

	clientConfig, err := ClientConfig(Identity{
		CAFile: clientLeaf.CAFile, CertFile: clientLeaf.CertFile, KeyFile: clientLeaf.KeyFile,
		PeerIdentity: IdentityCore,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := roundTrip(t, address, clientConfig); err != nil {
		t.Fatalf("honest mutual TLS request failed: %v", err)
	}
}

func TestWrongClientSANIsRejected(t *testing.T) {
	pki := newPKI(t)
	serverLeaf := pki.writeLeaf(t, "core", IdentityCore, 0o600)
	// The client presents a leaf whose URI SAN claims to be core, not harness.
	rogueClient := pki.writeLeaf(t, "rogue", IdentityCore, 0o600)
	address := startServer(t, serverLeaf, IdentityHarnessHost)
	clientConfig, err := ClientConfig(Identity{
		CAFile: rogueClient.CAFile, CertFile: rogueClient.CertFile, KeyFile: rogueClient.KeyFile,
		PeerIdentity: IdentityCore,
	})
	if err != nil {
		// Preferred fail-closed point: reject the wrong local identity before
		// the harness begins dialing.
		return
	}
	if err := roundTrip(t, address, clientConfig); err == nil {
		t.Fatal("a client with the wrong URI SAN completed a request")
	}
}

func TestAmbiguousClientSANIsRejected(t *testing.T) {
	pki := newPKI(t)
	serverLeaf := pki.writeLeaf(t, "core", IdentityCore, 0o600)
	ambiguousClient := pki.writeLeafURIs(t, "ambiguous", []string{IdentityHarnessHost, IdentityCore}, 0o600)
	address := startServer(t, serverLeaf, IdentityHarnessHost)
	clientConfig, err := ClientConfig(Identity{
		CAFile: ambiguousClient.CAFile, CertFile: ambiguousClient.CertFile, KeyFile: ambiguousClient.KeyFile,
		PeerIdentity: IdentityCore,
	})
	if err == nil {
		if roundErr := roundTrip(t, address, clientConfig); roundErr == nil {
			t.Fatal("a client with ambiguous URI SAN identities completed a request")
		}
	}
}

func TestWrongCAIsRejected(t *testing.T) {
	pki := newPKI(t)
	other := newPKI(t)
	serverLeaf := pki.writeLeaf(t, "core", IdentityCore, 0o600)
	foreignClient := other.writeLeaf(t, "harness", IdentityHarnessHost, 0o600)
	address := startServer(t, serverLeaf, IdentityHarnessHost)
	clientConfig, err := ClientConfig(Identity{
		CAFile: foreignClient.CAFile, CertFile: foreignClient.CertFile, KeyFile: foreignClient.KeyFile,
		PeerIdentity: IdentityCore,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := roundTrip(t, address, clientConfig); err == nil {
		t.Fatal("a client from a foreign CA completed a request")
	}
}

func TestMaterialGrammarIsEnforced(t *testing.T) {
	pki := newPKI(t)
	worldReadableKey := pki.writeLeaf(t, "loose", IdentityCore, 0o644)
	if _, err := ServerConfig(Identity{CAFile: worldReadableKey.CAFile, CertFile: worldReadableKey.CertFile, KeyFile: worldReadableKey.KeyFile, PeerIdentity: IdentityHarnessHost}); err == nil {
		t.Fatal("world-readable private key accepted")
	}
	if _, err := ServerConfig(Identity{CAFile: "", CertFile: worldReadableKey.CertFile, KeyFile: worldReadableKey.KeyFile, PeerIdentity: IdentityHarnessHost}); err == nil {
		t.Fatal("missing CA accepted")
	}
	if _, err := ServerConfig(Identity{CAFile: pki.caPEM, CertFile: pki.caPEM, KeyFile: pki.caPEM, PeerIdentity: IdentityHarnessHost}); err == nil {
		t.Fatal("certificate-less identity accepted")
	}
	symlink := filepath.Join(t.TempDir(), "linked.key")
	if err := os.Symlink(worldReadableKey.KeyFile, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := ServerConfig(Identity{CAFile: worldReadableKey.CAFile, CertFile: worldReadableKey.CertFile, KeyFile: symlink, PeerIdentity: IdentityHarnessHost}); err == nil {
		t.Fatal("symlinked private key accepted")
	}
	_ = errors.New
}
