// Package sdk is the Go SDK for building services on the platform.
//
// A service is a self-contained backend that declares what it offers in a
// manifest, serves it over REST and/or gRPC, and is discovered and called by the
// platform and by other services. This module gives you the pieces to build one,
// organised one package per concern:
//
//	service       serve HTTP and gRPC, with the platform conventions built in
//	auth          authenticate the caller and authorize the operation
//	tenancy       scope data to a tenant, and make the database enforce it
//	events        publish and consume domain events
//	workflow      run work that must survive a restart
//	client        call other services by (service, operation) rather than URL
//	contracts     the manifest and wire types that define the contract
//	errors        the platform error taxonomy
//	observability structured logging and OpenTelemetry tracing
//
// # Getting started
//
// The smallest useful service is a manifest, a route, and Run:
//
//	func main() {
//	    svc := service.New(contracts.ServiceManifest{
//	        ID:              "inventory",
//	        Name:            "Inventory",
//	        Version:         "0.1.0",
//	        PlatformVersion: "^0.1.0",
//	        Type:            contracts.ServiceTypeAPI,
//	        APIBaseURL:      "http://inventory:8090",
//	    })
//	    svc.GET("item.list", "/api/v1/items", listItems)
//
//	    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
//	    defer stop()
//	    if err := svc.Run(ctx, ":8090"); err != nil {
//	        log.Fatal(err)
//	    }
//	}
//
// Each package's own documentation covers its concern in full. Start with
// [github.com/neokarl/sdk-go/service].
//
// # Versioning
//
// [Version] is the version of this module. It is deliberately distinct from
// [github.com/neokarl/sdk-go/contracts.Version], which is the version of the platform
// *contract* — the manifest shape and the rules a service must satisfy. A
// service declares which contract it targets via its manifest's
// PlatformVersion; the SDK version is just which release of this code you build
// against.
package sdk
