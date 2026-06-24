// Package config loads and validates the core/trading-charts environment,
// mirroring the TS candlesEnvSchema (candles/src/config.ts): identical env var
// names (invariant i8), extending the shared base env (shared/config.BaseEnv).
package config

import (
	"fmt"

	"github.com/caarlos0/env/v11"

	sharedconfig "github.com/Sidiora-Technologies/KindleLaunch/shared/config"
)

// Config is the parsed candles environment.
type Config struct {
	sharedconfig.BaseEnv

	WebhookHMACSecret string `env:"WEBHOOK_HMAC_SECRET,required"`

	// WebSocket connection limits (parity with ws-candles.ts env vars).
	WSMaxConnections int `env:"WS_MAX_CONNECTIONS" envDefault:"10000"`
	WSMaxPerIP       int `env:"WS_MAX_PER_IP" envDefault:"20"`

	// CORS allowlist for the HTTP server.
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
