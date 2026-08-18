package tenancy

import (
	"context"

	"gorm.io/gorm"
)

// Scoped runs fn in a transaction with the caller's tenant established, which is
// what the row-security policy reads.
//
// The transaction is not incidental. SET LOCAL is cleared by commit *and*
// rollback, so a pooled connection can never carry one tenant's scope into the
// next borrower's query — a session-level SET on a pooled connection would do
// exactly that, silently, under load.
//
// Every read and write of tenant-owned data should go through here. A query that
// doesn't will see nothing rather than everything, which is the failure mode we
// chose: quiet and safe, rather than loud or leaky.
func Scoped(ctx context.Context, db *gorm.DB, fn func(tx *gorm.DB) error) error {
	tenant, ok := TenantFrom(ctx)
	if !ok {
		return ErrNoTenant
	}
	return inTenant(ctx, db, tenant, fn)
}

// AcrossTenants runs fn with every tenant's rows visible.
//
// This is the deliberate hole, for maintenance that genuinely spans tenants and
// has no request behind it: reconciling scans left running by a killed process,
// a retention sweep, a backfill. Such work cannot name a tenant — "all of them"
// is the answer — and under a normal scope it would quietly touch nothing, which
// is the worst outcome available: a maintenance task that reports success and
// did nothing.
//
// Every call is a place isolation does not apply, so keep them few, keep them at
// the composition root, and never reach for this to make a request-path query
// "just work" — that is the bug it would be hiding.
func AcrossTenants(ctx context.Context, db *gorm.DB, fn func(tx *gorm.DB) error) error {
	return inTenant(ctx, db, Unrestricted, fn)
}

func inTenant(ctx context.Context, db *gorm.DB, tenant string, fn func(tx *gorm.DB) error) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// set_config rather than `SET LOCAL x = ?`: SET takes no bind parameters,
		// and this is the parameterised equivalent, third argument = local.
		if err := tx.Exec(`SELECT set_config(?, ?, true)`, Setting, tenant).Error; err != nil {
			return err
		}
		return fn(tx)
	})
}
