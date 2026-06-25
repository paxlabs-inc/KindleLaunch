// Package internaltest provides real ephemeral infrastructure (Postgres, Redis)
// for the core/api integration tests via testcontainers — never fakes (house
// rule no_stub). core/api owns NO schema of its own: it READS the schemas the
// core/* services write (candles, stats, indexer). This helper creates the
// faithful read-only subset of those tables core/api queries, using the same
// column names/types as the live schemas (invariant i2) so the snapshot REST
// reads exercise real SQL against a real database.
//
// It is imported only by *_test.go files, so it adds nothing to the production
// binary and is invisible to the per-package coverage gate.
package internaltest

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

// readSchemaDDL creates the read-only tables core/api queries. Columns/types
// match the live candles.candles, stats.pool_stats and stats.pool_holders
// tables 1:1 for the fields the gateway reads (invariant i2).
const readSchemaDDL = `
CREATE SCHEMA IF NOT EXISTS candles;
CREATE TABLE candles.candles (
	pool_address      varchar(42) NOT NULL,
	timeframe         varchar(4)  NOT NULL,
	candle_start      bigint      NOT NULL,
	open              text        NOT NULL,
	high              text        NOT NULL,
	low               text        NOT NULL,
	close             text        NOT NULL,
	volume_usdl       text        NOT NULL DEFAULT '0',
	volume_token      text        NOT NULL DEFAULT '0',
	buy_volume_usdl   text        NOT NULL DEFAULT '0',
	sell_volume_usdl  text        NOT NULL DEFAULT '0',
	trade_count       integer     NOT NULL DEFAULT 0,
	unique_traders    integer     NOT NULL DEFAULT 0,
	large_trade_count integer     NOT NULL DEFAULT 0,
	last_trade_ts     bigint      NOT NULL,
	sequence_num      bigint      NOT NULL,
	mcap_open         text        NOT NULL DEFAULT '0',
	mcap_high         text        NOT NULL DEFAULT '0',
	mcap_low          text        NOT NULL DEFAULT '0',
	mcap_close        text        NOT NULL DEFAULT '0',
	PRIMARY KEY (pool_address, timeframe, candle_start)
);
CREATE INDEX idx_candles_seq ON candles.candles (pool_address, timeframe, sequence_num);

CREATE SCHEMA IF NOT EXISTS stats;
CREATE TABLE stats.pool_stats (
	pool_address            varchar(42) PRIMARY KEY,
	token_address           varchar(42) NOT NULL,
	price                   text    NOT NULL DEFAULT '0',
	price_change_1m         text    NOT NULL DEFAULT '0',
	price_change_5m         text    NOT NULL DEFAULT '0',
	price_change_15m        text    NOT NULL DEFAULT '0',
	price_change_1h         text    NOT NULL DEFAULT '0',
	price_change_24h        text    NOT NULL DEFAULT '0',
	price_change_dollar_1m  text    NOT NULL DEFAULT '0',
	price_change_dollar_5m  text    NOT NULL DEFAULT '0',
	price_change_dollar_15m text    NOT NULL DEFAULT '0',
	price_change_dollar_1h  text    NOT NULL DEFAULT '0',
	price_change_dollar_24h text    NOT NULL DEFAULT '0',
	high_24h                text    NOT NULL DEFAULT '0',
	low_24h                 text    NOT NULL DEFAULT '0',
	volume_24h              text    NOT NULL DEFAULT '0',
	volume_1h               text    NOT NULL DEFAULT '0',
	volume_5m               text    NOT NULL DEFAULT '0',
	market_cap              text    NOT NULL DEFAULT '0',
	buy_count_24h           integer NOT NULL DEFAULT 0,
	sell_count_24h          integer NOT NULL DEFAULT 0,
	unique_traders_24h      integer NOT NULL DEFAULT 0,
	holder_count            integer NOT NULL DEFAULT 0,
	top10_concentration     text    NOT NULL DEFAULT '0',
	creator_holdings_pct    text    NOT NULL DEFAULT '0',
	risk_rating             integer NOT NULL DEFAULT 50,
	risk_factors            text    DEFAULT '[]',
	creator_address         varchar(42),
	created_at              bigint  NOT NULL,
	updated_at              bigint  NOT NULL
);
CREATE TABLE stats.pool_holders (
	pool_address   varchar(42) NOT NULL,
	holder_address varchar(42) NOT NULL,
	balance        text        NOT NULL,
	pct_of_supply  text        NOT NULL,
	last_updated   bigint      NOT NULL,
	PRIMARY KEY (pool_address, holder_address)
);
CREATE INDEX idx_ph_balance ON stats.pool_holders (pool_address, balance);
`

// NewPostgres starts a postgres:16-alpine container, creates the read-only
// schema core/api queries, and returns a ready pgx pool. The container and pool
// are torn down via t.Cleanup.
func NewPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	_, pool := NewPostgresWithDSN(t)
	return pool
}

// NewPostgresWithDSN is NewPostgres but also returns the DSN, for callers that
// build their own pool from config (e.g. the app integration test).
func NewPostgresWithDSN(t *testing.T) (string, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	ctr, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("api_test"),
		tcpostgres.WithUsername("kl"),
		tcpostgres.WithPassword("kl"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() {
		if err := ctr.Terminate(ctx); err != nil {
			t.Logf("terminate container: %v", err)
		}
	})

	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse pool config: %v", err)
	}
	cfg.MaxConns = 8
	cfg.MaxConnIdleTime = 30 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	t.Cleanup(pool.Close)

	if _, err := pool.Exec(ctx, readSchemaDDL); err != nil {
		t.Fatalf("create read schema: %v", err)
	}
	return dsn, pool
}

// NewRedisURL starts a redis:7-alpine container and returns its connection URL.
func NewRedisURL(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	ctr, err := tcredis.Run(ctx, "redis:7-alpine")
	if err != nil {
		t.Fatalf("start redis container: %v", err)
	}
	t.Cleanup(func() {
		if err := ctr.Terminate(ctx); err != nil {
			t.Logf("terminate container: %v", err)
		}
	})
	uri, err := ctr.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("redis connection string: %v", err)
	}
	return uri
}

// NewRedis starts a redis:7-alpine container and returns a connected client.
func NewRedis(t *testing.T) *goredis.Client {
	t.Helper()
	opt, err := goredis.ParseURL(NewRedisURL(t))
	if err != nil {
		t.Fatalf("parse redis url: %v", err)
	}
	rdb := goredis.NewClient(opt)
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}
