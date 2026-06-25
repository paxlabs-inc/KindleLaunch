// Package rest is the thin, "acceptable" REST snapshot surface of core/api.
//
// By design the gateway is PUSH-FIRST: live updates ride the WSS/SSE multiplexer
// (internal/ws, internal/sse), never client polling. REST is reserved for the
// bootstrap SNAPSHOT a client needs once before it starts consuming deltas:
// TradingView UDF candle history, a pool-stats snapshot, ranked lists, platform
// metrics, and a token-detail BFF aggregation. Every read comes from
// internal/store (Postgres + Redis the core/* services write); every response
// is bounded by the in-process cache (ETag/304 + singleflight) and the gateway's
// per-IP/key rate limit + global load-shed.
package rest

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	sharedhttp "github.com/Sidiora-Technologies/KindleLaunch/shared/http"
	"github.com/Sidiora-Technologies/KindleLaunch/shared/util"

	"github.com/Sidiora-Technologies/KindleLaunch/core/api/internal/cache"
	"github.com/Sidiora-Technologies/KindleLaunch/core/api/internal/store"
)

// Snapshot TTLs (seconds) mirror the per-service freshness the TS SDK proxy used
// (candles ~1s, stats ~3s, rankings ~30s, BFF ~5s, platform ~30s).
const (
	ttlHistory  = 1 * time.Second
	ttlStats    = 3 * time.Second
	ttlRankings = 30 * time.Second
	ttlBFF      = 5 * time.Second
	ttlPlatform = 30 * time.Second
)

// Register mounts the snapshot REST routes on r.
func Register(r chi.Router, st *store.Store, c *cache.Cache) {
	// TradingView UDF (chart bootstrap).
	r.Get("/udf/config", udfConfig)
	r.Get("/udf/symbols", udfSymbols)
	r.Get("/udf/time", udfTime)
	r.Get("/udf/history", c.Handler(udfHistoryKey, ttlHistory, udfHistoryFetch(st)))

	// Pool stats (static /stats/batch before the param route).
	r.Get("/stats/batch", statsBatch(st))
	r.Get("/stats/{poolAddress}", statsByPool(st))

	// Rankings + platform + token BFF.
	r.Get("/rankings/{category}", rankingsByCategory(st, c))
	r.Get("/platform/metrics", platformMetrics(st))
	r.Get("/bff/token/{poolAddress}", tokenBFF(st, c))
}

// ---- UDF (ported from core/trading-charts httpapi/udf.go) -----------------

func udfConfig(w http.ResponseWriter, _ *http.Request) {
	sharedhttp.WriteJSON(w, http.StatusOK, map[string]any{
		"supports_search":          true,
		"supports_group_request":   false,
		"supports_marks":           false,
		"supports_timescale_marks": false,
		"supports_time":            true,
		"exchanges":                []map[string]string{{"value": "sidiora", "name": "Sidiora", "desc": "Sidiora Launchpad"}},
		"symbols_types":            []map[string]string{{"name": "token", "value": "token"}},
		"supported_resolutions":    []string{"1", "5", "15", "60", "240", "1D", "1W"},
		"currency_codes":           []map[string]string{{"id": "USDL", "code": "USDL", "description": "USDL Stablecoin"}},
	})
}

func udfSymbols(w http.ResponseWriter, r *http.Request) {
	symbol := r.URL.Query().Get("symbol")
	sharedhttp.WriteJSON(w, http.StatusOK, map[string]any{
		"name":                   symbol,
		"full_name":              symbol + "/USDL",
		"description":            "Token " + symbol,
		"type":                   "token",
		"session":                "24x7",
		"exchange":               "sidiora",
		"listed_exchange":        "sidiora",
		"timezone":               "Etc/UTC",
		"has_intraday":           true,
		"has_weekly_and_monthly": true,
		"supported_resolutions":  []string{"1", "5", "15", "60", "240", "1D", "1W"},
		"pricescale":             100000000,
		"ticker":                 symbol,
		"currency_code":          "USDL",
		"has_empty_bars":         false,
	})
}

func udfTime(w http.ResponseWriter, _ *http.Request) {
	sharedhttp.WriteJSON(w, http.StatusOK, time.Now().Unix())
}

func resolutionToTimeframe(resolution string) string {
	m := map[string]string{
		"1": "1m", "5": "5m", "15": "15m", "60": "1h", "240": "4h",
		"1D": "1d", "D": "1d", "1W": "1w", "W": "1w",
	}
	if tf, ok := m[resolution]; ok {
		return tf
	}
	return "1h"
}

func udfHistoryKey(r *http.Request) string {
	q := r.URL.Query()
	return "udf:history:" + q.Get("symbol") + ":" + q.Get("resolution") + ":" +
		q.Get("from") + ":" + q.Get("to") + ":" + q.Get("countback")
}

func udfHistoryFetch(st *store.Store) func(*http.Request) ([]byte, error) {
	return func(r *http.Request) ([]byte, error) {
		q := r.URL.Query()
		symbol := q.Get("symbol")
		timeframe := resolutionToTimeframe(q.Get("resolution"))

		var rows []store.CandleRow
		var err error
		if cb := q.Get("countback"); cb != "" {
			limit := parseIntDefault(cb, 300)
			if limit <= 0 {
				limit = 300
			}
			rows, err = st.CandleHistoryCountback(r.Context(), symbol, timeframe, parseInt64(q.Get("to")), limit)
		} else {
			rows, err = st.CandleHistoryRange(r.Context(), symbol, timeframe, parseInt64(q.Get("from")), parseInt64(q.Get("to")))
		}
		if err != nil {
			return nil, err
		}
		return marshalUDFHistory(rows), nil
	}
}

// marshalUDFHistory builds the TradingView UDF history envelope. Money fields
// are formatted from text bigints (no float math upstream; invariant i1).
func marshalUDFHistory(rows []store.CandleRow) []byte {
	if len(rows) == 0 {
		return mustJSON(map[string]string{"s": "no_data"})
	}
	n := len(rows)
	t := make([]int64, n)
	o := make([]float64, n)
	h := make([]float64, n)
	l := make([]float64, n)
	c := make([]float64, n)
	v := make([]float64, n)
	for i := range rows {
		row := &rows[i]
		t[i] = row.CandleStart
		o[i] = fprice(row.Open)
		h[i] = fprice(row.High)
		l[i] = fprice(row.Low)
		c[i] = fprice(row.Close)
		v[i] = fvol(row.VolumeUsdl)
	}
	return mustJSON(map[string]any{"s": "ok", "t": t, "o": o, "h": h, "l": l, "c": c, "v": v})
}

func fprice(raw string) float64 {
	s, err := util.FormatPrice(raw)
	if err != nil {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}

func fvol(raw string) float64 {
	s, err := util.FormatVolume(raw)
	if err != nil {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}

// ---- shared helpers --------------------------------------------------------

func parseIntDefault(raw string, def int) int {
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return n
}

func parseInt64(raw string) int64 {
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}
