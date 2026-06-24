package engine_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/Sidiora-Technologies/KindleLaunch/core/trading-charts/internal/engine"
	"github.com/Sidiora-Technologies/KindleLaunch/core/trading-charts/internal/internaltest"
	"github.com/Sidiora-Technologies/KindleLaunch/core/trading-charts/internal/store"
)

const gPool = "0xpool00000000000000000000000000000000cccc"

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// seedCursorWithCandle plants a cursor + its anchor candle `back` seconds before
// the current 1m bucket and returns (lastStart, currentBucket).
func seedCursorWithCandle(t *testing.T, st *store.Store, back int64) (int64, int64) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().Unix()
	currentBucket := (now / 60) * 60
	last := currentBucket - back

	if err := st.InsertCandle(ctx, store.CandleRow{
		PoolAddress: gPool, Timeframe: "1m", CandleStart: last,
		Open: "1234", High: "1234", Low: "1234", Close: "1234",
		VolumeUsdl: "10", VolumeToken: "10", BuyVolumeUsdl: "10", SellVolumeUsdl: "0",
		TradeCount: 1, UniqueTraders: 1, LargeTradeCount: 0, LastTradeTs: last,
		SequenceNum: 10, McapOpen: "55", McapHigh: "55", McapLow: "55", McapClose: "55",
	}); err != nil {
		t.Fatalf("seed candle: %v", err)
	}
	if err := st.UpsertCursor(ctx, store.CursorRow{
		PoolAddress: gPool, Timeframe: "1m", LastClose: "1234",
		LastCandleStart: last, LastSequenceNum: 10,
	}); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}
	return last, currentBucket
}

func TestDetectAndFillGapsCarryForward(t *testing.T) {
	ctx := context.Background()
	_, pool := internaltest.NewPostgres(t)
	st := store.New(pool)

	last, currentBucket := seedCursorWithCandle(t, st, 300) // 5 minutes behind

	filled, err := engine.DetectAndFillGaps(ctx, pool, discardLogger(), &engine.GapFillOpts{MaxGapsPerPool: 100})
	if err != nil {
		t.Fatalf("DetectAndFillGaps: %v", err)
	}
	if filled != 4 {
		t.Fatalf("filled = %d, want 4 (gap from last+60 .. currentBucket-60)", filled)
	}

	// A synthesized candle carries the prior close + mcap, zero volume/trades.
	gap, err := st.GetCandle(ctx, gPool, "1m", last+60)
	if err != nil || gap == nil {
		t.Fatalf("gap candle = %v, err %v", gap, err)
	}
	if gap.Open != "1234" || gap.Close != "1234" || gap.McapClose != "55" {
		t.Errorf("gap carry-forward wrong: open=%s close=%s mcap=%s", gap.Open, gap.Close, gap.McapClose)
	}
	if gap.VolumeUsdl != "0" || gap.TradeCount != 0 || gap.UniqueTraders != 0 {
		t.Errorf("gap candle not empty: vol=%s trades=%d", gap.VolumeUsdl, gap.TradeCount)
	}

	// Total candles = 1 anchor + 4 filled.
	all, err := st.HistoryRange(ctx, gPool, "1m", 0, currentBucket)
	if err != nil {
		t.Fatalf("HistoryRange: %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("total candles = %d, want 5", len(all))
	}

	// Cursor advanced to the last filled bucket and sequence.
	cur, _ := st.GetCursor(ctx, gPool, "1m")
	if cur.LastCandleStart != currentBucket-60 {
		t.Errorf("cursor lastCandleStart = %d, want %d", cur.LastCandleStart, currentBucket-60)
	}
	if cur.LastSequenceNum != 14 {
		t.Errorf("cursor lastSequenceNum = %d, want 14", cur.LastSequenceNum)
	}

	// Idempotent: a second run finds no gap and fills nothing.
	again, err := engine.DetectAndFillGaps(ctx, pool, discardLogger(), nil)
	if err != nil {
		t.Fatalf("DetectAndFillGaps second: %v", err)
	}
	if again != 0 {
		t.Errorf("second run filled = %d, want 0", again)
	}
}

func TestDetectAndFillGapsMaxCap(t *testing.T) {
	ctx := context.Background()
	_, pool := internaltest.NewPostgres(t)
	st := store.New(pool)

	seedCursorWithCandle(t, st, 600) // 10 minutes behind

	filled, err := engine.DetectAndFillGaps(ctx, pool, discardLogger(), &engine.GapFillOpts{MaxGapsPerPool: 2})
	if err != nil {
		t.Fatalf("DetectAndFillGaps: %v", err)
	}
	if filled != 2 {
		t.Fatalf("filled = %d, want 2 (capped by MaxGapsPerPool)", filled)
	}
}

func TestDetectAndFillGapsNoGap(t *testing.T) {
	ctx := context.Background()
	_, pool := internaltest.NewPostgres(t)
	st := store.New(pool)

	// last_candle_start == current bucket => nextExpected is in the future.
	now := time.Now().Unix()
	cb := (now / 60) * 60
	if err := st.UpsertCursor(ctx, store.CursorRow{
		PoolAddress: gPool, Timeframe: "1m", LastClose: "1234",
		LastCandleStart: cb, LastSequenceNum: 0,
	}); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}

	filled, err := engine.DetectAndFillGaps(ctx, pool, discardLogger(), nil)
	if err != nil {
		t.Fatalf("DetectAndFillGaps: %v", err)
	}
	if filled != 0 {
		t.Errorf("filled = %d, want 0 (no gap)", filled)
	}
}

func TestDetectAndFillGapsSkipsUnknownTimeframeAndMissingAnchor(t *testing.T) {
	ctx := context.Background()
	_, pool := internaltest.NewPostgres(t)
	st := store.New(pool)

	now := time.Now().Unix()
	cb := (now / 60) * 60

	// Unknown timeframe ("zz" not in TIMEFRAMES) is skipped.
	if err := st.UpsertCursor(ctx, store.CursorRow{
		PoolAddress: gPool, Timeframe: "zz", LastClose: "1",
		LastCandleStart: cb - 300, LastSequenceNum: 0,
	}); err != nil {
		t.Fatalf("seed unknown-tf cursor: %v", err)
	}
	// Valid timeframe but NO anchor candle row -> scan fails -> skipped.
	if err := st.UpsertCursor(ctx, store.CursorRow{
		PoolAddress: gPool, Timeframe: "5m", LastClose: "1",
		LastCandleStart: cb - 3000, LastSequenceNum: 0,
	}); err != nil {
		t.Fatalf("seed missing-anchor cursor: %v", err)
	}

	filled, err := engine.DetectAndFillGaps(ctx, pool, discardLogger(), nil)
	if err != nil {
		t.Fatalf("DetectAndFillGaps: %v", err)
	}
	if filled != 0 {
		t.Errorf("filled = %d, want 0 (both cursors skipped)", filled)
	}
}

func TestDetectAndFillGapsCancelledCtx(t *testing.T) {
	_, pool := internaltest.NewPostgres(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := engine.DetectAndFillGaps(ctx, pool, discardLogger(), nil); err == nil {
		t.Error("DetectAndFillGaps with cancelled ctx should error")
	}
}
