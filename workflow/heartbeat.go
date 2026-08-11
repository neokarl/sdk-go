package workflow

import (
	"context"
	"sync"
	"time"
)

// heartbeatEvery is how often a running job reports liveness.
//
// Deliberately a timer, not a per-result callback. Work that produces nothing for
// minutes at a time — probing ten thousand hosts to find a handful alive — is
// working perfectly hard, and tying liveness to output makes the engine mistake
// it for a hung worker and kill it. That was a real incident, not a theory.
const heartbeatEvery = 15 * time.Second

// HeartbeatTimeout is how long an engine should tolerate silence before assuming
// the worker died. Comfortably more than heartbeatEvery, so a few missed ticks
// are survivable. Exported because engines need it when configuring an attempt.
const HeartbeatTimeout = 90 * time.Second

type beaterKey struct{}

// WithBeater attaches an engine's liveness callback to the context a handler
// runs under. Engine implementations call this; services never do.
func WithBeater(ctx context.Context, beat func(any)) context.Context {
	return context.WithValue(ctx, beaterKey{}, beat)
}

func beaterFrom(ctx context.Context) func(any) {
	beat, _ := ctx.Value(beaterKey{}).(func(any))
	return beat
}

// progress holds the most recent value a handler reported, so the timer always
// has something current to send without the handler blocking on a tick.
type progress struct {
	mu sync.Mutex
	v  any
}

func (p *progress) set(v any) {
	p.mu.Lock()
	p.v = v
	p.mu.Unlock()
}

func (p *progress) get() any {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.v
}

// heartbeater starts the liveness timer for one handler invocation and returns
// the func the handler calls to attach progress to it. With no engine beater in
// context (a unit test, say) it is an inert no-op, so handlers stay testable
// without an engine.
func heartbeater(ctx context.Context, stop <-chan struct{}) func(any) {
	beat := beaterFrom(ctx)
	if beat == nil {
		return func(any) {}
	}
	var latest progress
	go func() {
		t := time.NewTicker(heartbeatEvery)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				beat(latest.get())
			case <-stop:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return latest.set
}
