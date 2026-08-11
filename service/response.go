package service

import (
	"net/http"

	"platform/sdk/contracts"
)

// Response helpers: write the platform's canonical `{ success, data, error }`
// envelope (contracts.Envelope) from a handler. Success responses go through
// these; error responses are produced by returning one of the Error helpers
// (see errors.go), which the SDK's error handler renders in the same envelope.
// A plugin therefore never hand-rolls the envelope shape.

// OK writes a 200 success envelope wrapping data.
func OK(c Context, data any) error { return c.JSON(http.StatusOK, contracts.Ok(data)) }

// Created writes a 201 success envelope wrapping data.
func Created(c Context, data any) error { return c.JSON(http.StatusCreated, contracts.Ok(data)) }

// NoContent writes a 204 with no body — for deletes and other operations that
// return nothing.
func NoContent(c Context) error { return c.String(http.StatusNoContent, "") }
