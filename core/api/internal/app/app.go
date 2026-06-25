// Package app wires the core/api gateway together (config, pgx pool, Redis, the
// realtime broker + WSS/SSE multiplexer, the rate-limited REST snapshot surface,
// caching, load-shedding, graceful shutdown) and owns its lifecycle.
//
// Topology (per the push-first design): the core/* services WRITE to Postgres +
// Redis; this gateway READS from them. A single broker subscriber fans Redis
// pub/sub out to WSS/SSE clients; REST serves only cached snapshot reads. Public
// ingress is rate-limited per IP/key with a global in-flight load-shed
// (invariants i11 + i12).
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

	"github.com/Sidiora-Technologies/KindleLaunch/core/api/internal/broker"
	"github.com/Sidiora-Technologies/KindleLaunch/core/api/internal/cache"
	"github.com/Sidiora-Technologies/KindleLaunch/core/api/internal/config"
	"github.com/Sidiora-Technologies/KindleLaunch/core/api/internal/ratelimit"
	"github.com/Sidiora-Technologies/KindleLaunch/core/api/internal/rest"
	"github.com/Sidiora-Technologies/KindleLaunch/core/api/internal/sse"
	"github.com/Sidiora-Technologies/KindleLaunch/core/api/internal/store"
	"github.com/Sidiora-Technologies/KindleLaunch/core/api/internal/ws"
)

// App is a fully-wired, ready-to-run core/api gateway.
type App struct {
	Router http.Handler
	Broker *broker.Broker
	cfg    config.Config
	logger *slog.Logger

	pool    *pgxpool.Pool
	redis   *goredis.Client
	subRdb  *goredis.Client
	wsHub   *ws.Hub
	sseHub  *sse.Hub
	cache   *cache.Cache
	limiter *ratelimit.Limiter
}

// New builds every component. core/api owns no schema and runs no migrations: it
// reads the schemas the core/* services own (invariant i2).
func New(ctx context.Context, cfg config.Config, logger *slog.Logger) (*App, error) {
	pool, err := shareddb.NewPool(ctx, cfg.DatabaseURL, shareddb.PoolOptions{
		MaxConns:         40,
		MaxConnIdleTime:  30 * time.Second,
		ConnectTimeout:   3 * time.Second,
		StatementTimeout: 10 * time.Second,
	})
	if err != nil {
		return nil, err
	}

	rdb, err := sharedredis.NewClient(cfg.RedisURL)
	if err != nil {
		pool.Close()
		return nil, err
	}
	// A dedicated client for the broker's long-lived pub/sub subscription, kept
	// separate from the read/rate-limit client.
	subRdb, err := sharedredis.NewClient(cfg.RedisURL)
	if err != nil {
		_ = rdb.Close()
		pool.Close()
		return nil, err
	}

	st := store.New(pool, rdb)
	responseCache := cache.New(4096)
	br := broker.New(broker.Options{Redis: subRdb, Logger: logger})

	wsHub := ws.NewHub(ws.Options{
		Broker:     br,
		Logger:     logger,
		SendBuffer: cfg.ClientSendBuffer,
		Flush:      cfg.CoalesceFlush(),
		MaxConns:   cfg.WSMaxConnections,
		MaxPerIP:   cfg.WSMaxPerIP,
	})
	sseHub := sse.NewHub(sse.Options{
		Broker:     br,
		Logger:     logger,
		SendBuffer: cfg.ClientSendBuffer,
		Flush:      cfg.CoalesceFlush(),
		MaxConns:   cfg.SSEMaxConnections,
		MaxPerIP:   cfg.SSEMaxPerIP,
	})
	limiter := ratelimit.NewLimiter(cfg.MaxInFlightRequests)

	router := sharedhttp.NewRouter(sharedhttp.ServerOptions{
		CORSOrigins: cfg.CORSAllowedOrigins,
		Health: sharedhttp.HealthDeps{
			DB:    func(c context.Context) error { return pool.Ping(c) },
			Redis: func(c context.Context) error { return rdb.Ping(c).Err() },
		},
	})

	app := &App{
		Router:  router,
		Broker:  br,
		cfg:     cfg,
		logger:  logger,
		pool:    pool,
		redis:   rdb,
		subRdb:  subRdb,
		wsHub:   wsHub,
		sseHub:  sseHub,
		cache:   responseCache,
		limiter: limiter,
	}

	// Realtime group: connection-capped + a light per-IP connect rate limit, but
	// NOT the in-flight limiter (streams are long-lived and would pin slots).
	router.Group(func(gr chi.Router) {
		gr.Use(sharedhttp.RateLimit(sharedhttp.RateLimitOptions{
			Max:    cfg.RateLimitMax,
			Window: cfg.RateLimitWindow(),
			Redis:  rdb,
		}))
		wsHub.Register(gr)
		sseHub.Register(gr)
	})

	// REST snapshot group: per-IP/key token bucket + global in-flight load-shed.
	router.Group(func(gr chi.Router) {
		gr.Use(limiter.Middleware)
		gr.Use(sharedhttp.RateLimit(sharedhttp.RateLimitOptions{
			Max:     cfg.RateLimitMax,
			Window:  cfg.RateLimitWindow(),
			Redis:   rdb,
			KeyFunc: ratelimit.ClientKey,
		}))
		rest.Register(gr, st, responseCache)
		gr.Get("/status", app.statusHandler)
	})

	return app, nil
}

// statusHandler reports live gateway gauges (subscribers, connections, cache,
// in-flight, pool) for ops dashboards.
func (a *App) statusHandler(w http.ResponseWriter, _ *http.Request) {
	delivered, dropped := a.Broker.Stats()
	hits, misses := a.cache.Stats()
	sharedhttp.WriteJSON(w, http.StatusOK, map[string]any{
		"status":          "ok",
		"subscribers":     a.Broker.Subscribers(),
		"deliveredFrames": delivered,
		"droppedFrames":   dropped,
		"wsConnections":   a.wsHub.Connections(),
		"sseConnections":  a.sseHub.Connections(),
		"cacheHits":       hits,
		"cacheMisses":     misses,
		"inFlight":        a.limiter.InFlight(),
		"dbPools":         shareddb.PoolMetrics(),
	})
}

// Close releases all resources.
func (a *App) Close() {
	if a.pool != nil {
		a.pool.Close()
	}
	if a.redis != nil {
		_ = a.redis.Close()
	}
	if a.subRdb != nil {
		_ = a.subRdb.Close()
	}
}

// Run loads config, builds the App, starts the broker fan-out + HTTP server, and
// blocks until shutdown.
func Run(parent context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := sharedlog.New("core-api", cfg.LogLevel)
	logger.Info("starting core-api gateway")

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	app, err := New(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer app.Close()

	// Broker fan-out loop (single Redis subscriber for all clients).
	go func() {
		if err := app.Broker.Run(ctx); err != nil {
			logger.Error("broker stopped", slog.Any("err", err))
			cancel()
		}
	}()

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           app.Router,
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: SSE/WSS are long-lived; per-write deadlines bound
		// slow clients instead (internal/sse uses http.ResponseController).
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
