package simsvc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
)

// listenerShutdownGrace bounds how long a listener is given to drain on
// shutdown.
const listenerShutdownGrace = 5 * time.Second

// sweepInterval is how often finished runs past their retention window are
// purged.
const sweepInterval = time.Minute

// Serve runs the whole simulator: the containment guard, the capture harness,
// the synthetic sources and the control API. It returns when ctx is done or
// any listener fails.
//
// The start-up ORDER is deliberate. The service-account-token guard runs
// first, before any port is bound, so a pod that was granted an API token
// never reaches a state where it could accept a run. The capture and synthetic
// listeners bind next, so /readyz cannot report ready while a run would have
// nowhere to deliver.
func Serve(ctx context.Context, cfg Config, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if err := CheckServiceAccountToken(cfg.SATokenPath); err != nil {
		return err
	}
	for _, dir := range []string{cfg.RunDir, cfg.StorageDir, cfg.CaptureDir, cfg.LogDir} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("simsvc: create %s: %w", dir, err)
		}
	}

	harness := NewHarness(logger)
	exporter := NewSyntheticExporter()

	// A ListenConfig bound to ctx so a cancelled start-up does not leave a
	// half-bound socket behind.
	lc := &net.ListenConfig{}
	captureLn, err := lc.Listen(ctx, "tcp", cfg.CaptureListen)
	if err != nil {
		return fmt.Errorf("simsvc: bind capture listener: %w", err)
	}
	syntheticLn, err := lc.Listen(ctx, "tcp", cfg.SyntheticListen)
	if err != nil {
		return fmt.Errorf("simsvc: bind synthetic listener: %w", err)
	}
	otlpLn, err := lc.Listen(ctx, "tcp", cfg.OTLPGRPCListen)
	if err != nil {
		return fmt.Errorf("simsvc: bind otlp grpc listener: %w", err)
	}
	syslogLn, err := lc.Listen(ctx, "tcp", cfg.SyslogListen)
	if err != nil {
		return fmt.Errorf("simsvc: bind syslog listener: %w", err)
	}
	controlLn, err := lc.Listen(ctx, "tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("simsvc: bind control listener: %w", err)
	}

	queue := NewQueue(cfg, harness, exporter, logger)
	defer queue.Close()

	server := NewServer(cfg, queue, func() error { return readiness(cfg) }, logger)

	grpcServer := grpc.NewServer()
	registerOTLPGRPC(grpcServer, harness)

	captureSrv := &http.Server{Handler: harness.CaptureMux(), ReadHeaderTimeout: 10 * time.Second}
	syntheticSrv := &http.Server{Handler: exporter.Handler(), ReadHeaderTimeout: 10 * time.Second}
	controlSrv := &http.Server{Handler: server.Handler(), ReadHeaderTimeout: 10 * time.Second}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	g, gctx := errgroup.WithContext(runCtx)
	serveHTTP := func(name string, srv *http.Server, ln net.Listener) func() error {
		return func() error {
			logger.Info("simsvc: listener up", "listener", name, "addr", ln.Addr().String())
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return fmt.Errorf("simsvc: %s listener: %w", name, err)
			}
			return nil
		}
	}
	g.Go(serveHTTP("capture", captureSrv, captureLn))
	g.Go(serveHTTP("synthetic", syntheticSrv, syntheticLn))
	g.Go(serveHTTP("control", controlSrv, controlLn))
	g.Go(func() error {
		logger.Info("simsvc: listener up", "listener", "otlp-grpc", "addr", otlpLn.Addr().String())
		if err := grpcServer.Serve(otlpLn); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			return fmt.Errorf("simsvc: otlp grpc listener: %w", err)
		}
		return nil
	})
	g.Go(func() error {
		logger.Info("simsvc: listener up", "listener", "syslog", "addr", syslogLn.Addr().String())
		serveSyslog(gctx, syslogLn, harness, logger)
		return nil
	})
	g.Go(func() error {
		ticker := time.NewTicker(sweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-gctx.Done():
				return nil
			case <-ticker.C:
				if n := queue.Sweep(); n > 0 {
					logger.Info("simsvc: purged expired runs", "count", n)
				}
			}
		}
	})
	g.Go(func() error {
		<-gctx.Done()
		// A fresh context on purpose: gctx is already cancelled, and passing it
		// to Shutdown would abort every in-flight capture instead of draining.
		shutdownCtx, stop := context.WithTimeout(context.WithoutCancel(gctx), listenerShutdownGrace)
		defer stop()
		_ = captureSrv.Shutdown(shutdownCtx)   //nolint:errcheck // shutting down; a drain timeout is not actionable
		_ = syntheticSrv.Shutdown(shutdownCtx) //nolint:errcheck // same
		_ = controlSrv.Shutdown(shutdownCtx)   //nolint:errcheck // same
		grpcServer.Stop()
		_ = syslogLn.Close() //nolint:errcheck // same
		return nil
	})
	return g.Wait()
}

// readiness asserts the two things every run depends on: an executable Alloy
// binary and writable run storage. A simulator that reports ready without them
// answers every run with a failure the user cannot act on.
func readiness(cfg Config) error {
	info, err := os.Stat(cfg.AlloyBinary)
	if err != nil {
		return fmt.Errorf("alloy binary %s: %w", cfg.AlloyBinary, err)
	}
	if info.Mode()&0o111 == 0 {
		return fmt.Errorf("alloy binary %s is not executable", cfg.AlloyBinary)
	}
	for _, dir := range []string{cfg.RunDir, cfg.StorageDir, cfg.LogDir, cfg.CaptureDir} {
		probe, err := os.CreateTemp(dir, ".readyz-*")
		if err != nil {
			return fmt.Errorf("directory %s is not writable: %w", dir, err)
		}
		name := probe.Name()
		_ = probe.Close()   //nolint:errcheck // probe file; removal below is what matters
		_ = os.Remove(name) //nolint:errcheck // best effort on a tmpfs
	}
	return nil
}
