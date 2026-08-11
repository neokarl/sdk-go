package main

import (
	"fmt"
	"sort"
	"strings"
)

// renderLLMS produces a compact, agent-oriented guide from the same data as
// framework.json. Kept token-efficient: it points at the machine-readable
// artifacts for detail rather than duplicating them.
func renderLLMS(fw Framework, schema map[string]any) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# %s — framework guide for AI agents (v%s)\n\n", fw.Framework, fw.Version)
	b.WriteString("You are generating a plugin/service for this platform. A plugin ships a ")
	b.WriteString("ServiceManifest and optionally a gRPC service and/or a REST API. The platform ")
	b.WriteString("validates the manifest at load; obey the enforced rules below.\n\n")

	b.WriteString("## Manifest (what you declare)\n\n")
	if req, ok := schema["required"].([]string); ok {
		b.WriteString("Required: " + strings.Join(req, ", ") + "\n")
	}
	if props, ok := schema["properties"].(map[string]any); ok {
		names := make([]string, 0, len(props))
		for n := range props {
			names = append(names, n)
		}
		sort.Strings(names)
		b.WriteString("All fields: " + strings.Join(names, ", ") + "\n")
	}
	b.WriteString("Full JSON Schema (also the runtime validator): GET /manifest.schema.json\n\n")

	b.WriteString("## Platform services you can call\n\n")
	b.WriteString("The services currently installed (built-in or third-party) are discovered at\n")
	b.WriteString("runtime from the registry catalog: GET /api/v1/services. Each built-in service\n")
	b.WriteString("has an ergonomic Go client — e.g. import platform/services/user and call\n")
	b.WriteString("user.GetUser(ctx, id) after platform.Connect(platformURL).\n\n")

	b.WriteString("## Framework Go packages (import path — synopsis)\n\n")
	for _, p := range fw.GoPackages {
		fmt.Fprintf(&b, "- %s", p.ImportPath)
		if p.Doc != "" {
			fmt.Fprintf(&b, " — %s", firstLine(p.Doc))
		}
		b.WriteString("\n")
	}

	b.WriteString("\n## Rules (enforced)\n\n")
	for _, r := range fw.Rules {
		fmt.Fprintf(&b, "- [%s] %s (%s; enforced by %s)\n", r.Severity, r.Description, r.ID, r.EnforcedBy)
	}

	b.WriteString("\nFull machine-readable contract: GET /api/v1/framework\n")
	return b.String()
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
