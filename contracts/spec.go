package contracts

// Version is the semantic version of the platform framework contract — the
// public `go.neokarl.com/sdk` surface that plugins build against. It is distinct from
// a service's own version and from the running platform binary version; it is
// the version the generated framework docs describe.
const Version = "0.1.0"

// Rule is one enforced constraint of the framework contract. The generated docs
// list rules built from Rules() — a rule appears in the docs only because
// something enforces it, named in EnforcedBy. Keep this list in step with the
// checks; the docs are only as honest as this registry.
type Rule struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Severity    string `json:"severity"` // "error" | "warning"
	EnforcedBy  string `json:"enforcedBy"`
}

// Rules returns the active framework rules that a plugin/service must satisfy.
func Rules() []Rule {
	return []Rule{
		{
			ID:          "manifest.required-fields",
			Description: "id, name, version and platformVersion are required on every manifest.",
			Severity:    "error",
			EnforcedBy:  "registry.Service.validate + manifest.schema.json",
		},
		{
			ID:          "manifest.platformVersion.semver",
			Description: "platformVersion must be a caret range (e.g. ^1.0.0) that the running platform satisfies.",
			Severity:    "error",
			EnforcedBy:  "registry.Service.validate",
		},
		{
			ID:          "manifest.entry.conditional",
			Description: "entry (the frontend remote) is required for ui/hybrid services; api-only services omit it.",
			Severity:    "error",
			EnforcedBy:  "registry.Service.validate",
		},
		{
			ID:          "manifest.apis.shape",
			Description: "when apis are declared, apiBaseUrl is required and every operation needs a unique id, an allowed HTTP method, and a path template rooted at '/'.",
			Severity:    "error",
			EnforcedBy:  "registry.validateAPIs",
		},
		{
			ID:          "data.tenancy.scoped",
			Description: "every table a service migrates must carry a tenant_id and its isolation policy; opting out requires tenancy.Unscoped, not omission.",
			Severity:    "error",
			EnforcedBy:  "tenancy.Migrate (refuses a model that isn't tenancy.Owned) + tenancy.MustBeSound at boot",
		},
		{
			ID:          "json.non-nil-slices",
			Description: "handlers must return []T{} (never nil) so empty collections serialize as [] not null, or a frontend .map() crashes.",
			Severity:    "error",
			EnforcedBy:  "serviceregistry convention",
		},
	}
}
