// Command gateway runs the MithyaX API gateway.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/vamshireddy02/mithyax/gateway/internal/config"
	"github.com/vamshireddy02/mithyax/gateway/internal/httpserver"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-healthcheck" {
		os.Exit(healthcheck())
	}

	if err := run(); err != nil {
		slog.Error("gateway exited with error", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

// healthcheck backs the container's own HEALTHCHECK (see
// deployments/docker/docker-compose.yml): the gateway ships in
// gcr.io/distroless/static-debian12, which has no shell, wget, or curl
// for a normal exec-based check to run, so the binary checks itself by
// hitting its own GET /health over loopback and mapping the response to
// a process exit code.
func healthcheck() int {
	cfg, err := config.Load()
	if err != nil {
		return 1
	}
	resp, err := http.Get("http://localhost:" + cfg.Port + "/health")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := newLogger(cfg.LogLevel)
	slog.SetDefault(logger)

	logger.Info("starting gateway",
		slog.String("environment", cfg.Environment),
		slog.String("addr", cfg.Addr()),
	)

	srv, err := httpserver.New(cfg, logger)
	if err != nil {
		return err
	}

	// Run outside httpserver.New so a DB-touching failure here can never
	// affect building a Server in tests that don't configure a real
	// database (see httpserver.New's doc comment).
	migrateCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	err = srv.DB.Migrate(migrateCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("run database migrations: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		if err := srv.HTTP.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	logger.Info("shutdown signal received, draining in-flight requests",
		slog.Duration("timeout", cfg.ShutdownTimeout),
	)

	httpShutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := srv.HTTP.Shutdown(httpShutdownCtx); err != nil {
		return err
	}

	// Stop accepting new analyze jobs and let queued/in-flight ones
	// finish. This gets its own, longer budget: a video analysis can
	// take far longer than draining HTTP connections should.
	logger.Info("waiting for in-flight analyze jobs to finish",
		slog.Duration("timeout", cfg.WorkerShutdownTimeout),
	)

	workerShutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.WorkerShutdownTimeout)
	defer cancel()

	if err := srv.Pool.Shutdown(workerShutdownCtx); err != nil {
		return err
	}

	// Stop the async video/audio worker pools (Phase 7.5) the same way:
	// let an in-flight job finish rather than cutting it off. Bounded
	// by that job's own timeout, not a separate shutdown deadline —
	// see Server.StopWorkers.
	logger.Info("waiting for in-flight async analysis jobs to finish")
	srv.StopWorkers()

	if err := srv.Redis.Close(); err != nil {
		logger.Warn("failed to close redis client cleanly", slog.String("error", err.Error()))
	}
	srv.DB.Close()

	logger.Info("gateway stopped")
	return nil
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = slog.LevelInfo
	}

	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	return slog.New(handler)
}
