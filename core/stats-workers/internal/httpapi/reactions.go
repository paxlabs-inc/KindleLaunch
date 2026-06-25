package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	goredis "github.com/redis/go-redis/v9"

	"github.com/Sidiora-Technologies/KindleLaunch/shared/auth"
	sharedhttp "github.com/Sidiora-Technologies/KindleLaunch/shared/http"
)

const (
	reactionsCacheTTL = 15 * time.Second
	voteCooldown      = 5 * time.Second
)

// validReactions is the allowed reaction set, in TS declaration order (governs
// JSON key order via reactionCounts).
var validReactions = []string{"bullish", "hot", "diamond", "bearish", "trash", "warning"}

func isValidReaction(r string) bool {
	for _, v := range validReactions {
		if v == r {
			return true
		}
	}
	return false
}

// reactionCounts holds the per-reaction tallies; field order matches
// validReactions so the JSON object key order is byte-stable.
type reactionCounts struct {
	Bullish int `json:"bullish"`
	Hot     int `json:"hot"`
	Diamond int `json:"diamond"`
	Bearish int `json:"bearish"`
	Trash   int `json:"trash"`
	Warning int `json:"warning"`
}

func (c reactionCounts) total() int {
	return c.Bullish + c.Hot + c.Diamond + c.Bearish + c.Trash + c.Warning
}

// countReactions reads the SCARD of every reaction set for a pool.
func countReactions(ctx context.Context, rdb *goredis.Client, pool string) (reactionCounts, error) {
	get := func(reaction string) (int, error) {
		n, err := rdb.SCard(ctx, "reactions:"+pool+":"+reaction).Result()
		return int(n), err
	}
	var c reactionCounts
	var err error
	if c.Bullish, err = get("bullish"); err != nil {
		return c, err
	}
	if c.Hot, err = get("hot"); err != nil {
		return c, err
	}
	if c.Diamond, err = get("diamond"); err != nil {
		return c, err
	}
	if c.Bearish, err = get("bearish"); err != nil {
		return c, err
	}
	if c.Trash, err = get("trash"); err != nil {
		return c, err
	}
	if c.Warning, err = get("warning"); err != nil {
		return c, err
	}
	return c, nil
}

// RegisterReactions registers the reaction read/write routes (Redis-only state).
func RegisterReactions(r chi.Router, rdb *goredis.Client) {
	r.Get("/stats/{poolAddress}/reactions", reactionsGet(rdb))
	r.Post("/stats/{poolAddress}/reactions", reactionsPost(rdb))
}

func reactionsGet(rdb *goredis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pool := strings.ToLower(chi.URLParam(r, "poolAddress"))
		ctx := r.Context()
		cacheKey := "reactions:" + pool

		if cached, err := rdb.Get(ctx, cacheKey).Result(); err == nil && cached != "" {
			sharedhttp.WriteJSON(w, http.StatusOK, json.RawMessage(cached))
			return
		}

		counts, err := countReactions(ctx, rdb, pool)
		if err != nil {
			sharedhttp.WriteError(w, http.StatusInternalServerError, "Internal Server Error", "reactions lookup failed")
			return
		}
		result := map[string]any{"poolAddress": pool, "reactions": counts, "total": counts.total()}
		if payload, err := json.Marshal(result); err == nil {
			_ = rdb.Set(ctx, cacheKey, payload, reactionsCacheTTL).Err()
		}
		sharedhttp.WriteJSON(w, http.StatusOK, result)
	}
}

type reactionBody struct {
	Reaction      string `json:"reaction"`
	WalletAddress string `json:"walletAddress"`
	Signature     string `json:"signature"`
	Message       string `json:"message"`
}

func reactionsPost(rdb *goredis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pool := strings.ToLower(chi.URLParam(r, "poolAddress"))
		ctx := r.Context()

		var body reactionBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			sharedhttp.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid JSON body")
			return
		}

		if body.WalletAddress == "" || body.Reaction == "" {
			sharedhttp.WriteError(w, http.StatusBadRequest, "Bad Request", "walletAddress and reaction required")
			return
		}
		if body.Signature == "" || body.Message == "" {
			sharedhttp.WriteError(w, http.StatusBadRequest, "Bad Request", "signature and message required for authentication")
			return
		}

		wallet := strings.ToLower(body.WalletAddress)
		if !auth.VerifyWalletSignature(wallet, body.Message, body.Signature) {
			sharedhttp.WriteError(w, http.StatusForbidden, "Forbidden", "Invalid signature")
			return
		}

		reaction := strings.ToLower(body.Reaction)
		if !isValidReaction(reaction) {
			sharedhttp.WriteError(w, http.StatusBadRequest, "Bad Request",
				"Invalid reaction. Must be one of: "+strings.Join(validReactions, ", "))
			return
		}

		// Rate limit: 1 vote per cooldown window per wallet.
		cooldownKey := "reactions:cooldown:" + wallet
		if exists, err := rdb.Get(ctx, cooldownKey).Result(); err == nil && exists != "" {
			sharedhttp.WriteError(w, http.StatusTooManyRequests, "Too Many Requests", "Too many votes, slow down")
			return
		}
		_ = rdb.Set(ctx, cooldownKey, "1", voteCooldown).Err()

		setKey := "reactions:" + pool + ":" + reaction
		userVoteKey := "reactions:uservote:" + pool + ":" + wallet
		existingVote, _ := rdb.Get(ctx, userVoteKey).Result()

		if existingVote == reaction {
			// Toggle off.
			_ = rdb.SRem(ctx, setKey, wallet).Err()
			_ = rdb.Del(ctx, userVoteKey).Err()
		} else {
			if existingVote != "" && isValidReaction(existingVote) {
				_ = rdb.SRem(ctx, "reactions:"+pool+":"+existingVote, wallet).Err()
			}
			_ = rdb.SAdd(ctx, setKey, wallet).Err()
			_ = rdb.Set(ctx, userVoteKey, reaction, 0).Err()
		}

		// Invalidate aggregate cache.
		_ = rdb.Del(ctx, "reactions:"+pool).Err()

		counts, err := countReactions(ctx, rdb, pool)
		if err != nil {
			sharedhttp.WriteError(w, http.StatusInternalServerError, "Internal Server Error", "reactions update failed")
			return
		}

		var userVote any
		if cv, err := rdb.Get(ctx, userVoteKey).Result(); err == nil && cv != "" {
			userVote = cv
		}

		sharedhttp.WriteJSON(w, http.StatusOK, map[string]any{
			"poolAddress": pool,
			"reactions":   counts,
			"total":       counts.total(),
			"userVote":    userVote,
		})
	}
}
