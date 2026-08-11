// Package data is the gorm layer for tenant-scoped persistence.
//
// platform/sdk/tenancy gives the database-side guarantee: a row-security policy
// that makes a query which forgot its WHERE return nothing. That guarantee only
// applies to tables that actually carry a tenant column and actually got a
// policy — and "the author remembered" is exactly the kind of assumption row
// security exists to remove.
//
// So this package takes over the three places the column could go missing:
//
//   - Defining. Embed Owned and the column, its NOT NULL and its index
//     come with the model.
//   - Migrating. Migrate refuses a model that isn't tenant-owned, and installs
//     the policy in the same call — a table and its policy are created together
//     or not at all. Skipping is possible, but only by saying Unscoped out loud.
//   - Writing and reading. The Plugin fills tenant_id on insert from the
//     context, and Scoped establishes the session setting the policy reads.
//
// What is deliberately NOT here: a query callback adding `WHERE tenant_id = ?`.
// Filtering is the policy's job. Doing it twice would hide the case where the
// policy is missing, which is the case worth knowing about.
package tenancy

import "errors"

// ErrNoTenant means work that must be tenant-scoped had no tenant to scope to.
// It is deliberately an error rather than a silent default: a fallback tenant
// would make every boundary in the system decorative.
var ErrNoTenant = errors.New("tenancy: no tenant on the context")

// ErrNotScoped means a model reached Migrate without a tenant column. Wrap it in
// Unscoped if that is genuinely intended.
var ErrNotScoped = errors.New("tenancy: model is not tenant-owned")
