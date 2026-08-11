// Package errors defines the platform's standard error types and helpers.
// Handlers should always return one of these (or wrap external errors with
// `Wrap`); the centralized error handler in pkg/middleware turns them into
// the platform response envelope.
package errors

import (
	"errors"
	"fmt"
	"net/http"
)

// Code is a stable, machine-readable error identifier.
type Code string

const (
	CodeBadRequest     Code = "bad_request"
	CodeUnauthorized   Code = "unauthorized"
	CodeForbidden      Code = "forbidden"
	CodeNotFound       Code = "not_found"
	CodeConflict       Code = "conflict"
	CodeRateLimited    Code = "rate_limited"
	CodeInternal       Code = "internal"
	CodeUnavailable    Code = "unavailable"
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

func (e *PlatformError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

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
