package client

import (
	"context"
	"sync"
	"time"

	"github.com/sony/gobreaker"
	"google.golang.org/grpc"
)

// Resilience defaults for outbound gRPC calls. They apply to every peer call
// made through the Dialer unless a caller overrides them (a caller-set context
// deadline wins over defaultCallTimeout).
const (
	defaultCallTimeout = 15 * time.Second
	breakerTrips       = 5                // consecutive failures before the breaker opens
	breakerCooldown    = 30 * time.Second // open → half-open probe after this
)

// retryServiceConfig enables gRPC's built-in retry policy: transient
// connection failures (the platform restarting, a peer briefly gone) are
// retried with exponential backoff, transparently. Only UNAVAILABLE is retried
// — retrying anything the server actually processed would risk duplicates.
const retryServiceConfig = `{
  "methodConfig": [{
    "name": [{}],
    "retryPolicy": {
      "maxAttempts": 4,
      "initialBackoff": "0.1s",
      "maxBackoff": "2s",
      "backoffMultiplier": 2.0,
      "retryableStatusCodes": ["UNAVAILABLE"]
    }
  }]
}`

// timeoutInterceptor applies a default deadline when the caller didn't set one,
// so a hung peer can't hang the call forever.
func timeoutInterceptor(d time.Duration) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if _, ok := ctx.Deadline(); !ok {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, d)
			defer cancel()
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// breakerInterceptor fails fast when a peer method keeps failing: after
// breakerTrips consecutive failures the breaker opens and returns immediately
// (instead of piling more calls onto a downed peer), then probes once after
// breakerCooldown. One breaker per method.
func breakerInterceptor() grpc.UnaryClientInterceptor {
	var breakers sync.Map // method -> *gobreaker.CircuitBreaker
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		cbAny, _ := breakers.LoadOrStore(method, gobreaker.NewCircuitBreaker(gobreaker.Settings{
			Name:    method,
			Timeout: breakerCooldown,
			ReadyToTrip: func(c gobreaker.Counts) bool {
				return c.ConsecutiveFailures >= breakerTrips
			},
		}))
		cb := cbAny.(*gobreaker.CircuitBreaker)
		_, err := cb.Execute(func() (any, error) {
			return nil, invoker(ctx, method, req, reply, cc, opts...)
		})
		return err
	}
}
