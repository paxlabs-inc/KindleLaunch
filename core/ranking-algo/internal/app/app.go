// Package app wires the core/ranking-algo service together (config, pgxpool,
// Redis, ranking service, scheduler, rate-limited HTTP API, graceful shutdown)
// and owns its lifecycle. Ports @market-microservices/ranking-algo/src/index.ts.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"

	shareddb "github.com/Sidiora-Technologies/KindleLaunch/shared/db"
	sharedhttp "github.com/Sidiora-Technologies/KindleLaunch/shared/http"
	sharedlog "github.com/Sidiora-Technologies/KindleLaunch/shared/log"
	sharedprocess "github.com/Sidiora-Technologies/KindleLaunch/shared/process"
	sharedredis "github.com/Sidiora-Technologies/KindleLaunch/shared/redis"

	"github.com/Sidiora-Technologies/KindleLaunch/core/ranking-algo/internal/config"
	"github.com/Sidiora-Technologies/KindleLaunch/core/ranking-algo/internal/httpapi"
	"github.com/Sidiora-Technologies/KindleLaunch/core/ranking-algo/internal/ranker"
	"github.com/Sidiora-Technologies/KindleLaunch/core/ranking-algo/internal/store"
)

const (
	// rateLimitMax/Window mirror the TS registerRateLimit(max:100, windowSeconds:60).
	rateLimitMax    = 100
	rateLimitWindow = 60 * time.Second
)

// App is a fully-wired, ready-to-run ranking-algo service.
type App struct {
	Router  http.Handler
	Service *ranker.Service
	cfg     config.Config

	pool  *pgxpool.Pool
	redis *goredis.Client
}

// New builds every component (no migrations: ranking-algo reads schemas owned by
// other services — SECTION 9 read-mostly).
func New(ctx context.Context, cfg config.Config, logger *slog.Logger) (*App, error) {
	pool, err := shareddb.NewPool(ctx, cfg.DatabaseURL, shareddb.PoolOptions{
		MaxConns:         20,
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

	svc := ranker.NewService(store.New(pool), rdb, cfg.RankingMaxEntries, logger)

	router := sharedhttp.NewRouter(sharedhttp.ServerOptions{
		CORSOrigins: cfg.CORSAllowedOrigins,
		Health: sharedhttp.HealthDeps{
			DB:    func(c context.Context) error { return pool.Ping(c) },
			Redis: func(c context.Context) error { return rdb.Ping(c).Err() },
		},
	})

	// Rate-limited ranking routes (health stays un-throttled). Middleware must be
	// declared on the group before the routes are registered (chi rule).
	router.Group(func(gr chi.Router) {
		gr.Use(sharedhttp.RateLimit(sharedhttp.RateLimitOptions{
			Max:    rateLimitMax,
			Window: rateLimitWindow,
			Redis:  rdb,
		}))
		httpapi.RegisterRankings(gr, rdb)
	})

	return &App{
		Router:  router,
		Service: svc,
		cfg:     cfg,
		pool:    pool,
		redis:   rdb,
	}, nil
}

// Close releases all resources.
func (a *App) Close() {
	if a.pool != nil {
		a.pool.Close()
	}
	if a.redis != nil {
		_ = a.redis.Close()
	}
}

// Run loads config, builds the App, computes the initial rankings, schedules the
// recurring recomputes, serves the HTTP API, and blocks until shutdown.
func Run(parent context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := sharedlog.New("ranking-algo", cfg.LogLevel)
	logger.Info("starting ranking-algo service")

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	app, err := New(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer app.Close()

	// Initial run before scheduling (parity with index.ts await runAll/runNew).
	if err := app.Service.RunAll(ctx); err != nil {
		logger.Error("initial runAll failed", slog.Any("err", err))
	}
	if err := app.Service.RunNew(ctx); err != nil {
		logger.Error("initial runNew failed", slog.Any("err", err))
	}

	go app.scheduleLoop(ctx, cfg.TickInterval(), app.Service.RunAll, "runAll")
	go app.scheduleLoop(ctx, cfg.NewTickInterval(), app.Service.RunNew, "runNew")

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           app.Router,
		ReadHeaderTimeout: 10 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() {
		logger.Info("http server listening", slog.Int("port", cfg.Port))
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

// scheduleLoop invokes run on every tick until ctx is cancelled, logging (but
// not propagating) per-cycle errors so one bad cycle never kills the scheduler
// (parity with the try/catch in index.ts runAll/runNew).
func (a *App) scheduleLoop(ctx context.Context, interval time.Duration, run func(context.Context) error, name string) {
	logger := sharedlog.New("ranking-algo", a.cfg.LogLevel)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := run(ctx); err != nil {
				logger.Error("ranking cycle failed", slog.String("cycle", name), slog.Any("err", err))
			}
		}
	}
}
