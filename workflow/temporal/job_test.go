package temporal

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"

	"platform/sdk/workflow"
)

type scanJob struct {
	ScanID string
	Tool   string
}

// The job workflow's contract: run the handler, and if it ends badly leave a
// terminal record behind. Everything below is one of those two halves.
func TestJobWorkflow(t *testing.T) {
	const handler = "webtools.RunTool"
	failure := workflow.FailureName(handler)

	t.Run("success runs the handler and no failure path", func(t *testing.T) {
		var suite testsuite.WorkflowTestSuite
		env := suite.NewTestWorkflowEnvironment()

		var got scanJob
		env.RegisterActivityWithOptions(
			func(_ context.Context, j scanJob) error { got = j; return nil },
			activity.RegisterOptions{Name: handler},
		)
		var failed bool
		env.RegisterActivityWithOptions(
			func(context.Context, scanJob, string) error { failed = true; return nil },
			activity.RegisterOptions{Name: failure},
		)

		env.ExecuteWorkflow(jobWorkflow, inputFor(workflow.Job{
			ID: "scan-1", Handler: handler, Payload: scanJob{ScanID: "s1", Tool: "httpx"},
		}))

		if err := env.GetWorkflowError(); err != nil {
			t.Fatalf("workflow error: %v", err)
		}
		if got.ScanID != "s1" || got.Tool != "httpx" {
			t.Errorf("payload did not arrive typed: %+v", got)
		}
		if failed {
			t.Error("the failure path ran for a successful job")
		}
	})

	// The reason OnFail exists: once retries are exhausted no service code is
	// running, so without this a domain record says "running" forever.
	t.Run("permanent failure records a terminal outcome", func(t *testing.T) {
		var suite testsuite.WorkflowTestSuite
		env := suite.NewTestWorkflowEnvironment()

		env.RegisterActivityWithOptions(
			func(context.Context, scanJob) error { return errors.New("tool exploded") },
			activity.RegisterOptions{Name: handler},
		)
		var reason string
		var attempts int
		env.RegisterActivityWithOptions(
			func(_ context.Context, _ scanJob, r string) error { attempts++; reason = r; return nil },
			activity.RegisterOptions{Name: failure},
		)

		env.ExecuteWorkflow(jobWorkflow, inputFor(workflow.Job{
			ID: "scan-2", Handler: handler, Payload: scanJob{ScanID: "s2"},
			Retry: workflow.RetryQuick,
		}))

		if env.GetWorkflowError() == nil {
			t.Fatal("a failed job must fail its workflow")
		}
		if attempts != 1 {
			t.Fatalf("failure path ran %d times, want exactly 1", attempts)
		}
		if reason == "" {
			t.Error("the failure path was told nothing about why")
		}
	})

	// A job with no OnFail of its own still resolves: workflow.Handle always
	// registers a counterpart, so the workflow can invoke one by derived name
	// without knowing what the service supplied.
	t.Run("input always names a failure handler", func(t *testing.T) {
		in := inputFor(workflow.Job{ID: "x", Handler: handler})
		if in.OnFail != failure {
			t.Errorf("OnFail = %q, want %q", in.OnFail, failure)
		}
	})
}

// The heartbeat wrapper is reflection over a function whose payload type only
// the service knows. Temporal validates an activity by its reflect.Type, so the
// wrapper must be indistinguishable from what the service wrote — and it must
// still deliver the payload.
func TestWithBeaterPreservesTheHandler(t *testing.T) {
	var seen scanJob
	original := func(_ context.Context, j scanJob) error { seen = j; return nil }

	wrapped, ok := withBeater(original).(func(context.Context, scanJob) error)
	if !ok {
		t.Fatalf("wrapping changed the signature: %T", withBeater(original))
	}
	if err := wrapped(context.Background(), scanJob{ScanID: "s3", Tool: "nuclei"}); err != nil {
		t.Fatalf("wrapped handler: %v", err)
	}
	if seen.ScanID != "s3" || seen.Tool != "nuclei" {
		t.Errorf("payload did not survive the wrapper: %+v", seen)
	}
}

// A registered handler must reach Temporal as a working activity — the whole
// point of the reflection above. Running it through the test environment proves
// registration and invocation, which a direct call cannot.
func TestRegisteredHandlerRunsAsAnActivity(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()

	var seen scanJob
	env.RegisterActivityWithOptions(
		withBeater(func(_ context.Context, j scanJob) error { seen = j; return nil }),
		activity.RegisterOptions{Name: "webtools.RunTool"},
	)

	if _, err := env.ExecuteActivity("webtools.RunTool", scanJob{ScanID: "s4", Tool: "katana"}); err != nil {
		t.Fatalf("activity: %v", err)
	}
	if seen.ScanID != "s4" || seen.Tool != "katana" {
		t.Errorf("payload did not arrive: %+v", seen)
	}
}

func TestRetryPresetsTranslate(t *testing.T) {
	expensive := retryPolicy(workflow.RetryExpensive)
	if expensive.MaximumAttempts != 3 {
		t.Errorf("expensive attempts = %d, want 3", expensive.MaximumAttempts)
	}
	// Unbounded is the whole point of the forever preset; a nonzero cap here
	// would silently give up on a poll that was meant to wait.
	if forever := retryPolicy(workflow.RetryForever); forever.MaximumAttempts != 0 {
		t.Errorf("forever attempts = %d, want 0 (unlimited)", forever.MaximumAttempts)
	}
	// The zero value must be safe, not unlimited.
	if zero := retryPolicy(""); zero.MaximumAttempts != 3 {
		t.Errorf("zero-value preset attempts = %d, want 3", zero.MaximumAttempts)
	}
}

func TestBuildIDFallsBackToRevision(t *testing.T) {
	if got := buildID("  v2.1.0  "); got != "v2.1.0" {
		t.Errorf("configured build id = %q, want it trimmed", got)
	}
	if buildID("") == "" {
		t.Error("build id must never be empty — an unnamed worker is untraceable")
	}
}

// Timeout arithmetic is load-bearing: too tight and the engine kills work that
// was about to stop cleanly on its own deadline.
func TestJobTimeoutLeavesSlack(t *testing.T) {
	in := inputFor(workflow.Job{ID: "x", Handler: "h", Timeout: 5 * time.Minute})
	if in.Timeout != 5*time.Minute {
		t.Fatalf("timeout = %v", in.Timeout)
	}
	if slack < time.Minute {
		t.Errorf("slack of %v is too tight for setup and a final flush", slack)
	}
}
