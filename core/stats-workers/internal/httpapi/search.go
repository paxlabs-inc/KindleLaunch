package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	goredis "github.com/redis/go-redis/v9"

	sharedhttp "github.com/Sidiora-Technologies/KindleLaunch/shared/http"

	"github.com/Sidiora-Technologies/KindleLaunch/core/stats-workers/internal/store"
)

const (
	searchCacheTTL = 10 * time.Second
	maxSearchHits  = 20
)

// RegisterSearch registers GET /search.
func RegisterSearch(r chi.Router, st *store.Store, rdb *goredis.Client) {
	r.Get("/search", search(st, rdb))
}

// escapeLike escapes LIKE wildcards (% _ \) with a backslash, parity with the TS
// q.replace(/[%_\\]/g, ch => `\\${ch}`).
func escapeLike(s string) string {
	var b strings.Builder
	for _, ch := range s {
		if ch == '%' || ch == '_' || ch == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(ch)
	}
	return b.String()
}

func search(st *store.Store, rdb *goredis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		rawQ := r.URL.Query().Get("q")
		if len(strings.TrimSpace(rawQ)) < 2 {
			sharedhttp.WriteJSON(w, http.StatusOK, map[string]any{
				"results": []store.SearchResult{},
				"query":   rawQ,
			})
			return
		}

		query := strings.ToLower(strings.TrimSpace(rawQ))
		escaped := escapeLike(query)
		resultLimit := parseIntDefault(r.URL.Query().Get("limit"), maxSearchHits)
		if resultLimit > maxSearchHits || resultLimit <= 0 {
			resultLimit = maxSearchHits
		}
		cacheKey := "search:" + query + ":" + itoa(resultLimit)

		if cached, err := rdb.Get(ctx, cacheKey).Result(); err == nil && cached != "" {
			sharedhttp.WriteJSON(w, http.StatusOK, json.RawMessage(cached))
			return
		}

		isAddress := strings.HasPrefix(query, "0x") && len(query) >= 6
		likePattern := "%" + escaped + "%"

		rows, err := st.Search(ctx, isAddress, likePattern, resultLimit)
		if err != nil {
			sharedhttp.WriteError(w, http.StatusInternalServerError, "Internal Server Error", "search failed")
			return
		}

		result := map[string]any{
			"results": rows,
			"query":   strings.TrimSpace(rawQ),
		}
		if payload, err := json.Marshal(result); err == nil {
			_ = rdb.Set(ctx, cacheKey, payload, searchCacheTTL).Err()
		}
		sharedhttp.WriteJSON(w, http.StatusOK, result)
	}
}

// itoa is a tiny non-negative int-to-string helper for cache key building.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
