package app_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"

	"github.com/Sidiora-Technologies/KindleLaunch/shared/constants"

	"github.com/Sidiora-Technologies/KindleLaunch/core/api/internal/app"
	"github.com/Sidiora-Technologies/KindleLaunch/core/api/internal/config"
	"github.com/Sidiora-Technologies/KindleLaunch/core/api/internal/internaltest"
)

// newApp builds a fully-wired App against real Postgres + Redis and starts the
// broker fan-out loop. It returns an httptest server over the app router, a
// Redis client for publishing, and the pool for seeding the same DB the app
// reads from.
func newApp(t *testing.T) (*httptest.Server, *goredis.Client, *pgxpool.Pool) {
	t.Helper()
	dsn, pool := internaltest.NewPostgresWithDSN(t)
	redisURL := internaltest.NewRedisURL(t)

	cfg := config.Config{
		DatabaseURL:         dsn,
		RedisURL:            redisURL,
		Port:                3000,
		LogLevel:            "info",
		NodeEnv:             "test",
		RateLimitMax:        1000,
		RateLimitWindowSec:  60,
		MaxInFlightRequests: 1000,
		WSMaxConnections:    1000,
		WSMaxPerIP:          100,
		SSEMaxConnections:   1000,
		SSEMaxPerIP:         100,
		ClientSendBuffer:    256,
		CoalesceFlushMS:     20,
	}

	ctx, cancel := context.WithCancel(context.Background())
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := app.New(ctx, &cfg, logger)
	if err != nil {
		cancel()
		t.Fatalf("app.New: %v", err)
	}
	go func() { _ = a.Broker.Run(ctx) }()

	srv := httptest.NewServer(a.Router)
	opt, _ := goredis.ParseURL(redisURL)
	pub := goredis.NewClient(opt)
	t.Cleanup(func() {
		srv.Close()
		cancel()
		a.Close()
		_ = pub.Close()
	})
	return srv, pub, pool
}

func TestApp_HealthReady(t *testing.T) {
	srv, _, _ := newApp(t)
	resp, err := http.Get(srv.URL + "/health/ready")
	if err != nil {
		t.Fatalf("GET /health/ready: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ready status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["status"] != "ok" {
		t.Errorf("ready body status = %v, want ok", body["status"])
	}
}

func TestApp_StatusEndpoint(t *testing.T) {
	srv, _, _ := newApp(t)
	resp, err := http.Get(srv.URL + "/status")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if _, ok := body["subscribers"]; !ok {
		t.Errorf("/status missing subscribers gauge: %v", body)
	}
}

func TestApp_StatsRESTEndToEnd(t *testing.T) {
	srv, _, pool := newApp(t)
	_, err := pool.Exec(context.Background(), `
		INSERT INTO stats.pool_stats (pool_address, token_address, price, market_cap, holder_count, created_at, updated_at)
		VALUES ('0xpool','0xtoken','777','0',4,1,2)`)
	if err != nil {
		t.Fatalf("seed stats: %v", err)
	}

	resp, err := http.Get(srv.URL + "/stats/0xpool")
	if err != nil {
		t.Fatalf("GET /stats: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stats status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["price"] != "777" {
		t.Errorf("stats price = %v, want 777", body["price"])
	}
}

func TestApp_RealtimeSSERoundTrip(t *testing.T) {
	srv, pub, _ := newApp(t)

	// Wait for the broker's upstream subscription so the publish isn't lost.
	waitForSubscribers(t, pub, constants.ChannelSwap)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		srv.URL+"/stream?channels="+constants.ChannelSwap+"&pools=0xLIVE", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	br := bufio.NewReader(resp.Body)

	// Drain the opener and give the client subscription time to register.
	time.Sleep(100 * time.Millisecond)
	payload, _ := json.Marshal(map[string]any{"poolAddress": "0xLIVE", "blockTimestamp": 5})
	if err := pub.Publish(context.Background(), constants.ChannelSwap, payload).Err(); err != nil {
		t.Fatalf("publish: %v", err)
	}

	frame := readDataFrame(t, br)
	if frame["type"] != "swap" || frame["pool"] != "0xLIVE" {
		t.Fatalf("stream frame = %v, want swap/0xLIVE", frame)
	}
}

func waitForSubscribers(t *testing.T, rdb *goredis.Client, channel string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		res, err := rdb.PubSubNumSub(context.Background(), channel).Result()
		if err == nil && res[channel] >= 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("broker did not subscribe within timeout")
}

func readDataFrame(t *testing.T, br *bufio.Reader) map[string]any {
	t.Helper()
	type res struct {
		m   map[string]any
		err error
	}
	ch := make(chan res, 1)
	go func() {
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				ch <- res{nil, err}
				return
			}
			line = strings.TrimRight(line, "\n")
			if strings.HasPrefix(line, "data: ") {
				var m map[string]any
				e := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &m)
				ch <- res{m, e}
				return
			}
		}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("read stream: %v", r.err)
		}
		return r.m
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for SSE data frame")
		return nil
	}
}
