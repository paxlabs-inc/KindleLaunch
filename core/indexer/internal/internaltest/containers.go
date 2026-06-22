// Package internaltest provides real ephemeral infrastructure (Postgres, Redis)
// for the indexer's integration tests via testcontainers — never fakes. A
// migrated Postgres pool and a live Redis URL are spun up per call and torn down
// through t.Cleanup. It is imported only by *_test.go files, so it adds nothing
// to the production binary and is invisible to the per-package coverage gate.
package internaltest

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"github.com/Sidiora-Technologies/KindleLaunch/core/indexer/internal/migrate"
)

// NewPostgres starts a postgres:16-alpine container, applies the indexer goose
// migrations, and returns the connection DSN plus a ready pgx pool. Both the
// container and the pool are torn down via t.Cleanup.
func NewPostgres(t *testing.T) (string, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	ctr, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("indexer_test"),
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
