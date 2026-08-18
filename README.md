# Platform Go SDK

Build a service that runs on the platform: it declares what it offers, serves
it over REST and gRPC, authenticates its callers, keeps each tenant's data
apart, and talks to its peers without hardcoding a single URL.

```bash
go get go.neokarl.com/sdk
```

Requires Go 1.25.4 or later.

## The smallest service that works

```go
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"go.neokarl.com/sdk/contracts"
	"go.neokarl.com/sdk/service"
)

func main() {
	svc := service.New(contracts.ServiceManifest{
		ID:              "inventory",
		Name:            "Inventory",
		Version:         "0.1.0",
		PlatformVersion: "^0.1.0",
		Type:            contracts.ServiceTypeAPI,
		APIBaseURL:      "http://localhost:8090",
	}, service.WithoutAuth())

	svc.GET("item.list", "/api/v1/items", func(c service.Context) error {
		return service.OK(c, []string{"widget", "sprocket"})
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := svc.Run(ctx, ":8090"); err != nil {
		log.Fatal(err)
	}
}
```

That is a complete service. It serves your route, `/healthz`, `/readyz`, and
`/service.manifest.json` — the endpoint an operator points the platform at to
install you. Registering a route also declares it in that manifest, so what you
advertise and what you serve cannot drift apart.

Run it: `go run ./examples/minimal`. For the production-shaped version with
auth, a database and peer calls, see [`examples/full`](examples/full).

## What's in the box

One package per concern. Take what you need; nothing here requires anything
else except `contracts`.

| Package | What it's for |
| --- | --- |
| [`service`](service) | Serve REST and gRPC with the platform conventions built in |
| [`auth`](auth) | Verify who is calling, and whether they may |
| [`tenancy`](tenancy) | Keep tenants' data apart, enforced by the database |
| [`events`](events) | Publish and consume domain events |
| [`workflow`](workflow) | Run work that has to survive a restart |
| [`client`](client) | Call peer services by name, not by URL |
| [`contracts`](contracts) | The manifest and wire types |
| [`errors`](errors) | The platform error taxonomy |
| [`observability`](observability) | Structured logs and OpenTelemetry traces |

## Three things worth knowing early

**Authorization is one call, and it fails closed.** `service.WithAuth(verifier)`
installs authentication *and* authorization together. A route declaring
`service.Requires("inventory.read")` on a service that configured neither
`WithAuth` nor `WithoutAuth` panics at startup — a declared permission that
silently checks nothing is worse than no permission at all.

```go
svc := service.New(manifest, service.WithAuth(verifier))
svc.GET("item.get", "/api/v1/items/:id", h.get, service.Requires("inventory.read"))
```

**Tenant isolation is the database's job, not your queries'.**
`tenancy.Setup` migrates your models, installs a row-level-security policy per
table, and then verifies the result — refusing to start if isolation is not
actually in force. A query that forgets to filter returns nothing rather than
everything.

```go
if err := tenancy.Setup(ctx, db, &Item{}); err != nil {
	return err
}

// Inside a handler: the policy sees the tenant, so this cannot cross the boundary.
err := tenancy.Scoped(c.Ctx(), db, func(tx *gorm.DB) error {
	return tx.Find(&items).Error
})
```

**You call peers by operation, not by URL.** The platform catalog resolves
`(service, operation)` to a method and path, so a peer can move or rename a
route without breaking you.

```go
c, err := client.New(ctx, platformURL)
defer c.Close()

item, err := client.InvokeData[Item](ctx, c, client.Call{
	Service: "inventory",
	Op:      "item.get",
	Path:    map[string]string{"id": id},
})
```

## Documentation

- **Guides and tutorials** — https://go.neokarl.com/docs
- **API reference** — https://pkg.go.dev/go.neokarl.com/sdk
- **For LLM agents** — [`llms.txt`](https://go.neokarl.com/llms.txt), and
  `llms-full.txt` for the whole corpus in one file

## Development

```bash
go test ./...                              # unit tests
docker compose -f docker-compose.test.yml up -d
TEST_POSTGRES_DSN='postgres://sdk:sdk@localhost:55432/sdk?sslmode=disable' \
TEST_REDIS_ADDR=localhost:56379 \
  go test ./...                            # including the integration tests
golangci-lint run
```

The tenancy and events integration tests skip without those services. They
cover row-level tenant isolation and the durable event bus — the two things
most worth covering — so run them before changing either package.

## Contributing and security

Report vulnerabilities privately: see [SECURITY.md](SECURITY.md). Changes are
recorded in [CHANGELOG.md](CHANGELOG.md).

## License

Apache 2.0 — see [LICENSE](LICENSE).
