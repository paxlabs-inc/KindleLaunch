// Package internaltest provides real ephemeral infrastructure (Postgres, Redis)
// for the stats-workers integration tests via testcontainers — never fakes
// (house rule no_stub). A migrated Postgres pool (the stats schema, via the
// service's own goose migrations) and a live Redis client are spun up per call
// and torn down through t.Cleanup. It is imported only by *_test.go files, so it
// adds nothing to the production binary and is invisible to the per-package
// coverage gate.
package internaltest

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"github.com/Sidiora-Technologies/KindleLaunch/core/stats-workers/internal/migrate"
)

// metadataSchemaDDL creates the minimal metadata.token_metadata table the search
// route joins against. In production this table is owned by the media/metadata
// service in the SAME database (invariant i2); here we create a faithful subset
// of the columns the stats search query reads.
const metadataSchemaDDL = `
CREATE SCHEMA IF NOT EXISTS metadata;
CREATE TABLE IF NOT EXISTS metadata.token_metadata (
	token_address varchar(42) PRIMARY KEY,
	pool_address  varchar(42) NOT NULL,
	name          text,
	symbol        text,
	description   text,
	created_by    varchar(42) NOT NULL,
	created_at    bigint      NOT NULL
);`

// NewPostgres starts a postgres:16-alpine container, applies the stats-workers
// goose migrations (the stats schema), and returns a ready pgx pool. Both the
// container and the pool are torn down via t.Cleanup.
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
		tcpostgres.WithDatabase("stats_test"),
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

	if err := migrate.Up(ctx, dsn); err != nil {
		t.Fatalf("migrate up: %v", err)
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
	return dsn, pool
}

// EnsureMetadataSchema creates the metadata.token_metadata table used by the
// search route. Call this from tests that exercise GET /search.
func EnsureMetadataSchema(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), metadataSchemaDDL); err != nil {
		t.Fatalf("create metadata schema: %v", err)
	}
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
