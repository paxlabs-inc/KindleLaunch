// Package app wires the core/indexer service together (config, migrations,
// pgxpool, Redis, chain client, log sources, decoder, store, webhook publisher,
// block + backfill processors, status HTTP, graceful shutdown) and owns its
// lifecycle. Keeping the wiring here (not in main) makes the service testable
// end-to-end against testcontainers + httptest RPC.
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

	"github.com/Sidiora-Technologies/KindleLaunch/shared/chain"
	shareddb "github.com/Sidiora-Technologies/KindleLaunch/shared/db"
	sharedhttp "github.com/Sidiora-Technologies/KindleLaunch/shared/http"
	sharedlog "github.com/Sidiora-Technologies/KindleLaunch/shared/log"
	"github.com/Sidiora-Technologies/KindleLaunch/shared/process"
	sharedredis "github.com/Sidiora-Technologies/KindleLaunch/shared/redis"

	"github.com/Sidiora-Technologies/KindleLaunch/core/indexer/internal/chainreader"
	"github.com/Sidiora-Technologies/KindleLaunch/core/indexer/internal/config"
	"github.com/Sidiora-Technologies/KindleLaunch/core/indexer/internal/decode"
	"github.com/Sidiora-Technologies/KindleLaunch/core/indexer/internal/httpapi"
	"github.com/Sidiora-Technologies/KindleLaunch/core/indexer/internal/migrate"
	"github.com/Sidiora-Technologies/KindleLaunch/core/indexer/internal/processor"
	"github.com/Sidiora-Technologies/KindleLaunch/core/indexer/internal/publisher"
	"github.com/Sidiora-Technologies/KindleLaunch/core/indexer/internal/source"
	"github.com/Sidiora-Technologies/KindleLaunch/core/indexer/internal/store"
)

// App is a fully-wired, ready-to-run indexer service.
type App struct {
	Router    http.Handler
	Processor *processor.Processor
	Backfill  *processor.Backfill
	Publisher *publisher.Publisher

	cfg     config.Config
	pool    *pgxpool.Pool
	redis   *goredis.Client
	chain   *chain.Client
	sources *source.Bundle
	logger  *slog.Logger
}

// New runs migrations and builds every component for the configured run mode.
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

	chainClient, err := chain.NewClient(ctx, cfg.RPCURL, cfg.RPCURLFallback)
	if err != nil {
		pool.Close()
		_ = rdb.Close()
		return nil, err
	}

	nftReader, err := chainreader.NewNFTReader(chainClient, cfg.PoolRegistryAddress)
	if err != nil {
		chainClient.Close()
		pool.Close()
		_ = rdb.Close()
		return nil, err
	}

	sources, err := source.Create(ctx, cfg, logger)
	if err != nil {
		chainClient.Close()
		pool.Close()
		_ = rdb.Close()
		return nil, err
	}

	targets, err := publisher.BuildTargets(cfg.WebhookURLs, cfg.WebhookHMACSecret, cfg.WebhookHMACSecrets)
	if err != nil {
		_ = sources.Close()
		chainClient.Close()
		pool.Close()
		_ = rdb.Close()
		return nil, err
	}

	st := store.New(pool)
	pub := publisher.New(publisher.Options{Targets: targets, Logger: logger, Redis: rdb})

	proc := processor.New(processor.Deps{
		Source:              sources.Log,
		Head:                sources.Head,
		Store:               st,
		Publisher:           pub,
		Decoder:             decode.NewDecoder(),
		NFT:                 nftReader,
		EventEmitterAddress: cfg.EventEmitterAddress,
		ChainID:             int32(cfg.ChainID),
		StartBlock:          cfg.IndexerStartBlock,
		BatchSize:           cfg.IndexerBatchSize,
		Concurrency:         cfg.IndexerConcurrency,
		PollInterval:        time.Duration(cfg.IndexerPollIntervalMS) * time.Millisecond,
		Logger:              logger,
	})
	backfill := processor.NewBackfill(proc, cfg.BackfillFromBlock, cfg.BackfillBatchSize)

	router := sharedhttp.NewRouter(sharedhttp.ServerOptions{
		CORSOrigins: cfg.CORSAllowedOrigins,
		Health: sharedhttp.HealthDeps{
			DB:    func(c context.Context) error { return pool.Ping(c) },
			Redis: func(c context.Context) error { return rdb.Ping(c).Err() },
		},
	})
	httpapi.RegisterStatus(router, httpapi.StatusDeps{
		Store:     st,
		ChainID:   int32(cfg.ChainID),
		Publisher: pub,
		StartTime: time.Now(),
	})

	return &App{
		Router:    router,
		Processor: proc,
		Backfill:  backfill,
		Publisher: pub,
		cfg:       cfg,
		pool:      pool,
		redis:     rdb,
		chain:     chainClient,
		sources:   sources,
		logger:    logger,
	}, nil
}

// Close releases all resources.
func (a *App) Close() {
	_ = a.sources.Close()
	a.chain.Close()
	a.pool.Close()
	if err := a.redis.Close(); err != nil && a.logger != nil {
		a.logger.Warn("redis close", slog.String("err", err.Error()))
	}
}

// Run loads config, builds the App, serves the status HTTP server, runs the
// configured mode (backfill then live, or live directly), and blocks until ctx
// is cancelled or a signal arrives, then drains gracefully.
func Run(parent context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := sharedlog.New("indexer", cfg.LogLevel)
	logger.Info("starting indexer service",
		slog.String("mode", cfg.IndexerMode), slog.String("logSource", cfg.IndexerLogSource))

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	app, err := New(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer app.Close()

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

	// Backfill mode: run the historical replay first.
	if cfg.IndexerMode == config.ModeBackfill {
		app.Publisher.SetBackfillMode(true)
		if err := app.Backfill.Run(ctx); err != nil {
			return err
		}
		app.Publisher.SetBackfillMode(false)
		if cfg.BackfillOnly {
			logger.Info("backfill complete — BACKFILL_ONLY=true, exiting")
			shutdownServer(srv)
			return firstNonShutdownErr(serveErr)
		}
		logger.Info("backfill complete — transitioning to live mode")
	}

	procErr := make(chan error, 1)
	go func() { procErr <- app.Processor.Run(ctx) }()

	shutErr := process.Run(ctx, process.Options{
		Logger:     logger,
		OnShutdown: func(sctx context.Context) error { return srv.Shutdown(sctx) },
	})

	cancel()
	app.Publisher.Disconnect()

	select {
	case err := <-serveErr:
		return err
	case err := <-procErr:
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		return shutErr
	default:
		return shutErr
	}
}

func shutdownServer(srv *http.Server) {
	sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer scancel()
	_ = srv.Shutdown(sctx)
}

func firstNonShutdownErr(serveErr <-chan error) error {
	select {
	case err := <-serveErr:
		return err
	default:
		return nil
	}
}
