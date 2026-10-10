package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/homescreative3d-lang/Health-Tracker-App-Worker/internal/config"
	"github.com/homescreative3d-lang/Health-Tracker-App-Worker/internal/notifier"
	"github.com/homescreative3d-lang/Health-Tracker-App-Worker/internal/scheduler"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(2)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parseLevel(cfg.LogLevel)}))
	slog.SetDefault(logger)

	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		slog.Error("open database", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(30 * time.Minute)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		slog.Error("database connection failed", "error", err)
		os.Exit(1)
	}

	push := notifier.NewWebPush(cfg.WebPushPublicKey, cfg.WebPushPrivateKey, cfg.WebPushSubject)
	job := scheduler.New(db, push, cfg.FallbackTimezone, logger)
	logger.Info("notification worker started", "poll_interval", cfg.PollInterval.String())

	if err := job.Run(ctx, cfg.PollInterval); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("worker stopped unexpectedly", "error", err)
		os.Exit(1)
	}
	logger.Info("notification worker stopped")
}

func parseLevel(value string) slog.Level {
	switch strings.ToLower(value) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
