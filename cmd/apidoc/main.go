// Command apidoc extracts this module's public API into a JSON document, so a
// documentation site can render real signatures instead of hand-maintaining a
// table that drifts.
//
//	go run ./cmd/apidoc -out api.json
//
// Output is deterministic — everything sorted, no timestamps, no paths from the
// generating machine — so a regenerate-and-diff check can guarantee the
// published reference matches the code.
//
// It walks the module rather than consulting a list of packages to document.
// The list was the previous design and it went stale in exactly the way you
// would expect: packages were deleted and the reference kept describing them.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// modulePath is this module. Import paths in the output are built from it.
const modulePath = "github.com/neokarl/sdk-go"

// API is the whole document.
type API struct {
	Module   string    `json:"module"`
	Version  string    `json:"version"`
	Packages []Package `json:"packages"`
}

func main() {
	out := flag.String("out", "api.json", "output file, or - for stdout")
	root := flag.String("root", ".", "module root to walk")
	version := flag.String("version", "", "module version to record (defaults to sdk.Version)")
	flag.Parse()

	pkgs, err := collect(*root)
	if err != nil {
		fatal(err)
	}
	if len(pkgs) == 0 {
		fatal(fmt.Errorf("no packages found under %s — is this the module root?", *root))
	}

	v := *version
	if v == "" {
		v = readVersion(*root)
	}

	doc := API{Module: modulePath, Version: v, Packages: pkgs}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		fatal(err)
	}
	b = append(b, '\n')

	if *out == "-" {
		_, _ = os.Stdout.Write(b)
	} else if err := os.WriteFile(*out, b, 0o644); err != nil {
		fatal(err)
	}
	fmt.Fprintf(os.Stderr, "apidoc: %d packages → %s\n", len(pkgs), *out)
}

// skip reports whether a directory is outside the documented surface.
//
// internal/ is unreachable by consumers, cmd/ is tooling, examples/ is prose
// that happens to compile, and testdata is Go-reserved.
func skip(rel string) bool {
	if rel == "." {
		return false
	}
	for _, seg := range strings.Split(rel, string(filepath.Separator)) {
		switch {
		case seg == "internal", seg == "cmd", seg == "examples", seg == "testdata":
			return true
		case strings.HasPrefix(seg, "."), strings.HasPrefix(seg, "_"):
			return true
		}
	}
	return false
}

// readVersion pulls the Version constant out of sdk.go without importing the
// package, so apidoc stays a leaf that cannot be broken by the code it reads.
func readVersion(root string) string {
	b, err := os.ReadFile(filepath.Join(root, "sdk.go"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if _, rest, ok := strings.Cut(line, "const Version = "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`)
		} else {
			_ = rest
		}
	}
	return ""
}

func collect(root string) ([]Package, error) {
	var out []Package
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		if skip(rel) {
			if rel != "." {
				return filepath.SkipDir
			}
			return nil
		}
		importPath := modulePath
		if rel != "." {
			importPath = modulePath + "/" + filepath.ToSlash(rel)
		}
		p, perr := parsePackage(path, importPath)
		if perr != nil {
			return fmt.Errorf("%s: %w", rel, perr)
		}
		if p != nil {
			out = append(out, *p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ImportPath < out[b].ImportPath })
	return out, nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "apidoc:", err)
	os.Exit(1)
}
