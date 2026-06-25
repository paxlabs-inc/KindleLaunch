package app_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Sidiora-Technologies/KindleLaunch/core/ranking-algo/internal/app"
	"github.com/Sidiora-Technologies/KindleLaunch/core/ranking-algo/internal/config"
	"github.com/Sidiora-Technologies/KindleLaunch/core/ranking-algo/internal/internaltest"
)

func baseEnv(t *testing.T, dsn, redisURL string) {
	t.Helper()
	addr := "0x1111111111111111111111111111111111111111"
	env := map[string]string{
		"DATABASE_URL":            dsn,
		"REDIS_URL":               redisURL,
		"REDIS_BULL_URL":          redisURL,
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
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestNewServesAndRanks(t *testing.T) {
	dsn, pool := internaltest.NewPostgresWithDSN(t)
	redisURL := internaltest.NewRedisURL(t)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `
		INSERT INTO stats.pool_stats (
			pool_address, volume_24h, volume_1h, volume_5m, market_cap, price_change_24h,
			buy_count_24h, sell_count_24h, unique_traders_24h, holder_count, updated_at
		) VALUES ('0xhot','2400','200','30','100000','80',200,150,40,60,$1)
	`, time.Now().Unix()); err != nil {
		t.Fatalf("seed: %v", err)
	}

	baseEnv(t, dsn, redisURL)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	a, err := app.New(ctx, cfg, quietLogger())
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()

	if err := a.Service.RunAll(ctx); err != nil {
		t.Fatalf("RunAll: %v", err)
	}

	srv := httptest.NewServer(a.Router)
	defer srv.Close()

	// Ranking route returns the seeded pool.
	resp, err := http.Get(srv.URL + "/rankings/trending") //nolint:noctx,bodyclose // test client
	if err != nil {
		t.Fatalf("GET rankings: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rankings status = %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	items, ok := body["items"].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("expected ranked items, got %v", body["items"])
	}
	if items[0].(map[string]any)["poolAddress"] != "0xhot" {
		t.Errorf("top item = %v, want 0xhot", items[0])
	}

	// Health readiness deep-checks DB + Redis.
	hresp, err := http.Get(srv.URL + "/health/ready") //nolint:noctx,bodyclose // test client
	if err != nil {
		t.Fatalf("GET health: %v", err)
	}
	defer hresp.Body.Close()
	if hresp.StatusCode != http.StatusOK {
		t.Fatalf("health/ready status = %d, want 200", hresp.StatusCode)
	}
}

func TestNewFailsOnBadRedisURL(t *testing.T) {
	dsn, _ := internaltest.NewPostgresWithDSN(t)
	baseEnv(t, dsn, "redis://localhost:6379")
	// Override REDIS_URL with an unparseable value after baseEnv set a valid one.
	t.Setenv("REDIS_URL", "not-a-valid-redis-url")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if _, err := app.New(context.Background(), cfg, quietLogger()); err == nil {
		t.Fatal("app.New: expected error for invalid REDIS_URL")
	}
}
