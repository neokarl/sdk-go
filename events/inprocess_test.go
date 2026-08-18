package events

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// The in-process bus is what New returns when no Redis address is configured,
// so it is the first thing every new service exercises. It was also, until this
// file, the only backend with no tests at all.
//
// Consume blocks until its context is cancelled (see the Consumer interface),
// so every subscription here runs in a goroutine, and delivery is observed
// through channels rather than counters — the in-process backend dispatches
// asynchronously.

// subscribe starts a consumer and returns a channel of what it receives. It
// waits until the subscription is actually registered, so a Publish that
// follows cannot race it.
func subscribe(t *testing.T, bus Bus, names ...string) <-chan Event {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	got := make(chan Event, 8)
	ready := make(chan struct{})
	go func() {
		close(ready)
		_ = bus.Consume(ctx, "g", names, func(_ context.Context, e Event) error {
			got <- e
			return nil
		})
	}()
	<-ready
	waitForSubscribers(t, bus, 1)
	return got
}

// waitForSubscribers blocks until the bus has registered at least n consumers.
func waitForSubscribers(t *testing.T, bus Bus, n int) {
	t.Helper()
	p, ok := bus.(*inProcess)
	if !ok {
		return
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.RLock()
		count := len(p.subs)
		p.mu.RUnlock()
		if count >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("only %d subscribers registered, want %d", n, n)
}

func recv(t *testing.T, ch <-chan Event) Event {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("no event arrived")
		return Event{}
	}
}

func TestNewWithoutRedisIsInProcess(t *testing.T) {
	bus, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })

	if _, ok := bus.(*inProcess); !ok {
		t.Errorf("New with no RedisAddr returned %T, want the in-process bus", bus)
	}
	if err := bus.HealthCheck(context.Background()); err != nil {
		t.Errorf("HealthCheck: %v", err)
	}
}

func TestPublishReachesASubscriber(t *testing.T) {
	bus, _ := New(Config{})
	t.Cleanup(func() { _ = bus.Close() })

	got := subscribe(t, bus, "item.created")
	if err := bus.Publish(context.Background(), Event{Name: "item.created", Source: "inventory"}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	e := recv(t, got)
	if e.Name != "item.created" || e.Source != "inventory" {
		t.Errorf("event = %+v", e)
	}
	// The bus stamps the metadata a handler is entitled to assume is there.
	if e.ID == "" {
		t.Error("event arrived with no id")
	}
	if e.Timestamp.IsZero() {
		t.Error("event arrived with no timestamp")
	}
}

// A subscriber must only see what it asked for. Receiving everything and
// filtering inside the handler is the bug this prevents.
func TestSubscribersOnlySeeTheirOwnEvents(t *testing.T) {
	bus, _ := New(Config{})
	t.Cleanup(func() { _ = bus.Close() })

	got := subscribe(t, bus, "item.created")

	_ = bus.Publish(context.Background(), Event{Name: "item.deleted"})
	_ = bus.Publish(context.Background(), Event{Name: "item.updated"})
	_ = bus.Publish(context.Background(), Event{Name: "item.created", Source: "wanted"})

	// The first thing to arrive must be the one event that was subscribed to;
	// the other two must never appear.
	if e := recv(t, got); e.Source != "wanted" {
		t.Errorf("received an unsubscribed event: %+v", e)
	}
	select {
	case e := <-got:
		t.Errorf("received a second, unsubscribed event: %+v", e)
	case <-time.After(100 * time.Millisecond):
	}
}

// Every subscriber to an event gets it — this is pub/sub, not a work queue.
func TestEverySubscriberToAnEventGetsIt(t *testing.T) {
	bus, _ := New(Config{})
	t.Cleanup(func() { _ = bus.Close() })

	const n = 3
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var wg sync.WaitGroup
	wg.Add(n)
	var once sync.Map
	for i := range n {
		go func() {
			_ = bus.Consume(ctx, "g", []string{"item.created"}, func(_ context.Context, _ Event) error {
				if _, loaded := once.LoadOrStore(i, true); !loaded {
					wg.Done()
				}
				return nil
			})
		}()
	}
	waitForSubscribers(t, bus, n)

	_ = bus.Publish(context.Background(), Event{Name: "item.created"})

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("not every subscriber received the event")
	}
}

// One handler returning an error must not stop the others, and must not be
// reported to the publisher. A publisher cannot know who is subscribed, so it
// cannot be made responsible for their failures.
func TestAFailingHandlerDoesNotStopTheOthers(t *testing.T) {
	bus, _ := New(Config{})
	t.Cleanup(func() { _ = bus.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go func() {
		_ = bus.Consume(ctx, "bad", []string{"item.created"}, func(_ context.Context, _ Event) error {
			return errors.New("handler blew up")
		})
	}()
	reached := make(chan struct{}, 1)
	go func() {
		_ = bus.Consume(ctx, "good", []string{"item.created"}, func(_ context.Context, _ Event) error {
			reached <- struct{}{}
			return nil
		})
	}()
	waitForSubscribers(t, bus, 2)

	if err := bus.Publish(context.Background(), Event{Name: "item.created"}); err != nil {
		t.Errorf("Publish reported a subscriber's failure to the publisher: %v", err)
	}
	select {
	case <-reached:
	case <-time.After(2 * time.Second):
		t.Fatal("the healthy subscriber never ran")
	}
}

// Cancelling a consumer's context has to unregister it, or a long-lived process
// that restarts consumers accumulates dead subscriptions.
func TestCancellingAConsumerUnregistersIt(t *testing.T) {
	bus, _ := New(Config{})
	t.Cleanup(func() { _ = bus.Close() })
	p := bus.(*inProcess)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = bus.Consume(ctx, "g", []string{"item.created"}, func(context.Context, Event) error { return nil })
		close(done)
	}()
	waitForSubscribers(t, bus, 1)

	cancel()
	<-done

	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.subs) != 0 {
		t.Errorf("%d subscriptions left after cancellation", len(p.subs))
	}
}

// Shutdown ordering is rarely perfect. Publishing or closing twice during
// teardown must not panic, or it takes down whatever else was still draining.
func TestCloseIsIdempotentAndPublishAfterCloseIsSafe(t *testing.T) {
	bus, _ := New(Config{})
	if err := bus.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := bus.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	if err := bus.Publish(context.Background(), Event{Name: "item.created"}); err != nil {
		t.Logf("publish after close returned %v (acceptable; must not panic)", err)
	}
}
