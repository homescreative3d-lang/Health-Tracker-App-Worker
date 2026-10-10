package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL     string
	WebPushPublicKey string
	WebPushPrivateKey string
	WebPushSubject  string
	PollInterval    time.Duration
	LogLevel        string
	FallbackTimezone string
}

func Load() (Config, error) {
	cfg := Config{
		DatabaseURL:      strings.TrimSpace(os.Getenv("DATABASE_URL")),
		WebPushPublicKey: strings.TrimSpace(os.Getenv("WEBPUSH_PUBLIC_KEY")),
		WebPushPrivateKey: strings.TrimSpace(os.Getenv("WEBPUSH_PRIVATE_KEY")),
		WebPushSubject:   strings.TrimSpace(os.Getenv("WEBPUSH_SUBJECT")),
		LogLevel:         strings.TrimSpace(os.Getenv("LOG_LEVEL")),
		FallbackTimezone: strings.TrimSpace(os.Getenv("APP_TIMEZONE_FALLBACK")),
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	u, err := url.Parse(cfg.DatabaseURL)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return Config{}, fmt.Errorf("DATABASE_URL must be a valid postgres:// or postgresql:// URL")
	}
	if cfg.WebPushPublicKey == "" || cfg.WebPushPrivateKey == "" {
		return Config{}, fmt.Errorf("WEBPUSH_PUBLIC_KEY and WEBPUSH_PRIVATE_KEY are required")
	}
	if cfg.WebPushSubject == "" {
		cfg.WebPushSubject = "mailto:notifications@tended.app"
	}
	if cfg.PollInterval, err = durationEnv("POLL_INTERVAL", 15*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.PollInterval < 5*time.Second {
		return Config{}, fmt.Errorf("POLL_INTERVAL must be at least 5s")
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}
	if cfg.FallbackTimezone == "" {
		cfg.FallbackTimezone = "Asia/Kolkata"
	}
	if _, err := time.LoadLocation(cfg.FallbackTimezone); err != nil {
		return Config{}, fmt.Errorf("invalid APP_TIMEZONE_FALLBACK: %w", err)
	}
	return cfg, nil
}

func durationEnv(key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a Go duration such as 15s: %w", key, err)
	}
	return value, nil
}
