package main

import (
	"go/ast"
	"go/doc"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

// frameworkGoPackages is the public Go surface documented in the reference.
// The generated proto clients (api/*) are covered by the gRPC section instead,
// and cmd/* is tooling, so neither is listed here.
var frameworkGoPackages = []string{
	"service", "client", "transport", "telemetry", "auth", "mtls", "events", "contracts", "errors", "logger", "middleware",
	"workflow", "workflow/temporal", "lock",
	"tenancy",
}

// GoSymbol is one exported declaration (const group, type, or func).
type GoSymbol struct {
	Name      string `json:"name"`
	Doc       string `json:"doc,omitempty"`
	Signature string `json:"signature,omitempty"`
}

// GoPackage is the extracted reference for one framework Go package.
type GoPackage struct {
	ImportPath string     `json:"importPath"`
	Doc        string     `json:"doc,omitempty"`
	Consts     []GoSymbol `json:"consts,omitempty"`
	Types      []GoSymbol `json:"types,omitempty"`
	Funcs      []GoSymbol `json:"funcs,omitempty"`
}

// collectGoPackages extracts the API reference for each framework package under
// pkgRoot using go/doc. Deterministic (sorted) output.
func collectGoPackages(pkgRoot string) []GoPackage {
	var out []GoPackage
	for _, name := range frameworkGoPackages {
		if gp := parseGoPackage(filepath.Join(pkgRoot, name), "platform/sdk/"+name); gp != nil {
			out = append(out, *gp)
		}
	}
	return out
}

func parseGoPackage(dir, importPath string) *GoPackage {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ParseComments)
	if err != nil || len(pkgs) == 0 {
		return nil
	}
	names := make([]string, 0, len(pkgs))
	for n := range pkgs {
		names = append(names, n)
	}
	sort.Strings(names)
	astPkg := pkgs[names[0]]

	dpkg := doc.New(astPkg, importPath, 0)
	gp := &GoPackage{ImportPath: importPath, Doc: strings.TrimSpace(dpkg.Doc)}
	for _, c := range dpkg.Consts {
		gp.Consts = append(gp.Consts, GoSymbol{
			Name: strings.Join(c.Names, ", "), Doc: strings.TrimSpace(c.Doc),
			Signature: render(fset, c.Decl),
		})
	}
	for _, t := range dpkg.Types {
		gp.Types = append(gp.Types, GoSymbol{
			Name: t.Name, Doc: strings.TrimSpace(t.Doc), Signature: render(fset, t.Decl),
		})
	}
	for _, f := range dpkg.Funcs {
		gp.Funcs = append(gp.Funcs, GoSymbol{
			Name: f.Name, Doc: strings.TrimSpace(f.Doc), Signature: funcSig(fset, f.Decl),
		})
	}
	sort.Slice(gp.Consts, func(a, b int) bool { return gp.Consts[a].Name < gp.Consts[b].Name })
	sort.Slice(gp.Types, func(a, b int) bool { return gp.Types[a].Name < gp.Types[b].Name })
	sort.Slice(gp.Funcs, func(a, b int) bool { return gp.Funcs[a].Name < gp.Funcs[b].Name })
	return gp
}

func render(fset *token.FileSet, node any) string {
	var b strings.Builder
	_ = printer.Fprint(&b, fset, node)
	return b.String()
}

// funcSig renders a function's signature without its body.
func funcSig(fset *token.FileSet, decl *ast.FuncDecl) string {
	body := decl.Body
	decl.Body = nil
	s := render(fset, decl)
	decl.Body = body
	return s
}
