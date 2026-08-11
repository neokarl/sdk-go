package workflow

import (
	"context"
	"errors"
	"testing"
)

// fakeEngine records what Handle registers, which is the only externally visible
// effect the root package has without an engine behind it.
type fakeEngine struct {
	handlers map[string]any
	failures map[string]any
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{handlers: map[string]any{}, failures: map[string]any{}}
}

func (f *fakeEngine) Submit(context.Context, Job) error    { return nil }
func (f *fakeEngine) Cancel(context.Context, string) error { return nil }
func (f *fakeEngine) Status(context.Context, string) (Status, error) {
	return StatusUnknown, nil
}
func (f *fakeEngine) Queues() []string                        { return nil }
func (f *fakeEngine) RegisterFunc(name string, fn any)        { f.handlers[name] = fn }
func (f *fakeEngine) RegisterFailureFunc(name string, fn any) { f.failures[name] = fn }
func (f *fakeEngine) Start() error                            { return nil }
func (f *fakeEngine) Stop()                                   {}

type payload struct{ ID string }

// A failure counterpart is registered even when the service supplies none, so an
// engine can always invoke one by derived name. Deciding that at submit time
// instead would make a running workflow depend on what the process that started
// it happened to register — the classic replay hazard.
func TestHandleAlwaysRegistersAFailurePath(t *testing.T) {
	t.Run("with OnFail", func(t *testing.T) {
		e := newFakeEngine()
		Handle(e, "svc.Work", Handler[payload]{
			Run:    func(context.Context, payload, func(any)) error { return nil },
			OnFail: func(context.Context, payload, string) error { return nil },
		})
		if _, ok := e.handlers["svc.Work"]; !ok {
			t.Error("handler was not registered")
		}
		if _, ok := e.failures[FailureName("svc.Work")]; !ok {
			t.Error("failure path was not registered")
		}
	})

	t.Run("without OnFail", func(t *testing.T) {
		e := newFakeEngine()
		Handle(e, "svc.Work", Handler[payload]{
			Run: func(context.Context, payload, func(any)) error { return nil },
		})
		fn, ok := e.failures[FailureName("svc.Work")]
		if !ok {
			t.Fatal("a no-op failure path must still be registered")
		}
		call, ok := fn.(func(context.Context, payload, string) error)
		if !ok {
			t.Fatalf("failure path has the wrong signature: %T", fn)
		}
		if err := call(context.Background(), payload{}, "because"); err != nil {
			t.Errorf("the default failure path should do nothing quietly: %v", err)
		}
	})
}

// The registered function must keep the service's payload type: that is what
// lets an engine hand back decoded data instead of a map.
func TestHandleKeepsThePayloadTyped(t *testing.T) {
	e := newFakeEngine()
	var got payload
	Handle(e, "svc.Work", Handler[payload]{
		Run: func(_ context.Context, p payload, _ func(any)) error { got = p; return nil },
	})

	run, ok := e.handlers["svc.Work"].(func(context.Context, payload) error)
	if !ok {
		t.Fatalf("registered function has the wrong signature: %T", e.handlers["svc.Work"])
	}
	if err := run(context.Background(), payload{ID: "abc"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got.ID != "abc" {
		t.Errorf("payload = %+v, want ID abc", got)
	}
}

// A handler must be callable with no engine behind it — otherwise every service
// test would need a Temporal environment just to exercise its own logic.
func TestHandlerRunsWithoutAnEngine(t *testing.T) {
	e := newFakeEngine()
	sentinel := errors.New("domain failure")
	Handle(e, "svc.Work", Handler[payload]{
		Run: func(_ context.Context, _ payload, heartbeat func(any)) error {
			heartbeat("still going") // must not panic with no beater in context
			return sentinel
		},
	})

	run := e.handlers["svc.Work"].(func(context.Context, payload) error)
	if err := run(context.Background(), payload{}); !errors.Is(err, sentinel) {
		t.Errorf("error = %v, want the handler's own", err)
	}
}

func TestHandleRejectsAHandlerWithNoRun(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("registering a handler with no Run should fail loudly at startup")
		}
	}()
	Handle(newFakeEngine(), "svc.Work", Handler[payload]{})
}

func TestStatusTerminal(t *testing.T) {
	for _, s := range []Status{StatusSucceeded, StatusFailed, StatusCancelled} {
		if !s.Terminal() {
			t.Errorf("%q should be terminal", s)
		}
	}
	for _, s := range []Status{StatusRunning, StatusUnknown} {
		if s.Terminal() {
			t.Errorf("%q should not be terminal", s)
		}
	}
}
