package broker

import (
	"encoding/json"
	"strconv"

	"github.com/Sidiora-Technologies/KindleLaunch/shared/constants"
	"github.com/Sidiora-Technologies/KindleLaunch/shared/util"
)

// OutMessage is a fan-out frame: the pre-marshaled client bytes plus the routing
// metadata the broker/subscription need (pool for filtering, coalesceKey for
// backpressure; an empty coalesceKey marks a must-deliver frame).
type OutMessage struct {
	pool        string
	coalesceKey string
	bytes       []byte
}

// channelType maps a Redis channel to the client-facing event "type" field. The
// candle channel keeps the trading-charts name ("candle_update") for client
// parity; the rest expose a stable, de-prefixed event name.
var channelType = map[string]string{
	constants.ChannelSwap:               "swap",
	constants.ChannelMarketCreated:      "market_created",
	constants.ChannelPoolStateUpdated:   "pool_state_updated",
	constants.ChannelFeeRecorded:        "fee_recorded",
	constants.ChannelFeeDistributed:     "fee_distributed",
	constants.ChannelFeeStrategyChanged: "fee_strategy_changed",
	constants.ChannelOpticalExecuted:    "optical_executed",
	constants.ChannelConfigUpdated:      "config_updated",
	constants.ChannelCandleUpdate:       "candle_update",
}

// coalescable lists the channels whose ticks represent the LATEST STATE and may
// therefore be coalesced (latest-per-key) under backpressure. Discrete events
// (swaps, market_created, fee/optical events) are NOT coalesced so none is ever
// silently dropped — they are must-deliver (parity with the client, which only
// coalesces candle ticks).
var coalescable = map[string]bool{
	constants.ChannelCandleUpdate:     true,
	constants.ChannelPoolStateUpdated: true,
}

// routing is the minimal envelope the transform parses out of every payload to
// route and (for candles) coalesce by pool+timeframe.
type routing struct {
	PoolAddress string `json:"poolAddress"`
	Timeframe   string `json:"timeframe"`
}

// DefaultTransform converts a raw Redis channel payload into an OutMessage.
//
// For the candle channel it reproduces the trading-charts WS frame byte-shape
// (type "candle_update" with numeric OHLCV/mcap fields formatted from the text
// bigints) so existing /ws/candles clients are unaffected. For every other
// channel it forwards the raw payload under a uniform envelope:
//
//	{"type": <event>, "channel": <redis-channel>, "pool": <addr>, "data": <raw>}
//
// ok is false when the payload is not valid JSON (the broker then drops it).
func DefaultTransform(channel string, payload []byte) (OutMessage, bool) {
	var r routing
	if err := json.Unmarshal(payload, &r); err != nil {
		return OutMessage{}, false
	}

	typ, known := channelType[channel]
	if !known {
		typ = channel
	}

	coalesceKey := ""
	if coalescable[channel] {
		coalesceKey = channel + ":" + r.PoolAddress
		if r.Timeframe != "" {
			coalesceKey += ":" + r.Timeframe
		}
	}

	if channel == constants.ChannelCandleUpdate {
		bytes, ok := candleFrame(payload)
		if !ok {
			return OutMessage{}, false
		}
		return OutMessage{pool: r.PoolAddress, coalesceKey: coalesceKey, bytes: bytes}, true
	}

	frame, err := json.Marshal(map[string]any{
		"type":    typ,
		"channel": channel,
		"pool":    r.PoolAddress,
		"data":    json.RawMessage(payload),
	})
	if err != nil {
		return OutMessage{}, false
	}
	return OutMessage{pool: r.PoolAddress, coalesceKey: coalesceKey, bytes: frame}, true
}

// candlePayload mirrors the candles:update payload the indexer/charts service
// publishes (text bigints; invariant i1). All money fields are formatted to
// decimal strings then parsed to float for the TradingView-style client frame
// (identical to core/trading-charts ws.go broadcastCandleUpdate).
type candlePayload struct {
	PoolAddress     string `json:"poolAddress"`
	Timeframe       string `json:"timeframe"`
	CandleStart     int64  `json:"candleStart"`
	Open            string `json:"open"`
	High            string `json:"high"`
	Low             string `json:"low"`
	Close           string `json:"close"`
	VolumeUsdl      string `json:"volumeUsdl"`
	VolumeToken     string `json:"volumeToken"`
	BuyVolumeUsdl   string `json:"buyVolumeUsdl"`
	SellVolumeUsdl  string `json:"sellVolumeUsdl"`
	TradeCount      int    `json:"tradeCount"`
	UniqueTraders   int    `json:"uniqueTraders"`
	LargeTradeCount int    `json:"largeTradeCount"`
	McapOpen        string `json:"mcapOpen"`
	McapHigh        string `json:"mcapHigh"`
	McapLow         string `json:"mcapLow"`
	McapClose       string `json:"mcapClose"`
}

func candleFrame(payload []byte) ([]byte, bool) {
	var e candlePayload
	if err := json.Unmarshal(payload, &e); err != nil {
		return nil, false
	}
	frame, err := json.Marshal(map[string]any{
		"type": "candle_update",
		"data": map[string]any{
			"poolAddress":     e.PoolAddress,
			"timeframe":       e.Timeframe,
			"candleStart":     e.CandleStart,
			"open":            fprice(e.Open),
			"high":            fprice(e.High),
			"low":             fprice(e.Low),
			"close":           fprice(e.Close),
			"volumeUsdl":      fvol(e.VolumeUsdl),
			"volumeToken":     fvol(e.VolumeToken),
			"buyVolumeUsdl":   fvol(e.BuyVolumeUsdl),
			"sellVolumeUsdl":  fvol(e.SellVolumeUsdl),
			"tradeCount":      e.TradeCount,
			"uniqueTraders":   e.UniqueTraders,
			"largeTradeCount": e.LargeTradeCount,
			"mcapOpen":        fvol(e.McapOpen),
			"mcapHigh":        fvol(e.McapHigh),
			"mcapLow":         fvol(e.McapLow),
			"mcapClose":       fvol(e.McapClose),
		},
	})
	if err != nil {
		return nil, false
	}
	return frame, true
}

// fprice formats a text bigint price to a float using the shared 8-dp price
// formatter (exact decimal, no float math; invariant i1), returning 0 on error.
func fprice(raw string) float64 {
	s, err := util.FormatPrice(raw)
	if err != nil {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}

// fvol formats a text bigint volume to a float using the shared 2-dp volume
// formatter, returning 0 on error.
func fvol(raw string) float64 {
	s, err := util.FormatVolume(raw)
	if err != nil {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}
