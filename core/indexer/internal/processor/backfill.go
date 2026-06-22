package processor

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/Sidiora-Technologies/KindleLaunch/core/indexer/internal/db/sqlcdb"
)

// Backfill replays a historical block range into the typed tables, tracking
// progress in indexer.backfill_jobs so an interrupted run resumes. It persists
// rows WITHOUT fanning out webhooks (parity with the TS BackfillProcessor,
// which never publishes backfilled events to live consumers).
type Backfill struct {
	p         *Processor
	fromBlock *int64
	batchSize int
}

// NewBackfill builds a Backfill over an existing Processor. fromBlock overrides
// the start (BACKFILL_FROM_BLOCK); nil falls back to the processor's StartBlock.
func NewBackfill(p *Processor, fromBlock *int64, batchSize int) *Backfill {
	if batchSize <= 0 {
		batchSize = 2000
	}
	return &Backfill{p: p, fromBlock: fromBlock, batchSize: batchSize}
}

// Run executes (or resumes) a full backfill to the current head.
func (b *Backfill) Run(ctx context.Context) error {
	log := b.p.deps.Logger
	log.Info("starting full backfill")

	job, err := b.p.deps.Store.ActiveBackfillJob(ctx)
	if err != nil {
		return fmt.Errorf("backfill: load active job: %w", err)
	}

	var fromBlock, toBlock int64
	var jobID string
	if job != nil {
		jobID = job.ID
		fromBlock = job.LastProcessedBlock + 1
		toBlock = job.ToBlock
		log.Info("resuming existing backfill job", slog.String("jobId", jobID), slog.Int64("from", fromBlock), slog.Int64("to", toBlock))
	} else {
		head, herr := b.p.deps.Head.Head(ctx)
		if herr != nil {
			return fmt.Errorf("backfill: get head: %w", herr)
		}
		fromBlock = b.p.deps.StartBlock
		if b.fromBlock != nil {
			fromBlock = *b.fromBlock
		}
		toBlock = head
		jobID = uuid.NewString()
		if err := b.p.deps.Store.InsertBackfillJob(ctx, sqlcdb.InsertBackfillJobParams{
			ID:                 jobID,
			ChainID:            b.p.deps.ChainID,
			FromBlock:          fromBlock,
			ToBlock:            toBlock,
			LastProcessedBlock: fromBlock - 1,
			TotalBlocks:        toBlock - fromBlock + 1,
			Status:             "running",
		}); err != nil {
			return fmt.Errorf("backfill: insert job: %w", err)
		}
		log.Info("created new backfill job", slog.String("jobId", jobID), slog.Int64("from", fromBlock), slog.Int64("to", toBlock))
	}

	if err := b.run(ctx, jobID, fromBlock, toBlock); err != nil {
		msg := err.Error()
		if ferr := b.p.deps.Store.FailBackfillJob(ctx, jobID, msg); ferr != nil {
			log.Error("backfill: mark failed", slog.String("err", ferr.Error()))
		}
		return err
	}
	return nil
}

func (b *Backfill) run(ctx context.Context, jobID string, fromBlock, toBlock int64) error {
	start := time.Now()
	current := fromBlock
	for current <= toBlock {
		if err := ctx.Err(); err != nil {
			return err
		}
		batchEnd := current + int64(b.batchSize) - 1
		if batchEnd > toBlock {
			batchEnd = toBlock
		}
		if _, err := b.p.collect(ctx, current, batchEnd); err != nil {
			return err
		}
		if err := b.p.deps.Store.UpdateBackfillProgress(ctx, jobID, batchEnd); err != nil {
			return fmt.Errorf("backfill: update progress: %w", err)
		}
		b.p.deps.Logger.Info("backfill progress",
			slog.String("batch", fmt.Sprintf("%d-%d", current, batchEnd)),
			slog.Int64("to", toBlock))
		current = batchEnd + 1
	}

	if err := b.p.deps.Store.CompleteBackfillJob(ctx, jobID); err != nil {
		return fmt.Errorf("backfill: complete job: %w", err)
	}
	// Advance the live cursor so live mode picks up from here.
	if err := b.p.deps.Store.UpsertCursor(ctx, b.p.deps.ChainID, toBlock); err != nil {
		return fmt.Errorf("backfill: advance cursor: %w", err)
	}
	b.p.deps.Logger.Info("backfill completed",
		slog.String("jobId", jobID), slog.Int64("from", fromBlock), slog.Int64("to", toBlock),
		slog.Duration("elapsed", time.Since(start)))
	return nil
}
