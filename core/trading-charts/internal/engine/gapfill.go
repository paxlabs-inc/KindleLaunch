package engine

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Sidiora-Technologies/KindleLaunch/shared/constants"
)

// DetectAndFillGaps finds missing candles for all active pools and generates
// empty candles (carry-forward close) for any gaps up to the current timestamp
// (C-3). Returns the total number of candles filled.
func DetectAndFillGaps(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger, opts *GapFillOpts) (int, error) {
	maxGaps := 100
	if opts != nil && opts.MaxGapsPerPool > 0 {
		maxGaps = opts.MaxGapsPerPool
	}
	now := time.Now().Unix()
	totalFilled := 0

	rows, err := pool.Query(ctx, `
		SELECT pool_address, timeframe, last_close, last_candle_start, last_sequence_num
		FROM candles.candle_cursors
	`)
	if err != nil {
		return 0, fmt.Errorf("gapfill: query cursors: %w", err)
	}
	defer rows.Close()

	type cursor struct {
		poolAddr        string
		timeframe       string
		lastClose       string
		lastCandleStart int64
		lastSeqNum      int64
	}
	var cursors []cursor
	for rows.Next() {
		var c cursor
		if err := rows.Scan(&c.poolAddr, &c.timeframe, &c.lastClose, &c.lastCandleStart, &c.lastSeqNum); err != nil {
			return 0, fmt.Errorf("gapfill: scan cursor: %w", err)
		}
		cursors = append(cursors, c)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("gapfill: rows err: %w", err)
	}

	for _, c := range cursors {
		interval, ok := constants.Timeframes[c.timeframe]
		if !ok {
			continue
		}
		nextExpected := c.lastCandleStart + interval
		currentBucket := (now / interval) * interval
		if nextExpected >= currentBucket {
			continue
		}

		// Fetch the last candle's close + mcapClose + sequenceNum.
		var carryClose, carryMcap string
		var seq int64
		err := pool.QueryRow(ctx, `
			SELECT close, mcap_close, sequence_num
			FROM candles.candles
			WHERE pool_address = $1 AND timeframe = $2 AND candle_start = $3
		`, c.poolAddr, c.timeframe, c.lastCandleStart).Scan(&carryClose, &carryMcap, &seq)
		if err != nil {
			continue
		}

		filled := 0
		for t := nextExpected; t < currentBucket && filled < maxGaps; t += interval {
			seq++
			filled++

			_, err := pool.Exec(ctx, `
				INSERT INTO candles.candles (
					pool_address, timeframe, candle_start, open, high, low, close,
					volume_usdl, volume_token, buy_volume_usdl, sell_volume_usdl,
					trade_count, unique_traders, large_trade_count, last_trade_ts,
					sequence_num, mcap_open, mcap_high, mcap_low, mcap_close
				) VALUES ($1,$2,$3,$4,$5,$6,$7,'0','0','0','0',0,0,0,$8,$9,$10,$11,$12,$13)
				ON CONFLICT DO NOTHING
			`,
				c.poolAddr, c.timeframe, t,
				carryClose, carryClose, carryClose, carryClose,
				t, seq, carryMcap, carryMcap, carryMcap, carryMcap,
			)
			if err != nil {
				logger.Warn("gapfill: insert", slog.String("err", err.Error()))
				break
			}
		}

		if filled > 0 {
			newLastStart := nextExpected + int64(filled-1)*interval
			_, err := pool.Exec(ctx, `
				UPDATE candles.candle_cursors SET last_candle_start = $3, last_sequence_num = $4
				WHERE pool_address = $1 AND timeframe = $2
			`, c.poolAddr, c.timeframe, newLastStart, seq)
			if err != nil {
				logger.Warn("gapfill: update cursor", slog.String("err", err.Error()))
			}
			totalFilled += filled
			logger.Info("filled candle gaps (C-3)",
				slog.String("pool", c.poolAddr), slog.String("timeframe", c.timeframe), slog.Int("filled", filled))
		}
	}

	return totalFilled, nil
}

// GapFillOpts configures gap detection.
type GapFillOpts struct {
	MaxGapsPerPool int
}
