// Package processor is the indexer spine: it polls the chain head, fetches logs
// for each block range via the configured source, decodes launchpad events,
// writes the typed tables idempotently, and fans the events out as signed
// webhooks AFTER the DB write (so a downstream consumer never sees an event
// whose row failed to persist). Parity with the TS block-processor.ts.
package processor

import (
	"context"
	"fmt"
	"log/slog"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/Sidiora-Technologies/KindleLaunch/core/indexer/internal/db/sqlcdb"
	"github.com/Sidiora-Technologies/KindleLaunch/core/indexer/internal/decode"
	"github.com/Sidiora-Technologies/KindleLaunch/core/indexer/internal/publisher"
	"github.com/Sidiora-Technologies/KindleLaunch/core/indexer/internal/source"
	"github.com/Sidiora-Technologies/KindleLaunch/core/indexer/internal/store"
)

// NFTResolver resolves a pool's NFT id via the PoolRegistry (MarketCreated
// enrichment). A nil resolver, or a resolver error, yields a null nft_id —
// matching the TS fail-open behaviour.
type NFTResolver interface {
	NftIDByPool(ctx context.Context, pool common.Address) (*big.Int, error)
}

// Deps wires a Processor. All fields except NFT are required.
type Deps struct {
	Source              source.LogSource
	Head                source.HeadSource
	Store               *store.Store
	Publisher           *publisher.Publisher
	Decoder             *decode.Decoder
	NFT                 NFTResolver
	EventEmitterAddress string
	ChainID             int32
	StartBlock          int64
	BatchSize           int
	Concurrency         int
	PollInterval        time.Duration
	Logger              *slog.Logger
}

// Processor indexes launchpad events into Postgres + webhooks.
type Processor struct {
	deps      Deps
	monitored []string
	nftCache  map[string]*int64
}

// New builds a Processor.
func New(d Deps) *Processor {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.BatchSize <= 0 {
		d.BatchSize = 50
	}
	if d.PollInterval <= 0 {
		d.PollInterval = 100 * time.Millisecond
	}
	return &Processor{
		deps:      d,
		monitored: []string{strings.ToLower(d.EventEmitterAddress)},
		nftCache:  map[string]*int64{},
	}
}

// Run starts the live block-processing loop, blocking until ctx is cancelled.
func (p *Processor) Run(ctx context.Context) error {
	cursor, err := p.deps.Store.GetCursor(ctx, p.deps.ChainID)
	if err != nil {
		return fmt.Errorf("processor: load cursor: %w", err)
	}
	last := p.deps.StartBlock
	if cursor != nil {
		last = *cursor
	}
	p.deps.Logger.Info("starting block processor",
		slog.Int64("lastProcessed", last),
		slog.String("logSource", p.deps.Source.Name()),
		slog.String("headSource", p.deps.Head.Name()))

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		head, err := p.deps.Head.Head(ctx)
		if err != nil {
			p.deps.Logger.Error("get head failed, retrying", slog.String("err", err.Error()))
			if !sleep(ctx, p.deps.PollInterval) {
				return ctx.Err()
			}
			continue
		}
		from := last + 1
		if from > head {
			if !sleep(ctx, p.deps.PollInterval) {
				return ctx.Err()
			}
			continue
		}
		to := from + int64(p.deps.BatchSize) - 1
		if to > head {
			to = head
		}
		if err := p.ProcessBatch(ctx, from, to); err != nil {
			p.deps.Logger.Error("block processing error, retrying", slog.Int64("from", from), slog.Int64("to", to), slog.String("err", err.Error()))
			if !sleep(ctx, 5*time.Second) {
				return ctx.Err()
			}
			continue
		}
		last = to
	}
}

// ProcessBatch fetches, decodes, persists, and fans out the events for the
// inclusive [from,to] block range, then advances the cursor. Exported so tests
// drive one batch directly against real Postgres + a real RPC/SQL source.
func (p *Processor) ProcessBatch(ctx context.Context, from, to int64) error {
	events, err := p.collect(ctx, from, to)
	if err != nil {
		return err
	}
	if err := p.deps.Store.UpsertCursor(ctx, p.deps.ChainID, to); err != nil {
		return fmt.Errorf("upsert cursor: %w", err)
	}
	// Fan out AFTER the DB commit + cursor advance (i9 ordering).
	if p.deps.Publisher != nil {
		p.deps.Publisher.PublishBatch(ctx, events)
	}
	return nil
}

// collect fetches the range, decodes + persists every matched event, and
// returns the webhook envelopes (no cursor advance, no fan-out). Shared by the
// live ProcessBatch and the backfill processor (which discards the envelopes).
func (p *Processor) collect(ctx context.Context, from, to int64) ([]publisher.WebhookEvent, error) {
	res, err := p.deps.Source.FetchLogs(ctx, source.FetchOptions{
		MonitoredAddresses: p.monitored,
		FromBlock:          from,
		ToBlock:            to,
		Concurrency:        p.deps.Concurrency,
	})
	if err != nil {
		return nil, fmt.Errorf("fetch logs [%d..%d]: %w", from, to, err)
	}

	var events []publisher.WebhookEvent
	for i := range res.Logs {
		ev, derr := p.deps.Decoder.Decode(res.Logs[i])
		if derr != nil {
			p.deps.Logger.Warn("decode log failed", slog.String("err", derr.Error()))
			continue
		}
		if ev == nil {
			continue
		}
		blockTs, ok := res.BlockTimestamps[ev.BlockNumber]
		if !ok || blockTs == 0 {
			p.deps.Logger.Warn("skipping event — no valid block timestamp",
				slog.Uint64("blockNumber", ev.BlockNumber), slog.String("txHash", ev.TxHash))
			continue
		}
		wev, herr := p.processEvent(ctx, ev, blockTs, res.TxFromMap)
		if herr != nil {
			return nil, fmt.Errorf("process %s @ %d: %w", ev.EventName, ev.BlockNumber, herr)
		}
		if wev != nil {
			events = append(events, *wev)
		}
	}
	return events, nil
}

// processEvent persists one decoded event to its typed table and returns the
// webhook envelope to fan out (nil for events with no downstream contract).
func (p *Processor) processEvent(ctx context.Context, ev *decode.Event, blockTs int64, txFrom map[string]string) (*publisher.WebhookEvent, error) {
	blockNumber := int64(ev.BlockNumber)
	logIndex := int32(ev.LogIndex)
	id := fmt.Sprintf("%s-%d", ev.TxHash, ev.LogIndex)
	traceID := fmt.Sprintf("%s:%d", ev.TxHash, ev.LogIndex)

	// serializedArgs is the webhook arg map (decode already humanized bigints
	// to strings). Enrichments below mutate this copy, not the decoder output.
	args := cloneArgs(ev.Args)

	wev := func() *publisher.WebhookEvent {
		return &publisher.WebhookEvent{
			EventName:      ev.EventName,
			BlockNumber:    blockNumber,
			BlockTimestamp: blockTs,
			TxHash:         ev.TxHash,
			LogIndex:       int(ev.LogIndex),
			Args:           args,
			TraceID:        traceID,
		}
	}

	switch ev.EventName {
	case "MarketCreated":
		if err := p.handleMarketCreated(ctx, args, ev.TxHash, blockNumber, blockTs); err != nil {
			return nil, err
		}
		return wev(), nil

	case "Swap":
		eventSender := asString(args["sender"])
		actualUser := txFrom[ev.TxHash]
		if actualUser == "" {
			actualUser = eventSender
		}
		poolAddr, err := p.handleSwap(ctx, args, id, eventSender, actualUser, blockNumber, blockTs, logIndex)
		if err != nil {
			return nil, err
		}
		args["poolAddress"] = poolAddr
		args["sender"] = actualUser
		args["blockTimestamp"] = blockTs
		return wev(), nil

	case "PoolStateUpdated":
		poolID := asString(args["poolId"])
		if err := p.handlePoolStateUpdated(ctx, args, poolID, ev.TxHash, blockNumber, blockTs, logIndex); err != nil {
			return nil, err
		}
		poolAddr := ""
		if pool, err := p.deps.Store.GetPoolByPoolID(ctx, poolID); err != nil {
			return nil, err
		} else if pool != nil {
			poolAddr = pool.PoolAddress
		}
		args["poolAddress"] = poolAddr
		return wev(), nil

	case "FeeRecorded":
		if err := p.handleFeeRecorded(ctx, args, id, blockNumber, blockTs, logIndex); err != nil {
			return nil, err
		}
		return wev(), nil

	case "FeeDistributed":
		if err := p.handleFeeDistributed(ctx, args, id, blockNumber, blockTs, logIndex); err != nil {
			return nil, err
		}
		return wev(), nil

	case "FeeStrategyChanged":
		if err := p.handleFeeStrategyChanged(ctx, args, id, blockNumber, blockTs, logIndex); err != nil {
			return nil, err
		}
		return wev(), nil

	case "OpticalExecuted":
		if err := p.handleOpticalExecuted(ctx, args, id, blockNumber, blockTs, logIndex); err != nil {
			return nil, err
		}
		return wev(), nil

	case "TokenForTokenSwap":
		actualUser := txFrom[ev.TxHash]
		if actualUser == "" {
			actualUser = asString(args["sender"])
		}
		if err := p.handleTokenForTokenSwap(ctx, args, id, actualUser, blockNumber, blockTs, logIndex); err != nil {
			return nil, err
		}
		args["sender"] = actualUser
		args["blockTimestamp"] = blockTs
		return wev(), nil

	case "ConfigUpdated":
		if err := p.handleConfigUpdated(ctx, args, id, blockNumber, blockTs, logIndex); err != nil {
			return nil, err
		}
		return wev(), nil

	default:
		return nil, nil
	}
}

func (p *Processor) handleMarketCreated(ctx context.Context, args map[string]any, txHash string, blockNumber, blockTs int64) error {
	pool := asString(args["pool"])
	nftID := p.resolveNftID(ctx, pool)
	return p.deps.Store.InsertPool(ctx, sqlcdb.InsertPoolParams{
		PoolAddress:  pool,
		TokenAddress: asString(args["token"]),
		Creator:      asString(args["creator"]),
		Optical:      asString(args["optical"]),
		PoolID:       asString(args["poolId"]),
		NftID:        nftID,
		CreatedAt:    blockTs,
		CreatedBlock: blockNumber,
		TxHash:       txHash,
	})
}

func (p *Processor) handleSwap(ctx context.Context, args map[string]any, id, eventSender, actualUser string, blockNumber, blockTs int64, logIndex int32) (string, error) {
	poolID := asString(args["poolId"])
	poolAddr := ""
	if pool, err := p.deps.Store.GetPoolByPoolID(ctx, poolID); err != nil {
		return "", err
	} else if pool != nil {
		poolAddr = pool.PoolAddress
	}
	router := eventSender
	return poolAddr, p.deps.Store.InsertSwap(ctx, sqlcdb.InsertSwapParams{
		ID:             id,
		PoolID:         poolID,
		PoolAddress:    poolAddr,
		Sender:         actualUser,
		Router:         &router,
		IsBuy:          asBool(args["isBuy"]),
		AmountIn:       asString(args["amountIn"]),
		AmountOut:      asString(args["amountOut"]),
		Fee:            asString(args["fee"]),
		Price:          asString(args["price"]),
		BlockNumber:    blockNumber,
		BlockTimestamp: blockTs,
		TxHash:         strings.SplitN(id, "-", 2)[0],
		LogIndex:       logIndex,
	})
}

func (p *Processor) handlePoolStateUpdated(ctx context.Context, args map[string]any, poolID, txHash string, blockNumber, blockTs int64, logIndex int32) error {
	return p.deps.Store.InsertPoolStateSnapshot(ctx, sqlcdb.InsertPoolStateSnapshotParams{
		ID:             fmt.Sprintf("%s-%d-%d", poolID, blockNumber, logIndex),
		PoolID:         poolID,
		VirtualReserve: asString(args["virtualReserve"]),
		RealReserve:    asString(args["realReserve"]),
		TokenReserve:   asString(args["tokenReserve"]),
		Price:          asString(args["price"]),
		BlockNumber:    blockNumber,
		BlockTimestamp: blockTs,
		TxHash:         txHash,
		LogIndex:       logIndex,
	})
}

func (p *Processor) handleFeeRecorded(ctx context.Context, args map[string]any, id string, blockNumber, blockTs int64, logIndex int32) error {
	return p.deps.Store.InsertFeeEvent(ctx, sqlcdb.InsertFeeEventParams{
		ID:             id,
		PoolID:         asString(args["poolId"]),
		FeeAmount:      asString(args["feeAmount"]),
		ProtocolCut:    asString(args["protocolCut"]),
		PoolCut:        asString(args["poolCut"]),
		BlockNumber:    blockNumber,
		BlockTimestamp: blockTs,
		TxHash:         txHashFromID(id),
		LogIndex:       logIndex,
	})
}

func (p *Processor) handleFeeDistributed(ctx context.Context, args map[string]any, id string, blockNumber, blockTs int64, logIndex int32) error {
	nftID, err := asInt64(args["nftId"])
	if err != nil {
		return fmt.Errorf("nftId: %w", err)
	}
	strategy, err := asInt64(args["strategy"])
	if err != nil {
		return fmt.Errorf("strategy: %w", err)
	}
	return p.deps.Store.InsertFeeDistribution(ctx, sqlcdb.InsertFeeDistributionParams{
		ID:             id,
		PoolID:         asString(args["poolId"]),
		NftID:          nftID,
		Strategy:       int32(strategy),
		Amount:         asString(args["amount"]),
		Recipient:      asString(args["recipient"]),
		BlockNumber:    blockNumber,
		BlockTimestamp: blockTs,
		TxHash:         txHashFromID(id),
		LogIndex:       logIndex,
	})
}

func (p *Processor) handleFeeStrategyChanged(ctx context.Context, args map[string]any, id string, blockNumber, blockTs int64, logIndex int32) error {
	nftID, err := asInt64(args["nftId"])
	if err != nil {
		return fmt.Errorf("nftId: %w", err)
	}
	oldStrat, err := asInt64(args["oldStrategy"])
	if err != nil {
		return fmt.Errorf("oldStrategy: %w", err)
	}
	newStrat, err := asInt64(args["newStrategy"])
	if err != nil {
		return fmt.Errorf("newStrategy: %w", err)
	}
	return p.deps.Store.InsertFeeStrategyChange(ctx, sqlcdb.InsertFeeStrategyChangeParams{
		ID:             id,
		PoolID:         asString(args["poolId"]),
		NftID:          nftID,
		OldStrategy:    int32(oldStrat),
		NewStrategy:    int32(newStrat),
		BlockNumber:    blockNumber,
		BlockTimestamp: blockTs,
		TxHash:         txHashFromID(id),
		LogIndex:       logIndex,
	})
}

func (p *Processor) handleOpticalExecuted(ctx context.Context, args map[string]any, id string, blockNumber, blockTs int64, logIndex int32) error {
	return p.deps.Store.InsertOpticalExecution(ctx, sqlcdb.InsertOpticalExecutionParams{
		ID:             id,
		PoolID:         asString(args["poolId"]),
		Optical:        asString(args["optical"]),
		HookName:       asString(args["hookName"]),
		Data:           asString(args["data"]),
		BlockNumber:    blockNumber,
		BlockTimestamp: blockTs,
		TxHash:         txHashFromID(id),
		LogIndex:       logIndex,
	})
}

func (p *Processor) handleTokenForTokenSwap(ctx context.Context, args map[string]any, id, actualUser string, blockNumber, blockTs int64, logIndex int32) error {
	return p.deps.Store.InsertTokenForTokenSwap(ctx, sqlcdb.InsertTokenForTokenSwapParams{
		ID:               id,
		Sender:           actualUser,
		TokenIn:          asString(args["tokenIn"]),
		TokenOut:         asString(args["tokenOut"]),
		PoolIn:           asString(args["poolIn"]),
		PoolOut:          asString(args["poolOut"]),
		AmountIn:         asString(args["amountIn"]),
		IntermediateUsdl: asString(args["intermediateUsdl"]),
		AmountOut:        asString(args["amountOut"]),
		FeeIn:            asString(args["feeIn"]),
		FeeOut:           asString(args["feeOut"]),
		BlockNumber:      blockNumber,
		BlockTimestamp:   blockTs,
		TxHash:           txHashFromID(id),
		LogIndex:         logIndex,
	})
}

func (p *Processor) handleConfigUpdated(ctx context.Context, args map[string]any, id string, blockNumber, blockTs int64, logIndex int32) error {
	return p.deps.Store.InsertConfigUpdate(ctx, sqlcdb.InsertConfigUpdateParams{
		ID:             id,
		Key:            asString(args["key"]),
		OldValue:       asString(args["oldValue"]),
		NewValue:       asString(args["newValue"]),
		BlockNumber:    blockNumber,
		BlockTimestamp: blockTs,
		TxHash:         txHashFromID(id),
		LogIndex:       logIndex,
	})
}

// resolveNftID looks up (and caches) the pool's NFT id, returning nil on a
// missing resolver or any lookup error (fail-open parity with the TS handler).
func (p *Processor) resolveNftID(ctx context.Context, pool string) *int64 {
	key := strings.ToLower(pool)
	if v, ok := p.nftCache[key]; ok {
		return v
	}
	if p.deps.NFT == nil {
		p.nftCache[key] = nil
		return nil
	}
	bi, err := p.deps.NFT.NftIDByPool(ctx, common.HexToAddress(pool))
	if err != nil || bi == nil {
		p.deps.Logger.Warn("failed to read nftId for pool", slog.String("pool", pool))
		p.nftCache[key] = nil
		return nil
	}
	v := bi.Int64()
	p.nftCache[key] = &v
	return &v
}

func txHashFromID(id string) string { return strings.SplitN(id, "-", 2)[0] }

// ── arg coercion helpers ───────────────────────────────────────────

func cloneArgs(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+3)
	for k, v := range in {
		out[k] = v
	}
	return out
}

func asString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	default:
		return fmt.Sprint(x)
	}
}

func asBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case *big.Int:
		return x.Sign() != 0
	default:
		return false
	}
}

func asInt64(v any) (int64, error) {
	switch x := v.(type) {
	case string:
		return strconv.ParseInt(x, 10, 64)
	case int64:
		return x, nil
	case int:
		return int64(x), nil
	case uint8:
		return int64(x), nil
	case uint16:
		return int64(x), nil
	case uint32:
		return int64(x), nil
	case uint64:
		return int64(x), nil
	case *big.Int:
		return x.Int64(), nil
	default:
		return 0, fmt.Errorf("cannot coerce %T to int64", v)
	}
}

// sleep waits d or returns false if ctx is cancelled first.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
