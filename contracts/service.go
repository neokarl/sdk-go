package contracts

import "time"

// ServiceType matches BACKEND.md service taxonomy.
type ServiceType string

const (
	ServiceTypeUI     ServiceType = "ui"
	ServiceTypeAPI    ServiceType = "api"
	ServiceTypeHybrid ServiceType = "hybrid"
)

// NavItem mirrors a single service-contributed navigation entry.
type NavItem struct {
	Label string `json:"label"`
	Path  string `json:"path"`
	Icon  string `json:"icon,omitempty"`
	Order int    `json:"order,omitempty"`
}

// RouteSpec mirrors a single service-contributed route.
type RouteSpec struct {
	Path      string `json:"path"`
	Component string `json:"component"`
}

// APISpec is one HTTP operation a service's backend exposes. Consumers
// reference these by `(serviceId, id)` through the service registry rather
// than hardcoding URLs — so an API owner can move a path or rename a query
// parameter without breaking peers, provided the operation id and shape
// stay stable.
//
// `Path` is a template; `{param}` segments stand for path parameters that
// the consumer passes in at call time. `Request` / `Response` are free-
// form JSON Schema fragments — kept loose intentionally so authors can
// document operations incrementally without being blocked by full schemas.
type APISpec struct {
	// ID is unique within this service's catalog (e.g. "asset.get",
	// "finding.list"). Convention: dot-separated, lowerCamel segments.
	ID string `json:"id"`
	// Method is the HTTP verb (uppercase: GET/POST/PATCH/PUT/DELETE).
	Method string `json:"method"`
	// Path is the URL template relative to the service's apiBaseUrl,
	// e.g. "/api/v1/assets/{id}".
	Path string `json:"path"`
	// Summary / Description power the Swagger-style admin viewer.
	Summary     string `json:"summary,omitempty"`
	Description string `json:"description,omitempty"`
	// Optional schemas for the request body and response. Treated as
	// opaque JSON for now — the viewer pretty-prints them.
	Request  map[string]any `json:"request,omitempty"`
	Response map[string]any `json:"response,omitempty"`
}

// ServiceManifest is the wire format services ship to the platform. Identical
// shape to the frontend's ServiceManifest type — backend is the source of
// truth, the frontend reads it.
type ServiceManifest struct {
	ID              string      `json:"id" jsonschema:"required"`
	Name            string      `json:"name" jsonschema:"required"`
	Version         string      `json:"version" jsonschema:"required"`
	Type            ServiceType `json:"type,omitempty"`
	PlatformVersion string      `json:"platformVersion" jsonschema:"required"`
	// Entry is conditionally required (ui/hybrid services only), so it is not
	// marked required here — the rule manifest.entry.conditional enforces it.
	Entry       string      `json:"entry"`
	Description string      `json:"description,omitempty"`
	Permissions []string    `json:"permissions,omitempty"`
	Events      []string    `json:"events,omitempty"`
	Navigation  []NavItem   `json:"navigation,omitempty"`
	Routes      []RouteSpec `json:"routes,omitempty"`
	// APIBaseURL is the absolute root URL the service's backend serves
	// from (e.g. "http://engagements:8080"). APIs[].Path is appended to
	// it at invocation time. Empty for pure-UI services that don't run
	// a backend.
	APIBaseURL string `json:"apiBaseUrl,omitempty"`
	// APIs is the service's stable operation catalog. Empty for UI-only
	// services. The service registry exposes these by (serviceId, id) so
	// callers never need to know URL paths.
	APIs []APISpec `json:"apis,omitempty"`
	// GRPCAddress is the host:port the service's gRPC server listens on for
	// service-to-service calls (e.g. "engagements:9090"). Empty for services
	// that expose no gRPC surface. The servicekit dialer resolves a service id
	// to this address from the manifest catalog. gRPC is backend-only — the
	// browser still calls the HTTP APIs above.
	GRPCAddress string `json:"grpcAddress,omitempty"`
	// GRPCServices lists the fully-qualified gRPC service names this service
	// serves (e.g. "platform.user.v1.UserService") for discovery/introspection.
	GRPCServices []string `json:"grpcServices,omitempty"`
	// Authz is the service's authorization contract: the actions (scopes) it
	// defines and its default roles. On install the platform provisions these
	// into the PDP (Keycloak Authorization Services), so a service's roles and
	// permissions come into existence with it. Admins then assign roles to users
	// and may tune grants; nothing is hand-created in Keycloak.
	Authz *ServiceAuthz `json:"authz,omitempty"`
}

// ServiceAuthz is a service's declared authorization model.
type ServiceAuthz struct {
	// Scopes are the actions this service defines, e.g. "engagement:read".
	Scopes []string `json:"scopes,omitempty"`
	// Roles are the default roles the service ships, each granting a subset of
	// Scopes. Provisioned as realm roles + Authorization Services permissions.
	Roles []AuthzRole `json:"roles,omitempty"`
}

// AuthzRole is a default role a service declares.
type AuthzRole struct {
	Name        string   `json:"name" jsonschema:"required"`
	Description string   `json:"description,omitempty"`
	// Grants is the subset of the service's Scopes this role is permitted.
	Grants []string `json:"grants,omitempty"`
}

// ServiceStage is the lifecycle position the platform tracks for each service.
type ServiceStage string

const (
	StageDiscovered ServiceStage = "discovered"
	StageValidated  ServiceStage = "validated"
	StageEnabled    ServiceStage = "enabled"
	StageDisabled   ServiceStage = "disabled"
	StageFailed     ServiceStage = "failed"
)

// ServiceRecord is the registry-stored view: manifest + lifecycle state.
type ServiceRecord struct {
	Manifest    ServiceManifest `json:"manifest"`
	Stage       ServiceStage    `json:"stage"`
	Enabled     bool            `json:"enabled"`
	InstalledAt time.Time       `json:"installedAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
	LastError   string          `json:"lastError,omitempty"`
	TenantID    string          `json:"tenantId,omitempty"`
}

// ManifestRegistry is the response shape the frontend consumes at
// /api/v1/services/manifests — the shell's loader reads `services` from it.
type ManifestRegistry struct {
	Services []ServiceManifest `json:"services"`
}
