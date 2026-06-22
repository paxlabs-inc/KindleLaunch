// Package source provides swappable EVM log + chain-head sources for the
// indexer, parity with the TS source-factory (paxscan | rpc | rpc-getlogs).
//
//   - rpc-getlogs : load-balanced eth_getLogs over a pool of validator RPCs with
//     stale-node quarantine + failover (live-preferred). [evm.go]
//   - rpc         : single-node eth_getLogs (local-dev / disaster-recovery). [evm.go]
//   - paxscan     : SQL reads from the Blockscout DB (backfill-preferred). [paxscan.go]
//
// Meta-AG is excluded from the new chain (L4), so the monitored address set is
// just the EventEmitter and there is no meta-ag log partition.
package source

import (
	"context"
	"strings"

	ethtypes "github.com/ethereum/go-ethereum/core/types"

	"github.com/Sidiora-Technologies/KindleLaunch/protocol"
)

// FetchOptions bounds one fetchLogs call.
type FetchOptions struct {
	// MonitoredAddresses is the set of lowercased 0x-hex contract addresses to
	// fetch logs for (the EventEmitter for this build).
	MonitoredAddresses []string
	// FromBlock / ToBlock is the inclusive range.
	FromBlock int64
	ToBlock   int64
	// Concurrency caps parallel per-block enrichment calls (rpc sources only).
	Concurrency int
}

// FetchResult is the source-agnostic output consumed by the processor.
type FetchResult struct {
	// Logs are the matched logs in (block, logIndex) ascending order.
	Logs []ethtypes.Log
	// BlockTimestamps maps block number -> unix epoch seconds.
	BlockTimestamps map[uint64]int64
	// TxFromMap maps a lowercased tx hash -> the EOA `from` that signed it,
	// populated only for the Swap-like events that need it (see TxFromTopics).
	TxFromMap map[string]string
}

// LogSource fetches decoded-ready logs for a block range.
type LogSource interface {
	FetchLogs(ctx context.Context, opts FetchOptions) (FetchResult, error)
	Name() string
	Close() error
}

// HeadSource reports the current chain head.
type HeadSource interface {
	Head(ctx context.Context) (int64, error)
	Name() string
	Close() error
}

// txFromTopics is the set of topic0s whose authoritative EOA sender lives on the
// transaction's `from` rather than in the event args (Swap, TokenForTokenSwap).
// Computed once from the protocol registry so it always tracks the bindings.
// (BestRouteSwap / MultiHopSwap are meta-ag events, excluded — L4.)
var txFromTopics = buildTxFromTopics()

func buildTxFromTopics() map[string]struct{} {
	want := map[string]struct{}{"Swap": {}, "TokenForTokenSwap": {}}
	out := make(map[string]struct{})
	for _, d := range protocol.Events() {
		if _, ok := want[d.Name]; ok {
			out[strings.ToLower(d.Topic0.Hex())] = struct{}{}
		}
	}
	return out
}

// needsTxFrom reports whether a log's topic0 requires an EOA-sender lookup.
func needsTxFrom(topic0 string) bool {
	_, ok := txFromTopics[strings.ToLower(topic0)]
	return ok
}
