package consumer_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Sidiora-Technologies/KindleLaunch/core/stats-workers/internal/consumer"
	"github.com/Sidiora-Technologies/KindleLaunch/core/stats-workers/internal/internaltest"
	"github.com/Sidiora-Technologies/KindleLaunch/core/stats-workers/internal/store"
)

const holderBalance = "100000000000000" // 1e14 = 10% of supply

// waitForHolderCount polls pool_stats.holder_count until it equals want or the
// timeout elapses (the debounced refresh runs asynchronously).
func waitForHolderCount(t *testing.T, ctx context.Context, st *store.Store, addr string, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if poolHolderCount(t, ctx, st, addr) == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("holder_count for %s never reached %d within %s (last=%d)", addr, want, timeout, poolHolderCount(t, ctx, st, addr))
}

func TestHolderTracker(t *testing.T) {
	ctx := context.Background()
	st := store.New(internaltest.NewPostgres(t))
	rdb := internaltest.NewRedis(t)

	t.Run("refresh is debounced, not synchronous", func(t *testing.T) {
		const addr = "0xholder_defer"
		seedPoolStats(t, ctx, st, addr, nil, "1000000")
		ht := consumer.NewHolderTracker(st, rdb, discardLogger(), 250*time.Millisecond)
		defer ht.Close()

		if err := ht.ProcessSwap(ctx, consumer.HolderSwap{
			PoolAddress: addr, Sender: "0xbuyer", IsBuy: true, AmountIn: "0", AmountOut: holderBalance,
		}); err != nil {
			t.Fatalf("process swap: %v", err)
		}
		// The holder row exists immediately (synchronous balance delta)...
		if bal, ok, _ := st.GetHolderBalance(ctx, addr, "0xbuyer"); !ok || bal != holderBalance {
			t.Fatalf("balance = %s ok=%v, want %s", bal, ok, holderBalance)
		}
		// ...but holder_count has NOT been refreshed yet (debounced).
		if n := poolHolderCount(t, ctx, st, addr); n != 0 {
			t.Fatalf("holder_count = %d immediately after swap, want 0 (refresh deferred)", n)
		}
		// After the debounce window the single refresh runs and writes the count.
		waitForHolderCount(t, ctx, st, addr, 1, 3*time.Second)
	})

	t.Run("a burst of swaps coalesces into one refresh", func(t *testing.T) {
		const addr = "0xholder_burst"
		seedPoolStats(t, ctx, st, addr, nil, "1000000")
		ht := consumer.NewHolderTracker(st, rdb, discardLogger(), 300*time.Millisecond)
		defer ht.Close()

		const n = 5
		for i := 0; i < n; i++ {
			sender := fmt.Sprintf("0xburst%02d", i)
			if err := ht.ProcessSwap(ctx, consumer.HolderSwap{
				PoolAddress: addr, Sender: sender, IsBuy: true, AmountIn: "0", AmountOut: holderBalance,
			}); err != nil {
				t.Fatalf("process swap %d: %v", i, err)
			}
		}
		// The coalesced refresh fires once after the window and sees all n holders.
		waitForHolderCount(t, ctx, st, addr, n, 3*time.Second)
	})

	t.Run("new-holder sell is a no-op and schedules no refresh", func(t *testing.T) {
		const addr = "0xholder_nosell"
		seedPoolStats(t, ctx, st, addr, nil, "1000000")
		// Sentinel holder_count: if a refresh ran it would recompute this to 0.
		if _, err := st.Pool().Exec(ctx, `UPDATE stats.pool_stats SET holder_count=99 WHERE pool_address=$1`, addr); err != nil {
			t.Fatalf("seed sentinel: %v", err)
		}
		ht := consumer.NewHolderTracker(st, rdb, discardLogger(), 100*time.Millisecond)
		defer ht.Close()

		if err := ht.ProcessSwap(ctx, consumer.HolderSwap{
			PoolAddress: addr, Sender: "0xnewseller", IsBuy: false, AmountIn: holderBalance, AmountOut: "0",
		}); err != nil {
			t.Fatalf("process new-holder sell: %v", err)
		}
		// No holder row was created.
		if _, ok, _ := st.GetHolderBalance(ctx, addr, "0xnewseller"); ok {
			t.Fatal("new-holder sell must not create a holder row")
		}
		// Well past the debounce window, the sentinel is untouched -> no refresh ran.
		time.Sleep(400 * time.Millisecond)
		if n := poolHolderCount(t, ctx, st, addr); n != 99 {
			t.Fatalf("holder_count = %d, want 99 (no refresh should have run)", n)
		}
	})

	t.Run("Close cancels a pending refresh", func(t *testing.T) {
		const addr = "0xholder_close"
		seedPoolStats(t, ctx, st, addr, nil, "1000000")
		ht := consumer.NewHolderTracker(st, rdb, discardLogger(), 800*time.Millisecond)

		if err := ht.ProcessSwap(ctx, consumer.HolderSwap{
			PoolAddress: addr, Sender: "0xlate", IsBuy: true, AmountIn: "0", AmountOut: holderBalance,
		}); err != nil {
			t.Fatalf("process swap: %v", err)
		}
		ht.Close() // stop the pending timer before it fires

		time.Sleep(1100 * time.Millisecond)
		if n := poolHolderCount(t, ctx, st, addr); n != 0 {
			t.Fatalf("holder_count = %d after Close, want 0 (timer should have been cancelled)", n)
		}
	})

	t.Run("RefreshNow recomputes synchronously and invalidates the cache", func(t *testing.T) {
		const addr = "0xholder_refreshnow"
		seedPoolStats(t, ctx, st, addr, nil, "1000000")
		if err := rdb.Set(ctx, "stats:"+addr, `{"stale":true}`, time.Minute).Err(); err != nil {
			t.Fatalf("seed cache: %v", err)
		}
		// Two holders inserted directly so RefreshNow has data to aggregate.
		for i, h := range []string{"0xrn1", "0xrn2"} {
			if _, err := st.Pool().Exec(ctx, `
				INSERT INTO stats.pool_holders (pool_address, holder_address, balance, pct_of_supply, last_updated)
				VALUES ($1,$2,$3,'1000',$4)`, addr, h, holderBalance, 100+i); err != nil {
				t.Fatalf("insert holder: %v", err)
			}
		}
		ht := consumer.NewHolderTracker(st, rdb, discardLogger(), time.Hour) // long debounce: prove the sync path
		defer ht.Close()

		if err := ht.RefreshNow(ctx, addr); err != nil {
			t.Fatalf("refresh now: %v", err)
		}
		if n := poolHolderCount(t, ctx, st, addr); n != 2 {
			t.Fatalf("holder_count = %d, want 2", n)
		}
		if exists, _ := rdb.Exists(ctx, "stats:"+addr).Result(); exists != 0 {
			t.Fatal("RefreshNow must delete the stats cache key")
		}
	})
}
