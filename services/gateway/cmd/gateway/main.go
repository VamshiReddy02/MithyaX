// Command gateway runs the MithyaX API gateway.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/vamshireddy02/mithyax/gateway/internal/config"
	"github.com/vamshireddy02/mithyax/gateway/internal/httpserver"
)

func main() {
	if err := run(); err != nil {
		slog.Error("gateway exited with error", slog.String("error", err.Error()))
		os.Exit(1)
	}
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

	srv := httpserver.New(cfg, logger)

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

	if err := srv.Redis.Close(); err != nil {
		logger.Warn("failed to close redis client cleanly", slog.String("error", err.Error()))
	}

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
