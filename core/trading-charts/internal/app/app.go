// Package app wires the core/trading-charts service together (config, migrations,
// pgxpool, Redis, candle builder, swap consumer, gap-fill timer, UDF + webhook +
// WS HTTP, graceful shutdown) and owns its lifecycle.
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

	"github.com/Sidiora-Technologies/KindleLaunch/core/trading-charts/internal/config"
	"github.com/Sidiora-Technologies/KindleLaunch/core/trading-charts/internal/consumer"
	"github.com/Sidiora-Technologies/KindleLaunch/core/trading-charts/internal/engine"
	"github.com/Sidiora-Technologies/KindleLaunch/core/trading-charts/internal/httpapi"
	"github.com/Sidiora-Technologies/KindleLaunch/core/trading-charts/internal/migrate"
	"github.com/Sidiora-Technologies/KindleLaunch/core/trading-charts/internal/store"
)

// App is a fully-wired, ready-to-run trading-charts service.
type App struct {
	Router   http.Handler
	Builder  *engine.Builder
	Consumer *consumer.SwapConsumer

	pool  *pgxpool.Pool
	redis *goredis.Client
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
	builder := engine.New(pool, rdb, st, logger)

	swapConsumer, err := consumer.New(builder, cfg.RedisURL, logger)
	if err != nil {
		pool.Close()
		_ = rdb.Close()
		return nil, err
	}

	router := sharedhttp.NewRouter(sharedhttp.ServerOptions{
		CORSOrigins: cfg.CORSAllowedOrigins,
		Health: sharedhttp.HealthDeps{
			DB:    func(c context.Context) error { return pool.Ping(c) },
			Redis: func(c context.Context) error { return rdb.Ping(c).Err() },
		},
	})

	// Register routes (NewRouter returns *chi.Mux, which satisfies chi.Router).
	httpapi.RegisterUDF(router, st)
	httpapi.RegisterWebhook(router, httpapi.WebhookDeps{
		Builder: builder,
		Logger:  logger,
		Secret:  cfg.WebhookHMACSecret,
	})
	httpapi.RegisterWS(router, httpapi.WSDeps{
		RedisURL:       cfg.RedisURL,
		Logger:         logger,
		MaxConnections: cfg.WSMaxConnections,
		MaxPerIP:       cfg.WSMaxPerIP,
	})

	return &App{
		Router:   router,
		Builder:  builder,
		Consumer: swapConsumer,
		pool:     pool,
		redis:    rdb,
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
	if a.Consumer != nil {
		_ = a.Consumer.Close()
	}
}

// Run loads config, builds the App, serves HTTP, starts the swap consumer and
// gap-fill timer, and blocks until a shutdown signal arrives.
func Run(parent context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := sharedlog.New("candles", cfg.LogLevel)
	logger.Info("starting candles service")

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	app, err := New(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer app.Close()

	// Start Redis pub/sub swap consumer.
	if err := app.Consumer.Start(ctx); err != nil {
		return fmt.Errorf("app: start consumer: %w", err)
	}

	// C-3: Periodic candle gap detection (every 60s).
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				filled, err := engine.DetectAndFillGaps(ctx, app.pool, logger, nil)
				if err != nil {
					logger.Error("gap fill cycle failed", slog.Any("err", err))
				} else if filled > 0 {
					logger.Info("gap fill cycle complete", slog.Int("filled", filled))
				}
			}
		}
	}()

	// Run once on startup after a brief delay.
	go func() {
		time.Sleep(5 * time.Second)
		filled, err := engine.DetectAndFillGaps(ctx, app.pool, logger, nil)
		if err != nil {
			logger.Error("initial gap fill failed", slog.Any("err", err))
		} else if filled > 0 {
			logger.Info("initial gap fill complete", slog.Int("filled", filled))
		}
	}()

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
