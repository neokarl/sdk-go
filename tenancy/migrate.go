package tenancy

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"gorm.io/gorm"
)

// unscoped marks a model as deliberately outside tenant isolation.
type unscoped struct{ model any }

// Unscoped exempts one model from tenant scoping. Reach for it only for tables
// that genuinely belong to no tenant — a shared lookup table, the events outbox.
//
// It exists so that opting out is a visible act in the source rather than an
// omission nobody notices. A reviewer can grep for it; an absent tenant column
// looks like nothing at all.
func Unscoped(model any) any { return unscoped{model: model} }

// Migrate creates the service's tables and their isolation policies together.
//
// It refuses any model that is not tenant-owned, naming the offending struct, so
// a table that forgot its tenant column fails at boot rather than becoming a
// quiet hole in the boundary. The policy install is the same idempotent
// EnsurePolicies a service would otherwise have to remember to call —
// the point of folding it in here is that it can no longer be forgotten
// separately from the migration.
func Migrate(ctx context.Context, db *gorm.DB, models ...any) error {
	var scoped []any // models whose tables need policies
	var all []any    // everything to hand to AutoMigrate

	for _, m := range models {
		if u, ok := m.(unscoped); ok {
			all = append(all, u.model)
			continue
		}
		if !isScoped(m) {
			return fmt.Errorf("%w: %s — embed tenancy.Owned, or pass tenancy.Unscoped(%s{}) if it truly belongs to no tenant",
				ErrNotScoped, typeName(m), typeName(m))
		}
		all = append(all, m)
		scoped = append(scoped, m)
	}

	if err := db.WithContext(ctx).AutoMigrate(all...); err != nil {
		return fmt.Errorf("tenancy: migrate: %w", err)
	}

	tables, err := tableNames(db, scoped)
	if err != nil {
		return err
	}
	if len(tables) == 0 {
		return nil
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("tenancy: reach the underlying connection: %w", err)
	}
	return EnsurePolicies(ctx, sqlDB, tables...)
}

// BackfillTenant adopts tables that already hold rows.
//
// It exists because AutoMigrate cannot add a NOT NULL column to a populated
// table: there is no value for the rows already there. So the column arrives
// nullable, the existing rows are assigned to `tenant`, and only then is the
// constraint applied. Idempotent, and a no-op on a table that is already scoped
// — run it before Migrate.
//
// Choosing `tenant` is a real decision, not a formality: it decides who can still
// see the data afterwards. For a system that has been effectively single-tenant,
// the platform's own DEFAULT_TENANT is usually the honest answer.
func BackfillTenant(ctx context.Context, db *gorm.DB, tenant string, tables ...string) error {
	if strings.TrimSpace(tenant) == "" {
		return fmt.Errorf("tenancy: backfill needs a tenant to assign existing rows to")
	}
	for _, table := range tables {
		t, err := quote(table)
		if err != nil {
			return err
		}
		if !db.Migrator().HasTable(table) {
			continue // nothing to adopt; Migrate will create it scoped from the start
		}
		stmts := []struct {
			sql  string
			args []any
		}{
			{fmt.Sprintf(`ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s TEXT`, t, Column), nil},
			{fmt.Sprintf(`UPDATE %s SET %s = ? WHERE %s IS NULL`, t, Column, Column), []any{tenant}},
			{fmt.Sprintf(`ALTER TABLE %s ALTER COLUMN %s SET NOT NULL`, t, Column), nil},
		}
		for _, s := range stmts {
			if err := db.WithContext(ctx).Exec(s.sql, s.args...).Error; err != nil {
				return fmt.Errorf("tenancy: backfill %s: %w", table, err)
			}
		}
	}
	return nil
}

// isScoped reports whether a model carries a tenant. The check is on the pointer
// type because SetTenant needs a pointer receiver, and callers pass models both
// ways (&Row{} and Row{}).
func isScoped(model any) bool {
	t := reflect.TypeOf(model)
	if t == nil {
		return false
	}
	if t.Kind() != reflect.Ptr {
		t = reflect.PointerTo(t)
	}
	return t.Implements(reflect.TypeOf((*Model)(nil)).Elem())
}

func typeName(model any) string {
	t := reflect.TypeOf(model)
	if t == nil {
		return "<nil>"
	}
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t.Name()
}

// tableNames asks gorm what each model is called, so the policy lands on the
// same table AutoMigrate just created — including whatever the service's naming
// strategy or a TableName() method decided.
func tableNames(db *gorm.DB, models []any) ([]string, error) {
	out := make([]string, 0, len(models))
	for _, m := range models {
		stmt := &gorm.Statement{DB: db}
		if err := stmt.Parse(m); err != nil {
			return nil, fmt.Errorf("tenancy: resolve table name for %s: %w", typeName(m), err)
		}
		out = append(out, stmt.Table)
	}
	return out, nil
}
