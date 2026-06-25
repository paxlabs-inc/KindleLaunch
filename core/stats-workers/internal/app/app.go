// Package app wires the core/stats-workers service together (config, migrations,
// pgxpool, Redis, the five event consumers, HTTP read API + HMAC webhook,
// background platform-metrics precompute + bucket-trader cleanup, rate limiting,
// graceful shutdown) and owns its lifecycle. Ports @analytics_microservices/stats
// server.ts + index.ts.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"

	shareddb "github.com/Sidiora-Technologies/KindleLaunch/shared/db"
	sharedhttp "github.com/Sidiora-Technologies/KindleLaunch/shared/http"
	sharedlog "github.com/Sidiora-Technologies/KindleLaunch/shared/log"
	sharedprocess "github.com/Sidiora-Technologies/KindleLaunch/shared/process"
	sharedredis "github.com/Sidiora-Technologies/KindleLaunch/shared/redis"

	"github.com/Sidiora-Technologies/KindleLaunch/core/stats-workers/internal/config"
	"github.com/Sidiora-Technologies/KindleLaunch/core/stats-workers/internal/consumer"
	"github.com/Sidiora-Technologies/KindleLaunch/core/stats-workers/internal/httpapi"
	"github.com/Sidiora-Technologies/KindleLaunch/core/stats-workers/internal/migrate"
	"github.com/Sidiora-Technologies/KindleLaunch/core/stats-workers/internal/store"
)

const (
	// rateLimitMax / rateLimitWindow mirror the TS registerRateLimit(app,
	// { max: 100, windowSeconds: 60 }).
	rateLimitMax    = 100
	rateLimitWindow = 60 * time.Second

	// platformPrecomputeInterval / bucketCleanupInterval mirror the TS
	// PRECOMPUTE_INTERVAL_MS (25s) and BUCKET_TRADERS_CLEANUP_INTERVAL_MS (1h).
	platformPrecomputeInterval = 25 * time.Second
	bucketCleanupInterval      = time.Hour

	// bucketRetentionSeconds is the bucket_traders prune cutoff (now-24h), the
	// hardcoded TS cleanup window.
	bucketRetentionSeconds = 86400
)

// App is a fully-wired, ready-to-run stats service.
type App struct {
	Router http.Handler

	logger *slog.Logger
	store  *store.Store
	pool   *pgxpool.Pool
	redis  *goredis.Client
	holder *consumer.HolderTracker
}

// New runs migrations and builds every component.
func New(ctx context.Context, cfg config.Config, logger *slog.Logger) (*App, error) {
	if err := migrate.Up(ctx, cfg.DatabaseURL); err != nil {
		return nil, err
	}

	pool, err := shareddb.NewPool(ctx, cfg.DatabaseURL, shareddb.PoolOptions{
		MaxConns:         30,
		MaxConnIdleTime:  10 * time.Second,
		ConnectTimeout:   3 * time.Second,
		StatementTimeout: 15 * time.Second,
	})
	if err != nil {
		return nil, err
	}

	rdb, err := sharedredis.NewClient(cfg.RedisURL)
	if err != nil {
		pool.Close()
		return nil, err
	}

	st := store.New(pool)

	// Event consumers (driven synchronously by the webhook dispatch).
	swapConsumer := consumer.NewSwapConsumer(st, rdb, logger)
	marketConsumer, err := consumer.NewMarketConsumer(st, logger)
	if err != nil {
		pool.Close()
		_ = rdb.Close()
		return nil, err
	}
	stateConsumer := consumer.NewStateConsumer(st, logger)
	holderTracker := consumer.NewHolderTracker(st, rdb, logger, 0) // 0 -> 10s debounce default
	multihopConsumer := consumer.NewMultihopConsumer(st, logger)

	router := sharedhttp.NewRouter(sharedhttp.ServerOptions{
		CORSOrigins: cfg.CORSAllowedOrigins,
		Health: sharedhttp.HealthDeps{
			DB:    func(c context.Context) error { return pool.Ping(c) },
			Redis: func(c context.Context) error { return rdb.Ping(c).Err() },
		},
	})

	// Read API + webhook (NewRouter returns *chi.Mux, satisfying chi.Router). The
	// static routes (/stats/platform, /stats/batch, /stats/cross-token-swaps/...)
	// are registered before the /stats/{poolAddress} param route so chi resolves
	// them first.
	httpapi.RegisterPlatform(router, st, rdb)
	httpapi.RegisterCrossTokenSwaps(router, st)
	httpapi.RegisterPoolStats(router, st, rdb)
	httpapi.RegisterPoolHolders(router, st)
	httpapi.RegisterPoolTransactions(router, st)
	httpapi.RegisterPoolAnalytics(router, st)
	httpapi.RegisterSearch(router, st, rdb)
	httpapi.RegisterPressure(router, st, rdb)
	httpapi.RegisterReactions(router, rdb)
	httpapi.RegisterWebhook(router, httpapi.WebhookDeps{
		Swap:     swapConsumer,
		Market:   marketConsumer,
		State:    stateConsumer,
		Holder:   holderTracker,
		Multihop: multihopConsumer,
		Logger:   logger,
		Secret:   cfg.WebhookHMACSecret,
	})

	// Redis-backed global rate limit (100 req / 60s per client), wrapped as the
	// outermost layer so it applies to every route (parity with the TS global
	// registerRateLimit).
	handler := sharedhttp.RateLimit(sharedhttp.RateLimitOptions{
		Max:    rateLimitMax,
		Window: rateLimitWindow,
		Redis:  rdb,
	})(router)

	return &App{
		Router: handler,
		logger: logger,
		store:  st,
		pool:   pool,
		redis:  rdb,
		holder: holderTracker,
	}, nil
}

// Close releases all resources and stops pending holder-refresh timers.
func (a *App) Close() {
	if a.holder != nil {
		a.holder.Close()
	}
	if a.pool != nil {
		a.pool.Close()
	}
	if a.redis != nil {
		_ = a.redis.Close()
	}
}

// startBackgroundJobs launches the platform-metrics precompute loop and the
// bucket_traders cleanup loop, both stopping when ctx is cancelled (parity with
// the TS setInterval timers registered in registerPlatformRoutes).
func (a *App) startBackgroundJobs(ctx context.Context) {
	go a.loop(ctx, platformPrecomputeInterval, "platform precompute", func(c context.Context) error {
		return httpapi.PrecomputePlatformMetrics(c, a.store, a.redis)
	})
	go a.loop(ctx, bucketCleanupInterval, "bucket-trader cleanup", func(c context.Context) error {
		cutoff := shareddb.NowSeconds() - bucketRetentionSeconds
		removed, err := a.store.PruneBucketTraders(c, cutoff)
		if err == nil && removed > 0 {
			a.logger.Info("pruned stale bucket_traders", slog.Int64("removed", removed), slog.Int64("cutoff", cutoff))
		}
		return err
	})
}

// loop runs fn on a ticker until ctx is cancelled, logging (but not propagating)
// errors so a transient failure never kills the loop.
func (a *App) loop(ctx context.Context, interval time.Duration, name string, fn func(context.Context) error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := fn(ctx); err != nil {
				a.logger.Error("background job failed", slog.String("job", name), slog.Any("err", err))
			}
		}
	}
}

// Run loads config, builds the App, serves HTTP, starts the background jobs, and
// blocks until a shutdown signal arrives.
func Run(parent context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := sharedlog.New("stats", cfg.LogLevel)
	logger.Info("starting stats service")

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	app, err := New(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer app.Close()

	app.startBackgroundJobs(ctx)

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           app.Router,
		ReadHeaderTimeout: 10 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() {
		logger.Info("http server listening", slog.Int("port", cfg.Port),
			slog.String("webhook", "POST /webhooks/events"))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			cancel()
		}
	}()

	shutErr := sharedprocess.Run(ctx, sharedprocess.Options{
		Logger:     logger,
		OnShutdown: func(sctx context.Context) error { return srv.Shutdown(sctx) },
	})

	cancel()

	select {
	case err := <-serveErr:
		return err
	default:
		return shutErr
	}
}
