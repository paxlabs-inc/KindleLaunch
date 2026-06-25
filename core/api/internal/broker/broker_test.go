package broker

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/Sidiora-Technologies/KindleLaunch/shared/constants"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newBroker() *Broker {
	return New(Options{Logger: discardLogger()})
}

func swapPayload(pool string, ts int64) []byte {
	b, _ := json.Marshal(map[string]any{
		"poolAddress":    pool,
		"sender":         "0x1111111111111111111111111111111111111111",
		"blockTimestamp": ts,
	})
	return b
}

func candleUpdatePayload(pool, tf string, start int64, closePrice string) []byte {
	b, _ := json.Marshal(map[string]any{
		"poolAddress": pool, "timeframe": tf, "candleStart": start,
		"open": "1000000", "high": "2000000", "low": "500000", "close": closePrice,
		"volumeUsdl": "0", "volumeToken": "0", "buyVolumeUsdl": "0", "sellVolumeUsdl": "0",
		"tradeCount": 1, "uniqueTraders": 1, "largeTradeCount": 0,
		"mcapOpen": "0", "mcapHigh": "0", "mcapLow": "0", "mcapClose": "0",
	})
	return b
}

func poolStatePayload(pool string, n int64) []byte {
	b, _ := json.Marshal(map[string]any{"poolAddress": pool, "reserve": n})
	return b
}

func chanSet(chs ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(chs))
	for _, c := range chs {
		m[c] = struct{}{}
	}
	return m
}

func TestDefaultTransform_CandleFrameShape(t *testing.T) {
	m, ok := DefaultTransform(constants.ChannelCandleUpdate, candleUpdatePayload("0xAAA", "1m", 60, "1500000"))
	if !ok {
		t.Fatal("transform candle: ok=false")
	}
	if m.pool != "0xAAA" {
		t.Errorf("pool = %q, want 0xAAA", m.pool)
	}
	if m.coalesceKey != constants.ChannelCandleUpdate+":0xAAA:1m" {
		t.Errorf("coalesceKey = %q", m.coalesceKey)
	}
	var frame struct {
		Type string         `json:"type"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(m.bytes, &frame); err != nil {
		t.Fatalf("frame unmarshal: %v", err)
	}
	if frame.Type != "candle_update" {
		t.Errorf("type = %q, want candle_update", frame.Type)
	}
	if frame.Data["poolAddress"] != "0xAAA" {
		t.Errorf("data.poolAddress = %v", frame.Data["poolAddress"])
	}
	// OHLC must be numeric (float), not the raw text bigints (parity with charts).
	if _, isNum := frame.Data["open"].(float64); !isNum {
		t.Errorf("data.open should be numeric, got %T", frame.Data["open"])
	}
}

func TestDefaultTransform_GenericEnvelope(t *testing.T) {
	m, ok := DefaultTransform(constants.ChannelSwap, swapPayload("0xBBB", 123))
	if !ok {
		t.Fatal("transform swap: ok=false")
	}
	if m.coalesceKey != "" {
		t.Errorf("swap should be must-deliver (no coalesceKey), got %q", m.coalesceKey)
	}
	var frame struct {
		Type    string          `json:"type"`
		Channel string          `json:"channel"`
		Pool    string          `json:"pool"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(m.bytes, &frame); err != nil {
		t.Fatalf("frame unmarshal: %v", err)
	}
	if frame.Type != "swap" || frame.Channel != constants.ChannelSwap || frame.Pool != "0xBBB" {
		t.Errorf("unexpected frame: %+v", frame)
	}
}

func TestDefaultTransform_InvalidJSON(t *testing.T) {
	if _, ok := DefaultTransform(constants.ChannelSwap, []byte("{not json")); ok {
		t.Error("invalid JSON should yield ok=false")
	}
}

func TestDispatch_RoutesByChannelAndPool(t *testing.T) {
	b := newBroker()
	sub := b.Subscribe(Filter{
		Channels: chanSet(constants.ChannelSwap),
		Pools:    map[string]struct{}{"0xAAA": {}},
	}, 16)
	defer sub.Close()

	b.Dispatch(constants.ChannelSwap, swapPayload("0xAAA", 1))                             // match
	b.Dispatch(constants.ChannelSwap, swapPayload("0xBBB", 2))                             // wrong pool
	b.Dispatch(constants.ChannelCandleUpdate, candleUpdatePayload("0xAAA", "1m", 60, "1")) // wrong channel

	got := sub.Drain()
	if len(got) != 1 {
		t.Fatalf("Drain len = %d, want 1 (only the matching swap)", len(got))
	}
}

func TestDispatch_WildcardReceivesAllChannels(t *testing.T) {
	b := newBroker()
	sub := b.Subscribe(Filter{All: true}, 16) // wildcard
	defer sub.Close()

	b.Dispatch(constants.ChannelSwap, swapPayload("0xAAA", 1))
	b.Dispatch(constants.ChannelMarketCreated, swapPayload("0xCCC", 2))

	if got := sub.Drain(); len(got) != 2 {
		t.Fatalf("wildcard Drain len = %d, want 2", len(got))
	}
}

func TestDispatch_GlobalEventReachesPoolFilteredSubscriber(t *testing.T) {
	b := newBroker()
	sub := b.Subscribe(Filter{
		Channels: chanSet(constants.ChannelConfigUpdated),
		Pools:    map[string]struct{}{"0xAAA": {}},
	}, 16)
	defer sub.Close()

	// config_updated carries no poolAddress -> global -> delivered despite the
	// pool filter.
	b.Dispatch(constants.ChannelConfigUpdated, []byte(`{"setting":"x"}`))
	if got := sub.Drain(); len(got) != 1 {
		t.Fatalf("global event Drain len = %d, want 1", len(got))
	}
}

func TestDispatch_CoalescesLatestCandlePerKey(t *testing.T) {
	b := newBroker()
	sub := b.Subscribe(Filter{Channels: chanSet(constants.ChannelCandleUpdate)}, 16)
	defer sub.Close()

	b.Dispatch(constants.ChannelCandleUpdate, candleUpdatePayload("0xAAA", "1m", 60, "100"))
	b.Dispatch(constants.ChannelCandleUpdate, candleUpdatePayload("0xAAA", "1m", 61, "200"))
	b.Dispatch(constants.ChannelCandleUpdate, candleUpdatePayload("0xAAA", "1m", 62, "300"))

	got := sub.Drain()
	if len(got) != 1 {
		t.Fatalf("coalesced Drain len = %d, want 1 (latest only)", len(got))
	}
	var frame struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(got[0], &frame); err != nil {
		t.Fatalf("frame unmarshal: %v", err)
	}
	if frame.Data["candleStart"].(float64) != 62 {
		t.Errorf("coalesced candleStart = %v, want 62 (latest)", frame.Data["candleStart"])
	}
}

func TestDispatch_DistinctCoalesceKeysKept(t *testing.T) {
	b := newBroker()
	sub := b.Subscribe(Filter{Channels: chanSet(constants.ChannelCandleUpdate)}, 16)
	defer sub.Close()

	b.Dispatch(constants.ChannelCandleUpdate, candleUpdatePayload("0xAAA", "1m", 60, "1"))
	b.Dispatch(constants.ChannelCandleUpdate, candleUpdatePayload("0xAAA", "5m", 60, "1"))
	b.Dispatch(constants.ChannelCandleUpdate, candleUpdatePayload("0xBBB", "1m", 60, "1"))

	if got := sub.Drain(); len(got) != 3 {
		t.Fatalf("distinct keys Drain len = %d, want 3", len(got))
	}
}

func TestDispatch_MustDeliverPreservesOrder(t *testing.T) {
	b := newBroker()
	sub := b.Subscribe(Filter{Channels: chanSet(constants.ChannelSwap)}, 16)
	defer sub.Close()

	for i := int64(1); i <= 3; i++ {
		b.Dispatch(constants.ChannelSwap, swapPayload("0xAAA", i))
	}
	got := sub.Drain()
	if len(got) != 3 {
		t.Fatalf("swaps Drain len = %d, want 3 (no drops)", len(got))
	}
	for i, raw := range got {
		var frame struct {
			Data struct {
				BlockTimestamp int64 `json:"blockTimestamp"`
			} `json:"data"`
		}
		_ = json.Unmarshal(raw, &frame)
		if frame.Data.BlockTimestamp != int64(i+1) {
			t.Errorf("frame %d ts = %d, want %d", i, frame.Data.BlockTimestamp, i+1)
		}
	}
}

func TestDeliver_EvictsSlowConsumerOnOverflow(t *testing.T) {
	b := newBroker()
	sub := b.Subscribe(Filter{Channels: chanSet(constants.ChannelSwap)}, 2)
	defer sub.Close()

	for i := int64(0); i < 5; i++ {
		b.Dispatch(constants.ChannelSwap, swapPayload("0xAAA", i))
	}
	if !sub.Evicted() {
		t.Fatal("subscriber exceeding buffer should be evicted")
	}
}

func TestDeliver_EvictsOnDistinctCoalesceKeyOverflow(t *testing.T) {
	b := newBroker()
	sub := b.Subscribe(Filter{Channels: chanSet(constants.ChannelPoolStateUpdated)}, 2)
	defer sub.Close()

	for i := 0; i < 5; i++ {
		b.Dispatch(constants.ChannelPoolStateUpdated, poolStatePayload(fmt.Sprintf("0x%040d", i), int64(i)))
	}
	if !sub.Evicted() {
		t.Fatal("distinct coalesce keys exceeding buffer should evict")
	}
}

func TestSignal_FiresForMustDeliverNotCoalesced(t *testing.T) {
	b := newBroker()

	coalesced := b.Subscribe(Filter{Channels: chanSet(constants.ChannelCandleUpdate)}, 16)
	defer coalesced.Close()
	b.Dispatch(constants.ChannelCandleUpdate, candleUpdatePayload("0xAAA", "1m", 60, "1"))
	select {
	case <-coalesced.Signal():
		t.Error("coalesced delivery should NOT signal (waits for flush tick)")
	default:
	}

	must := b.Subscribe(Filter{Channels: chanSet(constants.ChannelSwap)}, 16)
	defer must.Close()
	b.Dispatch(constants.ChannelSwap, swapPayload("0xAAA", 1))
	select {
	case <-must.Signal():
	default:
		t.Error("must-deliver delivery should signal the writer")
	}
}

func TestDispatch_DropsInvalidPayload(t *testing.T) {
	b := newBroker()
	sub := b.Subscribe(Filter{All: true}, 16)
	defer sub.Close()

	b.Dispatch(constants.ChannelSwap, []byte("{broken"))
	if got := sub.Drain(); got != nil {
		t.Errorf("invalid payload should deliver nothing, got %d frames", len(got))
	}
	if _, dropped := b.Stats(); dropped != 1 {
		t.Errorf("dropped = %d, want 1", dropped)
	}
}

func TestResubscribe_SwitchesChannelRouting(t *testing.T) {
	b := newBroker()
	sub := b.Subscribe(Filter{Channels: chanSet(constants.ChannelSwap)}, 16)
	defer sub.Close()

	// Initially only swaps reach the subscriber.
	b.Dispatch(constants.ChannelMarketCreated, swapPayload("0xAAA", 1))
	if got := sub.Drain(); got != nil {
		t.Fatalf("pre-resubscribe market_created leaked %d frames", len(got))
	}

	// Switch the subscription to market_created only.
	b.Resubscribe(sub, Filter{Channels: chanSet(constants.ChannelMarketCreated)})
	b.Dispatch(constants.ChannelSwap, swapPayload("0xAAA", 2))          // no longer wanted
	b.Dispatch(constants.ChannelMarketCreated, swapPayload("0xAAA", 3)) // now wanted
	got := sub.Drain()
	if len(got) != 1 {
		t.Fatalf("post-resubscribe Drain len = %d, want 1", len(got))
	}
}

func TestSubscribe_UnregisterReleasesIndex(t *testing.T) {
	b := newBroker()
	sub := b.Subscribe(Filter{Channels: chanSet(constants.ChannelSwap)}, 16)
	if b.Subscribers() != 1 {
		t.Fatalf("Subscribers = %d, want 1", b.Subscribers())
	}
	sub.Close()
	if b.Subscribers() != 0 {
		t.Fatalf("Subscribers after close = %d, want 0", b.Subscribers())
	}
	// After close, dispatch must not panic and must not buffer.
	b.Dispatch(constants.ChannelSwap, swapPayload("0xAAA", 1))
	if got := sub.Drain(); got != nil {
		t.Errorf("closed subscription received %d frames", len(got))
	}
}
