package server

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// file → functions allowed to read a listenAddress field, and why.
var postureListenAddressReaders = map[string]map[string]string{
	"posture.go":           {"posture": "the posture facts"},
	"server.go":            {"StartWithReady": "normalizes and binds the address", "ActualAddr": "dialable address for local clients"},
	"startup_log.go":       {"logStartupSummaryBlock": "prints the address", "formatStartupLogSummary": "prints the summary's copy"},
	"config_management.go": {"configManagement": "input to the exported classifier internal/app also calls"},
}

// internal/cloud function → files allowed to call it.
var postureRestrictedCloudCalls = map[string]map[string]bool{
	"IsAuthenticatedTunnelRequest": {"posture.go": true},
	"IsLoopbackHostname":           {"posture.go": true, "listen_address.go": true}, // listen_address.go: request hostnames, not the listener
}

// posturePrimitiveUses reports reads of a listenAddress field and calls to the
// restricted internal/cloud functions outside their allowlists, whatever the
// receiver or import alias is called.
func posturePrimitiveUses(fset *token.FileSet, path string, f *ast.File) []string {
	var out []string
	cloudNames := map[string]bool{}
	for _, imp := range f.Imports {
		if imp.Path.Value != `"github.com/skyhook-io/radar/internal/cloud"` {
			continue
		}
		name := "cloud"
		if imp.Name != nil {
			name = imp.Name.Name
		}
		cloudNames[name] = true
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
					if _, ok := postureListenAddressReaders[path][fn.Name.Name]; !ok {
						out = append(out, fmt.Sprintf("%s: %s reads a listenAddress field; decide in posture.go (exposureOf / posture) instead", fset.Position(n.Pos()), fn.Name.Name))
					}
				}
			case *ast.CallExpr:
				sel, ok := n.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || !cloudNames[pkg.Name] {
					return true
				}
				if allowed, restricted := postureRestrictedCloudCalls[sel.Sel.Name]; restricted && !allowed[path] {
					out = append(out, fmt.Sprintf("%s: %s calls cloud.%s; use posture.go (viaTunnel / exposureOf) instead", fset.Position(n.Pos()), fn.Name.Name, sel.Sel.Name))
				}
			}
			return true
		})
	}
	return out
}

// TestPostureFactsReadOnlyHere keeps deployment-posture decisions in
// posture.go: outside it, code in this package may not interpret the listen
// address or the tunnel marker itself. It cannot tell whether a new capability
// should have asked for a decision at all; the review checklist in CLAUDE.md
// covers that.
func TestPostureFactsReadOnlyHere(t *testing.T) {
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
		for _, msg := range posturePrimitiveUses(fset, path, f) {
			t.Error(msg)
		}
	}
}

func TestPosturePrimitiveUsesCatchesRenames(t *testing.T) {
	src := `package server

import cl "github.com/skyhook-io/radar/internal/cloud"

func (srv *Server) probe(r *http.Request) bool {
	return srv.listenAddress == "0.0.0.0" || cl.IsAuthenticatedTunnelRequest(r.Context()) || cl.IsLoopbackHostname(srv.listenAddress)
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "handlers.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := posturePrimitiveUses(fset, "handlers.go", f); len(got) != 4 {
		t.Fatalf("got %d findings, want 4 (two listenAddress reads, two aliased cloud calls):\n%s", len(got), strings.Join(got, "\n"))
	}
	if got := posturePrimitiveUses(fset, "posture.go", f); len(got) != 2 {
		t.Fatalf("in posture.go the cloud calls are allowed but probe is not an allowed reader; got %d findings, want 2:\n%s", len(got), strings.Join(got, "\n"))
	}
}
