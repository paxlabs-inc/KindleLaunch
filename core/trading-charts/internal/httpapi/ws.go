package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	goredis "github.com/redis/go-redis/v9"

	"github.com/Sidiora-Technologies/KindleLaunch/shared/constants"
	"github.com/Sidiora-Technologies/KindleLaunch/shared/util"
)

const (
	// sendBufferSize bounds each connection's outbound queue (invariant i11:
	// bounded buffers). When a client's queue is full it is evicted as a slow
	// consumer rather than letting the broadcast goroutine block or grow
	// unbounded.
	sendBufferSize = 256
	// writeWait bounds a single socket write so a stalled client cannot pin a
	// writer goroutine forever.
	writeWait = 10 * time.Second
	// pingInterval is the server-initiated keepalive cadence (C-5).
	pingInterval = 30 * time.Second
)

// WSDeps holds dependencies for the WebSocket candle stream.
type WSDeps struct {
	RedisURL       string
	Logger         *slog.Logger
	MaxConnections int
	MaxPerIP       int
}

type clientSub struct {
	conn       *websocket.Conn
	send       chan []byte
	pools      map[string]struct{}
	timeframes map[string]struct{}
	quit       chan struct{}
	quitOnce   sync.Once
}

// stop signals the client's writer goroutine to exit. It is idempotent and safe
// to call from multiple goroutines (broadcast eviction and read-loop teardown).
func (c *clientSub) stop() {
	c.quitOnce.Do(func() { close(c.quit) })
}

type wsHub struct {
	mu                sync.RWMutex
	clients           map[*clientSub]struct{}
	subscriptionIndex map[string]map[*clientSub]struct{}
	wildcardSubs      map[*clientSub]struct{}
	ipCounts          map[string]int
	logger            *slog.Logger
	maxConns          int
	maxPerIP          int
}

// RegisterWS registers the /ws WebSocket endpoint for real-time candle updates.
func RegisterWS(r chi.Router, deps WSDeps) {
	hub := &wsHub{
		clients:           make(map[*clientSub]struct{}),
		subscriptionIndex: make(map[string]map[*clientSub]struct{}),
		wildcardSubs:      make(map[*clientSub]struct{}),
		ipCounts:          make(map[string]int),
		logger:            deps.Logger,
		maxConns:          deps.MaxConnections,
		maxPerIP:          deps.MaxPerIP,
	}
	if hub.maxConns <= 0 {
		hub.maxConns = 10000
	}
	if hub.maxPerIP <= 0 {
		hub.maxPerIP = 20
	}

	// Redis subscriber for candle updates (dedicated connection).
	go hub.runRedisSubscriber(deps.RedisURL)

	r.Get("/ws", hub.handleWS)
}

func (h *wsHub) subKey(pool, tf string) string {
	return pool + ":" + tf
}

func (h *wsHub) indexClient(c *clientSub) {
	h.removeFromIndex(c)
	if len(c.pools) == 0 || len(c.timeframes) == 0 {
		h.wildcardSubs[c] = struct{}{}
		return
	}
	for pool := range c.pools {
		for tf := range c.timeframes {
			key := h.subKey(pool, tf)
			set, ok := h.subscriptionIndex[key]
			if !ok {
				set = make(map[*clientSub]struct{})
				h.subscriptionIndex[key] = set
			}
			set[c] = struct{}{}
		}
	}
}

func (h *wsHub) removeFromIndex(c *clientSub) {
	delete(h.wildcardSubs, c)
	for key, set := range h.subscriptionIndex {
		delete(set, c)
		if len(set) == 0 {
			delete(h.subscriptionIndex, key)
		}
	}
}

func (h *wsHub) runRedisSubscriber(redisURL string) {
	rdb := goredis.NewClient(parseRedisOpts(redisURL))
	defer rdb.Close()

	ctx := context.Background()
	ps := rdb.Subscribe(ctx, constants.ChannelCandleUpdate)
	defer ps.Close()

	ch := ps.Channel()
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			h.broadcastCandleUpdate(msg.Payload)
		case <-ctx.Done():
			return
		}
	}
}

func (h *wsHub) broadcastCandleUpdate(payload string) {
	var event struct {
		PoolAddress     string `json:"poolAddress"`
		Timeframe       string `json:"timeframe"`
		CandleStart     int64  `json:"candleStart"`
		Open            string `json:"open"`
		High            string `json:"high"`
		Low             string `json:"low"`
		Close           string `json:"close"`
		VolumeUsdl      string `json:"volumeUsdl"`
		VolumeToken     string `json:"volumeToken"`
		BuyVolumeUsdl   string `json:"buyVolumeUsdl"`
		SellVolumeUsdl  string `json:"sellVolumeUsdl"`
		TradeCount      int    `json:"tradeCount"`
		UniqueTraders   int    `json:"uniqueTraders"`
		LargeTradeCount int    `json:"largeTradeCount"`
		McapOpen        string `json:"mcapOpen"`
		McapHigh        string `json:"mcapHigh"`
		McapLow         string `json:"mcapLow"`
		McapClose       string `json:"mcapClose"`
	}
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		h.logger.Error("ws: unmarshal candle update", slog.String("err", err.Error()))
		return
	}

	var ferr error
	fp := func(s string) float64 {
		v, e := util.FormatPrice(s)
		if e != nil {
			ferr = e
		}
		return parseFloat(v)
	}
	fv := func(s string) float64 {
		v, e := util.FormatVolume(s)
		if e != nil {
			ferr = e
		}
		return parseFloat(v)
	}

	msg, err := json.Marshal(map[string]interface{}{
		"type": "candle_update",
		"data": map[string]interface{}{
			"poolAddress":     event.PoolAddress,
			"timeframe":       event.Timeframe,
			"candleStart":     event.CandleStart,
			"open":            fp(event.Open),
			"high":            fp(event.High),
			"low":             fp(event.Low),
			"close":           fp(event.Close),
			"volumeUsdl":      fv(event.VolumeUsdl),
			"volumeToken":     fv(event.VolumeToken),
			"buyVolumeUsdl":   fv(event.BuyVolumeUsdl),
			"sellVolumeUsdl":  fv(event.SellVolumeUsdl),
			"tradeCount":      event.TradeCount,
			"uniqueTraders":   event.UniqueTraders,
			"largeTradeCount": event.LargeTradeCount,
			"mcapOpen":        fv(event.McapOpen),
			"mcapHigh":        fv(event.McapHigh),
			"mcapLow":         fv(event.McapLow),
			"mcapClose":       fv(event.McapClose),
		},
	})
	if ferr != nil {
		h.logger.Warn("ws: candle field format error, using zero", slog.String("err", ferr.Error()))
	}
	if err != nil {
		h.logger.Error("ws: marshal candle update", slog.String("err", err.Error()))
		return
	}

	key := h.subKey(event.PoolAddress, event.Timeframe)
	h.mu.RLock()
	indexed := h.subscriptionIndex[key]
	wild := h.wildcardSubs
	h.mu.RUnlock()

	for client := range indexed {
		h.enqueue(client, msg)
	}
	for client := range wild {
		if wildcardMatches(client, event.PoolAddress, event.Timeframe) {
			h.enqueue(client, msg)
		}
	}
}

// wildcardMatches reports whether a wildcard subscriber (one that left pools or
// timeframes unset) should receive an update for pool/tf. An unset dimension
// matches everything on that dimension.
func wildcardMatches(c *clientSub, pool, tf string) bool {
	poolMatch := len(c.pools) == 0
	if !poolMatch {
		_, poolMatch = c.pools[pool]
	}
	tfMatch := len(c.timeframes) == 0
	if !tfMatch {
		_, tfMatch = c.timeframes[tf]
	}
	return poolMatch && tfMatch
}

// enqueue performs a non-blocking send to the client's bounded queue. If the
// queue is full the client is a slow consumer and is evicted (invariant i11
// slow-client eviction) instead of blocking the broadcast goroutine.
func (h *wsHub) enqueue(c *clientSub, msg []byte) {
	select {
	case c.send <- msg:
	case <-c.quit:
	default:
		if h.logger != nil {
			h.logger.Warn("ws: evicting slow client (send buffer full)")
		}
		c.stop()
	}
}

// writePump is the SOLE writer for a connection (gorilla/websocket permits only
// one concurrent writer). It drains the bounded send queue and emits keepalive
// pings, each bounded by writeWait. It exits on write error or stop(), closing
// the connection so the read loop unblocks and teardown runs.
func (h *wsHub) writePump(c *clientSub) {
	ticker := time.NewTicker(pingInterval)
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()
	for {
		select {
		case msg := <-c.send:
			if err := c.conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			ping, err := json.Marshal(map[string]interface{}{
				"type": "ping",
				"ts":   time.Now().UnixMilli(),
			})
			if err != nil {
				continue
			}
			if err := c.conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, ping); err != nil {
				return
			}
		case <-c.quit:
			return
		}
	}
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func (h *wsHub) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	clientIP := clientIPFromRequest(r)

	h.mu.Lock()
	if len(h.clients) >= h.maxConns {
		h.mu.Unlock()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"error","message":"Server at capacity"}`)) //nolint:errcheck // best-effort capacity notice; the connection is closed immediately after
		_ = conn.Close()
		return
	}
	if h.ipCounts[clientIP] >= h.maxPerIP {
		h.mu.Unlock()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"error","message":"Too many connections from this IP"}`)) //nolint:errcheck // best-effort limit notice; the connection is closed immediately after
		_ = conn.Close()
		return
	}
	h.ipCounts[clientIP]++

	sub := &clientSub{
		conn:       conn,
		send:       make(chan []byte, sendBufferSize),
		pools:      make(map[string]struct{}),
		timeframes: make(map[string]struct{}),
		quit:       make(chan struct{}),
	}
	h.clients[sub] = struct{}{}
	h.mu.Unlock()

	// writePump is the single writer for this connection; the loop below is the
	// single reader. All outbound frames go through sub.send (invariant i11).
	go h.writePump(sub)

	defer func() {
		sub.stop()
		h.mu.Lock()
		h.removeFromIndex(sub)
		delete(h.clients, sub)
		h.ipCounts[clientIP]--
		if h.ipCounts[clientIP] <= 0 {
			delete(h.ipCounts, clientIP)
		}
		h.mu.Unlock()
		_ = conn.Close()
	}()

	// Send welcome message.
	if welcome, err := json.Marshal(map[string]interface{}{
		"type":    "connected",
		"message": `Send { type: "subscribe", pools: ["0x..."], timeframes: ["1m"] } to start receiving candle updates`,
	}); err == nil {
		h.enqueue(sub, welcome)
	}

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}

		var msg struct {
			Type       string   `json:"type"`
			Pools      []string `json:"pools"`
			Timeframes []string `json:"timeframes"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			if errMsg, mErr := json.Marshal(map[string]string{"type": "error", "message": "Invalid JSON"}); mErr == nil {
				h.enqueue(sub, errMsg)
			}
			continue
		}

		switch msg.Type {
		case "subscribe":
			h.mu.Lock()
			for _, p := range msg.Pools {
				sub.pools[p] = struct{}{}
			}
			for _, tf := range msg.Timeframes {
				sub.timeframes[tf] = struct{}{}
			}
			h.indexClient(sub)
			h.mu.Unlock()
			if resp, err := json.Marshal(map[string]interface{}{
				"type":       "subscribed",
				"pools":      keys(sub.pools),
				"timeframes": keys(sub.timeframes),
			}); err == nil {
				h.enqueue(sub, resp)
			}

		case "unsubscribe":
			h.mu.Lock()
			for _, p := range msg.Pools {
				delete(sub.pools, p)
			}
			for _, tf := range msg.Timeframes {
				delete(sub.timeframes, tf)
			}
			h.indexClient(sub)
			h.mu.Unlock()
			if resp, err := json.Marshal(map[string]interface{}{
				"type":       "unsubscribed",
				"pools":      keys(sub.pools),
				"timeframes": keys(sub.timeframes),
			}); err == nil {
				h.enqueue(sub, resp)
			}

		case "ping":
			if pong, err := json.Marshal(map[string]string{"type": "pong"}); err == nil {
				h.enqueue(sub, pong)
			}
		}
	}
}

func keys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func parseFloat(s string) float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}

func clientIPFromRequest(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func parseRedisOpts(redisURL string) *goredis.Options {
	opts, err := goredis.ParseURL(redisURL)
	if err != nil {
		return &goredis.Options{}
	}
	return opts
}
