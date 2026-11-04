package mcp

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeSelfSignedCert creates a throwaway certificate valid for 127.0.0.1 and
// returns the PEM file paths. Used to exercise the manual TLS mode offline.
func writeSelfSignedCert(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshalling key: %v", err)
	}

	dir := t.TempDir()
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("writing certificate: %v", err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("writing key: %v", err)
	}
	return certFile, keyFile
}

// waitHealthy polls url until it answers 200 or the deadline expires.
func waitHealthy(t *testing.T, client *http.Client, url string) *http.Response {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			return resp
		}
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("GET %s never succeeded: %v", url, lastErr)
	return nil
}

// runServe starts serve in the background and returns its result channel.
func runServe(ctx context.Context, srv *http.Server, ln net.Listener, opts TLSOptions) chan error {
	done := make(chan error, 1)
	go func() { done <- serve(ctx, srv, ln, opts, discardLogger()) }()
	return done
}

func waitServeDone(t *testing.T, done chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serve returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return after cancel")
	}
}

func healthMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	return mux
}

func TestServePlainHTTP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: healthMux(), ReadHeaderTimeout: 5 * time.Second}

	ctx, cancel := context.WithCancel(context.Background())
	done := runServe(ctx, srv, ln, TLSOptions{}) // zero value = off

	client := &http.Client{Timeout: 5 * time.Second}
	resp := waitHealthy(t, client, "http://"+ln.Addr().String()+"/health")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if resp.TLS != nil {
		t.Error("plain HTTP response unexpectedly negotiated TLS")
	}

	cancel()
	waitServeDone(t, done)
}

func TestServeManualTLS(t *testing.T) {
	certFile, keyFile := writeSelfSignedCert(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: healthMux(), ReadHeaderTimeout: 5 * time.Second}

	ctx, cancel := context.WithCancel(context.Background())
	done := runServe(ctx, srv, ln, TLSOptions{
		Mode:     TLSModeManual,
		CertFile: certFile,
		KeyFile:  keyFile,
	})

	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
	resp := waitHealthy(t, client, "https://"+ln.Addr().String()+"/health")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if resp.TLS == nil {
		t.Error("response is not over TLS")
	} else if resp.TLS.Version < tls.VersionTLS12 {
		t.Errorf("TLS version = %x, want >= TLS 1.2", resp.TLS.Version)
	}

	cancel()
	waitServeDone(t, done)
}

func TestServeManualTLSMissingFiles(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	err = serve(context.Background(), &http.Server{}, ln, TLSOptions{
		Mode:     TLSModeManual,
		CertFile: filepath.Join(t.TempDir(), "missing-cert.pem"),
		KeyFile:  filepath.Join(t.TempDir(), "missing-key.pem"),
	}, discardLogger())
	if err == nil || !strings.Contains(err.Error(), "loading TLS key pair") {
		t.Errorf("serve() = %v, want a key pair loading error", err)
	}
}

func TestServeAutoUnlistenableAcmeAddr(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	err = serve(context.Background(), &http.Server{}, ln, TLSOptions{
		Mode:         TLSModeAuto,
		Domain:       "mcp.example.com",
		CacheDir:     t.TempDir(),
		AcmeHTTPAddr: "not-a-valid-address",
	}, discardLogger())
	if err == nil || !strings.Contains(err.Error(), "ACME") {
		t.Errorf("serve() = %v, want an ACME listener error", err)
	}
}
