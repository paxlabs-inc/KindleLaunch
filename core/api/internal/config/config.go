// Package config loads and validates the core/api gateway environment.
//
// Unlike the data/processor services, core/api is a pure EDGE: it reads from
// the shared Postgres + Redis (written by the core/* services) and fans data
// out over WSS/SSE, plus a thin rate-limited REST snapshot surface. It needs no
// chain RPC or contract addresses, so it deliberately does NOT embed the full
// shared baseEnvSchema (which marks RPC_URL + the 10 contract addresses as
// required). Env var names that ARE shared (DATABASE_URL, REDIS_URL, PORT,
// LOG_LEVEL, NODE_ENV, CORS_ALLOWED_ORIGINS) keep their canonical names so the
// deploy configs port as-is (invariant i8).
package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
)

// Config is the parsed core/api gateway environment.
type Config struct {
	// DatabaseURL is the shared Postgres the gateway reads from (snapshot REST).
	DatabaseURL string `env:"DATABASE_URL,required"`
	// RedisURL backs both the pub/sub fan-out and the read-through caches.
	RedisURL string `env:"REDIS_URL,required"`

	Port     int    `env:"PORT" envDefault:"3000"`
	LogLevel string `env:"LOG_LEVEL" envDefault:"info"`
	NodeEnv  string `env:"NODE_ENV" envDefault:"production"`

	// CORSAllowedOrigins is the comma-separated CORS allowlist (empty/"*" = all).
	CORSAllowedOrigins string `env:"CORS_ALLOWED_ORIGINS"`

	// --- Rate limiting (public ingress, invariant i12) ---------------------

	// RateLimitMax is the max requests per window per IP/key on the REST surface.
	RateLimitMax int `env:"API_RATE_LIMIT_MAX" envDefault:"100"`
	// RateLimitWindowSec is the rate-limit window in seconds.
	RateLimitWindowSec int `env:"API_RATE_LIMIT_WINDOW_SEC" envDefault:"60"`
	// MaxInFlightRequests caps concurrent in-flight REST requests; over capacity
	// the gateway load-sheds with 503 instead of exhausting memory (i12, no-OOM).
	MaxInFlightRequests int `env:"API_MAX_INFLIGHT_REQUESTS" envDefault:"10000"`

	// --- Realtime connection caps + backpressure (invariant i11, 500K) -----

	// WSMaxConnections caps total concurrent WebSocket connections.
	WSMaxConnections int `env:"WS_MAX_CONNECTIONS" envDefault:"50000"`
	// WSMaxPerIP caps concurrent WebSocket connections from a single IP.
	WSMaxPerIP int `env:"WS_MAX_PER_IP" envDefault:"20"`
	// SSEMaxConnections caps total concurrent SSE connections.
	SSEMaxConnections int `env:"SSE_MAX_CONNECTIONS" envDefault:"50000"`
	// SSEMaxPerIP caps concurrent SSE connections from a single IP.
	SSEMaxPerIP int `env:"SSE_MAX_PER_IP" envDefault:"20"`
	// ClientSendBuffer bounds each connection's outbound queue; a client whose
	// queue overflows is evicted as a slow consumer rather than blocking fan-out.
	ClientSendBuffer int `env:"CLIENT_SEND_BUFFER" envDefault:"256"`
	// CoalesceFlushMS is the RAF-style coalescing window: high-frequency ticks
	// (swaps, candles, pool state) are collapsed to the latest-per-key and
	// flushed at most once per window, so a burst never floods a client.
	CoalesceFlushMS int `env:"COALESCE_FLUSH_MS" envDefault:"100"`

	// --- Auth -------------------------------------------------------------

	// AdminAPIKey, when set, guards privileged routes (e.g. internal metrics);
	// empty leaves those routes unmounted. Public read routes never require it.
	AdminAPIKey string `env:"ADMIN_API_KEY"`
}

var (
	validLevels = map[string]struct{}{"debug": {}, "info": {}, "warn": {}, "error": {}}
	validNodes  = map[string]struct{}{"development": {}, "production": {}, "test": {}}
)

// Load parses the process environment into a validated Config.
func Load() (Config, error) {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}

// Validate enforces field-level rules (URL shape, enums, positive bounds).
func (c *Config) Validate() error {
	var errs []error

	if err := requireURL("DATABASE_URL", c.DatabaseURL); err != nil {
		errs = append(errs, err)
	}
	if err := requireRedisURL("REDIS_URL", c.RedisURL); err != nil {
		errs = append(errs, err)
	}
	if _, ok := validLevels[c.LogLevel]; !ok {
		errs = append(errs, fmt.Errorf("LOG_LEVEL %q must be one of debug|info|warn|error", c.LogLevel))
	}
	if _, ok := validNodes[c.NodeEnv]; !ok {
		errs = append(errs, fmt.Errorf("NODE_ENV %q must be one of development|production|test", c.NodeEnv))
	}
	if c.Port <= 0 || c.Port > 65535 {
		errs = append(errs, fmt.Errorf("PORT %d out of range 1-65535", c.Port))
	}
	for _, p := range []struct {
		name string
		val  int
	}{
		{"API_RATE_LIMIT_MAX", c.RateLimitMax},
		{"API_RATE_LIMIT_WINDOW_SEC", c.RateLimitWindowSec},
		{"API_MAX_INFLIGHT_REQUESTS", c.MaxInFlightRequests},
		{"WS_MAX_CONNECTIONS", c.WSMaxConnections},
		{"WS_MAX_PER_IP", c.WSMaxPerIP},
		{"SSE_MAX_CONNECTIONS", c.SSEMaxConnections},
		{"SSE_MAX_PER_IP", c.SSEMaxPerIP},
		{"CLIENT_SEND_BUFFER", c.ClientSendBuffer},
		{"COALESCE_FLUSH_MS", c.CoalesceFlushMS},
	} {
		if p.val <= 0 {
			errs = append(errs, fmt.Errorf("%s must be positive, got %d", p.name, p.val))
		}
	}

	return errors.Join(errs...)
}

// RateLimitWindow returns the rate-limit window as a time.Duration.
func (c *Config) RateLimitWindow() time.Duration {
	return time.Duration(c.RateLimitWindowSec) * time.Second
}

// CoalesceFlush returns the coalescing window as a time.Duration.
func (c *Config) CoalesceFlush() time.Duration {
	return time.Duration(c.CoalesceFlushMS) * time.Millisecond
}

func requireURL(name, raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("%s %q is not a valid URL", name, raw)
	}
	return nil
}

// requireRedisURL accepts redis:// and rediss:// DSNs (host required).
func requireRedisURL(name, raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "redis" && u.Scheme != "rediss") {
		return fmt.Errorf("%s %q is not a valid redis:// URL", name, raw)
	}
	return nil
}
