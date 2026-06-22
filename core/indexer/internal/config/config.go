// Package config loads and validates the core/indexer environment, mirroring the
// TS indexerEnvSchema (indexer/src/config.ts) one-to-one: identical env var
// names (invariant i8 — matches indexer/ecosystem.config.cjs / deploy env),
// defaults, and cross-field validation. It extends the shared base env
// (shared/config.BaseEnv) exactly as the TS schema extends baseEnvSchema.
//
// Meta-AG contract addresses are intentionally NOT modelled here: meta-ag is
// excluded from the new chain (L4), so the optional TRANSACTION_TRACKER_ADDRESS
// etc. fields are dropped.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/caarlos0/env/v11"

	sharedconfig "github.com/Sidiora-Technologies/KindleLaunch/shared/config"
)

// Log-source modes (INDEXER_LOG_SOURCE / INDEXER_HEAD_SOURCE).
const (
	SourcePaxscan    = "paxscan"
	SourceRPC        = "rpc"
	SourceRPCGetLogs = "rpc-getlogs"
)

// Indexer run modes (INDEXER_MODE).
const (
	ModeLive     = "live"
	ModeBackfill = "backfill"
)

// Config is the parsed indexer environment. The embedded BaseEnv carries the
// fields shared by every service (DATABASE_URL, REDIS_URL, RPC_URL, CHAIN_ID,
// the contract address book, LOG_LEVEL, PORT, ...). Field order follows
// config.ts.
type Config struct {
	sharedconfig.BaseEnv

	IndexerStartBlock     int64 `env:"INDEXER_START_BLOCK" envDefault:"0"`
	IndexerBatchSize      int   `env:"INDEXER_BATCH_SIZE" envDefault:"50"`
	IndexerPollIntervalMS int   `env:"INDEXER_POLL_INTERVAL_MS" envDefault:"100"`
	IndexerConcurrency    int   `env:"INDEXER_CONCURRENCY" envDefault:"25"`

	// Webhook fan-out targets + signing (matched-index CSV arrays).
	WebhookURLs        []string `env:"WEBHOOK_URLS" envSeparator:","`
	WebhookHMACSecret  string   `env:"WEBHOOK_HMAC_SECRET"`
	WebhookHMACSecrets []string `env:"WEBHOOK_HMAC_SECRETS" envSeparator:","`

	// Backfill configuration.
	IndexerMode         string `env:"INDEXER_MODE" envDefault:"live"`
	BackfillFromBlock   *int64 `env:"BACKFILL_FROM_BLOCK"`
	BackfillBatchSize   int    `env:"BACKFILL_BATCH_SIZE" envDefault:"2000"`
	BackfillConcurrency int    `env:"BACKFILL_CONCURRENCY" envDefault:"20"`
	BackfillOnly        bool   `env:"BACKFILL_ONLY" envDefault:"false"`

	// Log-source selection.
	PaxscanDatabaseURL string `env:"PAXSCAN_DATABASE_URL"`
	IndexerLogSource   string `env:"INDEXER_LOG_SOURCE" envDefault:"paxscan"`
	IndexerHeadSource  string `env:"INDEXER_HEAD_SOURCE"`

	// rpc-getlogs configuration.
	RPCURLs                      []string `env:"RPC_URLS" envSeparator:","`
	RPCGetLogsTimeoutMS          int      `env:"RPC_GETLOGS_TIMEOUT_MS" envDefault:"5000"`
	RPCGetLogsStaleThreshold     int      `env:"RPC_GETLOGS_STALE_THRESHOLD" envDefault:"50"`
	RPCGetLogsHealthIntervalMS   int      `env:"RPC_GETLOGS_HEALTH_INTERVAL_MS" envDefault:"30000"`
	RPCGetLogsReceiptConcurrency int      `env:"RPC_GETLOGS_RECEIPT_CONCURRENCY" envDefault:"10"`

	// Optional CORS allowlist for the status server (comma-separated; empty/"*"
	// allows all). Not in the TS indexer schema; added for the Go status edge.
	CORSAllowedOrigins string `env:"CORS_ALLOWED_ORIGINS"`
}

var (
	validModes   = map[string]struct{}{ModeLive: {}, ModeBackfill: {}}
	validSources = map[string]struct{}{SourcePaxscan: {}, SourceRPC: {}, SourceRPCGetLogs: {}}
)

// Load parses the process environment into a validated Config.
func Load() (Config, error) {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	cfg.WebhookURLs = cleanList(cfg.WebhookURLs)
	cfg.WebhookHMACSecrets = cleanList(cfg.WebhookHMACSecrets)
	cfg.RPCURLs = cleanList(cfg.RPCURLs)
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}

// HeadSource resolves the effective chain-head source: INDEXER_HEAD_SOURCE when
// set, otherwise INDEXER_LOG_SOURCE (parity with config.ts).
func (c *Config) HeadSource() string {
	if c.IndexerHeadSource != "" {
		return c.IndexerHeadSource
	}
	return c.IndexerLogSource
}

// Validate enforces the same field-level + cross-field rules as the zod
// indexerEnvSchema (including its superRefine block).
func (c *Config) Validate() error {
	var errs []error
	if err := c.BaseEnv.Validate(); err != nil {
		errs = append(errs, err)
	}

	if _, ok := validModes[c.IndexerMode]; !ok {
		errs = append(errs, fmt.Errorf("INDEXER_MODE %q must be one of live|backfill", c.IndexerMode))
	}
	if _, ok := validSources[c.IndexerLogSource]; !ok {
		errs = append(errs, fmt.Errorf("INDEXER_LOG_SOURCE %q must be one of paxscan|rpc|rpc-getlogs", c.IndexerLogSource))
	}
	if c.IndexerHeadSource != "" {
		if _, ok := validSources[c.IndexerHeadSource]; !ok {
			errs = append(errs, fmt.Errorf("INDEXER_HEAD_SOURCE %q must be one of paxscan|rpc|rpc-getlogs", c.IndexerHeadSource))
		}
	}
	if c.PaxscanDatabaseURL != "" {
		if err := requireURL("PAXSCAN_DATABASE_URL", c.PaxscanDatabaseURL); err != nil {
			errs = append(errs, err)
		}
	}

	// superRefine: log + head source dependencies.
	if c.IndexerLogSource == SourcePaxscan && c.PaxscanDatabaseURL == "" {
		errs = append(errs, errors.New("PAXSCAN_DATABASE_URL is required when INDEXER_LOG_SOURCE=paxscan"))
	}
	if c.IndexerLogSource == SourceRPCGetLogs && len(c.RPCURLs) == 0 {
		errs = append(errs, errors.New("RPC_URLS (comma-separated) is required when INDEXER_LOG_SOURCE=rpc-getlogs"))
	}
	head := c.HeadSource()
	if head == SourcePaxscan && c.PaxscanDatabaseURL == "" {
		errs = append(errs, errors.New("PAXSCAN_DATABASE_URL is required when INDEXER_HEAD_SOURCE=paxscan"))
	}
	if head == SourceRPCGetLogs && len(c.RPCURLs) == 0 {
		errs = append(errs, errors.New("RPC_URLS (comma-separated) is required when INDEXER_HEAD_SOURCE=rpc-getlogs"))
	}

	// superRefine: webhook signing validation.
	if len(c.WebhookURLs) > 0 {
		if len(c.WebhookHMACSecrets) == 0 && c.WebhookHMACSecret == "" {
			errs = append(errs, errors.New("WEBHOOK_HMAC_SECRET (or WEBHOOK_HMAC_SECRETS) is required when WEBHOOK_URLS is non-empty"))
		}
		if len(c.WebhookHMACSecrets) > 0 && len(c.WebhookHMACSecrets) != len(c.WebhookURLs) {
			errs = append(errs, fmt.Errorf("WEBHOOK_HMAC_SECRETS length (%d) must equal WEBHOOK_URLS length (%d)", len(c.WebhookHMACSecrets), len(c.WebhookURLs)))
		}
	}

	return errors.Join(errs...)
}

// cleanList trims whitespace and drops empty entries, mirroring the TS
// `.split(',').map(trim).filter(len>0)` transforms.
func cleanList(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func requireURL(name, raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("%s %q is not a valid URL", name, raw)
	}
	return nil
}
