package service

import platerr "go.neokarl.com/sdk/errors"

// Error helpers: return one of these from a handler and the SDK renders the
// platform's standard error envelope with the matching HTTP status.
//
// They cover the full set of platform error codes, so a handler never has to
// import the errors package for the ordinary cases. Reach for
// go.neokarl.com/sdk/errors directly when you want to attach a cause with Wrap
// or extra fields with WithDetail.

// BadRequest is a 400 — the request was malformed or failed validation.
func BadRequest(msg string) error { return platerr.New(platerr.CodeBadRequest, msg) }

// Unauthorized is a 401 — the caller is not authenticated.
func Unauthorized(msg string) error { return platerr.New(platerr.CodeUnauthorized, msg) }

// Forbidden is a 403 — the caller is authenticated but lacks the permission.
func Forbidden(msg string) error { return platerr.New(platerr.CodeForbidden, msg) }

// NotFound is a 404.
func NotFound(msg string) error { return platerr.New(platerr.CodeNotFound, msg) }

// Conflict is a 409 — e.g. a uniqueness violation.
func Conflict(msg string) error { return platerr.New(platerr.CodeConflict, msg) }

// RateLimited is a 429 — the caller exceeded a quota.
func RateLimited(msg string) error { return platerr.New(platerr.CodeRateLimited, msg) }

// NotImplemented is a 501 — the operation is declared but not built yet.
func NotImplemented(msg string) error { return platerr.New(platerr.CodeNotImplemented, msg) }

// Unavailable is a 503 — a dependency this request needs is down, and retrying
// later may succeed.
func Unavailable(msg string) error { return platerr.New(platerr.CodeUnavailable, msg) }

// Internal is a 500. Prefer a more specific helper whenever one fits: "internal
// error" tells the caller nothing they can act on.
func Internal(msg string) error { return platerr.New(platerr.CodeInternal, msg) }
