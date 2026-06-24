package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Sidiora-Technologies/KindleLaunch/core/trading-charts/internal/internaltest"
	"github.com/Sidiora-Technologies/KindleLaunch/core/trading-charts/internal/store"
)

const udfPool = "0xpool00000000000000000000000000000000eeee"

func udfRouter(t *testing.T) (*chi.Mux, *store.Store) {
	t.Helper()
	_, pool := internaltest.NewPostgres(t)
	st := store.New(pool)
	r := chi.NewRouter()
	RegisterUDF(r, st)
	return r, st
}

func doGET(t *testing.T, r http.Handler, path string) (*httptest.ResponseRecorder, map[string]interface{}) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v (body=%s)", path, err, rec.Body.String())
	}
	return rec, body
}

func TestUDFConfig(t *testing.T) {
	r, _ := udfRouter(t)
	rec, body := doGET(t, r, "/config")
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if body["supports_time"] != true {
		t.Errorf("supports_time = %v, want true", body["supports_time"])
	}
}

func TestUDFSymbols(t *testing.T) {
	r, _ := udfRouter(t)
	_, body := doGET(t, r, "/symbols?symbol=ABC")
	if body["name"] != "ABC" || body["full_name"] != "ABC/USDL" {
		t.Errorf("symbols wrong: %+v", body)
	}
}

func TestUDFTime(t *testing.T) {
	r, _ := udfRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/time", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var ts int64
	if err := json.Unmarshal(rec.Body.Bytes(), &ts); err != nil || ts <= 0 {
		t.Fatalf("time = %d, err %v", ts, err)
	}
}

func TestUDFHistoryNoData(t *testing.T) {
	r, _ := udfRouter(t)
	_, body := doGET(t, r, "/history?symbol="+udfPool+"&resolution=1&from=0&to=100")
	if body["s"] != "no_data" {
		t.Errorf("s = %v, want no_data", body["s"])
	}
}

func TestUDFHistoryRangeAndCountback(t *testing.T) {
	r, st := udfRouter(t)
	ctx := context.Background()

	for i := int64(0); i < 4; i++ {
		if err := st.InsertCandle(ctx, store.CandleRow{
			PoolAddress: udfPool, Timeframe: "1m", CandleStart: 60 + i*60,
			Open: "1000000000000000000", High: "2000000000000000000",
			Low: "500000000000000000", Close: "1500000000000000000",
			VolumeUsdl: "1000000000000000000", VolumeToken: "0",
			BuyVolumeUsdl: "0", SellVolumeUsdl: "0",
			TradeCount: 1, UniqueTraders: 1, LargeTradeCount: 0, LastTradeTs: 60 + i*60,
			SequenceNum: i, McapOpen: "0", McapHigh: "0", McapLow: "0", McapClose: "0",
		}); err != nil {
			t.Fatalf("InsertCandle %d: %v", i, err)
		}
	}

	// Range query (resolution 1 -> 1m).
	_, body := doGET(t, r, "/history?symbol="+udfPool+"&resolution=1&from=60&to=300")
	if body["s"] != "ok" {
		t.Fatalf("range s = %v, want ok (body=%+v)", body["s"], body)
	}
	ts, _ := body["t"].([]interface{})
	if len(ts) != 4 {
		t.Errorf("range len = %d, want 4", len(ts))
	}
	o, _ := body["o"].([]interface{})
	if len(o) == 0 || o[0].(float64) != 1.0 {
		t.Errorf("formatted open = %v, want 1.0", o)
	}

	// Countback query returns the last 2.
	_, cb := doGET(t, r, "/history?symbol="+udfPool+"&resolution=1&to=300&countback=2")
	if cb["s"] != "ok" {
		t.Fatalf("countback s = %v", cb["s"])
	}
	if cbt, _ := cb["t"].([]interface{}); len(cbt) != 2 {
		t.Errorf("countback len = %d, want 2", len(cbt))
	}

	// Unknown resolution falls back to 1h (no candles there -> no_data).
	_, hr := doGET(t, r, "/history?symbol="+udfPool+"&resolution=99&from=0&to=100000000000")
	if hr["s"] != "no_data" {
		t.Errorf("unknown-resolution s = %v, want no_data", hr["s"])
	}
}

func TestUDFHistoryToleratesNonNumericStoredValue(t *testing.T) {
	r, st := udfRouter(t)
	ctx := context.Background()

	// A corrupt (non-numeric) stored value must not 500 the endpoint; the
	// formatter error is swallowed to 0 (exercises parseFloatFormatted error).
	if err := st.InsertCandle(ctx, store.CandleRow{
		PoolAddress: udfPool, Timeframe: "1m", CandleStart: 60,
		Open: "xx", High: "xx", Low: "xx", Close: "xx",
		VolumeUsdl: "yy", VolumeToken: "0", BuyVolumeUsdl: "0", SellVolumeUsdl: "0",
		TradeCount: 1, UniqueTraders: 1, LargeTradeCount: 0, LastTradeTs: 60,
		SequenceNum: 0, McapOpen: "0", McapHigh: "0", McapLow: "0", McapClose: "0",
	}); err != nil {
		t.Fatalf("InsertCandle: %v", err)
	}
	rec, body := doGET(t, r, "/history?symbol="+udfPool+"&resolution=1&from=0&to=120")
	if rec.Code != 200 || body["s"] != "ok" {
		t.Fatalf("status=%d s=%v, want 200/ok", rec.Code, body["s"])
	}
	if o, _ := body["o"].([]interface{}); len(o) != 1 || o[0].(float64) != 0 {
		t.Errorf("corrupt open formatted = %v, want [0]", body["o"])
	}
}
