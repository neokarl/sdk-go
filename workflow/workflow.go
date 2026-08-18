// Package workflow runs work that must survive a restart.
//
// A service submits a Job — an id, a registered handler name, a payload, a time
// budget and a retry policy — and the engine guarantees the handler runs to a
// terminal outcome even if the process executing it dies. Nothing here mentions a
// particular engine: this package deliberately imports no engine SDK, so service
// code written against it stays portable.
//
// Two halves, deliberately different:
//
//   - Running a job (this package) generalizes across engines. Submit, Cancel,
//     Status, and a typed handler are all an ordinary service needs.
//   - Authoring a workflow does not. Engines differ in determinism rules, replay,
//     history limits, versioning, signals and child workflows, so that surface
//     lives in the engine subpackage (go.neokarl.com/sdk/workflow/temporal) which you
//     import on purpose. It is a one-way door: a workflow authored there has an
//     engine-shaped history forever.
//
// For events emitted *by* a job — progress, lifecycle — see go.neokarl.com/sdk/events.
// This package publishes nothing itself; only the service knows what its statuses
// mean.
package workflow

import (
	"context"
	"time"
)

// Status is where a submitted job has got to.
type Status string

const (
	// StatusUnknown means the engine has no record of the job — it was never
	// submitted, or its history has been purged.
	StatusUnknown Status = "unknown"
	// StatusRunning means the job is in flight, including waiting to be retried.
	StatusRunning Status = "running"
	// StatusSucceeded means the handler returned nil.
	StatusSucceeded Status = "succeeded"
	// StatusFailed means the handler exhausted its retries.
	StatusFailed Status = "failed"
	// StatusCancelled means the job was cancelled before finishing.
	StatusCancelled Status = "cancelled"
)

// Terminal reports whether the job has stopped for good.
func (s Status) Terminal() bool {
	return s == StatusSucceeded || s == StatusFailed || s == StatusCancelled
}

// Job is one unit of durable work.
//
// Payload carries the service's own type; the engine serializes it and hands it
// back to the handler decoded. Keep it small — ids and parameters, never results.
// Engines cap the size of a job's history, and a handler that discovers 50,000
// rows must write them to its own store, not carry them here.
type Job struct {
	// ID deduplicates: submitting the same id twice is refused rather than
	// running the work twice. Derive it from the domain id it represents.
	ID string
	// Queue names the worker pool that should run this job. Empty means the
	// engine's default pool. This is how a job runs on a particular machine.
	Queue string
	// Handler is the name registered with Handle.
	Handler string
	// Payload is passed to the handler.
	Payload any
	// Timeout bounds a single attempt. Zero means the engine's default.
	Timeout time.Duration
	// Retry selects how hard the engine tries again. Zero value is RetryQuick.
	Retry RetryPreset
}

// Handler is what a job runs, plus what to do when it ends badly.
//
// Run receives the decoded payload and a heartbeat func. Call heartbeat
// periodically during long work: it is how the engine distinguishes a busy
// worker from a dead one. The engine calls it on a timer for you, so passing
// progress is optional and only useful to a human watching.
//
// OnFail records a terminal outcome — it does NOT undo work, so this is not a
// Saga compensation. It runs once, when the job ends without success: retries
// exhausted, or cancelled. It exists because at that moment no service code is
// running; only the engine knows the attempt was the last one. Without it, a
// domain record that says "running" stays that way forever.
type Handler[T any] struct {
	Run    func(ctx context.Context, payload T, heartbeat func(any)) error
	OnFail func(ctx context.Context, payload T, reason string) error
}

// Engine executes jobs durably.
type Engine interface {
	// Submit starts a job and returns once it is accepted, not once it finishes.
	Submit(ctx context.Context, j Job) error
	// Cancel asks a running job to stop. The job's OnFail still runs.
	Cancel(ctx context.Context, id string) error
	// Status reports a job's progress. Unknown ids yield StatusUnknown.
	Status(ctx context.Context, id string) (Status, error)
	// Queues are the worker pools this process serves; empty means submit-only.
	Queues() []string
	// RegisterFunc registers a handler function under a name. Low-level: use
	// Handle, which keeps the payload typed and adds heartbeating.
	RegisterFunc(name string, fn any)
	// RegisterFailureFunc registers the counterpart invoked when a job ends
	// without success. Low-level, as above.
	RegisterFailureFunc(name string, fn any)
	// Start begins serving Queues. Idempotent; safe to call with no queues.
	Start() error
	// Stop drains and closes. Safe to call more than once.
	Stop()
}

// Handle registers a typed handler under a name, in the shape of http.Handle.
//
// The engine cannot accept a generic function directly, so this closes over T
// and registers a concrete one — which is also where the heartbeat timer is
// added, so a handler never has to remember to prove it is alive.
func Handle[T any](e Engine, name string, h Handler[T]) {
	if h.Run == nil {
		panic("workflow: Handle requires a Run func for " + name)
	}
	e.RegisterFunc(name, func(ctx context.Context, payload T) error {
		stop := make(chan struct{})
		defer close(stop)
		beat := heartbeater(ctx, stop)
		return h.Run(ctx, payload, beat)
	})
	// The failure counterpart is registered even when the service supplied none,
	// so the engine can always invoke it by a derived name. The alternative —
	// deciding at submit time whether it exists — would make a workflow's
	// behaviour depend on what happened to be registered in the process that
	// started it, which is exactly the kind of thing that breaks on replay.
	onFail := h.OnFail
	if onFail == nil {
		onFail = func(context.Context, T, string) error { return nil }
	}
	e.RegisterFailureFunc(failureName(name), func(ctx context.Context, payload T, reason string) error {
		return onFail(ctx, payload, reason)
	})
}

// FailureName derives the registered name of a handler's OnFail counterpart.
// Engines need it to invoke the failure path by name.
func FailureName(handler string) string { return failureName(handler) }

func failureName(handler string) string { return handler + ".onfail" }
