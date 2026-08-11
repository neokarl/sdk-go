package tenancy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The models under test. scopedRow is what a service should write; plainRow is
// the mistake this package exists to catch.
type scopedRow struct {
	Owned
	ID   string `gorm:"primaryKey"`
	Note string
}

func (scopedRow) TableName() string { return "data_test_scoped" }

type plainRow struct {
	ID string `gorm:"primaryKey"`
}

func (plainRow) TableName() string { return "data_test_plain" }

const (
	testRole    = "data_test_role"
	testPass    = "test"
	scopedTable = "data_test_scoped"
	plainTable  = "data_test_plain"
	strayTable  = "data_test_stray"
)

// openTestDB connects gorm as a NON-superuser that owns its tables — the only
// arrangement in which row security can be observed at all, and the arrangement
// a service should eventually have.
func openGormDB(t *testing.T) (*gorm.DB, func()) {
	t.Helper()
	adminDSN := os.Getenv("TEST_POSTGRES_DSN")
	if adminDSN == "" {
		adminDSN = "postgres://platform:platform@localhost:5432/platform?sslmode=disable"
	}
	adb, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Skipf("no postgres: %v", err)
	}
	ctx := context.Background()
	if err := adb.PingContext(ctx); err != nil {
		_ = adb.Close()
		t.Skipf("no postgres: %v", err)
	}

	exec := func(q string) {
		if _, err := adb.ExecContext(ctx, q); err != nil {
			t.Fatalf("setup %q: %v", q, err)
		}
	}
	for _, tbl := range []string{scopedTable, plainTable, strayTable} {
		exec(`DROP TABLE IF EXISTS ` + tbl)
	}
	exec(fmt.Sprintf(`DO $$ BEGIN
		IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '%s') THEN
			CREATE ROLE %s LOGIN PASSWORD '%s';
		END IF; END $$`, testRole, testRole, testPass))
	exec(fmt.Sprintf(`GRANT USAGE, CREATE ON SCHEMA public TO %s`, testRole))

	dsn := fmt.Sprintf("postgres://%s:%s@localhost:5432/platform?sslmode=disable", testRole, testPass)
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open as %s: %v", testRole, err)
	}
	if err := db.Use(Plugin{}); err != nil {
		t.Fatalf("install plugin: %v", err)
	}

	return db, func() {
		for _, tbl := range []string{scopedTable, plainTable, strayTable} {
			_, _ = adb.ExecContext(ctx, `DROP TABLE IF EXISTS `+tbl)
		}
		_, _ = adb.ExecContext(ctx, fmt.Sprintf(`REVOKE USAGE, CREATE ON SCHEMA public FROM %s`, testRole))
		_, _ = adb.ExecContext(ctx, `DROP ROLE IF EXISTS `+testRole)
		_ = adb.Close()
	}
}

// The headline: a model without a tenant column cannot be migrated by accident.
func TestMigrateRefusesAModelThatIsNotTenantOwned(t *testing.T) {
	db, cleanup := openGormDB(t)
	defer cleanup()

	err := Migrate(context.Background(), db, &plainRow{})
	if !errors.Is(err, ErrNotScoped) {
		t.Fatalf("Migrate accepted an unscoped model: %v", err)
	}
	// The message has to name the struct, or the author can't act on it.
	if !strings.Contains(err.Error(), "plainRow") {
		t.Errorf("error does not name the offending model: %v", err)
	}
	// And it must not have created the table on the way to refusing.
	if db.Migrator().HasTable(plainTable) {
		t.Error("the table was created despite the refusal")
	}
}

// A tenant-owned model gets its table AND its policy from the same call, which is
// the property that makes the two impossible to get out of step.
func TestMigrateInstallsThePolicyWithTheTable(t *testing.T) {
	db, cleanup := openGormDB(t)
	defer cleanup()
	ctx := context.Background()

	if err := Migrate(ctx, db, &scopedRow{}); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	st, err := Check(ctx, sqlDB, scopedTable)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !st.Enabled || !st.Forced {
		t.Errorf("row security is not in force: %+v", st)
	}
	if st.Bypassed {
		t.Fatal("the test role bypasses row security, so this test proves nothing")
	}

	var notNull bool
	if err := sqlDB.QueryRowContext(ctx, `
		SELECT attnotnull FROM pg_attribute
		 WHERE attrelid = $1::regclass AND attname = $2`, scopedTable, Column).Scan(&notNull); err != nil {
		t.Fatalf("inspect column: %v", err)
	}
	if !notNull {
		t.Error("tenant_id is nullable — a row could exist belonging to no tenant")
	}
}

// Opting out has to be possible, but only by saying so.
func TestUnscopedMigratesWithoutAPolicy(t *testing.T) {
	db, cleanup := openGormDB(t)
	defer cleanup()
	ctx := context.Background()

	if err := Migrate(ctx, db, Unscoped(&plainRow{})); err != nil {
		t.Fatalf("Migrate(Unscoped): %v", err)
	}
	if !db.Migrator().HasTable(plainTable) {
		t.Fatal("the exempt table was not created")
	}
	sqlDB, _ := db.DB()
	st, err := Check(ctx, sqlDB, plainTable)
	if err != nil {
		t.Fatal(err)
	}
	if st.Enabled {
		t.Error("an explicitly unscoped table should not carry row security")
	}
}

// A write with no tenant must fail rather than write a row belonging to nobody.
func TestCreateWithoutATenantIsRefused(t *testing.T) {
	db, cleanup := openGormDB(t)
	defer cleanup()
	ctx := context.Background()
	if err := Migrate(ctx, db, &scopedRow{}); err != nil {
		t.Fatal(err)
	}

	err := db.WithContext(ctx).Create(&scopedRow{ID: "x1", Note: "orphan"}).Error
	if !errors.Is(err, ErrNoTenant) {
		t.Fatalf("a create with no tenant was not refused: %v", err)
	}
}

// The everyday path: the caller never mentions the tenant, and the row still
// lands in the right one.
func TestCreateInsideScopedFillsTheTenant(t *testing.T) {
	db, cleanup := openGormDB(t)
	defer cleanup()
	ctx := With(context.Background(), "acme")
	if err := Migrate(context.Background(), db, &scopedRow{}); err != nil {
		t.Fatal(err)
	}

	err := Scoped(ctx, db, func(tx *gorm.DB) error {
		return tx.Create(&scopedRow{ID: "a1", Note: "filled"}).Error
	})
	if err != nil {
		t.Fatalf("Scoped create: %v", err)
	}

	var got string
	if err := Scoped(ctx, db, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT tenant_id FROM ` + scopedTable + ` WHERE id = 'a1'`).Scan(&got).Error
	}); err != nil {
		t.Fatal(err)
	}
	if got != "acme" {
		t.Errorf("tenant_id = %q, want the context's tenant", got)
	}
}

// The guarantee, stated as a test: the same query returns one tenant's rows, the
// other's, or — unscoped — nothing at all.
func TestReadsSeeOnlyTheScopedTenant(t *testing.T) {
	db, cleanup := openGormDB(t)
	defer cleanup()
	if err := Migrate(context.Background(), db, &scopedRow{}); err != nil {
		t.Fatal(err)
	}

	for _, tenant := range []string{"acme", "globex"} {
		ctx := With(context.Background(), tenant)
		if err := Scoped(ctx, db, func(tx *gorm.DB) error {
			return tx.Create(&scopedRow{ID: tenant + "-1", Note: "row"}).Error
		}); err != nil {
			t.Fatalf("seed %s: %v", tenant, err)
		}
	}

	count := func(tenant string) int64 {
		t.Helper()
		var n int64
		ctx := With(context.Background(), tenant)
		if err := Scoped(ctx, db, func(tx *gorm.DB) error {
			// Deliberately no WHERE — this is the forgotten filter the policy is for.
			return tx.Table(scopedTable).Count(&n).Error
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count("acme"); n != 1 {
		t.Errorf("acme saw %d rows, want only its own", n)
	}
	if n := count("globex"); n != 1 {
		t.Errorf("globex saw %d rows, want only its own", n)
	}

	// Outside Scoped there is no tenant, so there is nothing to see.
	var n int64
	if err := db.Table(scopedTable).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("an unscoped read saw %d rows — the policy is not failing closed", n)
	}
}

// The maintenance escape has to actually reach every tenant — and, just as
// importantly, must not be reachable by ordinary scoped work.
func TestAcrossTenantsSeesEveryTenant(t *testing.T) {
	db, cleanup := openGormDB(t)
	defer cleanup()
	ctx := context.Background()
	if err := Migrate(ctx, db, &scopedRow{}); err != nil {
		t.Fatal(err)
	}
	for _, tenant := range []string{"acme", "globex"} {
		if err := Scoped(With(ctx, tenant), db, func(tx *gorm.DB) error {
			return tx.Create(&scopedRow{ID: tenant, Note: "row"}).Error
		}); err != nil {
			t.Fatal(err)
		}
	}

	var n int64
	if err := AcrossTenants(ctx, db, func(tx *gorm.DB) error {
		return tx.Table(scopedTable).Count(&n).Error
	}); err != nil {
		t.Fatalf("AcrossTenants: %v", err)
	}
	if n != 2 {
		t.Errorf("maintenance saw %d rows, want every tenant's 2", n)
	}

	// A tenant that happens to be named "*" must not get the same power by
	// accident — the escape is the function, not a value a caller can supply.
	if err := Scoped(With(ctx, "acme"), db, func(tx *gorm.DB) error {
		return tx.Table(scopedTable).Count(&n).Error
	}); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("a scoped read saw %d rows, want only its own", n)
	}
}

// Adoption, which is the case every existing service is in: rows already exist,
// and AutoMigrate cannot add a NOT NULL column to a populated table.
func TestBackfillAdoptsATableThatAlreadyHasRows(t *testing.T) {
	db, cleanup := openGormDB(t)
	defer cleanup()
	ctx := context.Background()

	// The "before" state: the table as it exists today, with no tenant column.
	if err := db.Exec(`CREATE TABLE ` + scopedTable + ` (id text primary key, note text)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO ` + scopedTable + ` (id, note) VALUES ('old1','before'), ('old2','before')`).Error; err != nil {
		t.Fatal(err)
	}

	// Migrating straight into a NOT NULL column is what fails without a backfill.
	if err := BackfillTenant(ctx, db, "default", scopedTable); err != nil {
		t.Fatalf("BackfillTenant: %v", err)
	}
	if err := Migrate(ctx, db, &scopedRow{}); err != nil {
		t.Fatalf("Migrate after backfill: %v", err)
	}

	// The pre-existing rows are now the default tenant's, and visible to it.
	var n int64
	if err := Scoped(With(ctx, "default"), db, func(tx *gorm.DB) error {
		return tx.Table(scopedTable).Count(&n).Error
	}); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("the default tenant sees %d of its adopted rows, want 2", n)
	}
	// And to nobody else.
	if err := Scoped(With(ctx, "other"), db, func(tx *gorm.DB) error {
		return tx.Table(scopedTable).Count(&n).Error
	}); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("another tenant sees %d adopted rows, want 0", n)
	}

	// Running it twice must be harmless — services call it on every boot.
	if err := BackfillTenant(ctx, db, "default", scopedTable); err != nil {
		t.Errorf("BackfillTenant is not idempotent: %v", err)
	}
}

// Verify is the backstop for what Migrate never saw: a table made by hand.
func TestVerifyCatchesATableCreatedBehindTheLibrarysBack(t *testing.T) {
	db, cleanup := openGormDB(t)
	defer cleanup()
	ctx := context.Background()
	if err := Migrate(ctx, db, &scopedRow{}); err != nil {
		t.Fatal(err)
	}

	rep, err := Verify(ctx, db)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !rep.Sound() {
		t.Fatalf("a properly migrated schema reported unsound: %v", rep.Problems())
	}

	// Now do it the way that slips past: raw DDL, no policy.
	if err := db.Exec(`CREATE TABLE ` + strayTable + ` (id text primary key, tenant_id text)`).Error; err != nil {
		t.Fatal(err)
	}
	rep, err = Verify(ctx, db)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if rep.Sound() {
		t.Error("Verify called the schema sound while an unprotected table sat in it")
	}
	if err := MustBeSound(ctx, db); err == nil {
		t.Error("MustBeSound would have let the service start")
	} else if !strings.Contains(err.Error(), strayTable) {
		t.Errorf("the error doesn't name the offending table: %v", err)
	}
}
