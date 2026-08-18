package workflow

import "time"

// RetryPreset says how hard the engine should try again, without naming an
// engine's own policy type.
//
// Presets rather than a free-form policy because the choice is a judgement about
// the *work*, not a tuning knob: re-running something expensive costs real time
// and money, re-running a database write costs nothing. Services that tried to
// pick numbers directly ended up with four incompatible policies across two
// codebases, agreeing on nothing.
type RetryPreset string

const (
	// RetryQuick suits short, cheap, idempotent steps — a status write, a
	// record update. The zero value means this.
	RetryQuick RetryPreset = "quick"
	// RetryExpensive suits work a retry re-runs from the beginning: a bulk import, a
	// crawl, anything measured in minutes. Few attempts, generous backoff.
	RetryExpensive RetryPreset = "expensive"
	// RetryForever suits polling something that will eventually answer, where
	// giving up is worse than waiting. Bounded only by the job's own timeout.
	RetryForever RetryPreset = "forever"
)

// RetryPolicy is a preset in engine-neutral terms; engines translate it.
type RetryPolicy struct {
	// MaxAttempts caps tries. Zero means unlimited.
	MaxAttempts int
	// Initial is the first backoff interval.
	Initial time.Duration
	// Max caps the backoff interval.
	Max time.Duration
	// Multiplier grows the interval between attempts.
	Multiplier float64
}

// Policy resolves a preset. An unrecognised preset — including the zero value —
// resolves to RetryQuick, so a job is never accidentally unbounded.
func (p RetryPreset) Policy() RetryPolicy {
	switch p {
	case RetryExpensive:
		return RetryPolicy{MaxAttempts: 3, Initial: 5 * time.Second, Max: time.Minute, Multiplier: 2}
	case RetryForever:
		return RetryPolicy{MaxAttempts: 0, Initial: 2 * time.Second, Max: time.Minute, Multiplier: 2}
	default:
		return RetryPolicy{MaxAttempts: 3, Initial: time.Second, Max: 30 * time.Second, Multiplier: 2}
	}
}
