package tenancy

import (
	"fmt"
	"reflect"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// Plugin fills tenant_id on insert from the context's tenant.
//
// Without it, every create is a place someone can forget, and a forgotten one
// either writes a NULL or — worse, under a policy — is rejected at the database
// with an error that says nothing about tenancy. With it, a tenant-owned model
// inserted inside Scoped simply gets the right value.
//
// It refuses rather than guesses. A create with no tenant on the context fails
// with ErrNoTenant, and a create naming a *different* tenant than the context
// fails too: that is either a bug or an attempt to write across the boundary,
// and the policy's WITH CHECK would reject it a moment later anyway — better to
// say so in Go, where the message can explain itself.
type Plugin struct{}

func (Plugin) Name() string { return "platform:tenancy" }

func (Plugin) Initialize(db *gorm.DB) error {
	return db.Callback().Create().Before("gorm:create").Register("tenancy:fill", fill)
}

func fill(db *gorm.DB) {
	if db.Error != nil || db.Statement == nil || db.Statement.Schema == nil {
		return
	}
	field := db.Statement.Schema.LookUpField(Column)
	if field == nil {
		return // not a tenant-owned table; nothing to fill
	}

	ctx := db.Statement.Context
	tenant, ok := From(ctx)
	if !ok {
		db.AddError(ErrNoTenant)
		return
	}

	rv := db.Statement.ReflectValue
	switch rv.Kind() {
	case reflect.Slice, reflect.Array: // batch insert
		for i := 0; i < rv.Len(); i++ {
			if err := setTenant(db, field, rv.Index(i), tenant); err != nil {
				db.AddError(err)
				return
			}
		}
	case reflect.Struct:
		if err := setTenant(db, field, rv, tenant); err != nil {
			db.AddError(err)
		}
	}
}

func setTenant(db *gorm.DB, field *schema.Field, rv reflect.Value, tenant string) error {
	ctx := db.Statement.Context
	if current, zero := field.ValueOf(ctx, rv); !zero {
		if s, ok := current.(string); ok && s != "" {
			if s != tenant {
				return fmt.Errorf("tenancy: refusing to write a row for tenant %q while scoped to %q", s, tenant)
			}
			return nil // already correct; leave it alone
		}
	}
	return field.Set(ctx, rv, tenant)
}
