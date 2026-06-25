// Package config loads and validates the core/ranking-algo environment,
// mirroring the TS rankingEnvSchema (ranking-algo/src/config.ts): identical env
// var names (invariant i8) and defaults, extending the shared base env
// (shared/config.BaseEnv).
package config

import (
	"fmt"
	"time"

	"github.com/caarlos0/env/v11"

	sharedconfig "github.com/Sidiora-Technologies/KindleLaunch/shared/config"
)

// Config is the parsed ranking-algo environment.
type Config struct {
	sharedconfig.BaseEnv

	// RankingTickIntervalMS is the cadence for the heavy rankers (trending,
	// breakout, top-volume, movers, unusual). Parity default 30000ms.
	RankingTickIntervalMS int `env:"RANKING_TICK_INTERVAL_MS" envDefault:"30000"`
	// RankingNewTickIntervalMS is the cadence for the new-pools ranker. Parity
	// default 10000ms.
	RankingNewTickIntervalMS int `env:"RANKING_NEW_TICK_INTERVAL_MS" envDefault:"10000"`
	// RankingMaxEntries caps how many entries each ranked Redis ZSET holds.
	// Parity default 200.
	RankingMaxEntries int `env:"RANKING_MAX_ENTRIES" envDefault:"200"`

	// CORSAllowedOrigins is the comma-separated CORS allowlist for the HTTP API.
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
	if cfg.RankingTickIntervalMS <= 0 {
		return Config{}, fmt.Errorf("config: RANKING_TICK_INTERVAL_MS must be positive")
	}
	if cfg.RankingNewTickIntervalMS <= 0 {
		return Config{}, fmt.Errorf("config: RANKING_NEW_TICK_INTERVAL_MS must be positive")
	}
	if cfg.RankingMaxEntries <= 0 {
		return Config{}, fmt.Errorf("config: RANKING_MAX_ENTRIES must be positive")
	}
	return cfg, nil
}

// TickInterval returns the heavy-ranker cadence as a time.Duration.
func (c Config) TickInterval() time.Duration {
	return time.Duration(c.RankingTickIntervalMS) * time.Millisecond
}

// NewTickInterval returns the new-pools ranker cadence as a time.Duration.
func (c Config) NewTickInterval() time.Duration {
	return time.Duration(c.RankingNewTickIntervalMS) * time.Millisecond
}
