# Changelog

All notable changes to this module are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

First release prepared for external use. Everything below is relative to the
pre-release code that lived inside the platform application repository, so all
of it is breaking — there are no published versions to be compatible with.

### Changed

- **Module path is now `github.com/neokarl/sdk-go`.** It was `platform/sdk`, a
  dot-less path the Go module proxy cannot resolve, which meant every consumer
  needed a filesystem `replace` directive. Imports become
  `github.com/neokarl/sdk-go/service` and so on.
- **Minimum Go version is 1.25.4**, down from 1.26.2. This is the floor imposed
  by `go.temporal.io/sdk`; nothing in this module requires it directly.
- `middleware` and `transport` moved under `internal/`. Every middleware and
  interceptor they provide is installed automatically by `service` and
  `client`; the values they put on the context are now read through the package
  that owns each one (see below).
- `logger` and `telemetry` merged into `observability`.
- `mtls` dissolved into the packages that use it: `service.WithMTLS` for the
  server side, `client.WithMTLS` for the dial side, and
  `auth.PeerServiceFrom` for reading the verified peer service from a context.
- Context accessors follow one convention, `XFrom(ctx)` / `WithX(ctx, v)`, with
  a single owner per value: `auth` owns the identity, `tenancy` owns the
  tenant, `observability` owns the request id and logger.
- Lifecycle follows one convention: `Start` never blocks, `Run(ctx, …)` blocks
  until the context is done, and anything long-lived has `Close`.
- `service.Run` no longer installs its own signal handler. It takes a context,
  so a service can be embedded in a process that already handles signals.

### Added

- `service.WithAuth` installs authentication and authorization together. This
  replaces `service.WithAuthorizer`, which enabled only half the pairing and
  silently 403'd every scoped route if you did not also install the
  authentication middleware by hand.
- `service.WithoutAuth`, an explicit opt-out. A route declaring `Requires(...)`
  with neither option set now panics at registration rather than serving
  unguarded.
- `tenancy.Setup`, which installs the GORM plugin, migrates, and verifies RLS
  soundness in the correct order. The plugin installation step was previously
  mandatory and undocumented.
- `client.New` returns an explicit client with `Close`, replacing the
  package-level `Connect` singleton and its uncancellable refresh goroutine.

### Removed

- `lock`. It had no callers and its Redis implementation was untested. It will
  return if a use case does.
- `cmd/docsgen` and `cmd/certgen`. Both were tooling for the platform
  application, not SDK API, and both had that application's paths and service
  names baked into their defaults.
- `workflow.ErrNoHandler` and `temporal.ErrNoQueue`, two sentinel errors that
  were declared but never returned, so `errors.Is` against them never matched.

### Fixed

- `service.New` no longer defaults CORS to `*`.
- `auth` no longer disables audience validation silently when no audience is
  configured; use `auth.WithoutAudienceCheck` to opt out explicitly.
- Tracing no longer overwrites the process-global OpenTelemetry provider as a
  side effect of initialisation.
