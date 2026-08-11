package events

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// A live stream is Redis-only on purpose: an in-process fallback would look like
// working software until the publisher moved to another worker, and then hang
// every reader.
func TestNewLiveRequiresRedis(t *testing.T) {
	if _, err := NewLive[tick](LiveConfig{Prefix: "test:live"}); !errors.Is(err, ErrNoRedis) {
		t.Errorf("err = %v, want ErrNoRedis", err)
	}
	if _, err := NewLive[tick](LiveConfig{RedisAddr: "localhost:6379"}); err == nil {
		t.Error("a stream with no prefix would collide with every other service's channels")
	}
}

// liveForTest connects to the Redis the dev stack runs, and skips when there
// isn't one rather than failing a unit-test run on a machine without it.
func liveForTest(t *testing.T) Live[tick] {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("no Redis at %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = rdb.Close() })
	return NewLiveRedis[tick](rdb, LiveConfig{Prefix: "test:live"})
}

// Broadcast is the property that rules out consumer groups: every open browser
// tab must see every event, not one tab per event.
func TestLiveBroadcastsToEverySubscriber(t *testing.T) {
	live := liveForTest(t)

	a, stopA := live.Subscribe("scan-1")
	defer stopA()
	b, stopB := live.Subscribe("scan-1")
	defer stopB()
	other, stopOther := live.Subscribe("scan-2")
	defer stopOther()

	// Redis subscriptions are established asynchronously.
	time.Sleep(150 * time.Millisecond)
	live.Publish("scan-1", tick{Found: 7})

	for name, ch := range map[string]<-chan tick{"a": a, "b": b} {
		select {
		case ev := <-ch:
			if ev.Found != 7 {
				t.Errorf("%s got %+v", name, ev)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("subscriber %s received nothing", name)
		}
	}

	select {
	case ev := <-other:
		t.Errorf("a subscriber to another key received %+v", ev)
	case <-time.After(200 * time.Millisecond):
	}
}

// Unsubscribing must close the channel, or every finished stream leaks a
// goroutine parked on a receive.
func TestLiveUnsubscribeClosesTheChannel(t *testing.T) {
	live := liveForTest(t)
	ch, stop := live.Subscribe("scan-3")
	time.Sleep(100 * time.Millisecond)
	stop()
	stop() // must be safe twice: handlers defer it and may also call it

	select {
	case _, more := <-ch:
		if more {
			t.Error("channel delivered an event after unsubscribe")
		}
	case <-time.After(2 * time.Second):
		t.Error("channel was never closed")
	}
}

// A reader that stops reading must not be able to stall the work producing the
// events — losing progress ticks is cheap, blocking a scan is not.
func TestLiveDropsForASlowSubscriber(t *testing.T) {
	live := liveForTest(t)
	_, stop := live.Subscribe("scan-4")
	defer stop()
	time.Sleep(100 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ { // far beyond the 32-deep buffer
			live.Publish("scan-4", tick{Found: i})
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publishing blocked on a subscriber that stopped reading")
	}
}
