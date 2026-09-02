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
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/nyroway/nyro/go"

func TestFoundationBoundaryPolicy(t *testing.T) {
	t.Parallel()
	llmProtocol := foundationBoundary{
		prefix:             "internal/llm/protocol",
		allowInternalExact: []string{"internal/llm"},
	}
	platform := foundationBoundary{prefix: "internal/platform", allowThirdParty: true}
	quotaRule := foundationBoundary{prefix: "internal/quota", allowThirdParty: true}
	routingRule := foundationBoundary{prefix: "internal/llm/routing"}
	providerRule := foundationBoundary{
		prefix:             "internal/llm/provider",
		allowInternalExact: []string{"internal/llm/protocol"},
	}
	tests := []struct {
		name string
		rule foundationBoundary
		imp  directImport
		want bool
	}{
		{"llm protocol may import llm", llmProtocol, directImport{path: modulePath + "/internal/llm"}, true},
		{"llm protocol rejects provider", llmProtocol, directImport{path: modulePath + "/internal/llm/provider"}, false},
		{"provider may import protocol", providerRule, directImport{path: modulePath + "/internal/llm/protocol"}, true},
		{"provider rejects gateway", providerRule, directImport{path: modulePath + "/internal/gateway"}, false},
		{"platform standard library", platform, directImport{path: "database/sql", standard: true}, true},
		{"platform own subtree", platform, directImport{path: modulePath + "/internal/platform/state"}, true},
		{"platform third party", platform, directImport{path: "github.com/jackc/pgx/v5"}, true},
		{"platform protocol", platform, directImport{path: modulePath + "/internal/llm/protocol"}, false},
		{"quota standard library", quotaRule, directImport{path: "sync", standard: true}, true},
		{"quota own subtree", quotaRule, directImport{path: modulePath + "/internal/quota/redis"}, true},
		{"quota go-redis", quotaRule, directImport{path: "github.com/redis/go-redis/v9"}, true},
		{"quota platform state", quotaRule, directImport{path: modulePath + "/internal/platform/state"}, false},
		{"quota storage", quotaRule, directImport{path: modulePath + "/internal/storage"}, false},
		{"quota gateway", quotaRule, directImport{path: modulePath + "/internal/gateway"}, false},
		{"routing standard library", routingRule, directImport{path: "sort", standard: true}, true},
		{"routing storage", routingRule, directImport{path: modulePath + "/internal/storage"}, false},
		{"routing third party", routingRule, directImport{path: "example.com/dependency"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := importAllowedByFoundationBoundary(tt.rule, tt.imp); got != tt.want {
				t.Fatalf("importAllowedByFoundationBoundary(%+v, %+v) = %v, want %v", tt.rule, tt.imp, got, tt.want)
			}
		})
	}
}

func TestLLMModelBoundaryPolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		imp  directImport
		want bool
	}{
		{"standard library", directImport{path: "encoding/json", standard: true}, true},
		{"llm runtime", directImport{path: modulePath + "/internal/llm/runtime"}, false},
		{"other internal", directImport{path: modulePath + "/internal/llm/provider"}, false},
		{"third party", directImport{path: "example.com/dependency"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := llmModelImportAllowed(tt.imp); got != tt.want {
				t.Fatalf("llmModelImportAllowed(%+v) = %v, want %v", tt.imp, got, tt.want)
			}
		})
	}
}

func TestConfigSnapshotBoundaryPolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		imp  directImport
		want bool
	}{
		{"standard library", directImport{path: "sync/atomic", standard: true}, true},
		{"storage contract", directImport{path: modulePath + "/internal/storage"}, true},
		{"storage backend", directImport{path: modulePath + "/internal/storage/memory"}, false},
		{"config sync", directImport{path: modulePath + "/internal/configsync"}, false},
		{"third party", directImport{path: "google.golang.org/grpc"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := configSnapshotImportAllowed(tt.imp); got != tt.want {
				t.Fatalf("configSnapshotImportAllowed(%+v) = %v, want %v", tt.imp, got, tt.want)
			}
		})
	}
}

func TestKernelBoundaryPolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		imp  directImport
		want bool
	}{
		{"standard library", directImport{path: "sync", standard: true}, true},
		{"kernel subtree", directImport{path: modulePath + "/internal/kernel/extension"}, false},
		{"other internal", directImport{path: modulePath + "/internal/config"}, false},
		{"third party", directImport{path: "example.com/dependency"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := kernelImportAllowed(tt.imp); got != tt.want {
				t.Fatalf("kernelImportAllowed(%+v) = %v, want %v", tt.imp, got, tt.want)
			}
		})
	}
}

type foundationBoundary struct {
	prefix             string
	allowThirdParty    bool
	allowInternal      []string
	allowInternalExact []string
}

type directImport struct {
	path     string
	standard bool
}

var foundationBoundaries = []foundationBoundary{
	{prefix: "internal/llm/protocol", allowInternalExact: []string{"internal/llm"}},
	{prefix: "internal/llm/provider", allowInternalExact: []string{"internal/llm/protocol"}},
	{prefix: "internal/llm/routing"},
	{prefix: "internal/platform", allowThirdParty: true},
	{prefix: "internal/quota", allowThirdParty: true},
}

func importAllowedByFoundationBoundary(rule foundationBoundary, imp directImport) bool {
	if imp.standard || packageWithin(imp.path, modulePath+"/"+rule.prefix) {
		return true
	}
	for _, allowed := range rule.allowInternal {
		if packageWithin(imp.path, modulePath+"/"+allowed) {
			return true
		}
	}
	for _, allowed := range rule.allowInternalExact {
		if imp.path == modulePath+"/"+allowed {
			return true
		}
	}
	return rule.allowThirdParty && !packageWithin(imp.path, modulePath)
}

func packageWithin(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+"/")
}

func configSnapshotImportAllowed(imp directImport) bool {
	return imp.standard || imp.path == modulePath+"/internal/storage"
}

func kernelImportAllowed(imp directImport) bool {
	return imp.standard
}

func llmModelImportAllowed(imp directImport) bool {
	return imp.standard
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
	"internal/kernel":                              layerFoundation,
	"internal/llm":                                 layerFoundation,
	"internal/llm/provider":                        layerFoundation,
	"internal/llm/provider/httptransport":          layerFoundation,
	"internal/llm/protocol":                        layerFoundation,
	"internal/llm/protocol/anthropic/messages":     layerFoundation,
	"internal/llm/protocol/gemini/generatecontent": layerFoundation,
	"internal/llm/protocol/openai/chatcompletions": layerFoundation,
	"internal/llm/protocol/openai/embeddings":      layerFoundation,
	"internal/llm/protocol/openai/responses":       layerFoundation,
	"internal/llm/routing":                         layerFoundation,
	"internal/pipeline":                            layerFoundation,
	"internal/platform/database":                   layerFoundation,
	"internal/platform/database/postgres":          layerFoundation,
	"internal/platform/database/sqlite":            layerFoundation,
	"internal/platform/observe":                    layerFoundation,
	"internal/platform/observe/otlphttp":           layerFoundation,
	"internal/platform/observe/sqlite":             layerFoundation,
	"internal/platform/state":                      layerFoundation,
	"internal/platform/state/redis":                layerFoundation,
	"internal/platform/state/sqlite":               layerFoundation,
	"internal/quota":                               layerFoundation,
	"internal/quota/redis":                         layerFoundation,
	"internal/telemetry/schema":                    layerFoundation,
	"internal/version":                             layerFoundation,
	"internal/webutil":                             layerFoundation,
	"internal/envflag":                             layerFoundation,

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
	"internal/config/snapshot":             layerData,
	"internal/schemadump":                  layerData,

	// Layer 2 — telemetry runtime.
	"internal/telemetry": layerObs,

	// Layer 3 — serve.
	"internal/gateway":         layerServe,
	"internal/gateway/runtime": layerServe,
	"internal/admin":           layerServe,
	"internal/bootstrap":       layerServe,
	"internal/webui":           layerServe,
}

// upwardEdge is a single importer→imported pair that violates the layer rule.
type upwardEdge struct{ from, to string }

// knownUpwardEdges freezes intentional upward imports. The set is empty: new
// violations fail, and TestNoStaleKnownUpwardEdges ensures temporary entries
// are removed once their underlying dependency is fixed.
var knownUpwardEdges = map[upwardEdge]string{}

// TestFoundationSubtreesStayIsolated applies stricter rules inside isolated
// layer-0 packages and subtrees. Numeric layers alone cannot prevent horizontal
// coupling between packages assigned to the same layer.
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

func TestConfigSnapshotStaysIsolated(t *testing.T) {
	t.Parallel()
	for _, pkg := range loadInternalPackages(t) {
		if pkg.name != "internal/config/snapshot" {
			continue
		}
		for _, imp := range pkg.directImports {
			if !configSnapshotImportAllowed(imp) {
				t.Errorf("config snapshot boundary: %s imports %s", pkg.name, imp.path)
			}
		}
	}
}

func TestProviderCoreStaysTransportNeutral(t *testing.T) {
	t.Parallel()
	for _, pkg := range loadInternalPackages(t) {
		if pkg.name != "internal/llm/provider" {
			continue
		}
		for _, imp := range pkg.directImports {
			if imp.path == "net/http" {
				t.Error("internal/llm/provider imports net/http; HTTP belongs in provider/httptransport")
			}
		}
	}
}

func TestKernelUsesOnlyStandardLibrary(t *testing.T) {
	t.Parallel()
	for _, pkg := range loadInternalPackages(t) {
		if !packageWithin(pkg.name, "internal/kernel") {
			continue
		}
		for _, imp := range pkg.directImports {
			if !kernelImportAllowed(imp) {
				t.Errorf("kernel boundary: %s imports %s", pkg.name, imp.path)
			}
		}
	}
}

func TestLLMModelUsesOnlyStandardLibrary(t *testing.T) {
	t.Parallel()
	for _, pkg := range loadInternalPackages(t) {
		if pkg.name != "internal/llm" {
			continue
		}
		for _, imp := range pkg.directImports {
			if !llmModelImportAllowed(imp) {
				t.Errorf("llm model boundary: %s imports %s", pkg.name, imp.path)
			}
		}
	}
}

func TestLLMPluginsUseExplicitComposition(t *testing.T) {
	t.Parallel()
	root := moduleRoot(t)
	for _, sourceRoot := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, sourceRoot), func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if bytes.Contains(raw, []byte("Code generated")) && bytes.Contains(raw, []byte("DO NOT EDIT")) {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			file, err := parser.ParseFile(token.NewFileSet(), path, raw, 0)
			if err != nil {
				return err
			}
			restrictedSource := packageWithin(filepath.ToSlash(filepath.Dir(rel)), "internal/llm/protocol") ||
				packageWithin(filepath.ToSlash(filepath.Dir(rel)), "internal/llm/provider") ||
				packageWithin(filepath.ToSlash(filepath.Dir(rel)), "internal/bootstrap")
			for _, declaration := range file.Decls {
				fn, ok := declaration.(*ast.FuncDecl)
				if ok && fn.Recv == nil && fn.Name.Name == "init" &&
					(packageWithin(filepath.ToSlash(filepath.Dir(rel)), "internal/llm/protocol") ||
						packageWithin(filepath.ToSlash(filepath.Dir(rel)), "internal/llm/provider")) {
					t.Errorf("implicit registration: %s declares func init", rel)
				}
			}
			for _, imported := range file.Imports {
				if imported.Name == nil || imported.Name.Name != "_" {
					continue
				}
				importPath, err := strconv.Unquote(imported.Path.Value)
				if err != nil {
					return err
				}
				pluginImport := packageWithin(importPath, modulePath+"/internal/llm/protocol") ||
					packageWithin(importPath, modulePath+"/internal/llm/provider")
				if restrictedSource || pluginImport {
					t.Errorf("implicit composition: %s blank-imports %s", rel, importPath)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", sourceRoot, err)
		}
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	return filepath.Dir(strings.TrimSpace(string(out)))
}

func TestGatewayRootDoesNotImportRuntime(t *testing.T) {
	t.Parallel()
	for _, pkg := range loadInternalPackages(t) {
		if pkg.name != "internal/gateway" {
			continue
		}
		for _, imp := range pkg.imports {
			if packageWithin(imp, "internal/gateway/runtime") {
				t.Errorf("gateway boundary: %s imports its runtime assembly package %s", pkg.name, imp)
			}
		}
	}
}

func TestGatewayRootDoesNotImportConfigSync(t *testing.T) {
	t.Parallel()
	for _, pkg := range loadInternalPackages(t) {
		if pkg.name != "internal/gateway" {
			continue
		}
		for _, imp := range pkg.imports {
			if packageWithin(imp, "internal/configsync") {
				t.Errorf("gateway boundary: %s imports config-sync transport package %s", pkg.name, imp)
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
	name          string   // module-relative, e.g. "internal/gateway"
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
