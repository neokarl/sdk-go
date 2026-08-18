// sse.go — Server-Sent Events, the browser end of a Live stream.

package events

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// DefaultKeepAlive is how often an idle stream sends a comment line. Proxies and
// load balancers close connections that go quiet, and a long import can legitimately
// produce nothing for minutes.
const DefaultKeepAlive = 25 * time.Second

// SSE serves a channel of events to a browser as text/event-stream.
//
// Authentication and authorization are the caller's job, done before handing the
// channel over: who may read a stream is a question only the service can answer.
//
// Typical use, having already subscribed to a Live stream:
//
//	ch, unsubscribe := live.Subscribe(id)
//	defer unsubscribe()
//	return events.SSE[ScanEvent]{
//	    Initial: func() (ScanEvent, bool) { return snapshot(), true },
//	    IsFinal: func(ev ScanEvent) bool { return ev.Done },
//	}.Serve(ctx, w, ch)
type SSE[T any] struct {
	// Initial sends one event before the tail begins, so a client that connects
	// late sees current state instead of waiting for the next change. Return
	// false to send nothing.
	Initial func() (T, bool)
	// IsFinal ends the stream after an event. Without it the stream runs until
	// the client disconnects, which is right for an open-ended feed and wrong
	// for something that finishes.
	IsFinal func(T) bool
	// KeepAlive overrides DefaultKeepAlive. Negative disables it.
	KeepAlive time.Duration
}

// Serve streams events until the context is done, the channel closes, or IsFinal
// says the stream is over. It returns nil on all three: a client hanging up is
// the normal way an SSE request ends, not a failure.
func (s SSE[T]) Serve(ctx context.Context, w http.ResponseWriter, events <-chan T) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("events: SSE needs a flushable ResponseWriter, got %T", w)
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	// Nginx buffers proxied responses by default, which holds events until the
	// buffer fills — indistinguishable from a stalled job.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// Prime the connection so the client's EventSource fires onopen promptly
	// rather than after the first real event.
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	if s.Initial != nil {
		if ev, send := s.Initial(); send {
			if err := writeEvent(w, flusher, ev); err != nil {
				return nil
			}
			if s.IsFinal != nil && s.IsFinal(ev) {
				return nil
			}
		}
	}

	interval := s.KeepAlive
	if interval == 0 {
		interval = DefaultKeepAlive
	}
	var ticks <-chan time.Time
	if interval > 0 {
		t := time.NewTicker(interval)
		defer t.Stop()
		ticks = t.C
	}

	for {
		select {
		case ev, more := <-events:
			if !more {
				return nil
			}
			if err := writeEvent(w, flusher, ev); err != nil {
				return nil // the client went away
			}
			if s.IsFinal != nil && s.IsFinal(ev) {
				return nil
			}
		case <-ticks:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return nil
			}
			flusher.Flush()
		case <-ctx.Done():
			return nil
		}
	}
}

func writeEvent[T any](w http.ResponseWriter, flusher http.Flusher, ev T) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		return nil // skip an unencodable event rather than killing the stream
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}
