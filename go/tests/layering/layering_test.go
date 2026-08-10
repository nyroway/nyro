// Package layering enforces nyro's internal dependency direction.
//
// The rule: a package may import its own layer or any layer below it, never
// above. Layers are declared in packageLayer below — that table, not the
// directory tree, is the source of truth. Foundation subtrees add stricter
// horizontal boundaries that the numeric layers cannot express.
//
// Each package also carries its layer in its own package comment
// ("// Layer: N (...)"), which is what a reader sees first; this test is what
// keeps those comments honest.
package layering_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os/exec"
	"sort"
	"strings"
	"testing"
)

const modulePath = "github.com/nyroway/nyro/go"

func TestFoundationBoundaryPolicy(t *testing.T) {
	t.Parallel()
	llm := foundationBoundary{prefix: "internal/protocol/llm"}
	platform := foundationBoundary{prefix: "internal/platform", allowThirdParty: true}
	tests := []struct {
		name string
		rule foundationBoundary
		imp  directImport
		want bool
	}{
		{"llm standard library", llm, directImport{path: "encoding/json", standard: true}, true},
		{"llm own subtree", llm, directImport{path: modulePath + "/internal/protocol/llm/spec"}, true},
		{"llm third party", llm, directImport{path: "example.com/dependency"}, false},
		{"llm other internal", llm, directImport{path: modulePath + "/internal/provider"}, false},
		{"platform standard library", platform, directImport{path: "database/sql", standard: true}, true},
		{"platform own subtree", platform, directImport{path: modulePath + "/internal/platform/state"}, true},
		{"platform third party", platform, directImport{path: "github.com/jackc/pgx/v5"}, true},
		{"platform protocol", platform, directImport{path: modulePath + "/internal/protocol/llm/spec"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := importAllowedByFoundationBoundary(tt.rule, tt.imp); got != tt.want {
				t.Fatalf("importAllowedByFoundationBoundary(%+v, %+v) = %v, want %v", tt.rule, tt.imp, got, tt.want)
			}
		})
	}
}

type foundationBoundary struct {
	prefix          string
	allowThirdParty bool
}

type directImport struct {
	path     string
	standard bool
}

var foundationBoundaries = []foundationBoundary{
	{prefix: "internal/protocol/llm"},
	{prefix: "internal/platform", allowThirdParty: true},
}

func importAllowedByFoundationBoundary(rule foundationBoundary, imp directImport) bool {
	if imp.standard || packageWithin(imp.path, modulePath+"/"+rule.prefix) {
		return true
	}
	return rule.allowThirdParty && !packageWithin(imp.path, modulePath)
}

func packageWithin(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+"/")
}

// Layer numbers. Lower may not import higher.
const (
	layerFoundation = 0 // protocol and platform foundations shared by higher layers
	layerData       = 1 // persistence and configuration
	layerObs        = 2 // instrumentation, sits between data and serve
	layerServe      = 3 // HTTP surfaces and orchestration
)

// packageLayer assigns every internal package a layer. A package missing from
// this map fails TestEveryInternalPackageIsClassified, so adding a package
// forces a deliberate layering decision rather than silently defaulting.
var packageLayer = map[string]int{
	// Layer 0 — foundation.
	"internal/pipeline":                                  layerFoundation,
	"internal/protocol/llm":                              layerFoundation,
	"internal/protocol/llm/codec":                        layerFoundation,
	"internal/protocol/llm/codec/anthropic/messages":     layerFoundation,
	"internal/protocol/llm/codec/gemini/generatecontent": layerFoundation,
	"internal/protocol/llm/codec/openai/chatcompletions": layerFoundation,
	"internal/protocol/llm/codec/openai/embeddings":      layerFoundation,
	"internal/protocol/llm/codec/openai/responses":       layerFoundation,
	"internal/protocol/llm/ir":                           layerFoundation,
	"internal/protocol/llm/spec":                         layerFoundation,
	"internal/platform/database":                         layerFoundation,
	"internal/platform/database/postgres":                layerFoundation,
	"internal/platform/database/sqlite":                  layerFoundation,
	"internal/platform/observe":                          layerFoundation,
	"internal/platform/observe/otlphttp":                 layerFoundation,
	"internal/platform/observe/sqlite":                   layerFoundation,
	"internal/platform/state":                            layerFoundation,
	"internal/platform/state/redis":                      layerFoundation,
	"internal/platform/state/sqlite":                     layerFoundation,
	"internal/quota":                                     layerFoundation,
	"internal/provider":                                  layerFoundation,
	"internal/version":                                   layerFoundation,
	"internal/webutil":                                   layerFoundation,
	"internal/envflag":                                   layerFoundation,

	// Layer 1 — data.
	"internal/storage":                     layerData,
	"internal/storage/model":               layerData,
	"internal/storage/query":               layerData,
	"internal/storage/database":            layerData,
	"internal/storage/memory":              layerData,
	"internal/storage/gen":                 layerData,
	"internal/configsync":                  layerData,
	"internal/configsync/pki":              layerData,
	"internal/configsync/pb/configsync/v1": layerData,
	"internal/config":                      layerData,
	"internal/schemadump":                  layerData,

	// Layer 2 — observability.
	"internal/observability": layerObs,

	// Layer 3 — serve.
	"internal/router":    layerServe,
	"internal/proxy":     layerServe,
	"internal/admin":     layerServe,
	"internal/dataplane": layerServe,
	"internal/bootstrap": layerServe,
	"internal/webui":     layerServe,
}

// upwardEdge is a single importer→imported pair that violates the layer rule.
type upwardEdge struct{ from, to string }

// knownUpwardEdges freezes the upward imports that already existed when this
// test was written. They are allowed so the test can land green, but the set
// may only shrink: a new violation fails, and removing one of these without
// updating the map also fails (TestNoStaleKnownUpwardEdges).
//
// Both entries are the same underlying issue: observability owns the exporter
// registry (Signal, ExporterDef, ExportersFor, IsExporterSettingKey) that
// layer-1 config validation needs. That registry is layer-0 configuration
// metadata that happens to live inside a layer-2 runtime package. Splitting it
// out is the fix; it is out of scope for the pipeline work.
var knownUpwardEdges = map[upwardEdge]string{
	{from: "internal/config", to: "internal/observability"}:     "exporter registry used to validate settings.observability.* YAML",
	{from: "internal/configsync", to: "internal/observability"}: "IsExporterSettingKey used when flattening settings",
}

// TestFoundationSubtreesStayIsolated applies stricter rules inside the two
// layer-0 subtrees. Numeric layers alone cannot prevent horizontal coupling
// between packages assigned to the same layer.
func TestFoundationSubtreesStayIsolated(t *testing.T) {
	t.Parallel()
	for _, pkg := range loadInternalPackages(t) {
		for _, rule := range foundationBoundaries {
			if !packageWithin(pkg.name, rule.prefix) {
				continue
			}
			for _, imp := range pkg.directImports {
				if !importAllowedByFoundationBoundary(rule, imp) {
					t.Errorf("foundation boundary: %s imports %s", pkg.name, imp.path)
				}
			}
		}
	}
}

// TestNoUpwardImports is the actual constraint: no internal package may import
// a package from a higher layer, except the frozen edges above.
func TestNoUpwardImports(t *testing.T) {
	t.Parallel()
	for _, pkg := range loadInternalPackages(t) {
		fromLayer, ok := packageLayer[pkg.name]
		if !ok {
			continue // reported by TestEveryInternalPackageIsClassified
		}
		for _, imp := range pkg.imports {
			toLayer, ok := packageLayer[imp]
			if !ok {
				continue // not an internal package (stdlib or third party)
			}
			if toLayer <= fromLayer {
				continue
			}
			if _, known := knownUpwardEdges[upwardEdge{pkg.name, imp}]; known {
				continue
			}
			t.Errorf("upward import: %s (layer %d) imports %s (layer %d)\n"+
				"  a package may only import its own layer or below;\n"+
				"  either move the shared code down a layer or, if this is\n"+
				"  genuinely unavoidable, add it to knownUpwardEdges with a reason",
				pkg.name, fromLayer, imp, toLayer)
		}
	}
}

// TestNoStaleKnownUpwardEdges keeps the exception list from outliving the
// exceptions. Once an edge is genuinely removed from the code, this fails
// until the entry is deleted, so the list can only shrink.
func TestNoStaleKnownUpwardEdges(t *testing.T) {
	t.Parallel()
	actual := map[upwardEdge]bool{}
	for _, pkg := range loadInternalPackages(t) {
		for _, imp := range pkg.imports {
			actual[upwardEdge{pkg.name, imp}] = true
		}
	}
	for edge, reason := range knownUpwardEdges {
		if !actual[edge] {
			t.Errorf("stale exception: %s no longer imports %s (%q) — delete this entry from knownUpwardEdges",
				edge.from, edge.to, reason)
		}
	}
}

// TestEveryInternalPackageIsClassified makes the layer table exhaustive, so a
// newly added package cannot slip past TestNoUpwardImports unclassified.
func TestEveryInternalPackageIsClassified(t *testing.T) {
	t.Parallel()
	for _, pkg := range loadInternalPackages(t) {
		if _, ok := packageLayer[pkg.name]; !ok {
			t.Errorf("package %s has no layer assignment — add it to packageLayer "+
				"and document the layer in its package comment", pkg.name)
		}
	}
}

// pkgInfo is one package and its production imports.
type pkgInfo struct {
	name          string   // module-relative, e.g. "internal/proxy"
	imports       []string // module-relative internal import paths
	directImports []directImport
}

type listedPackage struct {
	ImportPath string
	Imports    []string
	Standard   bool
}

// loadInternalPackages shells out to `go list` for the real import graph.
// Production imports only: test-only imports may legitimately point anywhere
// (a layer-0 test may spin up a layer-3 server to exercise itself).
func loadInternalPackages(t *testing.T) []pkgInfo {
	t.Helper()
	out, err := exec.Command("go", "list", "-deps", "-json", modulePath+"/internal/...").Output()
	if err != nil {
		t.Fatalf("go list failed: %v", err)
	}
	listed := make(map[string]listedPackage)
	decoder := json.NewDecoder(bytes.NewReader(out))
	for {
		var pkg listedPackage
		err := decoder.Decode(&pkg)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		listed[pkg.ImportPath] = pkg
	}

	var pkgs []pkgInfo
	internalRoot := modulePath + "/internal"
	for importPath, listedPkg := range listed {
		if !packageWithin(importPath, internalRoot) {
			continue
		}
		p := pkgInfo{name: trimModule(importPath)}
		for _, impPath := range listedPkg.Imports {
			dependency, ok := listed[impPath]
			if !ok {
				t.Fatalf("go list omitted direct dependency %s imported by %s", impPath, importPath)
			}
			p.directImports = append(p.directImports, directImport{
				path:     impPath,
				standard: dependency.Standard,
			})
			if packageWithin(impPath, internalRoot) {
				p.imports = append(p.imports, trimModule(impPath))
			}
		}
		sort.Strings(p.imports)
		sort.Slice(p.directImports, func(i, j int) bool {
			return p.directImports[i].path < p.directImports[j].path
		})
		pkgs = append(pkgs, p)
	}
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].name < pkgs[j].name })
	if len(pkgs) == 0 {
		t.Fatal("go list returned no packages")
	}
	return pkgs
}

func trimModule(path string) string {
	return strings.TrimPrefix(path, modulePath+"/")
}
