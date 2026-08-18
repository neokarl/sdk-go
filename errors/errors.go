// Package errors defines the platform's standard error taxonomy.
//
// A handler returns one of these and the SDK renders it as the platform error
// envelope with the matching HTTP status — so every service in the platform
// fails the same shape, and a caller can branch on [Code] rather than parsing
// prose.
//
//	func (h *handler) get(c service.Context) error {
//	    item, err := h.store.Find(c.Ctx(), c.Param("id"))
//	    if goerrors.Is(err, sql.ErrNoRows) {
//	        return service.NotFound("no such item")
//	    }
//	    if err != nil {
//	        return errors.Wrap(err, errors.CodeInternal, "could not read item")
//	    }
//	    return service.OK(c, item)
//	}
//
// [Wrap] keeps the underlying cause for the logs without leaking it to the
// client. The service package has shorthand constructors for the common codes.
package errors

import (
	"errors"
	"fmt"
	"net/http"
)

// Code is a stable, machine-readable error identifier. It is the `code` field
// of the error envelope, and it determines the HTTP status.
type Code string

const (
	// CodeBadRequest is a malformed or invalid request (400).
	CodeBadRequest Code = "bad_request"
	// CodeUnauthorized means the caller is not authenticated (401).
	CodeUnauthorized Code = "unauthorized"
	// CodeForbidden means the caller is authenticated but lacks the
	// permission (403).
	CodeForbidden Code = "forbidden"
	// CodeNotFound means the addressed resource does not exist (404).
	CodeNotFound Code = "not_found"
	// CodeConflict means the request conflicts with current state, such as a
	// uniqueness violation (409).
	CodeConflict Code = "conflict"
	// CodeRateLimited means the caller has exceeded a quota (429).
	CodeRateLimited Code = "rate_limited"
	// CodeInternal is an unexpected failure inside the service (500). Prefer a
	// more specific code whenever one fits.
	CodeInternal Code = "internal"
	// CodeUnavailable means a dependency this request needs is down, and
	// retrying later may succeed (503).
	CodeUnavailable Code = "unavailable"
	// CodeNotImplemented means the operation is declared but not built yet (501).
	CodeNotImplemented Code = "not_implemented"
)

// PlatformError is the canonical error returned by services and handlers.
// It carries enough information for the centralized error handler to render
// a consistent response without leaking internal details.
type PlatformError struct {
	Code    Code           `json:"code"`
	Status  int            `json:"-"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
	Cause   error          `json:"-"`
}

// Error implements the error interface.
func (e *PlatformError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap returns the wrapped cause, so errors.Is and errors.As see through a
// PlatformError to whatever it wraps.
func (e *PlatformError) Unwrap() error { return e.Cause }

// New constructs a PlatformError. Status is inferred from the code.
func New(code Code, message string) *PlatformError {
	return &PlatformError{Code: code, Status: statusFor(code), Message: message}
}

// Wrap attaches an underlying cause for logging. The cause is never returned
// to the client.
func Wrap(err error, code Code, message string) *PlatformError {
	return &PlatformError{Code: code, Status: statusFor(code), Message: message, Cause: err}
}

// WithDetail returns a copy of the error with an extra detail entry.
func (e *PlatformError) WithDetail(key string, value any) *PlatformError {
	cp := *e
	if cp.Details == nil {
		cp.Details = map[string]any{}
	}
	cp.Details[key] = value
	return &cp
}

// As is a thin wrapper that lets callers unwrap to *PlatformError.
func As(err error) (*PlatformError, bool) {
	var pe *PlatformError
	if errors.As(err, &pe) {
		return pe, true
	}
	return nil, false
}

// IsCode reports whether err is a PlatformError carrying the given code. Use it
// to branch on the failure kind without unwrapping by hand:
//
//	if errors.IsCode(err, errors.CodeNotFound) { ... }
func IsCode(err error, code Code) bool {
	pe, ok := As(err)
	return ok && pe.Code == code
}

// Status returns the HTTP status a code renders as.
func Status(code Code) int { return statusFor(code) }

func statusFor(code Code) int {
	switch code {
	case CodeBadRequest:
		return http.StatusBadRequest
	case CodeUnauthorized:
		return http.StatusUnauthorized
	case CodeForbidden:
		return http.StatusForbidden
	case CodeNotFound:
		return http.StatusNotFound
	case CodeConflict:
		return http.StatusConflict
	case CodeRateLimited:
		return http.StatusTooManyRequests
	case CodeUnavailable:
		return http.StatusServiceUnavailable
	case CodeNotImplemented:
		return http.StatusNotImplemented
	default:
		return http.StatusInternalServerError
	}
}
