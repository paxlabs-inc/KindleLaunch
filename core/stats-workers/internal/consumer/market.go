package consumer

import (
	"context"
	"fmt"
	"log/slog"

	shareddb "github.com/Sidiora-Technologies/KindleLaunch/shared/db"

	"github.com/Sidiora-Technologies/KindleLaunch/core/stats-workers/internal/store"
)

// initialPriceWad is the launch price WAD for 10K USDL / 1B tokens:
// (10000 * 1e6) * 1e18 / (1e9 * 1e6) = 1e13. Parity with the TS INITIAL_PRICE_WAD.
const initialPriceWad = "10000000000000"

// MarketEvent is a decoded MarketCreated webhook event.
type MarketEvent struct {
	Pool    string
	Token   string
	Creator *string
}

// MarketConsumer bootstraps a pool_stats row when a market is created. Ports
// MarketConsumer.
type MarketConsumer struct {
	store       *store.Store
	logger      *slog.Logger
	initialMcap string
}

// NewMarketConsumer builds a MarketConsumer, precomputing the initial market cap
// (computeMarketCap(initialPriceWad)) exactly as the TS module does at load.
func NewMarketConsumer(st *store.Store, logger *slog.Logger) (*MarketConsumer, error) {
	mcap, err := shareddb.ComputeMarketCap(initialPriceWad)
	if err != nil {
		return nil, fmt.Errorf("consumer: initial mcap: %w", err)
	}
	return &MarketConsumer{store: st, logger: logger, initialMcap: mcap}, nil
}

// ProcessEvent inserts the initial pool_stats row, idempotently. Ports
// MarketConsumer.processEvent.
func (c *MarketConsumer) ProcessEvent(ctx context.Context, ev MarketEvent) error {
	now := shareddb.NowSeconds()
	inserted, err := c.store.InsertInitialPoolStats(ctx, store.InitialPoolStats{
		PoolAddress:    ev.Pool,
		TokenAddress:   ev.Token,
		CreatorAddress: ev.Creator,
		Price:          initialPriceWad,
		MarketCap:      c.initialMcap,
		High24h:        initialPriceWad,
		Low24h:         initialPriceWad,
		CreatedAt:      now,
		UpdatedAt:      now,
	})
	if err != nil {
		return err
	}
	if inserted {
		c.logger.Info("pool stats initialized", slog.String("pool", ev.Pool), slog.String("token", ev.Token))
	}
	return nil
}
