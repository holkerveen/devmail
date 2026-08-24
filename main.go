package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"
)

// version is stamped at build time with -ldflags "-X main.version=...". It is
// reported by /healthz so a stale deploy is visible without shelling in.
var version = "dev"

// shutdownGrace bounds the graceful phase. go-smtp's Shutdown waits on a
// WaitGroup that only drains when each connection's handler returns, and it
// does NOT close idle connections the way http.Server does -- a mail library
// holding a pooled idle connection would otherwise block SIGTERM until that
// connection's ReadTimeout expires. Past this deadline we force Close().
const shutdownGrace = 10 * time.Second

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	cfg, err := Load()
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}
	log.Info("devmail starting", "version", version, "config", cfg.String())

	// Keep Go's GC target consistent with the ring budget. Without this the
	// default GOGC=100 lets the heap reach roughly twice the live set before
	// collecting, so a pod sized from MaxTotalBytes would OOM at rest.
	if os.Getenv("GOMEMLIMIT") == "" {
		limit := cfg.MaxTotalBytes * 3
		debug.SetMemoryLimit(limit)
		log.Info("memory limit set from DEVMAIL_MAX_TOTAL_BYTES", "bytes", limit)
	}

	store := NewStore(cfg.MaxMessages, cfg.MaxTotalBytes)

	tlsCfg, err := TLSConfig(cfg, log)
	if err != nil {
		log.Error("tls", "err", err)
		os.Exit(1)
	}

	// smtpReady gates /healthz. See NewHandler for why an HTTP-only health
	// check is actively harmful here.
	var smtpReady atomic.Bool

	smtpSrv := NewSMTPServer(cfg, store, tlsCfg, log)
	httpSrv := &http.Server{
		Addr:              ":" + strconv.Itoa(cfg.HTTPPort),
		Handler:           NewHandler(cfg, store, &smtpReady, log),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// Either listener dying is fatal. A devmail with a live UI and a dead SMTP
	// port looks healthy while silently accepting nothing.
	errc := make(chan error, 2)

	go func() {
		ln, err := tlsListen(cfg, tlsCfg)
		if err != nil {
			errc <- err
			return
		}
		smtpReady.Store(true)
		log.Info("smtp: listening", "addr", ln.Addr().String(), "implicitTLS", true)
		errc <- smtpSrv.Serve(ln)
	}()

	go func() {
		log.Info("http: listening", "addr", httpSrv.Addr)
		err := httpSrv.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errc <- err
	}()

	select {
	case <-ctx.Done():
		log.Info("shutdown: signal received")
	case err := <-errc:
		if err != nil {
			log.Error("listener failed", "err", err)
			os.Exit(1)
		}
	}

	shutdown(smtpSrv, httpSrv, log)
}

func shutdown(smtpSrv smtpShutdowner, httpSrv *http.Server, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()

	if err := httpSrv.Shutdown(ctx); err != nil {
		log.Warn("http: graceful shutdown failed", "err", err)
	}

	if err := smtpSrv.Shutdown(ctx); err != nil {
		// Shutdown returns ctx.Err() and leaves connections OPEN on expiry.
		// Unlike http.Server there is no fallback inside the library, so the
		// forced close has to happen here or the process hangs until SIGKILL.
		log.Warn("smtp: graceful shutdown timed out, forcing close", "err", err)
		if err := smtpSrv.Close(); err != nil {
			log.Warn("smtp: forced close failed", "err", err)
		}
	}
	log.Info("shutdown: complete")
}

// smtpShutdowner is the slice of *smtp.Server that shutdown needs, so the
// shutdown path is testable without a live listener.
type smtpShutdowner interface {
	Shutdown(context.Context) error
	Close() error
}
