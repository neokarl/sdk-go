// Package events is the framework's durable event bus. Producers publish
// domain events; consumers receive them via per-service consumer groups with
// at-least-once delivery, replay, and dead-lettering.
//
// Two backends sit behind the same interface:
//   - Redis Streams (durable): one stream per event name (events:<name>),
//     consumer groups per subscribing service, XACK/redelivery/DLQ. Used when
//     RedisAddr is set.
//   - in-process (best-effort): single-process fan-out for solo dev, so a
//     service boots without Redis. NOT durable — no persistence or replay.
//
// At-least-once delivery is the deal you accept for durability: a consumer may
// see an event more than once (redelivery after a crash), so handlers must be
// idempotent. The Redis backend dedups by event id as a first line of defence;
// handlers should still be safe to re-run.
package events

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Event is the wire envelope for every published event.
type Event struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"` // dot-namespaced, e.g. "order.created"
	Source    string         `json:"source"`
	TenantID  string         `json:"tenantId,omitempty"`
	Timestamp time.Time      `json:"timestamp"`
	Payload   map[string]any `json:"payload,omitempty"`
}

// ensureMeta fills id + timestamp if the caller left them blank.
func (e *Event) ensureMeta() {
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
}

// Handler processes one event. Returning an error means "not done" — the event
// is left unacknowledged and will be redelivered (Redis backend). Returning nil
// acknowledges it.
type Handler func(ctx context.Context, e Event) error

// Publisher publishes events.
type Publisher interface {
	Publish(ctx context.Context, e Event) error
}

// Consumer subscribes a durable consumer group to a set of event names.
type Consumer interface {
	// Consume registers group as a durable consumer of the named event streams
	// and blocks, dispatching each event to h until ctx is cancelled. Multiple
	// instances sharing a group name load-balance; distinct group names each
	// receive every event.
	Consume(ctx context.Context, group string, names []string, h Handler) error
}

// Bus is the full event surface: publish, consume, health, close.
type Bus interface {
	Publisher
	Consumer
	HealthCheck(ctx context.Context) error
	Close() error
}

// Config selects and tunes the backend.
type Config struct {
	// RedisAddr empty selects the in-process backend (dev). Set it (e.g.
	// "localhost:6379") for the durable Redis Streams backend.
	RedisAddr     string
	RedisPassword string
	RedisDB       int

	// MaxLen caps each stream length (approximate trim) — the replay window.
	MaxLen int64
	// ClaimIdle is how long a delivered-but-unacked event waits before another
	// consumer may reclaim it.
	ClaimIdle time.Duration
	// MaxDeliveries is the attempt ceiling before an event is dead-lettered.
	MaxDeliveries int64
	// DedupTTL is how long processed event ids are remembered for idempotency.
	DedupTTL time.Duration
}

func (c *Config) withDefaults() {
	if c.MaxLen == 0 {
		c.MaxLen = 10000
	}
	if c.ClaimIdle == 0 {
		c.ClaimIdle = 30 * time.Second
	}
	if c.MaxDeliveries == 0 {
		c.MaxDeliveries = 5
	}
	if c.DedupTTL == 0 {
		c.DedupTTL = 24 * time.Hour
	}
}

// New builds the bus. Redis Streams when RedisAddr is set, else in-process.
func New(cfg Config) (Bus, error) {
	cfg.withDefaults()
	if cfg.RedisAddr == "" {
		return newInProcess(), nil
	}
	return newStreams(cfg)
}

// --- in-process backend (dev fallback, NOT durable) ------------------------

type inProcess struct {
	mu     sync.RWMutex
	subs   []*inprocSub
	closed bool
}

type inprocSub struct {
	names map[string]struct{}
	h     Handler
}

func newInProcess() *inProcess { return &inProcess{} }

func (p *inProcess) Publish(ctx context.Context, e Event) error {
	e.ensureMeta()
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, s := range p.subs {
		if _, ok := s.names[e.Name]; !ok {
			continue
		}
		// Best-effort async dispatch; errors are dropped (no redelivery here).
		go func(h Handler) { _ = h(ctx, e) }(s.h)
	}
	return nil
}

func (p *inProcess) Consume(ctx context.Context, _ string, names []string, h Handler) error {
	set := make(map[string]struct{}, len(names))
	for _, n := range names {
		set[n] = struct{}{}
	}
	sub := &inprocSub{names: set, h: h}
	p.mu.Lock()
	p.subs = append(p.subs, sub)
	p.mu.Unlock()

	<-ctx.Done()

	p.mu.Lock()
	for i, s := range p.subs {
		if s == sub {
			p.subs = append(p.subs[:i], p.subs[i+1:]...)
			break
		}
	}
	p.mu.Unlock()
	return ctx.Err()
}

func (p *inProcess) HealthCheck(context.Context) error { return nil }

func (p *inProcess) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	p.subs = nil
	return nil
}
