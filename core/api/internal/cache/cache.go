// Package cache provides the read-path response cache for core/api's snapshot
// REST surface: a bounded in-process LRU with per-entry TTL, strong ETags for
// conditional 304 responses, and singleflight stampede protection so a cold key
// hit by N concurrent requests triggers exactly ONE upstream fetch (SECTION 17
// caching + stampede protection). It sits in front of the Redis/Postgres reads
// in internal/store, shielding them from read-heavy bursts at 500K scale.
package cache

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	sharedhttp "github.com/Sidiora-Technologies/KindleLaunch/shared/http"
)

// Cache is a concurrency-safe LRU of JSON payloads with TTL + singleflight.
type Cache struct {
	mu       sync.Mutex
	ll       *list.List
	items    map[string]*list.Element
	capacity int
	inflight map[string]*flightCall

	hits   uint64
	misses uint64
}

type entry struct {
	key     string
	body    []byte
	etag    string
	expires time.Time
}

type flightCall struct {
	wg   sync.WaitGroup
	body []byte
	etag string
	err  error
}

// New builds a Cache holding at most capacity entries (LRU eviction).
func New(capacity int) *Cache {
	if capacity <= 0 {
		capacity = 1024
	}
	return &Cache{
		ll:       list.New(),
		items:    make(map[string]*list.Element, capacity),
		capacity: capacity,
		inflight: make(map[string]*flightCall),
	}
}

// FetchFunc produces a fresh payload for a cache miss.
type FetchFunc func(ctx context.Context) ([]byte, error)

// GetOrFetch returns the cached payload for key, or fetches+stores it. Concurrent
// callers for the same cold key share a single fetch (singleflight). A zero or
// negative ttl disables caching (the fetch result is returned but not stored).
func (c *Cache) GetOrFetch(ctx context.Context, key string, ttl time.Duration, fetch FetchFunc) (body []byte, etag string, err error) {
	now := time.Now()

	c.mu.Lock()
	if el, ok := c.items[key]; ok {
		if e, ok := el.Value.(*entry); ok && now.Before(e.expires) {
			c.ll.MoveToFront(el)
			b, et := e.body, e.etag
			c.hits++
			c.mu.Unlock()
			return b, et, nil
		}
		c.removeElement(el)
	}
	if fc, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		fc.wg.Wait()
		return fc.body, fc.etag, fc.err
	}
	fc := &flightCall{}
	fc.wg.Add(1)
	c.inflight[key] = fc
	c.misses++
	c.mu.Unlock()

	b, ferr := fetch(ctx)
	fc.body = b
	fc.err = ferr
	if ferr == nil {
		fc.etag = ETag(b)
	}
	fc.wg.Done()

	c.mu.Lock()
	delete(c.inflight, key)
	if ferr == nil && ttl > 0 {
		c.storeLocked(key, b, fc.etag, now.Add(ttl))
	}
	c.mu.Unlock()
	return fc.body, fc.etag, fc.err
}

// storeLocked inserts/refreshes an entry and evicts the LRU tail if over cap.
func (c *Cache) storeLocked(key string, body []byte, etag string, expires time.Time) {
	if el, ok := c.items[key]; ok {
		if e, ok := el.Value.(*entry); ok {
			e.body, e.etag, e.expires = body, etag, expires
			c.ll.MoveToFront(el)
			return
		}
	}
	el := c.ll.PushFront(&entry{key: key, body: body, etag: etag, expires: expires})
	c.items[key] = el
	for c.ll.Len() > c.capacity {
		if tail := c.ll.Back(); tail != nil {
			c.removeElement(tail)
		}
	}
}

func (c *Cache) removeElement(el *list.Element) {
	c.ll.Remove(el)
	if e, ok := el.Value.(*entry); ok {
		delete(c.items, e.key)
	}
}

// Len returns the number of cached entries.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}

// Stats returns cumulative hit/miss counters (for metrics).
func (c *Cache) Stats() (hits, misses uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, c.misses
}

// Handler wraps a JSON read as a cached, ETag-aware HTTP handler. keyFn derives
// the cache key from the request (typically path + relevant query); fetch loads
// the payload on a miss. On a matching If-None-Match it returns 304.
func (c *Cache) Handler(keyFn func(*http.Request) string, ttl time.Duration, fetch func(*http.Request) ([]byte, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := keyFn(r)
		body, _, err := c.GetOrFetch(r.Context(), key, ttl, func(ctx context.Context) ([]byte, error) {
			return fetch(r.WithContext(ctx))
		})
		if err != nil {
			sharedhttp.WriteError(w, http.StatusInternalServerError, "Internal Server Error", "read failed")
			return
		}
		ServeJSON(w, r, body, ttl)
	}
}

// ServeJSON writes a JSON body with a strong ETag and a public max-age derived
// from ttl (ttl<=0 => no-store), honouring a matching If-None-Match with 304.
// It is shared by Handler and by the manually-cached REST handlers so the
// conditional-request semantics are identical across the snapshot surface.
func ServeJSON(w http.ResponseWriter, r *http.Request, body []byte, ttl time.Duration) {
	etag := ETag(body)
	w.Header().Set("ETag", etag)
	secs := int(ttl / time.Second)
	if secs > 0 {
		w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(secs))
	} else {
		w.Header().Set("Cache-Control", "no-store")
	}
	if matchesETag(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(body); err != nil {
		return
	}
}

// ETag computes a strong ETag from the payload (quoted hex of a truncated
// SHA-256). Identical bodies yield identical ETags.
func ETag(body []byte) string {
	sum := sha256.Sum256(body)
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

// matchesETag reports whether the If-None-Match header matches etag. It honours
// the "*" wildcard and comma-separated lists, ignoring weak-validator prefixes.
func matchesETag(ifNoneMatch, etag string) bool {
	ifNoneMatch = strings.TrimSpace(ifNoneMatch)
	if ifNoneMatch == "" {
		return false
	}
	if ifNoneMatch == "*" {
		return true
	}
	for _, candidate := range strings.Split(ifNoneMatch, ",") {
		candidate = strings.TrimSpace(candidate)
		candidate = strings.TrimPrefix(candidate, "W/")
		if candidate == etag {
			return true
		}
	}
	return false
}
