package service

import platerr "platform/sdk/errors"

// Error helpers: return one of these from a handler and the SDK renders the
// platform's standard error envelope with the right HTTP status.

// BadRequest is a 400.
func BadRequest(msg string) error { return platerr.New(platerr.CodeBadRequest, msg) }

// NotFound is a 404.
func NotFound(msg string) error { return platerr.New(platerr.CodeNotFound, msg) }

// Conflict is a 409 — e.g. a uniqueness violation.
func Conflict(msg string) error { return platerr.New(platerr.CodeConflict, msg) }

// Unavailable is a 503 — e.g. a downstream dependency failed.
func Unavailable(msg string) error { return platerr.New(platerr.CodeUnavailable, msg) }

// Internal is a 500.
func Internal(msg string) error { return platerr.New(platerr.CodeInternal, msg) }
