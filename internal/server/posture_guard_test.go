package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestPostureFactsReadOnlyHere keeps deployment-posture decisions in
// posture.go: outside it, code in this package may not interpret the listen
// address or the tunnel marker itself. It cannot tell whether a new capability
// should have asked for a decision at all; the review checklist in CLAUDE.md
// covers that.
func TestPostureFactsReadOnlyHere(t *testing.T) {
	// file → functions allowed to read Server.listenAddress, and why.
	listenAddressReaders := map[string]map[string]string{
		"posture.go":           {"posture": "the posture facts"},
		"server.go":            {"StartWithReady": "normalizes and binds the address", "ActualAddr": "dialable address for local clients"},
		"startup_log.go":       {"logStartupSummaryBlock": "prints the address"},
		"config_management.go": {"configManagement": "input to the exported classifier internal/app also calls"},
	}
	// package.func → files allowed to call it.
	restrictedCalls := map[string]map[string]bool{
		"cloud.IsAuthenticatedTunnelRequest": {"posture.go": true},
		"cloud.IsLoopbackHostname":           {"posture.go": true, "listen_address.go": true}, // listen_address.go: request hostnames, not the listener
	}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.SelectorExpr:
					if n.Sel.Name == "listenAddress" {
						if recv, ok := n.X.(*ast.Ident); ok && recv.Name == "s" {
							if _, ok := listenAddressReaders[path][fn.Name.Name]; !ok {
								t.Errorf("%s: %s reads s.listenAddress; decide in posture.go (exposureOf / posture) instead", fset.Position(n.Pos()), fn.Name.Name)
							}
						}
					}
				case *ast.CallExpr:
					sel, ok := n.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					pkg, ok := sel.X.(*ast.Ident)
					if !ok {
						return true
					}
					name := pkg.Name + "." + sel.Sel.Name
					if allowed, restricted := restrictedCalls[name]; restricted && !allowed[path] {
						t.Errorf("%s: %s calls %s; use posture.go (viaTunnel / exposureOf) instead", fset.Position(n.Pos()), fn.Name.Name, name)
					}
				}
				return true
			})
		}
	}
}
