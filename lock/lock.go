// Package lock provides mutual exclusion by key, so work that must be serialized
// (e.g. one conversation's turns) stays serialized even across multiple service
// replicas. It offers a Redis-backed lock (multi-instance safe) and an in-process
// lock (single instance), selected by whether a Redis address is configured —
// mirroring the SDK's env-gated, safe-fallback convention (see platform/sdk/events).
package lock

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Locker serializes work by key. Acquire blocks until the lock is held (or ctx is
// done) and returns a release func; call it (once) to unlock.
type Locker interface {
	Acquire(ctx context.Context, key string) (release func(), err error)
}

// New returns a Redis-backed Locker when redisAddr is non-empty (safe across
// replicas), otherwise an in-process Locker.
func New(redisAddr string) Locker {
	if redisAddr == "" {
		return NewInProcess()
	}
	return NewRedis(redis.NewClient(&redis.Options{Addr: redisAddr}))
}

// --- in-process ---

type inProcess struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewInProcess returns a single-instance Locker backed by per-key mutexes.
func NewInProcess() Locker { return &inProcess{locks: map[string]*sync.Mutex{}} }

func (p *inProcess) Acquire(_ context.Context, key string) (func(), error) {
	p.mu.Lock()
	l := p.locks[key]
	if l == nil {
		l = &sync.Mutex{}
		p.locks[key] = l
	}
	p.mu.Unlock()
	l.Lock()
	return l.Unlock, nil
}

// --- Redis ---

type redisLocker struct {
	rdb   *redis.Client
	ttl   time.Duration
	spin  time.Duration
	owner string // static prefix; per-lock token is random
}

// NewRedis returns a Locker over the given Redis client. The lock is held with a
// lease (auto-refreshed while held) so a crashed holder's lock expires.
//
// The owner prefix on each token is diagnostic — it is what tells you which
// process is sitting on a contended lock. It defaults to this process's name
// rather than any particular service's, since this package is shared.
func NewRedis(rdb *redis.Client) Locker {
	return &redisLocker{rdb: rdb, ttl: 30 * time.Second, spin: 50 * time.Millisecond, owner: processName()}
}

// processName is the executable's base name, e.g. "web-tools".
func processName() string {
	exe, err := os.Executable()
	if err != nil {
		return "service"
	}
	return filepath.Base(exe)
}

var refreshScript = redis.NewScript(`if redis.call("get", KEYS[1]) == ARGV[1] then return redis.call("pexpire", KEYS[1], ARGV[2]) else return 0 end`)
var releaseScript = redis.NewScript(`if redis.call("get", KEYS[1]) == ARGV[1] then return redis.call("del", KEYS[1]) else return 0 end`)

func (r *redisLocker) Acquire(ctx context.Context, key string) (func(), error) {
	k := "lock:" + key
	token := r.owner + ":" + randToken()

	for {
		ok, err := r.rdb.SetNX(ctx, k, token, r.ttl).Result()
		if err != nil {
			return nil, err
		}
		if ok {
			break
		}
		select {
		case <-time.After(r.spin):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	// Refresh the lease while held so a long turn doesn't lose the lock, and so a
	// crash (which stops refreshing) lets it expire.
	refreshCtx, cancel := context.WithCancel(context.Background())
	go func() {
		t := time.NewTicker(r.ttl / 3)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				_ = refreshScript.Run(refreshCtx, r.rdb, []string{k}, token, r.ttl.Milliseconds()).Err()
			case <-refreshCtx.Done():
				return
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			_ = releaseScript.Run(context.Background(), r.rdb, []string{k}, token).Err()
		})
	}, nil
}

func randToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
