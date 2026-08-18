// Command minimal is the smallest complete platform service: a manifest, some
// routes, and Run.
//
//	go run ./examples/minimal
//	curl localhost:8090/api/v1/items
//	curl localhost:8090/service.manifest.json
//
// It runs with no dependencies at all — no database, no identity provider, no
// platform to register with. That is deliberate: the first thing you build
// should start.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/neokarl/sdk-go/contracts"
	"github.com/neokarl/sdk-go/service"
)

// Item is what this service serves. In a real service it would come from a
// database — see the crud example.
type Item struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

var items = map[string]Item{
	"1": {ID: "1", Name: "Widget"},
	"2": {ID: "2", Name: "Sprocket"},
}

func main() {
	// The manifest is this service's contract with the platform: who it is,
	// what it targets, and where to reach it. The SDK serves it at
	// /service.manifest.json and appends each route you register, so the
	// declared API and the implementation cannot drift.
	manifest := contracts.ServiceManifest{
		ID:              "inventory",
		Name:            "Inventory",
		Version:         "0.1.0",
		PlatformVersion: "^0.1.0",
		Type:            contracts.ServiceTypeAPI,
		APIBaseURL:      "http://localhost:8090",
		Description:     "The smallest useful platform service.",
	}

	// WithoutAuth says out loud that this service enforces nothing. A service
	// with no auth configured cannot register a route declaring Requires — it
	// panics at startup rather than serving a scope it does not check. See the
	// full example for the real thing.
	svc := service.New(manifest,
		service.WithoutAuth(),
		service.WithCORS("http://localhost:5173"),
	)

	svc.GET("item.list", "/api/v1/items", func(c service.Context) error {
		out := make([]Item, 0, len(items))
		for _, it := range items {
			out = append(out, it)
		}
		return service.OK(c, out)
	})

	svc.GET("item.get", "/api/v1/items/:id", func(c service.Context) error {
		item, ok := items[c.Param("id")]
		if !ok {
			// Returning a platform error renders the standard envelope with the
			// right status — here, 404.
			return service.NotFound("no item with that id")
		}
		return service.OK(c, item)
	})

	svc.POST("item.create", "/api/v1/items", func(c service.Context) error {
		var in Item
		if err := c.Bind(&in); err != nil {
			return service.BadRequest("body must be an item")
		}
		if in.ID == "" {
			return service.BadRequest("id is required")
		}
		items[in.ID] = in
		return service.Created(c, in)
	})

	// The caller owns signal handling, so this service can also be embedded in
	// a process that runs other things.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := svc.Run(ctx, ":8090"); err != nil {
		log.Fatal(err)
	}
}
