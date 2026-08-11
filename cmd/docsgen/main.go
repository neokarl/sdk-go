// Command docsgen generates the platform's machine-readable framework
// descriptor from the code itself — the single source of truth for plugin
// authors and AI agents. It emits three artifacts into -out:
//
//   - manifest.schema.json — JSON Schema for the plugin ServiceManifest
//     (reflected from contracts.ServiceManifest; doubles as the runtime validator)
//   - framework.json       — the aggregate descriptor: gRPC services, Go package
//     reference, enforced rules, framework version
//   - llms.txt             — a compact, agent-oriented guide over the same data
//
// Output is deterministic (no timestamps, everything sorted) so a CI regen +
// git-diff check can guarantee the docs never drift from the code.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"platform/sdk/contracts"
)

// Framework is the aggregate descriptor written to framework.json.
type Framework struct {
	Framework      string           `json:"framework"`
	Version        string           `json:"version"`
	ManifestSchema string           `json:"manifestSchemaRef"`
	GoPackages     []GoPackage      `json:"goPackages"`
	Rules          []contracts.Rule `json:"rules"`
}

func main() {
	out := flag.String("out", "internal/framework/data", "output directory for generated artifacts")
	pkgRoot := flag.String("pkgroot", "../platform/sdk/go", "root directory of the framework Go packages")
	flag.Parse()

	if err := os.MkdirAll(*out, 0o755); err != nil {
		fatal(err)
	}

	schema := manifestSchema()
	if err := writeJSON(filepath.Join(*out, "manifest.schema.json"), schema); err != nil {
		fatal(err)
	}

	fw := Framework{
		Framework:      "platform/sdk",
		Version:        contracts.Version,
		ManifestSchema: "manifest.schema.json",
		GoPackages:     collectGoPackages(*pkgRoot),
		Rules:          contracts.Rules(),
	}
	if err := writeJSON(filepath.Join(*out, "framework.json"), fw); err != nil {
		fatal(err)
	}

	if err := os.WriteFile(filepath.Join(*out, "llms.txt"), []byte(renderLLMS(fw, schema)), 0o644); err != nil {
		fatal(err)
	}

	fmt.Printf("docsgen: wrote manifest.schema.json, framework.json, llms.txt to %s\n", *out)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "docsgen:", err)
	os.Exit(1)
}
