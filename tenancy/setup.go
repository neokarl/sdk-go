package tenancy

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// Setup makes a database connection tenant-safe. Call it once at startup,
// before serving:
//
//	db, err := gorm.Open(postgres.Open(dsn))
//	if err != nil {
//	    return err
//	}
//	if err := tenancy.Setup(ctx, db, &Item{}, &Category{}); err != nil {
//	    return err
//	}
//
// It does the three things that must all happen, in the order they must happen
// in, and fails if any of them cannot:
//
//  1. Installs the GORM plugin that fills tenant_id on insert from the context.
//     Miss this step and writes land with an empty tenant.
//  2. Migrates the models and installs their row-security policies together, so
//     a table and its policy are created in the same call or not at all. A
//     model that is not tenant-owned is rejected by name — pass
//     [Unscoped] to exempt one deliberately.
//  3. Verifies the result: policies present, row security forced, and the
//     connected role not bypassing it. A service whose isolation is decorative
//     refuses to start rather than running and looking fine.
//
// Step 3 is why this is worth calling even when you already migrate elsewhere.
// A superuser connection silently bypasses every policy you install, and the
// only symptom is that isolation quietly does nothing.
func Setup(ctx context.Context, db *gorm.DB, models ...any) error {
	if db == nil {
		return fmt.Errorf("tenancy: Setup needs a database handle")
	}
	if err := db.Use(Plugin{}); err != nil {
		return fmt.Errorf("tenancy: install plugin: %w", err)
	}
	if err := Migrate(ctx, db, models...); err != nil {
		return err
	}
	return MustBeSound(ctx, db)
}
