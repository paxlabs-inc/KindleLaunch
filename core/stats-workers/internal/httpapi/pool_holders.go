package httpapi

import (
	"math/big"
	"net/http"

	"github.com/go-chi/chi/v5"

	shareddb "github.com/Sidiora-Technologies/KindleLaunch/shared/db"
	sharedhttp "github.com/Sidiora-Technologies/KindleLaunch/shared/http"

	"github.com/Sidiora-Technologies/KindleLaunch/core/stats-workers/internal/store"
)

var (
	bigTotalSupply = func() *big.Int { v, _ := new(big.Int).SetString(shareddb.TotalSupplyRaw, 10); return v }()
	bigTenThousand = big.NewInt(10000)
)

// holderWithRank is a holder row plus its 1-based rank (parity with the TS
// `{ ...h, rank }` enrichment; embedded fields marshal inline before rank).
type holderWithRank struct {
	store.HolderRow
	Rank int `json:"rank"`
}

// walletEntry is one entry of the distribution walletMap.
type walletEntry struct {
	Address string `json:"address"`
	Balance string `json:"balance"`
	PctBps  int64  `json:"pctBps"`
	Rank    int    `json:"rank"`
}

// bracketOut is one distribution bracket in the response.
type bracketOut struct {
	Label              string `json:"label"`
	Count              int    `json:"count"`
	TotalBalancePctBps int64  `json:"totalBalancePctBps"`
}

// RegisterPoolHolders registers the holder list + distribution routes.
func RegisterPoolHolders(r chi.Router, st *store.Store) {
	r.Get("/stats/{poolAddress}/holders/distribution", holderDistribution(st))
	r.Get("/stats/{poolAddress}/holders", holderList(st))
}

// holderList serves a paginated, rank-enriched holder list. Ports the TS
// GET /stats/:pool/holders route.
func holderList(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		poolAddress := chi.URLParam(r, "poolAddress")
		ctx := r.Context()
		q := r.URL.Query()
		limit := parseIntDefault(q.Get("limit"), 50)
		offset := parseIntDefault(q.Get("offset"), 0)

		holders, err := st.ListHolders(ctx, poolAddress, limit, offset)
		if err != nil {
			sharedhttp.WriteError(w, http.StatusInternalServerError, "Internal Server Error", "holders lookup failed")
			return
		}
		total, err := st.CountHolders(ctx, poolAddress)
		if err != nil {
			sharedhttp.WriteError(w, http.StatusInternalServerError, "Internal Server Error", "holders count failed")
			return
		}

		enriched := make([]holderWithRank, len(holders))
		for i, h := range holders {
			enriched[i] = holderWithRank{HolderRow: h, Rank: offset + i + 1}
		}

		sharedhttp.WriteJSON(w, http.StatusOK, map[string]any{
			"holders": enriched,
			"total":   total,
			"limit":   limit,
			"offset":  offset,
		})
	}
}

// pctOfSupplyBps returns Number((balance * 10000) / TOTAL_SUPPLY_RAW) as an
// integer (range [0,10000]), matching the TS distribution math.
func pctOfSupplyBps(balance *big.Int) int64 {
	if bigTotalSupply.Sign() <= 0 {
		return 0
	}
	v := new(big.Int).Mul(balance, bigTenThousand)
	v.Quo(v, bigTotalSupply)
	return v.Int64()
}

// holderDistribution serves the detailed holder distribution: brackets by % of
// supply, top-10/20/50 concentration, and a paginated wallet map. Ports the TS
// GET /stats/:pool/holders/distribution route (S-6 pagination).
func holderDistribution(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		poolAddress := chi.URLParam(r, "poolAddress")
		q := r.URL.Query()
		walletLimit := parseIntDefault(q.Get("limit"), 200)
		if walletLimit > 500 {
			walletLimit = 500
		}
		walletOffset := parseIntDefault(q.Get("offset"), 0)

		allHolders, err := st.ListHoldersByBalance(r.Context(), poolAddress)
		if err != nil {
			sharedhttp.WriteError(w, http.StatusInternalServerError, "Internal Server Error", "distribution lookup failed")
			return
		}

		totalHolders := len(allHolders)
		if totalHolders == 0 {
			sharedhttp.WriteJSON(w, http.StatusOK, map[string]any{
				"totalHolders": 0,
				"brackets":     []bracketOut{},
				"top10":        []walletEntry{},
				"top10Pct":     "0",
				"top20Pct":     "0",
				"top50Pct":     "0",
				"walletMap":    []walletEntry{},
			})
			return
		}

		// Bracket thresholds in basis points, declaration order matching the TS.
		bracketMins := []int64{1000, 500, 100, 10, 1, 0}
		bracketLabels := []string{">10%", "5-10%", "1-5%", "0.1-1%", "0.01-0.1%", "<0.01%"}
		bracketCount := make([]int, len(bracketMins))
		bracketTotal := make([]*big.Int, len(bracketMins))
		for i := range bracketTotal {
			bracketTotal[i] = new(big.Int)
		}

		top10Balance := new(big.Int)
		top20Balance := new(big.Int)
		top50Balance := new(big.Int)

		walletMap := make([]walletEntry, totalHolders)
		for i, h := range allHolders {
			balBig, ok := new(big.Int).SetString(h.Balance, 10)
			if !ok {
				balBig = new(big.Int)
			}
			pctBps := pctOfSupplyBps(balBig)

			if i < 10 {
				top10Balance.Add(top10Balance, balBig)
			}
			if i < 20 {
				top20Balance.Add(top20Balance, balBig)
			}
			if i < 50 {
				top50Balance.Add(top50Balance, balBig)
			}

			// Place into the first bracket whose min threshold it meets.
			for b, min := range bracketMins {
				if pctBps >= min {
					bracketCount[b]++
					bracketTotal[b].Add(bracketTotal[b], balBig)
					break
				}
			}

			walletMap[i] = walletEntry{
				Address: h.HolderAddress,
				Balance: h.Balance,
				PctBps:  pctBps,
				Rank:    i + 1,
			}
		}

		concentration := func(total *big.Int) string {
			if bigTotalSupply.Sign() <= 0 {
				return "0"
			}
			v := new(big.Int).Mul(total, bigTenThousand)
			v.Quo(v, bigTotalSupply)
			return v.String()
		}

		brackets := make([]bracketOut, len(bracketMins))
		for i := range bracketMins {
			brackets[i] = bracketOut{
				Label:              bracketLabels[i],
				Count:              bracketCount[i],
				TotalBalancePctBps: pctOfSupplyBps(bracketTotal[i]),
			}
		}

		top10 := walletMap
		if len(top10) > 10 {
			top10 = top10[:10]
		}

		// Paginate walletMap (S-6).
		end := walletOffset + walletLimit
		paginated := []walletEntry{}
		if walletOffset < len(walletMap) {
			if end > len(walletMap) {
				end = len(walletMap)
			}
			if walletOffset >= 0 {
				paginated = walletMap[walletOffset:end]
			}
		}

		sharedhttp.WriteJSON(w, http.StatusOK, map[string]any{
			"totalHolders":    totalHolders,
			"brackets":        brackets,
			"top10":           top10,
			"top10Pct":        concentration(top10Balance),
			"top20Pct":        concentration(top20Balance),
			"top50Pct":        concentration(top50Balance),
			"walletMap":       paginated,
			"walletMapTotal":  len(walletMap),
			"walletMapLimit":  walletLimit,
			"walletMapOffset": walletOffset,
		})
	}
}
