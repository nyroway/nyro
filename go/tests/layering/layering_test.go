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
	pipelineRule := foundationBoundary{
		prefix: "internal/llm/pipeline",
		allowInternalExact: []string{
			"internal/llm",
			"internal/llm/protocol",
			"internal/security/authn",
			"internal/security/authz",
		},
	}
	securityRule := foundationBoundary{prefix: "internal/security"}
	providerRule := foundationBoundary{
		prefix: "internal/llm/provider",
		allowInternalExact: []string{
			"internal/llm",
			"internal/llm/protocol",
		},
	}
	runtimeRule := foundationBoundary{
		prefix: "internal/llm/runtime",
		allowInternalExact: []string{
			"internal/config/snapshot",
			"internal/llm",
			"internal/llm/pipeline",
			"internal/llm/protocol",
			"internal/llm/provider",
			"internal/llm/routing",
			"internal/quota",
			"internal/security/authn",
		},
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
		{"provider may import canonical llm", providerRule, directImport{path: modulePath + "/internal/llm"}, true},
		{"provider rejects gateway", providerRule, directImport{path: modulePath + "/internal/gateway"}, false},
		{"runtime may import snapshot", runtimeRule, directImport{path: modulePath + "/internal/config/snapshot"}, true},
		{"runtime may import provider", runtimeRule, directImport{path: modulePath + "/internal/llm/provider"}, true},
		{"runtime may import authn contract", runtimeRule, directImport{path: modulePath + "/internal/security/authn"}, true},
		{"runtime rejects authz implementation", runtimeRule, directImport{path: modulePath + "/internal/security/authz"}, false},
		{"runtime rejects gateway", runtimeRule, directImport{path: modulePath + "/internal/gateway"}, false},
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
		{"pipeline may import llm", pipelineRule, directImport{path: modulePath + "/internal/llm"}, true},
		{"pipeline may import authn contract", pipelineRule, directImport{path: modulePath + "/internal/security/authn"}, true},
		{"pipeline rejects gateway", pipelineRule, directImport{path: modulePath + "/internal/gateway"}, false},
		{"security own subtree", securityRule, directImport{path: modulePath + "/internal/security/authn"}, true},
		{"security rejects llm runtime", securityRule, directImport{path: modulePath + "/internal/llm/runtime"}, false},
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
		{"storage contract", directImport{path: modulePath + "/internal/storage"}, false},
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
	{
		prefix:          "internal/llm/ingress/http",
		allowThirdParty: true,
		allowInternalExact: []string{
			"internal/llm",
			"internal/llm/protocol",
			"internal/llm/runtime",
			"internal/security/authn",
		},
	},
	{prefix: "internal/llm/protocol", allowInternalExact: []string{"internal/llm"}},
	{prefix: "internal/llm/provider", allowInternalExact: []string{"internal/llm", "internal/llm/protocol"}},
	{prefix: "internal/llm/routing"},
	{
		prefix: "internal/llm/pipeline",
		allowInternalExact: []string{
			"internal/llm",
			"internal/llm/protocol",
			"internal/security/authn",
			"internal/security/authz",
		},
	},
	{
		prefix: "internal/llm/runtime",
		allowInternalExact: []string{
			"internal/config/snapshot",
			"internal/llm",
			"internal/llm/pipeline",
			"internal/llm/protocol",
			"internal/llm/provider",
			"internal/llm/routing",
			"internal/quota",
			"internal/security/authn",
		},
	},
	{prefix: "internal/security"},
	{prefix: "internal/platform", allowThirdParty: true},
	{prefix: "internal/quota", allowThirdParty: true},
	{prefix: "internal/transport/httpserver"},
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
	return imp.standard
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
	layerRuntime    = 2 // trusted workload runtime and instrumentation
	layerObs        = layerRuntime
	layerServe      = 3 // HTTP surfaces and orchestration
)

// packageLayer assigns every internal package a layer. A package missing from
// this map fails TestEveryInternalPackageIsClassified, so adding a package
// forces a deliberate layering decision rather than silently defaulting.
var packageLayer = map[string]int{
	// Layer 0 — foundation.
	"internal/kernel":                              layerFoundation,
	"internal/llm":                                 layerFoundation,
	"internal/llm/pipeline":                        layerFoundation,
	"internal/llm/provider":                        layerFoundation,
	"internal/llm/provider/httptransport":          layerFoundation,
	"internal/llm/protocol":                        layerFoundation,
	"internal/llm/protocol/anthropic/messages":     layerFoundation,
	"internal/llm/protocol/gemini/generatecontent": layerFoundation,
	"internal/llm/protocol/openai/chatcompletions": layerFoundation,
	"internal/llm/protocol/openai/embeddings":      layerFoundation,
	"internal/llm/protocol/openai/responses":       layerFoundation,
	"internal/llm/routing":                         layerFoundation,
	"internal/llm/runtime":                         layerRuntime,
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
	"internal/security/authn":                      layerFoundation,
	"internal/security/authz":                      layerFoundation,
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
	"internal/gateway":              layerServe,
	"internal/llm/ingress/http":     layerServe,
	"internal/transport/httpserver": layerServe,
	"internal/admin":                layerServe,
	"internal/bootstrap":            layerServe,
	"internal/webui":                layerServe,
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

func TestLLMRuntimePackagesStayTransportNeutral(t *testing.T) {
	t.Parallel()
	for _, pkg := range loadInternalPackages(t) {
		if !packageWithin(pkg.name, "internal/llm/pipeline") &&
			!packageWithin(pkg.name, "internal/llm/runtime") &&
			!packageWithin(pkg.name, "internal/llm/routing") {
			continue
		}
		for _, imp := range pkg.directImports {
			if imp.path == "net/http" {
				t.Errorf("transport-neutral LLM package %s imports net/http", pkg.name)
			}
		}
	}
}

func TestLLMExchangeHasNoContextFunctionOrUntypedMapFields(t *testing.T) {
	t.Parallel()
	root := moduleRoot(t)
	path := filepath.Join(root, "internal", "llm", "pipeline", "exchange.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse Exchange source: %v", err)
	}
	found := false
	for _, declaration := range file.Decls {
		generic, ok := declaration.(*ast.GenDecl)
		if !ok || generic.Tok != token.TYPE {
			continue
		}
		for _, specification := range generic.Specs {
			typeSpec, ok := specification.(*ast.TypeSpec)
			if !ok || typeSpec.Name.Name != "Exchange" {
				continue
			}
			found = true
			structure, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				t.Fatal("pipeline.Exchange is not a struct")
			}
			for _, field := range structure.Fields.List {
				switch fieldType := field.Type.(type) {
				case *ast.FuncType:
					t.Errorf("pipeline.Exchange contains a function field %s", fieldNames(field))
				case *ast.MapType:
					key, keyOK := fieldType.Key.(*ast.Ident)
					value, valueOK := fieldType.Value.(*ast.Ident)
					if keyOK && key.Name == "string" && valueOK && value.Name == "any" {
						t.Errorf("pipeline.Exchange contains an untyped extension map field %s", fieldNames(field))
					}
				case *ast.SelectorExpr:
					owner, ownerOK := fieldType.X.(*ast.Ident)
					if ownerOK && owner.Name == "context" && fieldType.Sel.Name == "Context" {
						t.Errorf("pipeline.Exchange contains a context.Context field %s", fieldNames(field))
					}
				}
			}
		}
	}
	if !found {
		t.Fatal("pipeline.Exchange declaration not found")
	}
}

func TestSecurityDoesNotImportLLMConcreteRuntime(t *testing.T) {
	t.Parallel()
	for _, pkg := range loadInternalPackages(t) {
		if !packageWithin(pkg.name, "internal/security") {
			continue
		}
		for _, imp := range pkg.imports {
			if packageWithin(imp, "internal/llm/runtime") {
				t.Errorf("security boundary: %s imports concrete LLM runtime %s", pkg.name, imp)
			}
		}
	}
}

func fieldNames(field *ast.Field) string {
	names := make([]string, 0, len(field.Names))
	for _, name := range field.Names {
		names = append(names, name.Name)
	}
	return strings.Join(names, ", ")
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

func TestBuiltInLLMEnumerationLivesOnlyInBootstrapCatalogs(t *testing.T) {
	t.Parallel()
	root := moduleRoot(t)
	allowed := "internal/bootstrap/catalogs.go"
	constructors := map[string]bool{
		"Generic": true, "OpenAI": true, "Anthropic": true,
		"Gemini": true, "DeepSeek": true, "OpenRouter": true,
	}
	for _, sourceRoot := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, sourceRoot), func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if rel == allowed {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, raw, 0)
			if err != nil {
				return err
			}
			providerAliases := map[string]bool{}
			for _, imported := range file.Imports {
				importPath, err := strconv.Unquote(imported.Path.Value)
				if err != nil {
					return err
				}
				if strings.HasPrefix(importPath, modulePath+"/internal/llm/protocol/") {
					t.Errorf("concrete LLM codec import outside %s: %s imports %s", allowed, rel, importPath)
				}
				if importPath == modulePath+"/internal/llm/provider" {
					name := "provider"
					if imported.Name != nil {
						name = imported.Name.Name
					}
					providerAliases[name] = true
				}
			}
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !constructors[selector.Sel.Name] {
					return true
				}
				identifier, ok := selector.X.(*ast.Ident)
				if ok && providerAliases[identifier.Name] {
					t.Errorf("built-in Provider enumeration outside %s: %s calls %s.%s", allowed, rel, identifier.Name, selector.Sel.Name)
				}
				return true
			})
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
