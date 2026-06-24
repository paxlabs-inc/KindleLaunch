package config_test

import (
	"strings"
	"testing"

	"github.com/Sidiora-Technologies/KindleLaunch/core/trading-charts/internal/config"
)

// validEnv returns the full set of env vars required for a successful Load.
// Tests mutate a copy to exercise individual failure modes.
func validEnv() map[string]string {
	addr := "0x1111111111111111111111111111111111111111"
	return map[string]string{
		"DATABASE_URL":            "postgres://u:p@localhost:5432/charts",
		"REDIS_URL":               "redis://localhost:6379",
		"REDIS_BULL_URL":          "redis://localhost:6379/1",
		"RPC_URL":                 "https://rpc.example.com/rpc",
		"EVENT_EMITTER_ADDRESS":   addr,
		"POOL_REGISTRY_ADDRESS":   addr,
		"ROUTER_ADDRESS":          addr,
		"FACTORY_ADDRESS":         addr,
		"QUOTER_ADDRESS":          addr,
		"PROTOCOL_CONFIG_ADDRESS": addr,
		"FEE_ACCUMULATOR_ADDRESS": addr,
		"SIDIORA_NFT_ADDRESS":     addr,
		"FEES_ROUTER_ADDRESS":     addr,
		"POOL_BEACON_ADDRESS":     addr,
		"WEBHOOK_HMAC_SECRET":     strings.Repeat("s", 32),
	}
}

func applyEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
}

func TestLoadHappyPath(t *testing.T) {
	applyEnv(t, validEnv())

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	if cfg.WebhookHMACSecret != strings.Repeat("s", 32) {
		t.Errorf("WebhookHMACSecret = %q", cfg.WebhookHMACSecret)
	}
	if cfg.WSMaxConnections != 10000 {
		t.Errorf("WSMaxConnections default = %d, want 10000", cfg.WSMaxConnections)
	}
	if cfg.WSMaxPerIP != 20 {
		t.Errorf("WSMaxPerIP default = %d, want 20", cfg.WSMaxPerIP)
	}
	if cfg.Port != 3000 {
		t.Errorf("Port default = %d, want 3000", cfg.Port)
	}
}

func TestLoadOverridesWSLimits(t *testing.T) {
	env := validEnv()
	env["WS_MAX_CONNECTIONS"] = "500"
	env["WS_MAX_PER_IP"] = "5"
	env["CORS_ALLOWED_ORIGINS"] = "https://app.sidiora.fun"
	applyEnv(t, env)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.WSMaxConnections != 500 || cfg.WSMaxPerIP != 5 {
		t.Errorf("WS limits = %d/%d, want 500/5", cfg.WSMaxConnections, cfg.WSMaxPerIP)
	}
	if cfg.CORSAllowedOrigins != "https://app.sidiora.fun" {
		t.Errorf("CORS = %q", cfg.CORSAllowedOrigins)
	}
}

func TestLoadMissingSecret(t *testing.T) {
	env := validEnv()
	delete(env, "WEBHOOK_HMAC_SECRET")
	applyEnv(t, env)

	if _, err := config.Load(); err == nil {
		t.Fatal("Load: expected error for missing WEBHOOK_HMAC_SECRET")
	}
}

func TestLoadShortSecret(t *testing.T) {
	env := validEnv()
	env["WEBHOOK_HMAC_SECRET"] = "tooshort"
	applyEnv(t, env)

	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "at least 32") {
		t.Fatalf("Load: expected short-secret error, got %v", err)
	}
}

func TestLoadInvalidBaseEnv(t *testing.T) {
	env := validEnv()
	env["EVENT_EMITTER_ADDRESS"] = "not-an-address"
	applyEnv(t, env)

	if _, err := config.Load(); err == nil {
		t.Fatal("Load: expected base-env validation error for bad address")
	}
}

func TestLoadMissingRequiredBaseVar(t *testing.T) {
	env := validEnv()
	delete(env, "DATABASE_URL")
	applyEnv(t, env)

	if _, err := config.Load(); err == nil {
		t.Fatal("Load: expected error for missing DATABASE_URL")
	}
}
