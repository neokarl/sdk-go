// Command full is a production-shaped platform service: authentication and
// authorization, tenant-isolated persistence, tracing, events, and calls to
// peer services — wired in the order they have to be wired.
//
//	OIDC_ISSUER=http://localhost:8081/realms/platform \
//	POSTGRES_DSN='postgres://app:app@localhost:5432/app?sslmode=disable' \
//	PLATFORM_URL=http://localhost:8080 \
//	go run ./examples/full
//
// Read this alongside examples/minimal: everything here is an addition to those
// twenty lines, and each addition is independent. You do not need all of it.
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"go.neokarl.com/sdk/auth"
	"go.neokarl.com/sdk/client"
	"go.neokarl.com/sdk/contracts"
	"go.neokarl.com/sdk/events"
	"go.neokarl.com/sdk/observability"
	"go.neokarl.com/sdk/service"
	"go.neokarl.com/sdk/tenancy"
)

// Item is tenant-owned: embedding tenancy.Owned brings the tenant_id column,
// its NOT NULL and its index with it, and makes the model eligible for the
// row-security policy that tenancy.Setup installs.
type Item struct {
	ID   string `gorm:"primaryKey"`
	Name string
	tenancy.Owned
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 1. Observability first, so everything after it is traced and logged
	//    consistently. This binary owns its process, so it installs the tracer
	//    provider globally; a library would not.
	logger := observability.NewLogger("inventory", "0.1.0", os.Getenv("ENV"))
	tracing, err := observability.NewTracing(ctx, observability.WithGlobal(observability.TracingConfig{
		ServiceName: "inventory",
		Endpoint:    os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
	}))
	if err != nil {
		return err
	}
	defer func() { _ = tracing.Close(context.Background()) }()

	// 2. Database. tenancy.Setup installs the GORM plugin that fills tenant_id
	//    on insert, migrates the models with their row-security policies, and
	//    then verifies the result — refusing to start if isolation is not
	//    actually in force. That last check is the one worth having: a
	//    superuser connection silently bypasses every policy you install.
	db, err := gorm.Open(postgres.Open(os.Getenv("POSTGRES_DSN")))
	if err != nil {
		return err
	}
	if err := tenancy.Setup(ctx, db, &Item{}); err != nil {
		return err
	}

	// 3. Authentication. The verifier performs OIDC discovery against the
	//    issuer, so this fails if the identity provider is not up yet.
	verifier, err := auth.New(ctx, auth.Config{
		IssuerURL:      os.Getenv("OIDC_ISSUER"),
		Audience:       "inventory",
		ResourceServer: "platform-api",
	})
	if err != nil {
		return err
	}

	// 4. Peer services. One client for the process; it holds a background
	//    catalog refresh, so close it.
	peers, err := client.New(ctx, os.Getenv("PLATFORM_URL"), client.WithLogger(logger))
	if err != nil {
		return err
	}
	defer func() { _ = peers.Close() }()

	// 5. Events. With no Redis address this is an in-process bus — fine for
	//    development, not durable across a restart.
	bus, err := events.New(events.Config{RedisAddr: os.Getenv("REDIS_ADDR")})
	if err != nil {
		return err
	}
	defer func() { _ = bus.Close() }()

	h := &handlers{db: db, bus: bus, peers: peers}

	// 6. The service. WithAuth installs authentication *and* authorization —
	//    the verifier implements both halves, so Requires below actually
	//    enforces something.
	svc := service.New(contracts.ServiceManifest{
		ID:              "inventory",
		Name:            "Inventory",
		Version:         "0.1.0",
		PlatformVersion: "^0.1.0",
		Type:            contracts.ServiceTypeAPI,
		APIBaseURL:      "http://inventory:8090",
		Permissions:     []string{"inventory.read", "inventory.write"},
		Events:          []string{"item.created"},
	},
		service.WithLogger(logger),
		service.WithTracing(tracing.Provider()),
		service.WithAuth(verifier),
		service.WithCORS("http://localhost:5173"),
	)

	svc.GET("item.list", "/api/v1/items", h.list, service.Requires("inventory.read"))
	svc.POST("item.create", "/api/v1/items", h.create, service.Requires("inventory.write"))

	return svc.Run(ctx, ":8090")
}

type handlers struct {
	db    *gorm.DB
	bus   events.Bus
	peers *client.Client
}

// list reads through tenancy.Scoped, which establishes the session setting the
// row-security policy compares against. A query outside Scoped sees nothing —
// that is the point.
func (h *handlers) list(c service.Context) error {
	var out []Item
	err := tenancy.Scoped(c.Ctx(), h.db, func(tx *gorm.DB) error {
		return tx.Find(&out).Error
	})
	if errors.Is(err, tenancy.ErrNoTenant) {
		// The caller's tenant could not be established. Never fall back to a
		// default: that turns the boundary into decoration.
		return service.Forbidden("no tenant for this caller")
	}
	if err != nil {
		return service.Internal("could not list items")
	}
	return service.OK(c, out)
}

func (h *handlers) create(c service.Context) error {
	var in Item
	if err := c.Bind(&in); err != nil {
		return service.BadRequest("body must be an item")
	}

	// No tenant_id is set here. The GORM plugin fills it from the context on
	// insert, and the policy's WITH CHECK rejects a row naming another tenant.
	if err := tenancy.Scoped(c.Ctx(), h.db, func(tx *gorm.DB) error {
		return tx.Create(&in).Error
	}); err != nil {
		return service.Internal("could not create item")
	}

	// Publishing is best-effort here. If losing an event would be a
	// correctness problem, write it to the outbox in the same transaction as
	// the row instead — see events.WriteOutbox.
	_ = h.bus.Publish(c.Ctx(), events.Event{
		Name:    "item.created",
		Source:  "inventory",
		Payload: map[string]any{"id": in.ID},
	})

	return service.Created(c, in)
}
