package middleware

import (
	"encoding/json"
	goerrors "errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	platerr "github.com/neokarl/sdk-go/errors"
)

// serve runs one request through an echo instance whose error handler is the
// one under test, and returns the recorded response.
func serve(t *testing.T, h echo.HandlerFunc) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	e := echo.New()
	e.HideBanner = true
	e.HTTPErrorHandler = ErrorHandler()
	e.GET("/x", h)

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	var body map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("response was not JSON: %s", rec.Body)
		}
	}
	return rec, body
}

// A platform error renders as declared: its own status, its own code, its own
// message. This is the contract every service in the platform presents.
func TestPlatformErrorRendersAsDeclared(t *testing.T) {
	rec, body := serve(t, func(c echo.Context) error {
		return platerr.New(platerr.CodeConflict, "that name is taken").
			WithDetail("field", "name")
	})

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if body["success"] != false {
		t.Errorf("success = %v, want false", body["success"])
	}
	errObj, _ := body["error"].(map[string]any)
	if errObj["code"] != "conflict" {
		t.Errorf("code = %v", errObj["code"])
	}
	if errObj["message"] != "that name is taken" {
		t.Errorf("message = %v", errObj["message"])
	}
	details, _ := errObj["details"].(map[string]any)
	if details["field"] != "name" {
		t.Errorf("details = %v", errObj["details"])
	}
}

// The wrapped cause is for the logs. Leaking it to the client is how internal
// hostnames, SQL and stack traces end up in a browser.
func TestWrappedCauseIsNeverSentToTheClient(t *testing.T) {
	secret := goerrors.New("dial tcp 10.0.0.7:5432: connection refused")
	rec, body := serve(t, func(c echo.Context) error {
		return platerr.Wrap(secret, platerr.CodeUnavailable, "the store is unreachable")
	})

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "10.0.0.7") {
		t.Errorf("the response leaked the cause: %s", rec.Body)
	}
	errObj, _ := body["error"].(map[string]any)
	if errObj["message"] != "the store is unreachable" {
		t.Errorf("message = %v", errObj["message"])
	}
}

// An unclassified error must become a 500 with a generic body — never the raw
// error text, and never a 200.
func TestUnclassifiedErrorIsAGeneric500(t *testing.T) {
	rec, body := serve(t, func(c echo.Context) error {
		return goerrors.New("something with /etc/passwd in it")
	})

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "/etc/passwd") {
		t.Errorf("the response leaked the error text: %s", rec.Body)
	}
	errObj, _ := body["error"].(map[string]any)
	if errObj["code"] != "internal" || errObj["message"] != "internal server error" {
		t.Errorf("error = %v", errObj)
	}
}

// Echo raises its own errors before a handler ever runs — a 404 for an unknown
// route, a 405 for the wrong method. Those must arrive in the platform envelope
// too, or a caller has to parse two shapes.
func TestEchoErrorsUseThePlatformEnvelope(t *testing.T) {
	e := echo.New()
	e.HideBanner = true
	e.HTTPErrorHandler = ErrorHandler()
	e.GET("/x", func(c echo.Context) error { return nil })

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response was not JSON: %s", rec.Body)
	}
	errObj, _ := body["error"].(map[string]any)
	if errObj["code"] != "not_found" {
		t.Errorf("code = %v, want not_found", errObj["code"])
	}
}

// codeFor is what maps an echo status onto the platform taxonomy. Every status
// the platform uses needs an entry; anything else is internal.
func TestCodeForCoversThePlatformStatuses(t *testing.T) {
	want := map[int]string{
		http.StatusBadRequest:         "bad_request",
		http.StatusUnauthorized:       "unauthorized",
		http.StatusForbidden:          "forbidden",
		http.StatusNotFound:           "not_found",
		http.StatusConflict:           "conflict",
		http.StatusTooManyRequests:    "rate_limited",
		http.StatusServiceUnavailable: "unavailable",
		http.StatusNotImplemented:     "not_implemented",
		http.StatusTeapot:             "internal",
	}
	for status, code := range want {
		if got := codeFor(status); got != code {
			t.Errorf("codeFor(%d) = %q, want %q", status, got, code)
		}
	}
}
