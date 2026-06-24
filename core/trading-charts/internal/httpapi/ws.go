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

	o, _ := util.FormatPrice(event.Open)
	hh, _ := util.FormatPrice(event.High)
	l, _ := util.FormatPrice(event.Low)
	c, _ := util.FormatPrice(event.Close)
	vu, _ := util.FormatVolume(event.VolumeUsdl)
	vt, _ := util.FormatVolume(event.VolumeToken)
	bv, _ := util.FormatVolume(event.BuyVolumeUsdl)
	sv, _ := util.FormatVolume(event.SellVolumeUsdl)
	mo, _ := util.FormatVolume(event.McapOpen)
	mh, _ := util.FormatVolume(event.McapHigh)
	ml, _ := util.FormatVolume(event.McapLow)
	mc, _ := util.FormatVolume(event.McapClose)

	msg, _ := json.Marshal(map[string]interface{}{
		"type": "candle_update",
		"data": map[string]interface{}{
			"poolAddress":     event.PoolAddress,
			"timeframe":       event.Timeframe,
			"candleStart":     event.CandleStart,
			"open":            parseFloat(o),
			"high":            parseFloat(hh),
			"low":             parseFloat(l),
			"close":           parseFloat(c),
			"volumeUsdl":      parseFloat(vu),
			"volumeToken":     parseFloat(vt),
			"buyVolumeUsdl":   parseFloat(bv),
			"sellVolumeUsdl":  parseFloat(sv),
			"tradeCount":      event.TradeCount,
			"uniqueTraders":   event.UniqueTraders,
			"largeTradeCount": event.LargeTradeCount,
			"mcapOpen":        parseFloat(mo),
			"mcapHigh":        parseFloat(mh),
			"mcapLow":         parseFloat(ml),
			"mcapClose":       parseFloat(mc),
		},
	})

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
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			ping, _ := json.Marshal(map[string]interface{}{
				"type": "ping",
				"ts":   time.Now().UnixMilli(),
			})
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
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
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"error","message":"Server at capacity"}`))
		_ = conn.Close()
		return
	}
	if h.ipCounts[clientIP] >= h.maxPerIP {
		h.mu.Unlock()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"error","message":"Too many connections from this IP"}`))
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
	welcome, _ := json.Marshal(map[string]interface{}{
		"type":    "connected",
		"message": `Send { type: "subscribe", pools: ["0x..."], timeframes: ["1m"] } to start receiving candle updates`,
	})
	h.enqueue(sub, welcome)

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
			errMsg, _ := json.Marshal(map[string]string{"type": "error", "message": "Invalid JSON"})
			h.enqueue(sub, errMsg)
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
			resp, _ := json.Marshal(map[string]interface{}{
				"type":       "subscribed",
				"pools":      keys(sub.pools),
				"timeframes": keys(sub.timeframes),
			})
			h.enqueue(sub, resp)

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
			resp, _ := json.Marshal(map[string]interface{}{
				"type":       "unsubscribed",
				"pools":      keys(sub.pools),
				"timeframes": keys(sub.timeframes),
			})
			h.enqueue(sub, resp)

		case "ping":
			pong, _ := json.Marshal(map[string]string{"type": "pong"})
			h.enqueue(sub, pong)
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
	f, _ := strconv.ParseFloat(s, 64)
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
