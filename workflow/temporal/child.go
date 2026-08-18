package temporal

import (
	"fmt"

	"go.temporal.io/api/enums/v1"
	temporalwf "go.temporal.io/sdk/workflow"

	"github.com/neokarl/sdk-go/workflow"
)

// RunChild runs a Job as a child of a hand-written workflow, and blocks until it
// finishes.
//
// This is what keeps a job started on its own and a job started as one step of a
// larger workflow *the same execution*: same retries, same heartbeats, same
// terminal record, separately visible and cancellable. A parent that started the
// work some other way would quietly have different behaviour on every one of
// those.
//
// It takes workflow.Job rather than a child-specific type, so there is one
// description of a unit of work no matter who starts it.
func RunChild(ctx temporalwf.Context, j workflow.Job) error {
	if j.ID == "" || j.Handler == "" {
		return fmt.Errorf("temporal: child job needs an ID and a Handler")
	}
	opts := temporalwf.ChildWorkflowOptions{WorkflowID: j.ID}
	// Empty inherits the parent's queue, which is the sensible default: a step
	// that doesn't ask to run elsewhere runs where its parent runs.
	if j.Queue != "" {
		opts.TaskQueue = j.Queue
	}
	child := temporalwf.WithChildOptions(ctx, opts)
	return temporalwf.ExecuteChildWorkflow(child, jobWorkflow, inputFor(j)).Get(child, nil)
}

// statusOf maps Temporal's execution status onto the engine-neutral one.
func statusOf(s enums.WorkflowExecutionStatus) workflow.Status {
	switch s {
	case enums.WORKFLOW_EXECUTION_STATUS_RUNNING, enums.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW:
		return workflow.StatusRunning
	case enums.WORKFLOW_EXECUTION_STATUS_COMPLETED:
		return workflow.StatusSucceeded
	case enums.WORKFLOW_EXECUTION_STATUS_CANCELED, enums.WORKFLOW_EXECUTION_STATUS_TERMINATED:
		return workflow.StatusCancelled
	case enums.WORKFLOW_EXECUTION_STATUS_FAILED, enums.WORKFLOW_EXECUTION_STATUS_TIMED_OUT:
		return workflow.StatusFailed
	default:
		return workflow.StatusUnknown
	}
}
