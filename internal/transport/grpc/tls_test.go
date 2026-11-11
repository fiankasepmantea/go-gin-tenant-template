package grpc

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/auth"
	authv1 "github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/grpc/gen/tenantapp/auth/v1"
)

type pki struct {
	dir        string
	caPEM      []byte
	caCert     *x509.Certificate
	caKey      *ecdsa.PrivateKey
	serial     int64
	serverCert string
	serverKey  string
}

func newPKI(t *testing.T) *pki {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(der)
	p := &pki{dir: t.TempDir(), caCert: ca, caKey: key, serial: 1,
		caPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
	p.serverCert, p.serverKey = p.issue(t, "server", x509.ExtKeyUsageServerAuth)
	return p
}

// issue writes a certificate signed by the CA and returns the file names of the certificate and its key.
func (p *pki) issue(t *testing.T, name string, usage x509.ExtKeyUsage) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p.serial++
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(p.serial), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage},
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, p.caCert, &key.PublicKey, p.caKey)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	certFile, keyFile = filepath.Join(p.dir, name+".crt"), filepath.Join(p.dir, name+".key")
	must(t, os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	must(t, os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600))
	return certFile, keyFile
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// serveTLS starts the server on a real TCP port and returns its address.
func serveTLS(t *testing.T, cfg *tls.Config) string {
	t.Helper()
	as, err := NewAuthServer(fakeTokens{tokTenant: {UserID: "u1", TenantID: tenantID, TokenID: "s", Level: auth.LevelTenant}},
		&fakeAuthorizer{}, DBTx(nil), nil)
	must(t, err)
	srv, err := NewServer(Options{Auth: as, TLS: cfg})
	must(t, err)
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	must(t, err)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.Shutdown(time.Second) })
	return lis.Addr().String()
}

func validate(addr string, creds credentials.TransportCredentials) error {
	conn, err := grpclib.NewClient(addr, grpclib.WithTransportCredentials(creds))
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = authv1.NewAuthServiceClient(conn).Validate(ctx, &authv1.ValidateRequest{AccessToken: tokTenant})
	return err
}

func TestTLS(t *testing.T) {
	p := newPKI(t)
	cfg, err := TLSConfig(p.serverCert, p.serverKey, "")
	must(t, err)
	addr := serveTLS(t, cfg)

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(p.caPEM)
	if err := validate(addr, credentials.NewTLS(&tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS12})); err != nil {
		t.Fatalf("TLS client: %v", err)
	}
	// a client that does not trust the CA is refused
	if err := validate(addr, credentials.NewTLS(&tls.Config{ServerName: "localhost", MinVersion: tls.VersionTLS12})); err == nil {
		t.Fatal("an untrusted server certificate must fail")
	}
}

func TestMutualTLS(t *testing.T) {
	p := newPKI(t)
	caFile := filepath.Join(p.dir, "ca.pem")
	must(t, os.WriteFile(caFile, p.caPEM, 0o600))
	cfg, err := TLSConfig(p.serverCert, p.serverKey, caFile)
	must(t, err)
	addr := serveTLS(t, cfg)

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(p.caPEM)
	base := tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS12}

	// without a client certificate the call fails
	noCert := base.Clone()
	if err := validate(addr, credentials.NewTLS(noCert)); err == nil {
		t.Fatal("mTLS: a client without certificate must be refused")
	}
	// a certificate from another CA fails as well
	other := newPKI(t)
	certFile, keyFile := other.issue(t, "intruder", x509.ExtKeyUsageClientAuth)
	badPair, err := tls.LoadX509KeyPair(certFile, keyFile)
	must(t, err)
	bad := base.Clone()
	bad.Certificates = []tls.Certificate{badPair}
	if err := validate(addr, credentials.NewTLS(bad)); err == nil {
		t.Fatal("mTLS: a certificate of another CA must be refused")
	}
	// the right certificate works
	cc, ck := p.issue(t, "client", x509.ExtKeyUsageClientAuth)
	pair, err := tls.LoadX509KeyPair(cc, ck)
	must(t, err)
	good := base.Clone()
	good.Certificates = []tls.Certificate{pair}
	if err := validate(addr, credentials.NewTLS(good)); err != nil {
		t.Fatalf("mTLS client: %v", err)
	}
}

func TestPlaintextClientCannotTalkToTLSServer(t *testing.T) {
	p := newPKI(t)
	cfg, err := TLSConfig(p.serverCert, p.serverKey, "")
	must(t, err)
	addr := serveTLS(t, cfg)
	conn, err := grpclib.NewClient(addr, grpclib.WithTransportCredentials(plain()))
	must(t, err)
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = authv1.NewAuthServiceClient(conn).Validate(ctx, &authv1.ValidateRequest{AccessToken: tokTenant})
	if c := codeOf(err); c == codes.OK {
		t.Fatal("a plaintext client must not reach a TLS server")
	}
}

func TestTLSConfigErrors(t *testing.T) {
	p := newPKI(t)
	if _, err := TLSConfig("/nonexistent.crt", "/nonexistent.key", ""); err == nil {
		t.Error("missing certificate files must be an error")
	}
	if _, err := TLSConfig(p.serverCert, p.serverKey, "/nonexistent-ca.pem"); err == nil {
		t.Error("missing CA file must be an error")
	}
	notPEM := filepath.Join(p.dir, "junk.pem")
	must(t, os.WriteFile(notPEM, []byte("junk"), 0o600))
	if _, err := TLSConfig(p.serverCert, p.serverKey, notPEM); err == nil {
		t.Error("a CA file without a certificate must be an error")
	}
}
