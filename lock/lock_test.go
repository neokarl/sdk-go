package lock

import (
	"context"
	"sync"
	"testing"
)

// TestInProcessSerializes verifies the in-process lock actually serializes work on
// the same key (no lost updates under contention).
func TestInProcessSerializes(t *testing.T) {
	l := NewInProcess()
	ctx := context.Background()
	const goroutines, iters = 50, 200
	counter := 0
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				release, err := l.Acquire(ctx, "k")
				if err != nil {
					t.Errorf("acquire: %v", err)
					return
				}
				counter++ // protected by the lock
				release()
			}
		}()
	}
	wg.Wait()
	if counter != goroutines*iters {
		t.Errorf("counter = %d, want %d (lost updates → not serialized)", counter, goroutines*iters)
	}
}

// TestInProcessDistinctKeysIndependent ensures different keys don't block each other.
func TestInProcessDistinctKeysIndependent(t *testing.T) {
	l := NewInProcess()
	ctx := context.Background()
	relA, _ := l.Acquire(ctx, "a")
	defer relA()
	// A different key must be acquirable without releasing "a".
	done := make(chan struct{})
	go func() {
		relB, _ := l.Acquire(ctx, "b")
		relB()
		close(done)
	}()
	<-done // would deadlock/hang if distinct keys shared a lock
}
