package transport

import (
	"context"
	"testing"

	"google.golang.org/grpc/metadata"
)

// Identity has to survive the round trip through gRPC metadata, because that is
// what makes cross-service authorization possible at all: a service that cannot
// tell who originated a call has to either trust everyone or refuse everyone.
func TestIdentityRoundTripsThroughMetadata(t *testing.T) {
	want := Identity{
		UserID:    "user-1",
		TenantID:  "acme",
		RequestID: "req-9",
		Token:     "the-token",
	}

	// Client side: injectIdentity reads the identity off the context and puts
	// it on outgoing metadata.
	out := injectIdentity(WithIdentity(context.Background(), want))
	md, ok := metadata.FromOutgoingContext(out)
	if !ok {
		t.Fatal("the client side attached no outgoing metadata")
	}

	// Server side: it arrives as incoming metadata and is lifted back off.
	in := extractIdentity(metadata.NewIncomingContext(context.Background(), md))
	got, ok := IdentityFrom(in)
	if !ok {
		t.Fatal("no identity extracted from the metadata the client sent")
	}
	if got != want {
		t.Errorf("round trip changed the identity:\n got %+v\nwant %+v", got, want)
	}
}

// A context with no identity must report absence, so a caller can tell "nobody
// said who this is" apart from "a user with an empty id".
func TestNoIdentityReportsAbsence(t *testing.T) {
	if _, ok := IdentityFrom(context.Background()); ok {
		t.Error("a bare context reported an identity")
	}
}

// Nothing to propagate must mean nothing added, rather than a set of empty
// headers that look like a deliberately anonymous caller.
func TestInjectWithoutAnIdentityAddsNothing(t *testing.T) {
	out := injectIdentity(context.Background())
	if md, ok := metadata.FromOutgoingContext(out); ok && md.Len() > 0 {
		t.Errorf("metadata added with no identity on the context: %v", md)
	}
}

// Partial metadata is normal — an unauthenticated call still carries a request
// id for correlation. It must not be discarded wholesale.
func TestPartialIdentitySurvives(t *testing.T) {
	md := metadata.Pairs(mdRequestID, "req-1")
	got, ok := IdentityFrom(extractIdentity(metadata.NewIncomingContext(context.Background(), md)))
	if !ok {
		t.Fatal("metadata carrying only a request id was dropped")
	}
	if got.RequestID != "req-1" {
		t.Errorf("request id = %q", got.RequestID)
	}
	if got.UserID != "" || got.TenantID != "" {
		t.Errorf("fields appeared from nowhere: %+v", got)
	}
}

// The tenant must arrive intact. A hop that silently drops it turns a
// tenant-scoped query downstream into one with no tenant, which the row-security
// policy answers with zero rows — a confusing failure a long way from its cause.
func TestTenantSurvivesTheHop(t *testing.T) {
	out := injectIdentity(WithIdentity(context.Background(), Identity{TenantID: "acme"}))
	md, _ := metadata.FromOutgoingContext(out)

	got, ok := IdentityFrom(extractIdentity(metadata.NewIncomingContext(context.Background(), md)))
	if !ok || got.TenantID != "acme" {
		t.Errorf("tenant after the hop = %q (present: %v)", got.TenantID, ok)
	}
}

func TestIdentityFromReadsWhatWithIdentitySet(t *testing.T) {
	want := Identity{UserID: "u", TenantID: "t"}
	got, ok := IdentityFrom(WithIdentity(context.Background(), want))
	if !ok || got != want {
		t.Errorf("IdentityFrom = %+v, %v", got, ok)
	}
}
