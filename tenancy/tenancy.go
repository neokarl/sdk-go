// Package tenancy scopes a service's data to one tenant and makes the *database*
// enforce it, rather than trusting a dozen query paths to remember a WHERE clause.
//
// The mechanism is Postgres row-level security. Each scoped table carries a
// tenant_id and a policy comparing it to a session setting; the service sets that
// setting per transaction from the caller's verified identity. A query that forgets
// to filter returns nothing instead of everything, and a write that names another
// tenant is rejected.
//
// The package comes in two halves. This file holds the primitives, which reach the
// database through the Execer/Queryer interfaces and so work against any driver:
// the policy installer, the session setting, the Owned mixin, the boot check.
// The rest — Migrate, Plugin, Scoped, Verify — is the gorm layer that makes those
// primitives unavoidable, since a tenant column each author has to remember is the
// same assumption row security was adopted to remove.
//
// One thing is deliberately NOT here: deciding *which* tenant. That is identity,
// and it belongs to platform/sdk/auth (Verifier.TenantOf), which resolves it from a
// verified token and binds it with With.
//
// Note the neighbour: middleware.TenantIDFrom reads the X-Tenant-ID *header*, which
// a caller supplies and can therefore choose. It is a development convenience. This
// package is the enforced boundary. Never gate anything on the former.
package tenancy

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
)

// Setting is the Postgres session variable holding the current tenant.
const Setting = "app.tenant"

// PolicyName is the row-security policy this package installs.
const PolicyName = "tenant_isolation"

// Column is the tenant discriminator every scoped table must carry.
const Column = "tenant_id"

// Unrestricted is the session value that sees every tenant's rows.
//
// It exists for work that is genuinely cross-tenant and has no request behind it:
// marking scans interrupted after a restart, a retention sweep, a migration. Such
// a job cannot ask "which tenant?" because the answer is "all of them", and with
// no setting at all it would quietly affect nothing — the failure mode being a
// maintenance task that appears to run and does nothing.
//
// It is a deliberate hole and should read like one at the call site, which is why
// it goes through the function of the same name rather than being a value anyone
// can pass to With.
const Unrestricted = "*"

// Execer runs a statement. *sql.DB, *sql.Tx and any ORM's underlying handle
// satisfy it, which keeps this package free of a database-library dependency —
// the same approach events.WriteOutbox takes.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Queryer reads a single row, for the boot-time check below.
type Queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Owned gives a row its tenant column. Embed it in a model and the column, its
// NOT NULL and its index arrive with it — which is the point: the column stops
// being something each table's author has to remember.
//
// The struct tag is an inert string as far as this declaration is concerned;
// Migrate is what acts on it.
type Owned struct {
	TenantID string `gorm:"column:tenant_id;type:text;not null;index" json:"-"`
}

func (o Owned) Tenant() string       { return o.TenantID }
func (o *Owned) SetTenant(id string) { o.TenantID = id }

// Model is what a tenant-owned model satisfies. Detection goes through this
// interface rather than reflecting over field names, so a model that stores its
// tenant differently can still opt in honestly by implementing it.
//
// Note the neighbour: Scoped (the function) runs work inside a tenant. This is
// the thing a row *is*; that is the thing a query runs *in*.
type Model interface {
	Tenant() string
	SetTenant(string)
}

type ctxKey struct{}

// With carries a tenant on a context for work that has no request behind it —
// a queue worker, a scheduled job, a workflow activity. Such a caller cannot read
// its own row to discover the tenant, because reading requires the tenant first,
// so it must be told: carry it in the job payload and set it here.
func With(ctx context.Context, tenant string) context.Context {
	return context.WithValue(ctx, ctxKey{}, tenant)
}

// From returns the tenant carried by ctx.
func From(ctx context.Context) (string, bool) {
	t, ok := ctx.Value(ctxKey{}).(string)
	return t, ok && t != ""
}

// safeIdent guards the table names below: identifiers cannot be bound as
// parameters, so they are quoted and validated rather than interpolated blind.
var safeIdent = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

func quote(ident string) (string, error) {
	if !safeIdent.MatchString(ident) {
		return "", fmt.Errorf("tenancy: %q is not a plain lower-case identifier", ident)
	}
	return `"` + ident + `"`, nil
}

// EnsurePolicies enables row-level security on each table and installs the
// isolation policy. Idempotent, so it can run on every boot beside the migrations.
//
// FORCE is not optional: without it the table's *owner* bypasses its own policy,
// and a service that owns its schema (anything using AutoMigrate) is exactly that
// owner. It does not help against a superuser — nothing does — which is why the
// service should not connect as one. Check reports that.
//
// The policy compares against current_setting(Setting, true). The second argument
// is load-bearing: with no setting established it yields NULL rather than raising,
// so an unscoped query returns zero rows. Failing closed and quietly beats failing
// open or noisily. The Unrestricted arm is the one deliberate exception, for
// cross-tenant maintenance that has no tenant to name.
//
// The policy is dropped and recreated rather than created-if-absent. An existence
// guard would mean a change to the definition above never reaches a database that
// already has the old one — the policy would drift from this source silently,
// which for a security control is the worst of the options.
func EnsurePolicies(ctx context.Context, db Execer, tables ...string) error {
	for _, table := range tables {
		t, err := quote(table)
		if err != nil {
			return err
		}
		stmts := []string{
			fmt.Sprintf(`ALTER TABLE %s ENABLE ROW LEVEL SECURITY`, t),
			fmt.Sprintf(`ALTER TABLE %s FORCE ROW LEVEL SECURITY`, t),
			fmt.Sprintf(`DROP POLICY IF EXISTS %s ON %s`, PolicyName, t),
			fmt.Sprintf(`CREATE POLICY %s ON %s`+
				` USING (%s = current_setting('%s', true) OR current_setting('%s', true) = '%s')`+
				` WITH CHECK (%s = current_setting('%s', true) OR current_setting('%s', true) = '%s')`,
				PolicyName, t,
				Column, Setting, Setting, Unrestricted,
				Column, Setting, Setting, Unrestricted),
		}
		for _, s := range stmts {
			if _, err := db.ExecContext(ctx, s); err != nil {
				return fmt.Errorf("tenancy: %s: %w", table, err)
			}
		}
	}
	return nil
}

// Status describes whether a table's isolation is actually in force.
type Status struct {
	// Enabled and Forced mirror the table's row-security flags.
	Enabled bool
	Forced  bool
	// Bypassed reports that the *connected role* ignores row security whatever the
	// table says — a superuser, or one with BYPASSRLS. When true the policy is
	// decoration, which is worth failing a startup check over.
	Bypassed bool
}

// Sound reports whether isolation genuinely applies to this connection.
func (s Status) Sound() bool { return s.Enabled && s.Forced && !s.Bypassed }

// Check inspects one table so a service can refuse to start when its isolation is
// not real. The failure it catches is silent by nature: policies missing, or an
// application connected as a superuser, both look exactly like everything working.
func Check(ctx context.Context, db Queryer, table string) (Status, error) {
	var st Status
	if _, err := quote(table); err != nil {
		return st, err
	}
	err := db.QueryRowContext(ctx,
		`SELECT c.relrowsecurity, c.relforcerowsecurity,
		        (SELECT r.rolsuper OR r.rolbypassrls FROM pg_roles r WHERE r.rolname = current_user)
		   FROM pg_class c WHERE c.relname = $1`, table).
		Scan(&st.Enabled, &st.Forced, &st.Bypassed)
	if err != nil {
		return st, fmt.Errorf("tenancy: check %s: %w", table, err)
	}
	return st, nil
}
