// live.go — the ephemeral half of this package.
//
// Bus (events.go) is durable: at-least-once, replayable, consumed by competing
// members of a group. Live is its opposite on every axis, and deliberately so.

package events

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"

	"github.com/redis/go-redis/v9"
)

// ErrNoRedis means a Live stream was asked for without somewhere to publish it.
var ErrNoRedis = errors.New("events: live streams need a Redis address")

// Live fans an event out to every current subscriber of a key.
//
// Use it for the live tail of something in progress — a scan's counter ticking
// up, tokens streaming into a chat — where the reader wants what is happening
// *now* and old events are worthless. Use Bus instead for anything another
// service must not miss.
//
// The differences from Bus are not incidental:
//
//   - Broadcast, not competing consumers. Every subscriber of a key sees every
//     event, which is what a fan of open browser tabs needs and what a consumer
//     group is specifically not.
//   - Lossy on purpose. A subscriber that cannot keep up misses events rather
//     than slowing the publisher down; falling behind on a progress counter
//     costs nothing, and blocking the work that produces it costs everything.
//   - No replay. On reconnect a client wants current state, which it should
//     fetch, not a thousand stale ticks.
//
// Redis-backed always: a live stream that silently failed to cross processes
// would look like working software right up until the work moved to another
// worker, and then hang the reader.
type Live[T any] interface {
	// Subscribe returns a channel of events for a key and a func to stop.
	// Call unsubscribe exactly once; the channel is closed for you.
	Subscribe(key string) (events <-chan T, unsubscribe func())
	// Publish sends to every current subscriber. Best-effort and never blocking:
	// it must not be able to fail the work that produced the event.
	Publish(key string, ev T)
}

// LiveConfig configures a Live stream.
type LiveConfig struct {
	// RedisAddr is required.
	RedisAddr string
	// Prefix namespaces the channels, since the platform's Redis is shared by
	// every service. Required: e.g. "webtools:scan".
	Prefix string
	// Buffer is the per-subscriber queue depth. Default 32.
	Buffer int
}

func (c *LiveConfig) withDefaults() {
	if c.Buffer <= 0 {
		c.Buffer = 32
	}
}

// NewLive returns a Redis-backed Live stream.
func NewLive[T any](cfg LiveConfig) (Live[T], error) {
	cfg.withDefaults()
	if cfg.RedisAddr == "" {
		return nil, ErrNoRedis
	}
	if cfg.Prefix == "" {
		return nil, errors.New("events: live streams need a Prefix to namespace their channels")
	}
	return NewLiveRedis[T](redis.NewClient(&redis.Options{Addr: cfg.RedisAddr}), cfg), nil
}

// NewLiveRedis builds a Live stream on an existing Redis client, for a caller
// that manages the connection itself.
func NewLiveRedis[T any](rdb *redis.Client, cfg LiveConfig) Live[T] {
	cfg.withDefaults()
	return &liveRedis[T]{rdb: rdb, cfg: cfg}
}

type liveRedis[T any] struct {
	rdb *redis.Client
	cfg LiveConfig
}

func (l *liveRedis[T]) channel(key string) string { return l.cfg.Prefix + ":" + key }

func (l *liveRedis[T]) Subscribe(key string) (<-chan T, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	sub := l.rdb.Subscribe(ctx, l.channel(key))
	out := make(chan T, l.cfg.Buffer)

	go func() {
		defer close(out)
		for msg := range sub.Channel() {
			var ev T
			if err := json.Unmarshal([]byte(msg.Payload), &ev); err != nil {
				continue // a malformed event is not worth killing the stream for
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			default: // slow subscriber: drop, don't block
			}
		}
	}()

	var once sync.Once
	return out, func() {
		once.Do(func() {
			cancel()
			_ = sub.Close()
		})
	}
}

func (l *liveRedis[T]) Publish(key string, ev T) {
	payload, err := json.Marshal(ev)
	if err != nil {
		slog.Default().Warn("events: live publish encode", "key", key, "error", err)
		return
	}
	// Best-effort: losing a progress event degrades a live view, but it must
	// never fail the work that produced it.
	if err := l.rdb.Publish(context.Background(), l.channel(key), payload).Err(); err != nil {
		slog.Default().Warn("events: live publish", "key", key, "error", err)
	}
}
