# Security

This module ships code that makes security decisions: OIDC token verification
(`auth`), mutual-TLS credentials (`service`, `client`), and PostgreSQL
row-level-security policies for tenant isolation (`tenancy`). Treat bugs in
those paths as security issues.

## Reporting a vulnerability

Report privately — do not open a public issue. Use GitHub's private
vulnerability reporting on this repository, or email the maintainers.

Include the affected version, the package, and a reproduction if you have one.
Expect an acknowledgement within a few business days.

## What counts

In scope, roughly in order of severity:

- Tenant isolation bypass — any way for a request scoped to tenant A to read or
  write tenant B's rows, including a `tenancy` policy that doesn't apply, a
  scope that isn't set, or a migration that leaves RLS off.
- Authentication bypass — a token accepted that should not be, signature or
  issuer or audience or expiry checks that can be skipped.
- Authorization bypass — a route declaring `Requires(...)` that serves a caller
  lacking the scope.
- Identity spoofing across a service hop — a caller controlling the identity
  another service sees.
- mTLS weaknesses — accepting an unverified peer certificate, or a downgrade.

## Design notes worth knowing

Two properties are deliberate and are not bugs:

- `tenancy` enforces isolation **in the database**, via RLS policies, not by
  appending `WHERE tenant_id` in application code. The application setting the
  scope is a convenience; the policy is the boundary. `tenancy.MustBeSound`
  exists so a service refuses to start if that boundary is missing, and it is
  intended to run at boot in every environment.
- Tenant and user headers on an inbound HTTP request are **advisory**. They are
  read for logging and for defaulting, and are never trusted for access control.
  The authenticated identity comes from the verified token only.
