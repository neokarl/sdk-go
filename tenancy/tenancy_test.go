package tenancy

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestContextCarriesTheTenant(t *testing.T) {
	if _, ok := TenantFrom(context.Background()); ok {
		t.Error("a bare context must not appear to carry a tenant")
	}
	ctx := WithTenant(context.Background(), "acme")
	if got, ok := TenantFrom(ctx); !ok || got != "acme" {
		t.Errorf("From = %q, %v", got, ok)
	}
	// An empty tenant is not a tenant: it must not read as "scoped to nothing",
	// which would look identical to a correctly scoped call.
	if _, ok := TenantFrom(WithTenant(context.Background(), "")); ok {
		t.Error("an empty tenant must not count as carried")
	}
}

func TestRejectsUnsafeTableNames(t *testing.T) {
	for _, bad := range []string{"scans; DROP TABLE users", `sc"ans`, "Scans", "1scans", ""} {
		if err := EnsurePolicies(context.Background(), nil, bad); err == nil {
			t.Errorf("%q should have been refused before reaching the database", bad)
		}
	}
}

// openTestDB connects as a NON-superuser role, because that is the only way to
// observe row security at all: superusers bypass it, FORCE included.
func openTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	admin := os.Getenv("TEST_POSTGRES_DSN")
	if admin == "" {
		admin = "postgres://platform:platform@localhost:5432/platform?sslmode=disable"
	}
	adb, err := sql.Open("pgx", admin)
	if err != nil {
		t.Skipf("no postgres: %v", err)
	}
	ctx := context.Background()
	if err := adb.PingContext(ctx); err != nil {
		_ = adb.Close()
		t.Skipf("no postgres: %v", err)
	}

	const role = "tenancy_test_role"
	const table = "tenancy_test_rows"
	exec := func(q string) {
		if _, err := adb.ExecContext(ctx, q); err != nil {
			t.Fatalf("setup %q: %v", q, err)
		}
	}
	exec(`DROP TABLE IF EXISTS ` + table)
	exec(fmt.Sprintf(`DO $$ BEGIN
		IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '%s') THEN
			CREATE ROLE %s LOGIN PASSWORD 'test';
		END IF; END $$`, role, role))
	exec(fmt.Sprintf(`CREATE TABLE %s (id text primary key, tenant_id text not null, note text)`, table))
	exec(fmt.Sprintf(`ALTER TABLE %s OWNER TO %s`, table, role))
	exec(fmt.Sprintf(`GRANT USAGE ON SCHEMA public TO %s`, role))

	// Install the policy as the owner, which is what a service does at boot.
	odb, err := sql.Open("pgx", fmt.Sprintf("postgres://%s:test@localhost:5432/platform?sslmode=disable", role))
	if err != nil {
		t.Fatalf("open as %s: %v", role, err)
	}
	if err := EnsurePolicies(ctx, odb, table); err != nil {
		t.Fatalf("EnsurePolicies: %v", err)
	}
	return odb, func() {
		_ = odb.Close()
		_, _ = adb.ExecContext(ctx, `DROP TABLE IF EXISTS `+table)
		_, _ = adb.ExecContext(ctx, `DROP ROLE IF EXISTS `+role)
		_ = adb.Close()
	}
}

const testTable = "tenancy_test_rows"

// The guarantee, stated as a test: a query that forgets to filter sees nothing
// rather than everything.
func TestPolicyIsolatesTenants(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	ctx := context.Background()

	insert := func(tenant, id string) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback() //nolint:errcheck
		if _, err := tx.ExecContext(ctx, `SELECT set_config($1, $2, true)`, Setting, tenant); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO `+testTable+` (id, tenant_id, note) VALUES ($1, $2, 'x')`, id, tenant); err != nil {
			return err
		}
		return tx.Commit()
	}
	if err := insert("acme", "a1"); err != nil {
		t.Fatalf("insert acme: %v", err)
	}
	if err := insert("globex", "g1"); err != nil {
		t.Fatalf("insert globex: %v", err)
	}

	countAs := func(tenant string) int {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback() //nolint:errcheck
		if tenant != "" {
			if _, err := tx.ExecContext(ctx, `SELECT set_config($1, $2, true)`, Setting, tenant); err != nil {
				t.Fatal(err)
			}
		}
		var n int
		// Deliberately no WHERE — this is the forgotten filter the policy exists for.
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM `+testTable).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	if n := countAs("acme"); n != 1 {
		t.Errorf("acme saw %d rows, want only its own", n)
	}
	if n := countAs("globex"); n != 1 {
		t.Errorf("globex saw %d rows, want only its own", n)
	}
	// No tenant established: current_setting(..., true) is NULL, so nothing matches.
	if n := countAs(""); n != 0 {
		t.Errorf("an unscoped query saw %d rows, want 0 — the policy is not failing closed", n)
	}
}

// A write must not be able to plant a row in someone else's tenant, which is what
// WITH CHECK is for. Without it, scoping reads alone would still let a caller
// corrupt another tenant's data.
func TestPolicyRejectsCrossTenantWrites(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	ctx := context.Background()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `SELECT set_config($1, $2, true)`, Setting, "acme"); err != nil {
		t.Fatal(err)
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO `+testTable+` (id, tenant_id, note) VALUES ('x1', 'globex', 'planted')`)
	if err == nil {
		t.Fatal("a caller scoped to acme inserted a row belonging to globex")
	}
}

// Check is what lets a service refuse to start when its isolation is decorative.
func TestCheckReportsEnforcement(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	st, err := Check(context.Background(), db, testTable)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !st.Enabled || !st.Forced {
		t.Errorf("row security is not in force: %+v", st)
	}
	if st.Bypassed {
		t.Error("the test role bypasses row security, so this test proves nothing")
	}
	if !st.Sound() {
		t.Error("Sound() should be true when enabled, forced and not bypassed")
	}
}

// The same check against a superuser connection must report Bypassed — this is the
// startup guard that catches an application still connecting as one.
func TestCheckDetectsASuperuserConnection(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		dsn = "postgres://platform:platform@localhost:5432/platform?sslmode=disable"
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Skipf("no postgres: %v", err)
	}
	defer db.Close()
	if err := db.PingContext(context.Background()); err != nil {
		t.Skipf("no postgres: %v", err)
	}
	// Any table will do; the bypass flag is a property of the role.
	st, err := Check(context.Background(), db, "pg_class")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !st.Bypassed {
		t.Skip("the configured role is not a superuser, so there is nothing to detect")
	}
	if st.Sound() {
		t.Error("a superuser connection must never be reported as soundly isolated")
	}
}
