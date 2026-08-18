// job.go — how a workflow.Job becomes a Temporal workflow.
//
// The workflow is deliberately thin: one activity does the work, and the
// workflow exists to make that activity survive a restart, to give every job an
// addressable and cancellable execution, and to guarantee a terminal record.
// Everything expensive happens in the activity.

package temporal

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	temporalwf "go.temporal.io/sdk/workflow"

	"go.neokarl.com/sdk/workflow"
)

// slack is added to a job's own timeout before the engine gives up on the
// attempt, leaving room for setup and for the handler to stop cleanly on its own
// deadline rather than being cut off mid-write.
const slack = 2 * time.Minute

// defaultTimeout bounds a job that didn't ask for one.
const defaultTimeout = 10 * time.Minute

// jobInput is what the workflow carries. Ids and parameters only: workflow
// history has hard size limits, so results belong in the service's own store.
type jobInput struct {
	Handler string
	OnFail  string
	Payload any
	Timeout time.Duration
	Retry   workflow.RetryPreset
}

// jobWorkflow runs one job.
func jobWorkflow(ctx temporalwf.Context, in jobInput) error {
	timeout := in.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	// TaskQueue is deliberately left unset so the activity inherits the
	// workflow's queue. Pinning it here would send the actual work back to the
	// default pool even when a job was routed elsewhere — routing would appear
	// to work while the work happened on the wrong machine.
	opts := temporalwf.ActivityOptions{
		StartToCloseTimeout: timeout + slack,
		HeartbeatTimeout:    workflow.HeartbeatTimeout,
		RetryPolicy:         retryPolicy(in.Retry),
	}
	err := temporalwf.ExecuteActivity(temporalwf.WithActivityOptions(ctx, opts), in.Handler, in.Payload).Get(ctx, nil)
	if err == nil {
		return nil
	}

	// The job is over and it did not succeed, so leave a terminal record behind:
	// nothing else is running that knows this was the last attempt.
	//
	// Disconnected so this still runs when the cause was cancellation, which
	// would otherwise cancel this activity too.
	if in.OnFail != "" {
		cleanupCtx, cancel := temporalwf.NewDisconnectedContext(ctx)
		defer cancel()
		cleanup := temporalwf.WithActivityOptions(cleanupCtx, temporalwf.ActivityOptions{
			StartToCloseTimeout: 30 * time.Second,
			RetryPolicy:         retryPolicy(workflow.RetryQuick),
		})
		_ = temporalwf.ExecuteActivity(cleanup, in.OnFail, in.Payload, err.Error()).Get(cleanup, nil)
	}
	return err
}

// retryPolicy translates a preset into Temporal's policy.
func retryPolicy(p workflow.RetryPreset) *temporal.RetryPolicy {
	rp := p.Policy()
	return &temporal.RetryPolicy{
		InitialInterval:    rp.Initial,
		MaximumInterval:    rp.Max,
		BackoffCoefficient: rp.Multiplier,
		MaximumAttempts:    int32(rp.MaxAttempts),
	}
}

// RegisterJobWorkflow registers the workflow a Job runs as.
//
// New does this for every worker it builds, so a service normally never calls
// it. It is exported for the case that matters: testing a hand-written parent
// workflow that runs jobs as children, where the test environment needs the
// child registered too.
//
//	env := suite.NewTestWorkflowEnvironment()
//	wftemporal.RegisterJobWorkflow(env)
func RegisterJobWorkflow(r worker.WorkflowRegistry) { r.RegisterWorkflow(jobWorkflow) }

// --- workflow.Engine ---

// Submit starts a job. The job id is the workflow id, so submitting the same job
// twice is refused by Temporal rather than running the work twice.
func (e *Engine) Submit(ctx context.Context, j workflow.Job) error {
	if j.ID == "" || j.Handler == "" {
		return fmt.Errorf("temporal: job needs an ID and a Handler")
	}
	queue := j.Queue
	if queue == "" {
		queue = e.defaultQueue()
	}
	if queue == "" {
		return fmt.Errorf("%w: job %q names no queue and no default is configured", ErrNoQueue, j.ID)
	}
	_, err := e.client.ExecuteWorkflow(ctx,
		client.StartWorkflowOptions{ID: j.ID, TaskQueue: queue},
		jobWorkflow, inputFor(j))
	if err != nil {
		return fmt.Errorf("temporal: submit %q: %w", j.ID, err)
	}
	return nil
}

// Cancel asks a running job to stop; its OnFail still runs.
func (e *Engine) Cancel(ctx context.Context, id string) error {
	if err := e.client.CancelWorkflow(ctx, id, ""); err != nil {
		return fmt.Errorf("temporal: cancel %q: %w", id, err)
	}
	return nil
}

// Status reports where a job has got to. An id Temporal has never seen — or has
// already aged out of retention — reports StatusUnknown rather than an error.
func (e *Engine) Status(ctx context.Context, id string) (workflow.Status, error) {
	desc, err := e.client.DescribeWorkflowExecution(ctx, id, "")
	if err != nil {
		return workflow.StatusUnknown, nil
	}
	return statusOf(desc.GetWorkflowExecutionInfo().GetStatus()), nil
}

// RegisterFunc registers a job handler on every queue this process serves.
//
// It wraps the function so the handler's context carries a beater, which is what
// makes workflow.Handle's heartbeat timer live: only this package knows how to
// report liveness to Temporal, and only the root package knows the timer policy.
// Reflection is the price of keeping the payload type in the service's hands.
func (e *Engine) RegisterFunc(name string, fn any) {
	e.register(name, withBeater(fn))
}

// RegisterFailureFunc registers the counterpart run when a job ends badly. It
// gets no beater: it is a short, bounded write.
func (e *Engine) RegisterFailureFunc(name string, fn any) { e.register(name, fn) }

func (e *Engine) register(name string, fn any) {
	for _, w := range e.workers {
		w.RegisterActivityWithOptions(fn, activity.RegisterOptions{Name: name})
	}
}

func (e *Engine) defaultQueue() string {
	if e.cfg.DefaultQueue != "" {
		return e.cfg.DefaultQueue
	}
	if len(e.cfg.Queues) > 0 {
		return e.cfg.Queues[0]
	}
	return ""
}

func inputFor(j workflow.Job) jobInput {
	return jobInput{
		Handler: j.Handler,
		OnFail:  workflow.FailureName(j.Handler),
		Payload: j.Payload,
		Timeout: j.Timeout,
		Retry:   j.Retry,
	}
}

// withBeater returns fn with the same signature, but with the activity's
// heartbeat wired into the context the handler receives.
func withBeater(fn any) any {
	v := reflect.ValueOf(fn)
	if v.Kind() != reflect.Func || v.Type().NumIn() == 0 {
		return fn
	}
	return reflect.MakeFunc(v.Type(), func(args []reflect.Value) []reflect.Value {
		ctx, ok := args[0].Interface().(context.Context)
		if !ok {
			return v.Call(args)
		}
		beating := workflow.WithBeater(ctx, func(p any) { activity.RecordHeartbeat(ctx, p) })
		args[0] = reflect.ValueOf(beating)
		return v.Call(args)
	}).Interface()
}

// ErrNoQueue means a job could not be routed: it named no queue and the engine
// has no default. Test for it with errors.Is.
var ErrNoQueue = errors.New("temporal: job has no queue")
