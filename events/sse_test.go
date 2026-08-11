package events

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type tick struct {
	Found int  `json:"found"`
	Done  bool `json:"done"`
}

func TestSSEStreamsUntilFinal(t *testing.T) {
	ch := make(chan tick, 3)
	ch <- tick{Found: 1}
	ch <- tick{Found: 2, Done: true}
	ch <- tick{Found: 3} // must never be sent — the stream ended before it
	close(ch)

	rec := httptest.NewRecorder()
	err := SSE[tick]{IsFinal: func(ev tick) bool { return ev.Done }}.
		Serve(context.Background(), rec, ch)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}

	body := rec.Body.String()
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q", ct)
	}
	// The connection is primed so the client's onopen fires immediately.
	if !strings.HasPrefix(body, ": connected\n\n") {
		t.Errorf("stream did not open with a prime: %q", body)
	}
	if !strings.Contains(body, `data: {"found":1,"done":false}`) {
		t.Errorf("first event missing: %q", body)
	}
	if strings.Contains(body, `"found":3`) {
		t.Error("events kept flowing after the final one")
	}
}

func TestSSESendsInitialSnapshot(t *testing.T) {
	ch := make(chan tick)
	close(ch)

	rec := httptest.NewRecorder()
	_ = SSE[tick]{Initial: func() (tick, bool) { return tick{Found: 42}, true }}.
		Serve(context.Background(), rec, ch)

	if !strings.Contains(rec.Body.String(), `"found":42`) {
		t.Errorf("snapshot was not sent: %q", rec.Body.String())
	}
}

// A snapshot of already-finished work must end the stream immediately —
// otherwise a client opening a stream for a completed job hangs until timeout.
func TestSSEInitialCanBeFinal(t *testing.T) {
	ch := make(chan tick) // never closed, never written
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = SSE[tick]{
			Initial: func() (tick, bool) { return tick{Found: 7, Done: true}, true },
			IsFinal: func(ev tick) bool { return ev.Done },
		}.Serve(context.Background(), httptest.NewRecorder(), ch)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a stream opened on finished work never returned")
	}
}

// Proxies drop connections that go quiet, and a scan can legitimately produce
// nothing for minutes — this is the fix neither plugin had.
func TestSSEKeepAlive(t *testing.T) {
	ch := make(chan tick)
	ctx, cancel := context.WithCancel(context.Background())
	rec := &syncRecorder{ResponseRecorder: httptest.NewRecorder()}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = SSE[tick]{KeepAlive: 20 * time.Millisecond}.Serve(ctx, rec, ch)
	}()

	deadline := time.After(2 * time.Second)
	for {
		if strings.Contains(rec.body(), ": keepalive") {
			break
		}
		select {
		case <-deadline:
			t.Fatal("no keepalive was sent on an idle stream")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	cancel()
	<-done
}

func TestSSERequiresAFlushableWriter(t *testing.T) {
	ch := make(chan tick)
	close(ch)
	if err := (SSE[tick]{}).Serve(context.Background(), unflushable{}, ch); err == nil {
		t.Error("a writer that cannot flush would buffer the whole stream; that must be an error")
	}
}

type unflushable struct{}

func (unflushable) Header() http.Header       { return http.Header{} }
func (unflushable) Write([]byte) (int, error) { return 0, nil }
func (unflushable) WriteHeader(int)           {}

// syncRecorder guards the recorder's buffer, which the server goroutine writes
// while the test reads.
type syncRecorder struct {
	*httptest.ResponseRecorder
	mu sync.Mutex
}

func (r *syncRecorder) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ResponseRecorder.Write(b)
}

func (r *syncRecorder) body() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ResponseRecorder.Body.String()
}
