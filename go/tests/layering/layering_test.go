// Package layering enforces nyro's explicit architecture boundaries.
// Every internal package belongs to a named architecture domain, while
// capability-specific policies below constrain the import edges that matter.
package layering_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
		{"runtime may import snapshot", runtimeRule, directImport{path: modulePath + "/internal/config/snapshot"}, true},
		{"runtime may import provider", runtimeRule, directImport{path: modulePath + "/internal/llm/provider"}, true},
		{"runtime may import authn contract", runtimeRule, directImport{path: modulePath + "/internal/security/authn"}, true},
		{"runtime rejects authz implementation", runtimeRule, directImport{path: modulePath + "/internal/security/authz"}, false},
		{"platform standard library", platform, directImport{path: "database/sql", standard: true}, true},
		{"platform own subtree", platform, directImport{path: modulePath + "/internal/platform/state"}, true},
		{"platform third party", platform, directImport{path: "github.com/jackc/pgx/v5"}, true},
		{"platform protocol", platform, directImport{path: modulePath + "/internal/llm/protocol"}, false},
		{"quota standard library", quotaRule, directImport{path: "sync", standard: true}, true},
		{"quota own subtree", quotaRule, directImport{path: modulePath + "/internal/quota/redis"}, true},
		{"quota go-redis", quotaRule, directImport{path: "github.com/redis/go-redis/v9"}, true},
		{"quota platform state", quotaRule, directImport{path: modulePath + "/internal/platform/state"}, false},
		{"quota storage", quotaRule, directImport{path: modulePath + "/internal/storage"}, false},
		{"routing standard library", routingRule, directImport{path: "sort", standard: true}, true},
		{"routing storage", routingRule, directImport{path: modulePath + "/internal/storage"}, false},
		{"routing third party", routingRule, directImport{path: "example.com/dependency"}, false},
		{"pipeline may import llm", pipelineRule, directImport{path: modulePath + "/internal/llm"}, true},
		{"pipeline may import authn contract", pipelineRule, directImport{path: modulePath + "/internal/security/authn"}, true},
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

type architectureDomain string

const (
	domainAdmin       architectureDomain = "admin-http"
	domainBootstrap   architectureDomain = "bootstrap-composition"
	domainConfig      architectureDomain = "configuration"
	domainConfigSync  architectureDomain = "configuration-sync"
	domainKernel      architectureDomain = "microkernel"
	domainLLMIR       architectureDomain = "llm-canonical-ir"
	domainLLMIngress  architectureDomain = "llm-http-ingress"
	domainLLMPipeline architectureDomain = "llm-pipeline"
	domainLLMProtocol architectureDomain = "llm-protocol"
	domainLLMProvider architectureDomain = "llm-provider"
	domainLLMRouting  architectureDomain = "llm-routing"
	domainLLMRuntime  architectureDomain = "llm-runtime"
	domainPlatform    architectureDomain = "platform"
	domainQuota       architectureDomain = "quota"
	domainSecurity    architectureDomain = "security"
	domainStorage     architectureDomain = "storage"
	domainSupport     architectureDomain = "support"
	domainTelemetry   architectureDomain = "telemetry"
	domainTransport   architectureDomain = "generic-http-transport"
)

// packageDomains is exhaustive. Classification makes every new internal
// package an explicit architecture decision; dependency direction is enforced
// by the capability policies, not by an ordinal layer number.
var packageDomains = map[string]architectureDomain{
	"internal/admin":                               domainAdmin,
	"internal/bootstrap":                           domainBootstrap,
	"internal/config":                              domainConfig,
	"internal/config/snapshot":                     domainConfig,
	"internal/configsync":                          domainConfigSync,
	"internal/configsync/pb/configsync/v1":         domainConfigSync,
	"internal/configsync/pki":                      domainConfigSync,
	"internal/envflag":                             domainSupport,
	"internal/kernel":                              domainKernel,
	"internal/llm":                                 domainLLMIR,
	"internal/llm/ingress/http":                    domainLLMIngress,
	"internal/llm/pipeline":                        domainLLMPipeline,
	"internal/llm/protocol":                        domainLLMProtocol,
	"internal/llm/protocol/anthropic/messages":     domainLLMProtocol,
	"internal/llm/protocol/gemini/generatecontent": domainLLMProtocol,
	"internal/llm/protocol/openai/chatcompletions": domainLLMProtocol,
	"internal/llm/protocol/openai/embeddings":      domainLLMProtocol,
	"internal/llm/protocol/openai/responses":       domainLLMProtocol,
	"internal/llm/provider":                        domainLLMProvider,
	"internal/llm/provider/httptransport":          domainLLMProvider,
	"internal/llm/routing":                         domainLLMRouting,
	"internal/llm/runtime":                         domainLLMRuntime,
	"internal/platform/database":                   domainPlatform,
	"internal/platform/database/postgres":          domainPlatform,
	"internal/platform/database/sqlite":            domainPlatform,
	"internal/platform/observe":                    domainPlatform,
	"internal/platform/observe/otlphttp":           domainPlatform,
	"internal/platform/observe/sqlite":             domainPlatform,
	"internal/platform/state":                      domainPlatform,
	"internal/platform/state/redis":                domainPlatform,
	"internal/platform/state/sqlite":               domainPlatform,
	"internal/quota":                               domainQuota,
	"internal/quota/redis":                         domainQuota,
	"internal/schemadump":                          domainStorage,
	"internal/security/authn":                      domainSecurity,
	"internal/security/authz":                      domainSecurity,
	"internal/storage":                             domainStorage,
	"internal/storage/database":                    domainStorage,
	"internal/storage/gen":                         domainStorage,
	"internal/storage/memory":                      domainStorage,
	"internal/storage/model":                       domainStorage,
	"internal/storage/query":                       domainStorage,
	"internal/telemetry":                           domainTelemetry,
	"internal/telemetry/schema":                    domainTelemetry,
	"internal/transport/httpserver":                domainTransport,
	"internal/version":                             domainSupport,
	"internal/webui":                               domainAdmin,
	"internal/webutil":                             domainAdmin,
}

var legacyPackagePaths = []string{
	"internal/gateway",
	"internal/gateway/runtime",
	"internal/pipeline",
	"internal/router",
	"internal/provider",
	"internal/protocol/llm",
}

func TestLegacyPackagePathsAreAbsent(t *testing.T) {
	t.Parallel()
	root := moduleRoot(t)
	for _, legacy := range legacyPackagePaths {
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(legacy))); err == nil {
			t.Errorf("legacy package path still exists: %s", legacy)
		} else if !os.IsNotExist(err) {
			t.Errorf("inspect legacy package path %s: %v", legacy, err)
		}
	}
}

func TestNoHandwrittenDocGoFiles(t *testing.T) {
	t.Parallel()
	root := moduleRoot(t)
	err := walkHandwrittenProductionGo(root, func(path, rel string, _ []byte, _ *ast.File) error {
		if filepath.Base(path) == "doc.go" {
			t.Errorf("handwritten doc.go is forbidden: %s", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNoNumericLayerPackageComments(t *testing.T) {
	t.Parallel()
	root := moduleRoot(t)
	numericLayer := regexp.MustCompile(`(?m)^//\s*Layer:\s*[0-9]`)
	err := walkHandwrittenProductionGo(root, func(_ string, rel string, raw []byte, _ *ast.File) error {
		if numericLayer.Match(raw) {
			t.Errorf("numeric architecture layer comment is forbidden: %s", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestFoundationSubtreesStayIsolated applies exact import policies inside
// isolated capability packages and subtrees.
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

func TestLLMProtocolDependsOnlyOnCanonicalIRAndItsOwnSubtree(t *testing.T) {
	t.Parallel()
	for _, pkg := range loadInternalPackages(t) {
		if !packageWithin(pkg.name, "internal/llm/protocol") {
			continue
		}
		for _, imp := range pkg.directImports {
			if imp.standard || imp.path == modulePath+"/internal/llm" ||
				packageWithin(imp.path, modulePath+"/internal/llm/protocol") {
				continue
			}
			t.Errorf("LLM protocol boundary: %s imports %s", pkg.name, imp.path)
		}
	}
}

func TestProductionHTTPImportsStayInApprovedDomains(t *testing.T) {
	t.Parallel()
	approved := []string{
		"internal/admin",
		"internal/bootstrap",
		"internal/llm/ingress/http",
		"internal/llm/provider/httptransport",
		"internal/platform/observe/otlphttp",
		"internal/telemetry",
		"internal/transport/httpserver",
		"internal/webui",
		"internal/webutil",
	}
	for _, pkg := range loadInternalPackages(t) {
		for _, imp := range pkg.directImports {
			if imp.path != "net/http" {
				continue
			}
			allowed := false
			for _, prefix := range approved {
				if packageWithin(pkg.name, prefix) {
					allowed = true
					break
				}
			}
			if !allowed {
				t.Errorf("net/http boundary: %s imports net/http", pkg.name)
			}
		}
	}
}

func TestWorkloadDomainsDoNotCrossImport(t *testing.T) {
	t.Parallel()
	for _, pkg := range loadInternalPackages(t) {
		isLLM := packageWithin(pkg.name, "internal/llm")
		isMCP := packageWithin(pkg.name, "internal/mcp")
		importsLLM := false
		importsMCP := false
		for _, imp := range pkg.imports {
			importsLLM = importsLLM || packageWithin(imp, "internal/llm")
			importsMCP = importsMCP || packageWithin(imp, "internal/mcp")
			if (isLLM && packageWithin(imp, "internal/mcp")) ||
				(isMCP && packageWithin(imp, "internal/llm")) {
				t.Errorf("workload boundary: %s imports %s", pkg.name, imp)
			}
		}
		if importsLLM && importsMCP &&
			!packageWithin(pkg.name, "internal/bootstrap") &&
			!packageWithin(pkg.name, "internal/integration") {
			t.Errorf("cross-domain composition must live in bootstrap or integration: %s imports LLM and MCP", pkg.name)
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

func TestNoHandwrittenRegistrationInitOrInternalBlankImports(t *testing.T) {
	t.Parallel()
	root := moduleRoot(t)
	err := walkHandwrittenProductionGo(root, func(_ string, rel string, _ []byte, file *ast.File) error {
		for _, declaration := range file.Decls {
			fn, ok := declaration.(*ast.FuncDecl)
			if ok && fn.Recv == nil && fn.Name.Name == "init" {
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
			if packageWithin(importPath, modulePath+"/internal") {
				t.Errorf("implicit composition: %s blank-imports %s", rel, importPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
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

func walkHandwrittenProductionGo(root string, visit func(path, rel string, raw []byte, file *ast.File) error) error {
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
			file, err := parser.ParseFile(token.NewFileSet(), path, raw, parser.ParseComments)
			if err != nil {
				return err
			}
			return visit(path, filepath.ToSlash(rel), raw, file)
		})
		if err != nil {
			return fmt.Errorf("scan %s: %w", sourceRoot, err)
		}
	}
	return nil
}

func TestEveryInternalPackageHasArchitectureDomain(t *testing.T) {
	t.Parallel()
	actual := make(map[string]bool)
	for _, pkg := range loadInternalPackages(t) {
		actual[pkg.name] = true
		if domain, ok := packageDomains[pkg.name]; !ok || domain == "" {
			t.Errorf("package %s has no architecture domain assignment", pkg.name)
		}
	}
	for pkg := range packageDomains {
		if !actual[pkg] {
			t.Errorf("architecture domain assignment names absent package %s", pkg)
		}
	}
}

// pkgInfo is one package and its production imports.
type pkgInfo struct {
	name          string   // module-relative, e.g. "internal/llm/runtime"
	imports       []string // module-relative internal import paths
	directImports []directImport
}

type listedPackage struct {
	ImportPath string
	Imports    []string
	Standard   bool
}

// loadInternalPackages shells out to `go list` for the real import graph.
// Production imports only: test-only imports may legitimately cross domains
// to assemble an integration scenario.
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
