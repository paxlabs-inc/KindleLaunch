package httpapi

import (
	"encoding/json"
	"math"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	goredis "github.com/redis/go-redis/v9"

	shareddb "github.com/Sidiora-Technologies/KindleLaunch/shared/db"
	sharedhttp "github.com/Sidiora-Technologies/KindleLaunch/shared/http"

	"github.com/Sidiora-Technologies/KindleLaunch/core/stats-workers/internal/store"
)

const pressureCacheTTL = 10 * time.Second

// RegisterPressure registers GET /stats/{poolAddress}/pressure.
func RegisterPressure(r chi.Router, st *store.Store, rdb *goredis.Client) {
	r.Get("/stats/{poolAddress}/pressure", pressure(st, rdb))
}

// round2 mirrors JS Math.round(x*100)/100.
func round2(x float64) float64 { return math.Round(x*100) / 100 }

// direction classifies a buy percentage (>=55 bullish, <=45 bearish, else neutral).
func direction(buyPct float64) string {
	switch {
	case buyPct >= 55:
		return "bullish"
	case buyPct <= 45:
		return "bearish"
	default:
		return "neutral"
	}
}

func pressure(st *store.Store, rdb *goredis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		poolAddress := chi.URLParam(r, "poolAddress")
		ctx := r.Context()
		cacheKey := "pressure:" + poolAddress

		if cached, err := rdb.Get(ctx, cacheKey).Result(); err == nil && cached != "" {
			sharedhttp.WriteJSON(w, http.StatusOK, json.RawMessage(cached))
			return
		}

		now := shareddb.NowSeconds()
		ps, found, err := st.PressureStats(ctx, poolAddress, now-3600)
		if err != nil {
			sharedhttp.WriteError(w, http.StatusInternalServerError, "Internal Server Error", "pressure lookup failed")
			return
		}
		if !found {
			sharedhttp.WriteError(w, http.StatusNotFound, "Not Found", "Pool not found")
			return
		}

		totalTrades24h := ps.BuyCount24h + ps.SellCount24h
		buyPct24h := 50.0
		if totalTrades24h > 0 {
			buyPct24h = float64(ps.BuyCount24h) / float64(totalTrades24h) * 100
		}
		totalTrades1h := ps.BuyCount1h + ps.SellCount1h
		buyPct1h := 50.0
		if totalTrades1h > 0 {
			buyPct1h = float64(ps.BuyCount1h) / float64(totalTrades1h) * 100
		}

		result := map[string]any{
			"poolAddress": poolAddress,
			"24h": map[string]any{
				"buyCount":  ps.BuyCount24h,
				"sellCount": ps.SellCount24h,
				"buyPct":    round2(buyPct24h),
				"sellPct":   round2(100 - buyPct24h),
				"volume":    ps.Volume24h,
				"direction": direction(buyPct24h),
			},
			"1h": map[string]any{
				"buyCount":   ps.BuyCount1h,
				"sellCount":  ps.SellCount1h,
				"buyPct":     round2(buyPct1h),
				"sellPct":    round2(100 - buyPct1h),
				"buyVolume":  ps.BuyVolume1h,
				"sellVolume": ps.SellVolume1h,
				"direction":  direction(buyPct1h),
			},
			"updatedAt": now,
		}

		if payload, err := json.Marshal(result); err == nil {
			_ = rdb.Set(ctx, cacheKey, payload, pressureCacheTTL).Err()
		}
		sharedhttp.WriteJSON(w, http.StatusOK, result)
	}
}
