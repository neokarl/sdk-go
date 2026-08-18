package main

import (
	"go/ast"
	"go/doc"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"sort"
	"strings"
)

// Symbol is one exported declaration.
type Symbol struct {
	Name      string `json:"name"`
	Doc       string `json:"doc,omitempty"`
	Signature string `json:"signature,omitempty"`
}

// Type is an exported type together with the functions that construct it and
// the methods hanging off it.
//
// go/doc already groups these — constructors returning T are filed under T
// rather than in the package's function list, and methods live on the type. The
// previous extractor read neither, which is why its reference showed a dozen
// package-level functions for `service` and none of the methods you actually
// call on a Service.
type Type struct {
	Name      string   `json:"name"`
	Doc       string   `json:"doc,omitempty"`
	Signature string   `json:"signature,omitempty"`
	Funcs     []Symbol `json:"funcs,omitempty"`   // constructors returning this type
	Methods   []Symbol `json:"methods,omitempty"` // methods on this type
}

// Package is the extracted reference for one package.
type Package struct {
	ImportPath string   `json:"importPath"`
	Name       string   `json:"name"`
	Doc        string   `json:"doc,omitempty"`
	Consts     []Symbol `json:"consts,omitempty"`
	Vars       []Symbol `json:"vars,omitempty"`
	Types      []Type   `json:"types,omitempty"`
	Funcs      []Symbol `json:"funcs,omitempty"`
}

// parsePackage extracts one directory. It returns nil (no error) for a
// directory holding no Go package, so walking a module tree is not littered
// with special cases.
func parsePackage(dir, importPath string) (*Package, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	if len(pkgs) == 0 {
		return nil, nil
	}

	names := make([]string, 0, len(pkgs))
	for n := range pkgs {
		names = append(names, n)
	}
	sort.Strings(names)
	astPkg := pkgs[names[0]]

	dpkg := doc.New(astPkg, importPath, 0)
	p := &Package{
		ImportPath: importPath,
		Name:       dpkg.Name,
		Doc:        strings.TrimSpace(dpkg.Doc),
	}

	for _, c := range dpkg.Consts {
		p.Consts = append(p.Consts, Symbol{
			Name: strings.Join(c.Names, ", "), Doc: strings.TrimSpace(c.Doc),
			Signature: render(fset, c.Decl),
		})
	}
	for _, v := range dpkg.Vars {
		p.Vars = append(p.Vars, Symbol{
			Name: strings.Join(v.Names, ", "), Doc: strings.TrimSpace(v.Doc),
			Signature: render(fset, v.Decl),
		})
	}
	for _, t := range dpkg.Types {
		typ := Type{
			Name: t.Name, Doc: strings.TrimSpace(t.Doc), Signature: render(fset, t.Decl),
		}
		for _, f := range t.Funcs {
			typ.Funcs = append(typ.Funcs, Symbol{
				Name: f.Name, Doc: strings.TrimSpace(f.Doc), Signature: funcSig(fset, f.Decl),
			})
		}
		for _, m := range t.Methods {
			typ.Methods = append(typ.Methods, Symbol{
				Name: m.Name, Doc: strings.TrimSpace(m.Doc), Signature: funcSig(fset, m.Decl),
			})
		}
		sortSymbols(typ.Funcs)
		sortSymbols(typ.Methods)
		p.Types = append(p.Types, typ)
	}
	for _, f := range dpkg.Funcs {
		p.Funcs = append(p.Funcs, Symbol{
			Name: f.Name, Doc: strings.TrimSpace(f.Doc), Signature: funcSig(fset, f.Decl),
		})
	}

	sortSymbols(p.Consts)
	sortSymbols(p.Vars)
	sortSymbols(p.Funcs)
	sort.Slice(p.Types, func(a, b int) bool { return p.Types[a].Name < p.Types[b].Name })
	return p, nil
}

func sortSymbols(s []Symbol) {
	sort.Slice(s, func(a, b int) bool { return s[a].Name < s[b].Name })
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
