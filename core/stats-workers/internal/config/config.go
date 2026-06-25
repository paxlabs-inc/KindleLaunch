// Package config loads and validates the core/stats-workers environment,
// mirroring the TS statsEnvSchema (stats/src/config.ts): identical env var names
// (invariant i8), identical defaults, extending the shared base env
// (shared/config.BaseEnv).
package config

import (
	"fmt"
	"time"

	"github.com/caarlos0/env/v11"

	sharedconfig "github.com/Sidiora-Technologies/KindleLaunch/shared/config"
)

// Config is the parsed stats environment.
type Config struct {
	sharedconfig.BaseEnv

	// HolderRefreshIntervalMS / ActivePoolWindowHours mirror the TS
	// STATS_HOLDER_REFRESH_INTERVAL_MS (default 60000) and
	// STATS_ACTIVE_POOL_WINDOW_HOURS (default 24).
	HolderRefreshIntervalMS int `env:"STATS_HOLDER_REFRESH_INTERVAL_MS" envDefault:"60000"`
	ActivePoolWindowHours   int `env:"STATS_ACTIVE_POOL_WINDOW_HOURS" envDefault:"24"`

	// WebhookHMACSecret is the HMAC-SHA256 secret shared with the indexer fanout
	// publisher (matched-index CSV contract). Required, min 32 chars (zod parity).
	WebhookHMACSecret string `env:"WEBHOOK_HMAC_SECRET,required"`

	// CORSAllowedOrigins is the HTTP CORS allowlist (comma-separated; empty/"*"
	// allows all). Parity with the shared createServer CORS handling.
	CORSAllowedOrigins string `env:"CORS_ALLOWED_ORIGINS"`
}

// Load parses the process environment into a validated Config.
func Load() (Config, error) {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	if err := cfg.BaseEnv.Validate(); err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	if len(cfg.WebhookHMACSecret) < 32 {
		return Config{}, fmt.Errorf("config: WEBHOOK_HMAC_SECRET must be at least 32 characters")
	}
	return cfg, nil
}

// HolderRefreshInterval returns the holder-stats refresh interval as a Duration.
func (c Config) HolderRefreshInterval() time.Duration {
	return time.Duration(c.HolderRefreshIntervalMS) * time.Millisecond
}
