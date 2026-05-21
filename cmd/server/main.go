package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dhruvgandhi/rate-limiter/api"
	"github.com/dhruvgandhi/rate-limiter/internal/config"
	"github.com/dhruvgandhi/rate-limiter/internal/middleware"
	"github.com/dhruvgandhi/rate-limiter/internal/store"
)

func main() {
	configPath := flag.String("config", "", "path to YAML config (env vars override)")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		// Logger isn't built yet — fall back to stderr.
		slog.Error("config load failed", "err", err)
		os.Exit(1)
	}

	logger := buildLogger(cfg.Logging)
	slog.SetDefault(logger)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	rdb, err := store.NewRedis(ctx, cfg.Redis)
	cancel()
	if err != nil {
		logger.Error("redis connection failed", "addr", cfg.Redis.Addr, "err", err)
		os.Exit(1)
	}
	defer rdb.Close()

	limiter, err := middleware.New(cfg, rdb)
	if err != nil {
		logger.Error("middleware init failed", "err", err)
		os.Exit(1)
	}

	var adminAuth api.AdminAuth
	if cfg.Admin.Token != "" {
		adminAuth = &api.BearerTokenAuth{Token: cfg.Admin.Token}
	} else {
		logger.Warn("admin endpoints have NO AUTH — set admin.token or ADMIN_TOKEN before production")
	}

	handler := api.NewHandler(limiter, rdb, logger, adminAuth)
	srv := &http.Server{
		Addr:         cfg.Server.Addr,
		Handler:      handler.Routes(),
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
	}

	go func() {
		logger.Info("server starting",
			"addr", cfg.Server.Addr,
			"fail_mode", cfg.FailMode,
			"rules", len(cfg.Rules),
			"admin_auth", adminAuth != nil,
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("listen failed", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	sig := <-stop
	logger.Info("shutting down", "signal", sig.String())

	shCtx, shCancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer shCancel()
	if err := srv.Shutdown(shCtx); err != nil {
		logger.Error("shutdown error", "err", err)
	}
}

// buildLogger configures slog based on user config. JSON in prod is the only
// format that survives a real log pipeline; text is for local dev.
func buildLogger(cfg config.LoggingConfig) *slog.Logger {
	var level slog.Level
	switch strings.ToLower(cfg.Level) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if strings.ToLower(cfg.Format) == "text" {
		h = slog.NewTextHandler(os.Stdout, opts)
	} else {
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(h).With("service", "rate-limiter")
}
