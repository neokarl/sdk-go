// Package client is how a service CALLS other services: discovery (the platform
// catalog + REST Invoke, see registry.go) and gRPC dialing (dialer.go). Call
// Connect once at startup with the platform's base URL; the service client
// packages (platform/services/*) then dial peers through the shared connection —
// you never touch a dialer or a gRPC conn directly.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"google.golang.org/grpc"
)

var (
	mu        sync.RWMutex
	shared    *Dialer
	sharedReg *Registry
)

// Connect configures the shared connection to the platform. It must be called
// before any service client call (e.g. user.GetUser, or a REST Invoke). Call it
// once at startup; it is not meant to be called concurrently with client calls.
func Connect(platformBaseURL string, opts ...Option) {
	reg := NewRegistry(platformBaseURL, opts...)
	d := NewDialer(reg, nil)
	mu.Lock()
	shared = d
	sharedReg = reg
	mu.Unlock()
}

func registry() (*Registry, error) {
	mu.RLock()
	r := sharedReg
	mu.RUnlock()
	if r == nil {
		return nil, errors.New("client: not connected — call client.Connect(platformURL) first")
	}
	return r, nil
}

// WaitReady blocks until the shared registry's first catalog fetch completes (or
// ctx is cancelled), so a service that calls peers at boot doesn't race an empty
// catalog. No-op-safe: returns an error if Connect hasn't run.
func WaitReady(ctx context.Context) error {
	r, err := registry()
	if err != nil {
		return err
	}
	return r.WaitReady(ctx)
}

// BaseURL resolves a peer service's advertised APIBaseURL from the catalog — for
// reaching endpoints outside the op catalog (e.g. an SSE stream) by URL. Connect
// must have been called.
func BaseURL(serviceID string) (string, error) {
	r, err := registry()
	if err != nil {
		return "", err
	}
	return r.BaseURL(serviceID)
}

// Invoke calls another service's REST operation by (serviceId, opId) through the
// shared registry — the REST counterpart to Conn (gRPC). The registry resolves
// the URL + method from the platform catalog, so callers never hardcode paths.
// Connect must have been called.
func Invoke(ctx context.Context, call Call) error {
	r, err := registry()
	if err != nil {
		return err
	}
	return r.Invoke(ctx, call)
}

// InvokeData runs a REST Invoke and unwraps the platform's standard
// `{ success, data, error }` envelope, returning the typed `data`. This is the
// common case for calling a peer whose handlers use the platform envelope — it
// removes the per-caller envelope boilerplate. Use Invoke directly for binary
// responses (pass an *[]byte Out) or endpoints that don't envelope.
func InvokeData[T any](ctx context.Context, call Call) (T, error) {
	var zero T
	// Capture the raw body via the registry's *[]byte fast-path, then decode the
	// envelope here so the registry stays envelope-agnostic.
	var raw []byte
	call.Out = &raw
	if err := Invoke(ctx, call); err != nil {
		return zero, err
	}
	var env struct {
		Success bool `json:"success"`
		Data    T    `json:"data"`
		Error   *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return zero, fmt.Errorf("client: decode envelope for %s.%s: %w", call.Service, call.Op, err)
	}
	if !env.Success {
		if env.Error != nil {
			return zero, fmt.Errorf("client: %s.%s failed: %s", call.Service, call.Op, env.Error.Message)
		}
		return zero, fmt.Errorf("client: %s.%s failed without error detail", call.Service, call.Op)
	}
	return env.Data, nil
}

// Conn returns a cached gRPC connection to a platform service by id. The
// generated service client packages call this; you rarely need it directly. It
// errors if Connect has not been called.
func Conn(serviceID string) (grpc.ClientConnInterface, error) {
	mu.RLock()
	d := shared
	mu.RUnlock()
	if d == nil {
		return nil, errors.New("client: not connected — call client.Connect(platformURL) first")
	}
	return d.Conn(serviceID)
}
