package consumer

import (
	"context"
	"fmt"
	"log/slog"

	goredis "github.com/redis/go-redis/v9"

	shareddb "github.com/Sidiora-Technologies/KindleLaunch/shared/db"

	"github.com/Sidiora-Technologies/KindleLaunch/core/stats-workers/internal/store"
)

// SwapEvent is a decoded Swap webhook event (the fields the stats consumers use).
type SwapEvent struct {
	PoolAddress    string
	Sender         string
	IsBuy          bool
	AmountIn       string
	AmountOut      string
	Price          string
	Fee            string
	BlockTimestamp int64
	TxHash         string
	LogIndex       int
}

// SwapConsumer maintains transactions, volume buckets, price snapshots/changes
// and the pool_stats price/mcap/high-low for each swap. Ports StatsSwapConsumer.
type SwapConsumer struct {
	store  *store.Store
	redis  *goredis.Client
	logger *slog.Logger
}

// NewSwapConsumer builds a SwapConsumer.
func NewSwapConsumer(st *store.Store, rdb *goredis.Client, logger *slog.Logger) *SwapConsumer {
	return &SwapConsumer{store: st, redis: rdb, logger: logger}
}

// ProcessEvent applies one Swap event end to end, in the same ordered sequence as
// the TS StatsSwapConsumer.processEvent: (1) record the transaction, (2) update
// the 1-minute volume bucket + trader, (3) recompute rolling stats, (4) snapshot
// the price, (5) compute price changes, (6) update price/mcap/high-low, (7) cache.
func (c *SwapConsumer) ProcessEvent(ctx context.Context, ev SwapEvent) error {
	now := shareddb.NowSeconds()

	// 1. Record transaction (idempotent on txHash-logIndex).
	if err := c.store.InsertTransaction(ctx, store.TransactionRow{
		ID:             fmt.Sprintf("%s-%d", ev.TxHash, ev.LogIndex),
		PoolAddress:    ev.PoolAddress,
		Sender:         ev.Sender,
		IsBuy:          ev.IsBuy,
		AmountIn:       ev.AmountIn,
		AmountOut:      ev.AmountOut,
		Price:          ev.Price,
		Fee:            ev.Fee,
		BlockTimestamp: ev.BlockTimestamp,
		TxHash:         ev.TxHash,
	}); err != nil {
		return err
	}

	// 2. Update the 1-minute volume bucket (USDL leg = amountIn for buys).
	bucketStart := (ev.BlockTimestamp / 60) * 60
	volumeUsdl := ev.AmountOut
	if ev.IsBuy {
		volumeUsdl = ev.AmountIn
	}
	if err := c.store.ApplyVolumeBucket(ctx, ev.PoolAddress, bucketStart, volumeUsdl, ev.Price, ev.Sender, ev.IsBuy); err != nil {
		return err
	}

	// 3. Recompute rolling 5m/1h/24h stats.
	if err := c.store.RecalculateRollingStats(ctx, ev.PoolAddress, now); err != nil {
		return err
	}

	// 4. Store the 1-minute price snapshot.
	minuteTs := (now / 60) * 60
	if err := c.store.UpsertPriceSnapshot(ctx, ev.PoolAddress, minuteTs, ev.Price); err != nil {
		return err
	}

	// 5. Compute price changes across all windows.
	changes, err := c.store.ComputePriceChanges(ctx, ev.PoolAddress, ev.Price, now)
	if err != nil {
		return err
	}

	// 6. Update price, market cap, high/low and price changes.
	mcap, err := shareddb.ComputeMarketCap(ev.Price)
	if err != nil {
		return fmt.Errorf("consumer: compute market cap: %w", err)
	}
	if err := c.store.UpdatePoolStatsPriceAndChanges(ctx, ev.PoolAddress, ev.Price, mcap, now, changes); err != nil {
		return err
	}

	// 7. Cache the fresh row in Redis.
	return cachePoolStats(ctx, c.store, c.redis, ev.PoolAddress)
}
