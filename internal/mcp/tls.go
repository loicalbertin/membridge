package mcp

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

// TLSMode selects how an HTTP transport secures its listener.
type TLSMode string

const (
	// TLSModeOff serves plain HTTP (the historical behavior and default).
	TLSModeOff TLSMode = "off"
	// TLSModeManual serves HTTPS with a certificate and key loaded from disk.
	TLSModeManual TLSMode = "manual"
	// TLSModeAuto serves HTTPS with a Let's Encrypt certificate obtained via
	// ACME (TLS-ALPN-01 on the TLS listener, HTTP-01 on AcmeHTTPAddr).
	TLSModeAuto TLSMode = "auto"
)

// TLSOptions configures TLS for the HTTP transports. The zero value means
// "off", so an unconfigured server keeps serving plain HTTP.
type TLSOptions struct {
	Mode         TLSMode
	CertFile     string // manual
	KeyFile      string // manual
	Domain       string // auto: public DNS name for the certificate
	CacheDir     string // auto: persistent certificate cache
	AcmeHTTPAddr string // auto: HTTP-01 challenge and redirect listener
}

// serve runs srv on ln until ctx is cancelled, then shuts it down gracefully.
// Depending on opts.Mode it serves plain HTTP, HTTPS with files from disk, or
// HTTPS with a Let's Encrypt certificate; the latter also runs the HTTP-01
// challenge listener. It returns nil on a clean shutdown.
func serve(ctx context.Context, srv *http.Server, ln net.Listener, opts TLSOptions, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}

	var acmeSrv *http.Server
	var run func() error

	switch opts.Mode {
	case TLSModeManual:
		cert, err := tls.LoadX509KeyPair(opts.CertFile, opts.KeyFile)
		if err != nil {
			return fmt.Errorf("loading TLS key pair: %w", err)
		}
		srv.TLSConfig = &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{cert},
		}
		run = func() error { return srv.ServeTLS(ln, "", "") }

	case TLSModeAuto:
		acmeAddr := opts.AcmeHTTPAddr
		if acmeAddr == "" {
			acmeAddr = ":80"
		}
		mgr := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(opts.Domain),
			Cache:      autocert.DirCache(opts.CacheDir),
		}
		srv.TLSConfig = mgr.TLSConfig()

		acmeLn, err := net.Listen("tcp", acmeAddr)
		if err != nil {
			return fmt.Errorf("listen ACME HTTP %s: %w", acmeAddr, err)
		}
		acmeSrv = &http.Server{
			Handler:           mgr.HTTPHandler(nil),
			ReadHeaderTimeout: 10 * time.Second,
		}
		go func() {
			if err := acmeSrv.Serve(acmeLn); err != nil && err != http.ErrServerClosed {
				log.Error("ACME HTTP listener exited", "err", err)
			}
		}()
		log.Info("ACME HTTP-01 listener starting", "addr", acmeAddr)
		run = func() error { return srv.ServeTLS(ln, "", "") }

	default:
		run = func() error { return srv.Serve(ln) }
	}

	errCh := make(chan error, 1)
	go func() { errCh <- run() }()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if acmeSrv != nil {
			_ = acmeSrv.Shutdown(shutCtx)
		}
		if err := srv.Shutdown(shutCtx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		return nil
	case err := <-errCh:
		if acmeSrv != nil {
			_ = acmeSrv.Close()
		}
		if err != http.ErrServerClosed {
			return err
		}
		return nil
	}
}
