// Package layering enforces nyro's internal dependency direction.
//
// The rule: a package may import its own layer or any layer below it, never
// above. Layers are declared in packageLayer below — that table, not the
// directory tree, is the source of truth. internal/ stays deliberately flat
// (see docs/superpowers/plans/2026-07-29-go-pipeline-and-layering.md), so a
// grouping directory cannot express this and a test has to.
//
// Each package also carries its layer in its own package comment
// ("// Layer: N (...)"), which is what a reader sees first; this test is what
// keeps those comments honest.
package layering_test

import (
	"os/exec"
	"strings"
	"testing"
)

const modulePath = "github.com/nyroway/nyro/go"

// Layer numbers. Lower may not import higher.
const (
	layerFoundation = 0 // stdlib/llm only, shared by every layer above
	layerData       = 1 // persistence and configuration
	layerObs        = 2 // instrumentation, sits between data and serve
	layerServe      = 3 // HTTP surfaces and orchestration
)

// packageLayer assigns every internal package a layer. A package missing from
// this map fails TestEveryInternalPackageIsClassified, so adding a package
// forces a deliberate layering decision rather than silently defaulting.
var packageLayer = map[string]int{
	// Layer 0 — foundation.
	"internal/quota":    layerFoundation,
	"internal/provider": layerFoundation,
	"internal/version":  layerFoundation,
	"internal/webutil":  layerFoundation,
	"internal/envflag":  layerFoundation,
	"internal/plugin":   layerFoundation, // replaced by internal/pipeline in batch 2

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
	"internal/observability":         layerObs,
	"internal/observability/parquet": layerObs,

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
				continue // not an internal package (stdlib, llm/, third party)
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

// pkgInfo is one package and its module-relative internal imports.
type pkgInfo struct {
	name    string   // module-relative, e.g. "internal/proxy"
	imports []string // module-relative import paths
}

// loadInternalPackages shells out to `go list` for the real import graph.
// Production imports only: test-only imports may legitimately point anywhere
// (a layer-0 test may spin up a layer-3 server to exercise itself).
func loadInternalPackages(t *testing.T) []pkgInfo {
	t.Helper()
	out, err := exec.Command("go", "list", "-f", "{{.ImportPath}}\t{{join .Imports \" \"}}", modulePath+"/internal/...").Output()
	if err != nil {
		t.Fatalf("go list failed: %v", err)
	}
	var pkgs []pkgInfo
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		name, importList, found := strings.Cut(line, "\t")
		if !found {
			continue
		}
		p := pkgInfo{name: trimModule(name)}
		for imp := range strings.FieldsSeq(importList) {
			if rel := trimModule(imp); strings.HasPrefix(rel, "internal/") {
				p.imports = append(p.imports, rel)
			}
		}
		pkgs = append(pkgs, p)
	}
	if len(pkgs) == 0 {
		t.Fatal("go list returned no packages")
	}
	return pkgs
}

func trimModule(path string) string {
	return strings.TrimPrefix(path, modulePath+"/")
}
