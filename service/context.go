package service

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v4"
)

// Context is what a handler receives. It abstracts the underlying web framework
// so plugin handlers never import echo (or net/http, except via Request).
type Context interface {
	// Bind decodes the request body (JSON) into v.
	Bind(v any) error
	// JSON writes v as a JSON response with the given status code.
	JSON(status int, v any) error
	// String writes a plain-text response.
	String(status int, s string) error
	// Blob writes a raw byte body with the given content type — for binary
	// responses (e.g. a generated DOCX) that don't fit the JSON envelope.
	Blob(status int, contentType string, b []byte) error
	// Header sets a response header (e.g. Content-Disposition for a download).
	Header(key, value string)
	// Param returns a path parameter (e.g. the :id in /items/:id).
	Param(name string) string
	// Query returns a query-string parameter.
	Query(name string) string
	// Ctx returns the request context — it carries the propagated identity and
	// request id, and is what you pass to platform service clients.
	Ctx() context.Context
	// Request returns the raw *http.Request as an escape hatch.
	Request() *http.Request
}

// echoContext adapts echo.Context to Context. It is the only place that knows
// the server is echo.
type echoContext struct{ ec echo.Context }

func (c *echoContext) Bind(v any) error                  { return c.ec.Bind(v) }
func (c *echoContext) JSON(status int, v any) error      { return c.ec.JSON(status, v) }
func (c *echoContext) String(status int, s string) error { return c.ec.String(status, s) }
func (c *echoContext) Blob(status int, ct string, b []byte) error {
	return c.ec.Blob(status, ct, b)
}
func (c *echoContext) Header(key, value string) { c.ec.Response().Header().Set(key, value) }
func (c *echoContext) Param(name string) string { return c.ec.Param(name) }
func (c *echoContext) Query(name string) string { return c.ec.QueryParam(name) }
func (c *echoContext) Ctx() context.Context     { return c.ec.Request().Context() }
func (c *echoContext) Request() *http.Request   { return c.ec.Request() }
