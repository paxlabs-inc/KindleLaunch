// Package httpapi implements the HTTP routes for core/trading-charts: TradingView
// UDF endpoints (config, symbols, history, time), HMAC-authenticated webhook
// receiver, and a WebSocket candle stream. Ports candles/src/routes.
package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	sharedhttp "github.com/Sidiora-Technologies/KindleLaunch/shared/http"
	"github.com/Sidiora-Technologies/KindleLaunch/shared/util"

	"github.com/Sidiora-Technologies/KindleLaunch/core/trading-charts/internal/store"
)

// RegisterUDF registers all TradingView UDF routes on the router.
func RegisterUDF(r chi.Router, st *store.Store) {
	r.Get("/config", udfConfig)
	r.Get("/symbols", udfSymbols)
	r.Get("/history", udfHistory(st))
	r.Get("/time", udfTime)
}

func udfConfig(w http.ResponseWriter, _ *http.Request) {
	sharedhttp.WriteJSON(w, 200, map[string]interface{}{
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
	sharedhttp.WriteJSON(w, 200, map[string]interface{}{
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

func resolutionToTimeframe(resolution string) string {
	m := map[string]string{
		"1":   "1m",
		"5":   "5m",
		"15":  "15m",
		"60":  "1h",
		"240": "4h",
		"1D":  "1d",
		"D":   "1d",
		"1W":  "1w",
		"W":   "1w",
	}
	if tf, ok := m[resolution]; ok {
		return tf
	}
	return "1h"
}

func udfHistory(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		symbol := q.Get("symbol")
		fromStr := q.Get("from")
		toStr := q.Get("to")
		resolution := q.Get("resolution")
		countback := q.Get("countback")
		timeframe := resolutionToTimeframe(resolution)

		if countback != "" {
			limit, err := strconv.Atoi(countback)
			if err != nil || limit <= 0 {
				limit = 300
			}
			toTs, _ := strconv.ParseInt(toStr, 10, 64)

			rows, err := st.HistoryCountback(r.Context(), symbol, timeframe, toTs, limit)
			if err != nil {
				sharedhttp.WriteError(w, 500, "Internal Server Error", "history query failed")
				return
			}
			writeHistoryResponse(w, rows)
			return
		}

		fromTs, _ := strconv.ParseInt(fromStr, 10, 64)
		toTs, _ := strconv.ParseInt(toStr, 10, 64)

		rows, err := st.HistoryRange(r.Context(), symbol, timeframe, fromTs, toTs)
		if err != nil {
			sharedhttp.WriteError(w, 500, "Internal Server Error", "history query failed")
			return
		}
		writeHistoryResponse(w, rows)
	}
}

func writeHistoryResponse(w http.ResponseWriter, rows []store.CandleRow) {
	if len(rows) == 0 {
		sharedhttp.WriteJSON(w, 200, map[string]string{"s": "no_data"})
		return
	}

	t := make([]int64, len(rows))
	o := make([]float64, len(rows))
	h := make([]float64, len(rows))
	l := make([]float64, len(rows))
	c := make([]float64, len(rows))
	v := make([]float64, len(rows))

	for i, row := range rows {
		t[i] = row.CandleStart
		o[i], _ = parseFloatFormatted(row.Open)
		h[i], _ = parseFloatFormatted(row.High)
		l[i], _ = parseFloatFormatted(row.Low)
		c[i], _ = parseFloatFormatted(row.Close)
		v[i], _ = parseFloatFormattedVolume(row.VolumeUsdl)
	}

	sharedhttp.WriteJSON(w, 200, map[string]interface{}{
		"s": "ok",
		"t": t,
		"o": o,
		"h": h,
		"l": l,
		"c": c,
		"v": v,
	})
}

func parseFloatFormatted(raw string) (float64, error) {
	s, err := util.FormatPrice(raw)
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(s, 64)
}

func parseFloatFormattedVolume(raw string) (float64, error) {
	s, err := util.FormatVolume(raw)
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(s, 64)
}

func udfTime(w http.ResponseWriter, _ *http.Request) {
	sharedhttp.WriteJSON(w, 200, time.Now().Unix())
}
