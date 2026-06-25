package consumer

import (
	"context"
	"log/slog"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"

	shareddb "github.com/Sidiora-Technologies/KindleLaunch/shared/db"

	"github.com/Sidiora-Technologies/KindleLaunch/core/stats-workers/internal/store"
)

// defaultHolderDebounce bounds holder-stats refresh to at most once per pool per
// 10s (parity with the TS HOLDER_STATS_DEBOUNCE_MS constant; S-1).
const defaultHolderDebounce = 10 * time.Second

// HolderSwap is the subset of a Swap event the holder tracker consumes.
type HolderSwap struct {
	PoolAddress string
	Sender      string
	IsBuy       bool
	AmountIn    string
	AmountOut   string
}

// HolderTracker maintains per-holder balances and debounces the (expensive)
// holder-stats + risk enrichment. Ports HolderTracker. It is safe for concurrent
// use; pending refresh timers are stopped on Close so no goroutine leaks.
type HolderTracker struct {
	store    *store.Store
	redis    *goredis.Client
	logger   *slog.Logger
	debounce time.Duration

	mu     sync.Mutex
	timers map[string]*time.Timer
	closed bool
}

// NewHolderTracker builds a HolderTracker. A non-positive debounce falls back to
// the 10s parity default.
func NewHolderTracker(st *store.Store, rdb *goredis.Client, logger *slog.Logger, debounce time.Duration) *HolderTracker {
	if debounce <= 0 {
		debounce = defaultHolderDebounce
	}
	return &HolderTracker{
		store:    st,
		redis:    rdb,
		logger:   logger,
		debounce: debounce,
		timers:   make(map[string]*time.Timer),
	}
}

// ProcessSwap applies the sender's balance delta and schedules a debounced
// holder-stats refresh. A "new holder selling" swap is a no-op and skips the
// refresh (parity with the TS early return). Ports HolderTracker.processSwap.
func (h *HolderTracker) ProcessSwap(ctx context.Context, sw HolderSwap) error {
	now := shareddb.NowSeconds()
	applied, err := h.store.ApplyHolderDelta(ctx, sw.PoolAddress, sw.Sender, sw.IsBuy, sw.AmountIn, sw.AmountOut, now)
	if err != nil {
		return err
	}
	if !applied {
		return nil
	}
	h.debouncedRefresh(sw.PoolAddress)
	return nil
}

// debouncedRefresh schedules at most one refresh per pool within the debounce
// window (parity with the TS refreshTimers map + setTimeout/unref).
func (h *HolderTracker) debouncedRefresh(poolAddress string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	if _, pending := h.timers[poolAddress]; pending {
		return
	}
	h.timers[poolAddress] = time.AfterFunc(h.debounce, func() {
		h.mu.Lock()
		delete(h.timers, poolAddress)
		h.mu.Unlock()
		if err := h.RefreshNow(context.Background(), poolAddress); err != nil {
			h.logger.Error("failed to refresh holder stats",
				slog.String("pool", poolAddress), slog.Any("err", err))
		}
	})
}

// RefreshNow recomputes the holder stats + risk for a pool and invalidates the
// Redis cache, synchronously. Ports HolderTracker.refreshPoolHolderStats.
func (h *HolderTracker) RefreshNow(ctx context.Context, poolAddress string) error {
	now := shareddb.NowSeconds()
	if err := h.store.RefreshPoolHolderStats(ctx, poolAddress, now); err != nil {
		return err
	}
	return h.redis.Del(ctx, cacheKey(poolAddress)).Err()
}

// Close stops all pending debounce timers.
func (h *HolderTracker) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for k, t := range h.timers {
		t.Stop()
		delete(h.timers, k)
	}
}
