package errors

import (
	goerrors "errors"
	"net/http"
	"testing"
)

// Every code has to map to a status, because the status is what a caller
// actually branches on. A code that falls through to 500 is a bug that only
// shows up in production.
func TestEveryCodeMapsToItsStatus(t *testing.T) {
	want := map[Code]int{
		CodeBadRequest:     http.StatusBadRequest,
		CodeUnauthorized:   http.StatusUnauthorized,
		CodeForbidden:      http.StatusForbidden,
		CodeNotFound:       http.StatusNotFound,
		CodeConflict:       http.StatusConflict,
		CodeRateLimited:    http.StatusTooManyRequests,
		CodeInternal:       http.StatusInternalServerError,
		CodeUnavailable:    http.StatusServiceUnavailable,
		CodeNotImplemented: http.StatusNotImplemented,
	}
	for code, status := range want {
		if got := New(code, "x").Status; got != status {
			t.Errorf("New(%q).Status = %d, want %d", code, got, status)
		}
		if got := Status(code); got != status {
			t.Errorf("Status(%q) = %d, want %d", code, got, status)
		}
	}
}

// An unrecognised code must not become a 200. Failing closed matters more here
// than being clever.
func TestUnknownCodeIsInternalServerError(t *testing.T) {
	if got := New(Code("nonsense"), "x").Status; got != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", got)
	}
}

// Wrap keeps the cause reachable for logs and errors.Is, and the client-facing
// message separate from it.
func TestWrapPreservesTheCause(t *testing.T) {
	sentinel := goerrors.New("connection refused")
	err := Wrap(sentinel, CodeUnavailable, "the store is unreachable")

	if !goerrors.Is(err, sentinel) {
		t.Error("errors.Is could not see through the wrap")
	}
	if err.Message != "the store is unreachable" {
		t.Errorf("message = %q", err.Message)
	}
	if err.Status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", err.Status)
	}
}

func TestAsAndIsCode(t *testing.T) {
	err := New(CodeNotFound, "nope")

	if pe, ok := As(err); !ok || pe.Code != CodeNotFound {
		t.Errorf("As() = %+v, %v", pe, ok)
	}
	if !IsCode(err, CodeNotFound) {
		t.Error("IsCode did not match the code it carries")
	}
	if IsCode(err, CodeConflict) {
		t.Error("IsCode matched a code it does not carry")
	}
	if IsCode(goerrors.New("plain"), CodeNotFound) {
		t.Error("IsCode matched a plain error")
	}

	// A wrapped platform error is still findable, which is what lets a handler
	// return one from three layers down.
	if !IsCode(Wrap(err, CodeInternal, "outer"), CodeInternal) {
		t.Error("IsCode did not see the outer code")
	}
}

// WithDetail must copy, not mutate: a package-level sentinel error decorated
// per-request would otherwise accumulate every request's details.
func TestWithDetailDoesNotMutateTheOriginal(t *testing.T) {
	base := New(CodeBadRequest, "invalid")
	decorated := base.WithDetail("field", "name")

	if base.Details != nil {
		t.Errorf("the original grew details: %v", base.Details)
	}
	if decorated.Details["field"] != "name" {
		t.Errorf("details = %v", decorated.Details)
	}
	if second := base.WithDetail("field", "other"); second.Details["field"] != "other" {
		t.Errorf("a second decoration saw the first: %v", second.Details)
	}
}
