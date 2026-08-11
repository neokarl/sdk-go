// Package temporal runs platform/sdk/workflow jobs on Temporal.
//
// Import it in one place — the composition root — to construct the engine, and
// again wherever you author a workflow by hand. Everywhere else, code against
// platform/sdk/workflow and stay engine-neutral.
//
// The escape hatches (Client, Worker, Dial) return upstream Temporal types
// deliberately: anything the Temporal Go SDK can do is still available, and the
// receiver's type is what tells a reader they have left the abstraction. Work
// authored that way is outside the SDK's guarantees — no heartbeat wrapper, no
// OnFail — and its history is Temporal-shaped for good.
//
// Environment:
//
//	TEMPORAL_ADDRESS    host:port of the frontend (default 127.0.0.1:7233)
//	TEMPORAL_NAMESPACE  namespace (default "default")
//	WORKER_QUEUES       comma-separated pools this process serves
//	WORKER_BUILD_ID     what this build calls itself (default: the vcs revision)
//	WORKER_VERSIONING   enforce that build id — off by default, see Config
package temporal

import (
	"context"
	"fmt"
	"os"
	"runtime/debug"
	"strings"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"platform/sdk/workflow"
)

// Config describes the connection and this process's worker role.
type Config struct {
	// HostPort and Namespace address the Temporal frontend.
	HostPort  string
	Namespace string
	// Queues are the worker pools this process serves. Empty means submit-only:
	// the service can start and track jobs but executes none. It is *exactly*
	// these — a worker dedicated to a pool must not also drain the default one,
	// or routing a job to it would mean nothing.
	Queues []string
	// DefaultQueue is where a Job with no Queue of its own runs. Defaults to the
	// first served queue. Set it explicitly when a process serves a dedicated
	// pool but should still submit general work to the shared one.
	DefaultQueue string
	// BuildID is what this build announces itself as, so after a deploy you can
	// tell which build executed a task. Defaults to the vcs revision.
	BuildID string
	// Versioning enforces BuildID: a worker then only receives work for build
	// ids the task queue has been told about.
	//
	// OFF by default, and deliberately so. A versioned worker on a namespace
	// with versioning disabled — the server default — fails every poll with
	// "Worker versioning is disabled on this namespace" and silently does no
	// work at all. Register the id on the queue first, then enable this.
	//
	// This uses the legacy Build ID API rather than Worker Deployment
	// Versioning, which requires a Temporal server >= 1.27.
	Versioning bool
	// TuneWorker adjusts worker options after the defaults are applied — the
	// package constructs the workers, so this is how a service sets e.g.
	// MaxConcurrentActivityExecutionSize without widening the surface.
	TuneWorker func(*worker.Options)
}

// ConfigFromEnv reads the connection and worker settings from the environment.
func ConfigFromEnv() Config {
	return Config{
		HostPort:   envOr("TEMPORAL_ADDRESS", client.DefaultHostPort),
		Namespace:  envOr("TEMPORAL_NAMESPACE", client.DefaultNamespace),
		Queues:     splitList(os.Getenv("WORKER_QUEUES")),
		BuildID:    buildID(os.Getenv("WORKER_BUILD_ID")),
		Versioning: isTrue(os.Getenv("WORKER_VERSIONING")),
	}
}

// Engine is a workflow.Engine backed by Temporal.
type Engine struct {
	client  client.Client
	cfg     Config
	workers map[string]worker.Worker
	started bool
}

// Compile-time proof that the engine satisfies the abstraction.
var _ workflow.Engine = (*Engine)(nil)

// New dials Temporal and prepares a worker per configured queue. Workers do not
// poll until Start.
//
// It returns the concrete type rather than the interface so the escape hatches
// need no type assertion: hold *Engine in the composition root and pass it as
// workflow.Engine into domain code.
func New(ctx context.Context, cfg Config) (*Engine, error) {
	c, err := Dial(ctx, cfg)
	if err != nil {
		return nil, err
	}
	e := &Engine{client: c, cfg: cfg, workers: map[string]worker.Worker{}}

	opts := worker.Options{
		BuildID:                 cfg.BuildID,
		UseBuildIDForVersioning: cfg.Versioning,
	}
	if cfg.TuneWorker != nil {
		cfg.TuneWorker(&opts)
	}
	for _, q := range cfg.Queues {
		w := worker.New(c, q, opts)
		RegisterJobWorkflow(w)
		e.workers[q] = w
	}
	return e, nil
}

// Dial opens a Temporal client. The caller owns closing it.
func Dial(ctx context.Context, cfg Config) (client.Client, error) {
	c, err := client.DialContext(ctx, client.Options{
		HostPort:  cfg.HostPort,
		Namespace: cfg.Namespace,
	})
	if err != nil {
		return nil, fmt.Errorf("temporal: dial %s: %w", cfg.HostPort, err)
	}
	return c, nil
}

// NewWorker builds a worker on a queue against an existing client, for a caller
// that manages its own lifecycle rather than using an Engine.
func NewWorker(c client.Client, queue string) worker.Worker {
	return worker.New(c, queue, worker.Options{})
}

// Client returns the Temporal client — the escape hatch for everything this
// package does not wrap: signals, queries, schedules, custom start options.
func (e *Engine) Client() client.Client { return e.client }

// Worker returns the worker serving a queue, for registering hand-written
// workflows and activities. Nil if the queue is not in Config.Queues.
func (e *Engine) Worker(queue string) worker.Worker { return e.workers[queue] }

// Queues are the pools this process serves.
func (e *Engine) Queues() []string { return append([]string(nil), e.cfg.Queues...) }

// BuildID is what this process announces itself as.
func (e *Engine) BuildID() string { return e.cfg.BuildID }

// Start begins polling on every configured queue. If one fails to start, those
// already started are stopped, so a partial start never leaves half a process
// serving work.
func (e *Engine) Start() error {
	if e.started {
		return nil
	}
	started := make([]worker.Worker, 0, len(e.workers))
	for q, w := range e.workers {
		if err := w.Start(); err != nil {
			for _, s := range started {
				s.Stop()
			}
			return fmt.Errorf("temporal: start worker on %q: %w", q, err)
		}
		started = append(started, w)
	}
	e.started = true
	return nil
}

// Stop halts the workers and closes the client.
func (e *Engine) Stop() {
	for _, w := range e.workers {
		w.Stop()
	}
	e.started = false
	if e.client != nil {
		e.client.Close()
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func splitList(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func isTrue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// buildID resolves what this build calls itself: the supplied value, else the
// revision it was built from, so an unconfigured deployment still reports
// something true rather than nothing.
func buildID(configured string) string {
	if v := strings.TrimSpace(configured); v != "" {
		return v
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	var rev string
	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return "dev"
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if dirty {
		rev += "-dirty"
	}
	return rev
}
