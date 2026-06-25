package ranker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// Redis key + window constants (parity with rankers/helpers.ts and the
// ranking:<category> key scheme read by routes/rankings.ts).
const (
	keyTrending  = "ranking:trending"
	keyBreakout  = "ranking:breakout"
	keyTopVolume = "ranking:top_volume"
	keyMovers    = "ranking:movers"
	keyUnusual   = "ranking:unusual"
	keyNew       = "ranking:new"

	// tmpTTL bounds the lifetime of the staging key in case a process dies
	// between ZADD and RENAME (R-3 atomic swap), matching the TS expire(120).
	tmpTTL = 120 * time.Second

	// trendingExcludeTop is the number of top trending pools excluded from the
	// movers ranking (computeMovers reads zrange 0..49).
	trendingExcludeTop = 50

	// topVolumeLimit and newPoolsLimit cap the DB reads (computeTopVolume /
	// computeNew LIMIT 200).
	topVolumeLimit = 200
	newPoolsLimit  = 200

	// trendingWindow / breakoutWindow are the updated_at look-back windows.
	trendingWindow = 86400 // 24h (trending, top-volume, movers, unusual)
	breakoutWindow = 3600  // 1h
)

// VolumeRow is a top-volume DB row: a pool address and its raw 24h volume text.
type VolumeRow struct {
	Address   string
	Volume24h string
}

// NewPoolRow is a new-pools DB row: a pool address and its creation unix second.
type NewPoolRow struct {
	Address   string
	CreatedAt int64
}

// Source supplies the pre-filtered DB reads each ranker needs. It is implemented
// by internal/store; the interface lives here so the (pure) ranker package never
// imports the store, keeping the dependency edge one-way.
type Source interface {
	TrendingCandidates(ctx context.Context, sinceUpdatedAt int64) ([]PoolStat, error)
	BreakoutCandidates(ctx context.Context, sinceUpdatedAt int64) ([]PoolStat, error)
	MoversCandidates(ctx context.Context, sinceUpdatedAt int64) ([]PoolStat, error)
	UnusualCandidates(ctx context.Context, sinceUpdatedAt int64) ([]PoolStat, error)
	TopVolume(ctx context.Context, sinceUpdatedAt int64, limit int) ([]VolumeRow, error)
	NewPools(ctx context.Context, limit int) ([]NewPoolRow, error)
}

// Service computes the rankings from a Source and publishes them to Redis.
type Service struct {
	src        Source
	rdb        *goredis.Client
	maxEntries int
	logger     *slog.Logger
	now        func() int64 // unix seconds; overridable in tests
}

// NewService builds a ranking Service. maxEntries caps every published ZSET; a
// non-positive value falls back to 200 (parity default).
func NewService(src Source, rdb *goredis.Client, maxEntries int, logger *slog.Logger) *Service {
	if maxEntries <= 0 {
		maxEntries = 200
	}
	return &Service{
		src:        src,
		rdb:        rdb,
		maxEntries: maxEntries,
		logger:     logger,
		now:        func() int64 { return time.Now().Unix() },
	}
}

// Trending recomputes and publishes ranking:trending.
func (s *Service) Trending(ctx context.Context) error {
	pools, err := s.src.TrendingCandidates(ctx, s.now()-trendingWindow)
	if err != nil {
		return err
	}
	scored := make([]Scored, 0, len(pools))
	for _, p := range pools {
		scored = append(scored, Scored{Address: p.PoolAddress, Score: ScoreTrending(p)})
	}
	return s.write(ctx, keyTrending, scored)
}

// Breakout recomputes and publishes ranking:breakout.
func (s *Service) Breakout(ctx context.Context) error {
	pools, err := s.src.BreakoutCandidates(ctx, s.now()-breakoutWindow)
	if err != nil {
		return err
	}
	scored := make([]Scored, 0, len(pools))
	for _, p := range pools {
		if score, ok := ScoreBreakout(p); ok {
			scored = append(scored, Scored{Address: p.PoolAddress, Score: score})
		}
	}
	return s.write(ctx, keyBreakout, scored)
}

// TopVolume recomputes and publishes ranking:top_volume.
func (s *Service) TopVolume(ctx context.Context) error {
	rows, err := s.src.TopVolume(ctx, s.now()-trendingWindow, topVolumeLimit)
	if err != nil {
		return err
	}
	scored := make([]Scored, 0, len(rows))
	for _, r := range rows {
		score, ok := jsParseFloat(r.Volume24h)
		if !ok {
			continue
		}
		scored = append(scored, Scored{Address: r.Address, Score: score})
	}
	return s.write(ctx, keyTopVolume, scored)
}

// Movers recomputes and publishes ranking:movers, excluding pools already in the
// trending top-50 (computeMovers reads ranking:trending).
func (s *Service) Movers(ctx context.Context) error {
	pools, err := s.src.MoversCandidates(ctx, s.now()-trendingWindow)
	if err != nil {
		return err
	}
	top, err := s.rdb.ZRange(ctx, keyTrending, 0, trendingExcludeTop-1).Result()
	if err != nil {
		return fmt.Errorf("ranker: read trending set: %w", err)
	}
	trendingSet := make(map[string]struct{}, len(top))
	for _, a := range top {
		trendingSet[a] = struct{}{}
	}

	scored := make([]Scored, 0, len(pools))
	for _, p := range pools {
		if _, ok := trendingSet[p.PoolAddress]; ok {
			continue
		}
		if score, ok := ScoreMovers(p); ok {
			scored = append(scored, Scored{Address: p.PoolAddress, Score: score})
		}
	}
	return s.write(ctx, keyMovers, scored)
}

// Unusual recomputes and publishes ranking:unusual.
func (s *Service) Unusual(ctx context.Context) error {
	pools, err := s.src.UnusualCandidates(ctx, s.now()-trendingWindow)
	if err != nil {
		return err
	}
	scored := make([]Scored, 0, len(pools))
	for _, p := range pools {
		if score, ok := ScoreUnusual(p); ok {
			scored = append(scored, Scored{Address: p.PoolAddress, Score: score})
		}
	}
	return s.write(ctx, keyUnusual, scored)
}

// NewPools recomputes and publishes ranking:new (scored by creation time).
func (s *Service) NewPools(ctx context.Context) error {
	rows, err := s.src.NewPools(ctx, newPoolsLimit)
	if err != nil {
		return err
	}
	scored := make([]Scored, 0, len(rows))
	for _, r := range rows {
		scored = append(scored, Scored{Address: r.Address, Score: float64(r.CreatedAt)})
	}
	return s.write(ctx, keyNew, scored)
}

// RunAll recomputes the five heavy rankers concurrently (parity with the TS
// Promise.all in index.ts runAll). All rankers run even if one fails; the joined
// error is returned.
func (s *Service) RunAll(ctx context.Context) error {
	tasks := []func(context.Context) error{
		s.Trending, s.Breakout, s.TopVolume, s.Movers, s.Unusual,
	}
	errs := make([]error, len(tasks))
	var wg sync.WaitGroup
	for i, task := range tasks {
		wg.Add(1)
		go func(i int, task func(context.Context) error) {
			defer wg.Done()
			errs[i] = task(ctx)
		}(i, task)
	}
	wg.Wait()
	return errors.Join(errs...)
}

// RunNew recomputes the new-pools ranker (parity with runNew in index.ts).
func (s *Service) RunNew(ctx context.Context) error {
	return s.NewPools(ctx)
}

// write publishes entries to a Redis ZSET under key using the atomic
// staging-then-RENAME pattern (R-3): build the ranking in a temp key with a
// short TTL, then RENAME it over the live key so readers never observe an empty
// or half-built ranking. An empty ranking deletes the key. Entries are sorted by
// score descending (stably, preserving input order on ties, matching V8's stable
// Array.sort) and truncated to maxEntries.
func (s *Service) write(ctx context.Context, key string, entries []Scored) error {
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Score > entries[j].Score })
	if len(entries) > s.maxEntries {
		entries = entries[:s.maxEntries]
	}

	if len(entries) == 0 {
		if err := s.rdb.Del(ctx, key).Err(); err != nil {
			return fmt.Errorf("ranker: del %s: %w", key, err)
		}
		return nil
	}

	tmpKey := key + ":tmp:" + strconv.FormatInt(time.Now().UnixMilli(), 10)
	members := make([]goredis.Z, len(entries))
	for i, e := range entries {
		members[i] = goredis.Z{Score: e.Score, Member: e.Address}
	}

	pipe := s.rdb.Pipeline()
	pipe.ZAdd(ctx, tmpKey, members...)
	pipe.Expire(ctx, tmpKey, tmpTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("ranker: stage %s: %w", key, err)
	}
	if err := s.rdb.Rename(ctx, tmpKey, key).Err(); err != nil {
		return fmt.Errorf("ranker: rename %s: %w", key, err)
	}
	if s.logger != nil {
		s.logger.Debug("ranking published", slog.String("key", key), slog.Int("entries", len(entries)))
	}
	return nil
}
