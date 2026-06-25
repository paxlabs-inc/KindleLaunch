// Package internaltest provides real ephemeral infrastructure (Postgres, Redis)
// for the ranking-algo integration tests via testcontainers — never fakes
// (house rule no_stub). ranking-algo owns no schema of its own, so this helper
// creates the minimal, faithful subset of the read-only tables it queries
// (stats.pool_stats, indexer.pools) using the same column names/types as the
// live schemas (stats/src/db/schema.ts, indexer pools). It is imported only by
// *_test.go files, so it adds nothing to the production binary and is invisible
// to the per-package coverage gate.
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

// readSchemaDDL creates the read-only tables ranking-algo queries. The columns
// and types match the live stats.pool_stats and indexer.pools tables 1:1 for the
// fields the rankers read (invariant i2: same DB as the TS services).
const readSchemaDDL = `
CREATE SCHEMA IF NOT EXISTS stats;
CREATE TABLE stats.pool_stats (
	pool_address       varchar(42) PRIMARY KEY,
	volume_24h         text    NOT NULL DEFAULT '0',
	volume_1h          text    NOT NULL DEFAULT '0',
	volume_5m          text    NOT NULL DEFAULT '0',
	market_cap         text    NOT NULL DEFAULT '0',
	price_change_24h   text    NOT NULL DEFAULT '0',
	buy_count_24h      integer NOT NULL DEFAULT 0,
	sell_count_24h     integer NOT NULL DEFAULT 0,
	unique_traders_24h integer NOT NULL DEFAULT 0,
	holder_count       integer NOT NULL DEFAULT 0,
	updated_at         bigint  NOT NULL
);
CREATE SCHEMA IF NOT EXISTS indexer;
CREATE TABLE indexer.pools (
	pool_address varchar(42) PRIMARY KEY,
	created_at   bigint NOT NULL
);
`

// NewPostgres starts a postgres:16-alpine container, creates the read-only
// stats.pool_stats and indexer.pools tables, and returns a ready pgx pool. Both
// the container and the pool are torn down via t.Cleanup.
func NewPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	_, pool := NewPostgresWithDSN(t)
	return pool
}

// NewPostgresWithDSN is NewPostgres but also returns the connection DSN, for
// callers (e.g. the app integration test) that build their own pool from config.
func NewPostgresWithDSN(t *testing.T) (string, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	ctr, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("ranking_test"),
		tcpostgres.WithUsername("kl"),
		tcpostgres.WithPassword("kl"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() { _ = ctr.Terminate(ctx) })

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
	t.Cleanup(func() { _ = ctr.Terminate(ctx) })
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
