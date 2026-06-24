package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	goredis "github.com/redis/go-redis/v9"

	"github.com/Sidiora-Technologies/KindleLaunch/shared/constants"

	"github.com/Sidiora-Technologies/KindleLaunch/core/trading-charts/internal/internaltest"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// --- pure unit tests (no infrastructure) ---

func TestWildcardMatches(t *testing.T) {
	t.Parallel()
	none := &clientSub{pools: map[string]struct{}{}, timeframes: map[string]struct{}{}}
	if !wildcardMatches(none, "0xA", "1m") {
		t.Error("empty filters should match everything")
	}
	poolOnly := &clientSub{pools: map[string]struct{}{"0xA": {}}, timeframes: map[string]struct{}{}}
	if !wildcardMatches(poolOnly, "0xA", "5m") {
		t.Error("pool match + open timeframe should match")
	}
	if wildcardMatches(poolOnly, "0xB", "5m") {
		t.Error("non-matching pool should not match")
	}
	tfOnly := &clientSub{pools: map[string]struct{}{}, timeframes: map[string]struct{}{"1m": {}}}
	if wildcardMatches(tfOnly, "0xA", "5m") {
		t.Error("non-matching timeframe should not match")
	}
}

func TestEnqueueEvictsSlowClient(t *testing.T) {
	t.Parallel()
	h := &wsHub{logger: discardLogger()}
	c := &clientSub{send: make(chan []byte, 1), quit: make(chan struct{})}

	h.enqueue(c, []byte("one")) // fills the 1-slot buffer
	h.enqueue(c, []byte("two")) // buffer full -> evict

	select {
	case <-c.quit:
		// evicted as expected
	default:
		t.Fatal("slow client was not evicted (quit not closed)")
	}
}

func TestEnqueueAfterQuit(t *testing.T) {
	t.Parallel()
	h := &wsHub{logger: discardLogger()}
	c := &clientSub{send: make(chan []byte, 1), quit: make(chan struct{})}
	c.stop() // quit already closed
	// Should hit the <-c.quit branch without panicking or blocking.
	h.enqueue(c, []byte("x"))
}

func TestClientIPFromRequest(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.RemoteAddr = "1.2.3.4:5678"
	if ip := clientIPFromRequest(r); ip != "1.2.3.4" {
		t.Errorf("ip = %q, want 1.2.3.4", ip)
	}
	r.RemoteAddr = "noport"
	if ip := clientIPFromRequest(r); ip != "noport" {
		t.Errorf("ip = %q, want noport (fallback)", ip)
	}
}

func TestParseRedisOpts(t *testing.T) {
	t.Parallel()
	if opt := parseRedisOpts("redis://localhost:6379"); opt.Addr != "localhost:6379" {
		t.Errorf("addr = %q", opt.Addr)
	}
	if opt := parseRedisOpts("::::bad"); opt.Addr != "" {
		t.Errorf("invalid url should yield empty opts, got %q", opt.Addr)
	}
}

func TestKeysAndParseFloat(t *testing.T) {
	t.Parallel()
	ks := keys(map[string]struct{}{"a": {}, "b": {}})
	if len(ks) != 2 {
		t.Errorf("keys len = %d, want 2", len(ks))
	}
	if parseFloat("1.5") != 1.5 || parseFloat("") != 0 {
		t.Errorf("parseFloat wrong")
	}
}

// --- integration test (real WS dialer + real Redis) ---

func wsServer(t *testing.T, deps WSDeps) (*httptest.Server, string) {
	t.Helper()
	r := chi.NewRouter()
	RegisterWS(r, deps)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	return srv, wsURL
}

func dial(t *testing.T, wsURL string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", wsURL, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func readMsg(t *testing.T, conn *websocket.Conn) map[string]interface{} {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode ws message: %v (raw=%s)", err, raw)
	}
	return m
}

func TestWSSubscribeReceiveAndPing(t *testing.T) {
	redisURL := internaltest.NewRedisURL(t)
	_, wsURL := wsServer(t, WSDeps{RedisURL: redisURL, Logger: discardLogger(), MaxConnections: 10, MaxPerIP: 5})

	conn := dial(t, wsURL)

	// First frame is the welcome.
	if m := readMsg(t, conn); m["type"] != "connected" {
		t.Fatalf("first message type = %v, want connected", m["type"])
	}

	pool := "0xpoolWS00000000000000000000000000001111"
	if err := conn.WriteJSON(map[string]interface{}{
		"type": "subscribe", "pools": []string{pool}, "timeframes": []string{"1m"},
	}); err != nil {
		t.Fatalf("write subscribe: %v", err)
	}
	if m := readMsg(t, conn); m["type"] != "subscribed" {
		t.Fatalf("subscribe ack type = %v, want subscribed", m["type"])
	}

	// ping -> pong.
	if err := conn.WriteJSON(map[string]string{"type": "ping"}); err != nil {
		t.Fatalf("write ping: %v", err)
	}
	if m := readMsg(t, conn); m["type"] != "pong" {
		t.Fatalf("ping reply type = %v, want pong", m["type"])
	}

	// Publish a candle update; the hub's Redis subscriber fans it out. Retry
	// publishing to absorb the async subscribe setup race.
	opt, _ := goredis.ParseURL(redisURL)
	rdb := goredis.NewClient(opt)
	defer rdb.Close()

	payload, _ := json.Marshal(map[string]interface{}{
		"poolAddress": pool, "timeframe": "1m", "candleStart": 60,
		"open": "1000000000000000000", "high": "2000000000000000000",
		"low": "500000000000000000", "close": "1500000000000000000",
		"volumeUsdl": "1000000000000000000", "volumeToken": "0",
		"buyVolumeUsdl": "0", "sellVolumeUsdl": "0",
		"tradeCount": 1, "uniqueTraders": 1, "largeTradeCount": 0,
		"mcapOpen": "0", "mcapHigh": "0", "mcapLow": "0", "mcapClose": "0",
	})

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				_ = rdb.Publish(context.Background(), constants.ChannelCandleUpdate, payload).Err()
			}
		}
	}()

	// Read until we see the candle_update (skipping any keepalive pings).
	deadline := time.Now().Add(6 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("did not receive candle_update")
		}
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read candle_update: %v", err)
		}
		var m map[string]interface{}
		if err := json.Unmarshal(raw, &m); err != nil {
			continue
		}
		if m["type"] == "candle_update" {
			data, _ := m["data"].(map[string]interface{})
			if data["poolAddress"] != pool {
				t.Errorf("candle_update pool = %v, want %v", data["poolAddress"], pool)
			}
			if data["open"].(float64) != 1.0 {
				t.Errorf("candle_update open = %v, want 1.0", data["open"])
			}
			return
		}
	}
}

func TestWSUnsubscribeAndInvalidJSON(t *testing.T) {
	redisURL := internaltest.NewRedisURL(t)
	_, wsURL := wsServer(t, WSDeps{RedisURL: redisURL, Logger: discardLogger()})
	conn := dial(t, wsURL)
	_ = readMsg(t, conn) // welcome

	pool := "0xpoolWS00000000000000000000000000002222"
	_ = conn.WriteJSON(map[string]interface{}{"type": "subscribe", "pools": []string{pool}, "timeframes": []string{"1m"}})
	_ = readMsg(t, conn) // subscribed

	_ = conn.WriteJSON(map[string]interface{}{"type": "unsubscribe", "pools": []string{pool}, "timeframes": []string{"1m"}})
	if m := readMsg(t, conn); m["type"] != "unsubscribed" {
		t.Fatalf("unsubscribe ack = %v, want unsubscribed", m["type"])
	}

	// Invalid JSON yields an error frame, connection stays open.
	if err := conn.WriteMessage(websocket.TextMessage, []byte("{bad")); err != nil {
		t.Fatalf("write bad: %v", err)
	}
	if m := readMsg(t, conn); m["type"] != "error" {
		t.Fatalf("bad-json reply = %v, want error", m["type"])
	}
}

func TestWSPerIPLimit(t *testing.T) {
	redisURL := internaltest.NewRedisURL(t)
	_, wsURL := wsServer(t, WSDeps{RedisURL: redisURL, Logger: discardLogger(), MaxConnections: 10, MaxPerIP: 1})

	conn1 := dial(t, wsURL)
	if m := readMsg(t, conn1); m["type"] != "connected" {
		t.Fatalf("conn1 welcome = %v", m["type"])
	}

	// Second connection from the same IP must be rejected.
	conn2 := dial(t, wsURL)
	m := readMsg(t, conn2)
	if m["type"] != "error" {
		t.Fatalf("conn2 first frame = %v, want error", m["type"])
	}
	if msg, _ := m["message"].(string); !strings.Contains(msg, "Too many") {
		t.Errorf("conn2 error = %q, want 'Too many connections'", msg)
	}
}

func TestWSCapacityLimit(t *testing.T) {
	redisURL := internaltest.NewRedisURL(t)
	_, wsURL := wsServer(t, WSDeps{RedisURL: redisURL, Logger: discardLogger(), MaxConnections: 1, MaxPerIP: 5})

	conn1 := dial(t, wsURL)
	if m := readMsg(t, conn1); m["type"] != "connected" {
		t.Fatalf("conn1 welcome = %v", m["type"])
	}

	conn2 := dial(t, wsURL)
	m := readMsg(t, conn2)
	if m["type"] != "error" {
		t.Fatalf("conn2 first frame = %v, want error", m["type"])
	}
	if msg, _ := m["message"].(string); !strings.Contains(msg, "capacity") {
		t.Errorf("conn2 error = %q, want 'Server at capacity'", msg)
	}
}
