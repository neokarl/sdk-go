package events

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// These are integration tests against a real Redis (the durable backend). They
// skip when Redis is unreachable, so `go test` stays green without infra.
func testRedisAddr(t *testing.T) string {
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		t.Skipf("no redis at %s: %v", addr, err)
	}
	_ = rdb.Close()
	return addr
}

// uniqueName keeps tests from colliding on stream/group state across runs.
func uniqueName(prefix string) string {
	return fmt.Sprintf("test.%s.%d", prefix, time.Now().UnixNano())
}

func cleanup(addr, name string) {
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	defer rdb.Close()
	rdb.Del(context.Background(), streamKey(name), dlqKey(name))
}

func testConfig(addr string) Config {
	cfg := Config{RedisAddr: addr, ClaimIdle: 300 * time.Millisecond, MaxDeliveries: 4}
	cfg.withDefaults()
	return cfg
}

// TestStreamsPublishConsume: a published event is delivered to a consumer.
func TestStreamsPublishConsume(t *testing.T) {
	addr := testRedisAddr(t)
	name := uniqueName("pubsub")
	defer cleanup(addr, name)

	bus, err := New(testConfig(addr))
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()

	got := make(chan Event, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go bus.Consume(ctx, "g", []string{name}, func(_ context.Context, e Event) error {
		got <- e
		return nil
	})
	time.Sleep(200 * time.Millisecond) // let the group get created

	if err := bus.Publish(context.Background(), Event{Name: name, Source: "test", Payload: map[string]any{"k": "v"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-got:
		if e.Name != name || e.Payload["k"] != "v" {
			t.Fatalf("unexpected event: %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("event not delivered")
	}
}

// TestStreamsReplayWhileDown: an event published while the consumer is stopped
// is delivered when it comes back — the durability guarantee the in-process bus
// can't make.
func TestStreamsReplayWhileDown(t *testing.T) {
	addr := testRedisAddr(t)
	name := uniqueName("replay")
	defer cleanup(addr, name)

	bus, err := New(testConfig(addr))
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()

	// First run: create the group, then stop the consumer.
	ctx1, cancel1 := context.WithCancel(context.Background())
	go bus.Consume(ctx1, "g", []string{name}, func(context.Context, Event) error { return nil })
	time.Sleep(300 * time.Millisecond) // ensure group exists
	cancel1()
	time.Sleep(100 * time.Millisecond)

	// Publish while nothing is consuming.
	if err := bus.Publish(context.Background(), Event{Name: name, Source: "test"}); err != nil {
		t.Fatal(err)
	}

	// Second run: the same group must still receive the event (replay).
	got := make(chan Event, 1)
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go bus.Consume(ctx2, "g", []string{name}, func(_ context.Context, e Event) error {
		got <- e
		return nil
	})
	select {
	case <-got: // replayed successfully
	case <-time.After(5 * time.Second):
		t.Fatal("event published while consumer down was not replayed")
	}
}

// TestStreamsRedelivery: a handler that fails leaves the event unacked; the
// reclaim loop redelivers it until it succeeds (at-least-once).
func TestStreamsRedelivery(t *testing.T) {
	addr := testRedisAddr(t)
	name := uniqueName("redeliver")
	defer cleanup(addr, name)

	bus, err := New(testConfig(addr))
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()

	attempts := make(chan int, 8)
	var n int
	done := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go bus.Consume(ctx, "g", []string{name}, func(_ context.Context, e Event) error {
		n++
		attempts <- n
		if n < 2 {
			return fmt.Errorf("forced failure on attempt %d", n)
		}
		close(done)
		return nil
	})
	time.Sleep(200 * time.Millisecond)

	if err := bus.Publish(context.Background(), Event{Name: name, Source: "test"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		if n < 2 {
			t.Fatalf("expected redelivery (>=2 attempts), got %d", n)
		}
	case <-time.After(8 * time.Second):
		t.Fatalf("handler never succeeded after redelivery; attempts=%d", n)
	}
}
