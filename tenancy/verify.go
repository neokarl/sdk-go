package tenancy

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"gorm.io/gorm"
)

// Report is the boot-time picture of whether isolation is real.
type Report struct {
	Tables   map[string]Status
	Bypassed bool // the connected role ignores row security whatever the tables say
}

// Sound reports whether every table checked is genuinely isolated.
func (r Report) Sound() bool {
	if r.Bypassed || len(r.Tables) == 0 {
		return false
	}
	for _, st := range r.Tables {
		if !st.Sound() {
			return false
		}
	}
	return true
}

// Problems describes what is wrong, for a log line or a startup error.
func (r Report) Problems() []string {
	var out []string
	if r.Bypassed {
		out = append(out, "the connected role bypasses row-level security (superuser or BYPASSRLS) — every policy below is decoration")
	}
	names := make([]string, 0, len(r.Tables))
	for t := range r.Tables {
		names = append(names, t)
	}
	sort.Strings(names)
	for _, t := range names {
		st := r.Tables[t]
		switch {
		case !st.Enabled:
			out = append(out, fmt.Sprintf("%s: row-level security is not enabled", t))
		case !st.Forced:
			out = append(out, fmt.Sprintf("%s: row-level security is not FORCEd, so the table's owner bypasses it", t))
		}
	}
	return out
}

// Verify checks that the tables a service owns are actually isolated, so it can
// refuse to start when they are not.
//
// This is the layer that catches what Migrate cannot: a table created behind the
// library's back — by a hand-written migration, an older AutoMigrate call, or
// another service sharing the schema. Given no table names it discovers them
// from ownership, which is only meaningful once the service connects as its own
// role; while everything connects as one shared superuser it will both sweep in
// other services' tables and report Bypassed. That report is accurate: today's
// isolation genuinely is decorative.
func Verify(ctx context.Context, db *gorm.DB, tables ...string) (Report, error) {
	sqlDB, err := db.DB()
	if err != nil {
		return Report{}, fmt.Errorf("tenancy: reach the underlying connection: %w", err)
	}
	if len(tables) == 0 {
		if tables, err = Discover(ctx, db); err != nil {
			return Report{}, err
		}
	}

	rep := Report{Tables: make(map[string]Status, len(tables))}
	for _, t := range tables {
		st, err := Check(ctx, sqlDB, t)
		if err != nil {
			return rep, err
		}
		rep.Tables[t] = st
		if st.Bypassed {
			rep.Bypassed = true
		}
	}
	return rep, nil
}

// Discover lists the tables owned by the connected role. Ownership is what
// attributes a table to a service in a shared schema — which is precisely why
// each service wants its own database role.
func Discover(ctx context.Context, db *gorm.DB) ([]string, error) {
	var names []string
	err := db.WithContext(ctx).Raw(`
		SELECT c.relname
		  FROM pg_class c
		  JOIN pg_roles r ON r.oid = c.relowner
		 WHERE c.relkind = 'r'
		   AND c.relnamespace = 'public'::regnamespace
		   AND r.rolname = current_user
		 ORDER BY c.relname`).Scan(&names).Error
	if err != nil {
		return nil, fmt.Errorf("tenancy: discover owned tables: %w", err)
	}
	return names, nil
}

// MustBeSound is the one-line boot guard: log what is wrong and refuse to serve.
func MustBeSound(ctx context.Context, db *gorm.DB, tables ...string) error {
	rep, err := Verify(ctx, db, tables...)
	if err != nil {
		return err
	}
	if !rep.Sound() {
		return fmt.Errorf("tenancy: tenant isolation is not in force:\n  - %s",
			strings.Join(rep.Problems(), "\n  - "))
	}
	return nil
}
