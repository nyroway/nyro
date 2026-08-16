package resource

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/nyroway/nyro/go/internal/envflag"
)

// newModelTestRoot builds a root command with ModelCmd attached, wired the same
// way the real binary does it, so env-var binding and flag inheritance work.
func newModelTestRoot(t *testing.T, args ...string) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	root := &cobra.Command{
		Use:               "nyro",
		PersistentPreRunE: envflag.Bind,
		SilenceUsage:      true,
		SilenceErrors:     true,
	}
	root.AddCommand(ModelCmd())
	envflag.Decorate(root)
	output := new(bytes.Buffer)
	root.SetOut(output)
	root.SetErr(output)
	root.SetArgs(args)
	return root, output
}

// sampleModelRow returns a minimal modelRow for test servers to encode.
func sampleModelRow(id, model string) modelRow {
	return modelRow{
		ID:         id,
		Model:      model,
		Balance:    "weighted",
		EnableAuth: false,
		Enabled:    true,
		Providers: []modelProvider{
			{ID: "rp-1", ProviderID: "prov-abc", Model: model, Weight: 100, Priority: 1, Enabled: true},
		},
		CreatedAt: "2026-08-01T10:00:00Z",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

// --- Command shape ---

func TestModelCommandShape(t *testing.T) {
	cmd := ModelCmd()
	if cmd.Name() != "model" {
		t.Fatalf("command name = %q, want model", cmd.Name())
	}
	if len(cmd.Aliases) != 1 || cmd.Aliases[0] != "mod" {
		t.Fatalf("aliases = %v, want [mod]", cmd.Aliases)
	}
	if len(cmd.Commands()) != 5 {
		t.Fatalf("subcommands = %v, want create, ls, rm, show, update", cmd.Commands())
	}
	for _, name := range []string{"create", "ls", "rm", "show", "update"} {
		found, _, err := cmd.Find([]string{name})
		if err != nil || found.Name() != name {
			t.Fatalf("expected subcommand %q, found %v, err %v", name, found, err)
		}
	}
	found, _, err := cmd.Find([]string{"remove"})
	if err != nil || found.Name() != "rm" {
		t.Fatalf("expected remove alias for rm, found %v, err %v", found, err)
	}
}

func TestModelAliasResolves(t *testing.T) {
	root, _ := newModelTestRoot(t)
	found, remaining, err := root.Find([]string{"mod", "ls"})
	if err != nil {
		t.Fatalf("find alias: %v", err)
	}
	if found.Name() != "ls" || len(remaining) != 0 {
		t.Fatalf("Find(mod ls) = %s, remaining %v", found.Name(), remaining)
	}
}

// --- ls ---

func TestModelListRequestAndOutput(t *testing.T) {
	var method, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		_ = json.NewEncoder(w).Encode([]modelRow{sampleModelRow("route-id-1", "gpt-4o")})
	}))
	defer srv.Close()

	root, output := newModelTestRoot(t, "model", "ls", "--server", srv.URL)
	if err := root.Execute(); err != nil {
		t.Fatalf("execute model ls: %v", err)
	}
	if method != http.MethodGet {
		t.Fatalf("method = %q, want GET", method)
	}
	if path != modelListPath {
		t.Fatalf("path = %q, want %q", path, modelListPath)
	}
	for _, want := range []string{"ID", "MODEL", "BALANCE", "AUTH", "PROVIDERS", "ENABLED", "UPDATED", "route-id-1", "gpt-4o", "weighted", "1"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output missing %q\nfull output:\n%s", want, output.String())
		}
	}
}

func TestModelListEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()

	root, output := newModelTestRoot(t, "model", "ls", "--server", srv.URL)
	if err := root.Execute(); err != nil {
		t.Fatalf("execute model ls empty: %v", err)
	}
	if !strings.Contains(output.String(), "MODEL") {
		t.Fatalf("output missing table header: %q", output.String())
	}
}

func TestModelListUsesEnvironmentServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()
	t.Setenv("NYRO_MODEL_LS_SERVER", srv.URL)

	root, output := newModelTestRoot(t, "model", "ls")
	if err := root.Execute(); err != nil {
		t.Fatalf("execute model ls via env: %v", err)
	}
	if !strings.Contains(output.String(), "MODEL") {
		t.Fatalf("output = %q, want table header", output.String())
	}
}

// --- show ---

func TestModelShowByModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]modelRow{sampleModelRow("route-id-1", "gpt-4o")})
	}))
	defer srv.Close()

	root, output := newModelTestRoot(t, "model", "show", "gpt-4o", "--server", srv.URL)
	if err := root.Execute(); err != nil {
		t.Fatalf("execute model show: %v", err)
	}
	for _, want := range []string{"ID:", "route-id-1", "Model:", "gpt-4o", "Balance:", "weighted", "Auth:", "false", "Enabled:", "true", "Providers (1):", "prov-abc"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output missing %q\nfull output:\n%s", want, output.String())
		}
	}
}

func TestModelShowByID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]modelRow{sampleModelRow("route-id-1", "gpt-4o")})
	}))
	defer srv.Close()

	root, output := newModelTestRoot(t, "model", "show", "route-id-1", "--server", srv.URL)
	if err := root.Execute(); err != nil {
		t.Fatalf("execute model show by id: %v", err)
	}
	if !strings.Contains(output.String(), "gpt-4o") {
		t.Fatalf("output = %q, want model details", output.String())
	}
}

func TestModelShowNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()

	root, _ := newModelTestRoot(t, "model", "show", "nonexistent", "--server", srv.URL)
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found error, got %v", err)
	}
}

func TestModelShowRequiresExactlyOneArg(t *testing.T) {
	root, _ := newModelTestRoot(t, "model", "show")
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error for missing argument")
	}
	if !strings.Contains(err.Error(), "Usage") {
		t.Fatalf("expected Usage hint in error, got %q", err.Error())
	}
}

// --- create ---

func TestModelCreatePostBody(t *testing.T) {
	var captured createModelRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == providerListPath:
			// prov-abc has no static models — validation skipped
			_ = json.NewEncoder(w).Encode([]providerRow{{ID: "prov-abc", Name: "openai-prod"}})
		case r.Method == http.MethodPost && r.URL.Path == modelListPath:
			_ = json.NewDecoder(r.Body).Decode(&captured)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(sampleModelRow("new-id", "gpt-4o"))
		default:
			_, _ = w.Write([]byte("[]"))
		}
	}))
	defer srv.Close()

	root, _ := newModelTestRoot(t, "model", "create", "gpt-4o",
		"--provider", "prov-abc,model=gpt-4o",
		"--balance", "priority",
		"--enable-auth",
		"--server", srv.URL,
	)
	if err := root.Execute(); err != nil {
		t.Fatalf("execute model create: %v", err)
	}
	if captured.Model != "gpt-4o" {
		t.Errorf("body.model = %q, want gpt-4o", captured.Model)
	}
	if captured.Balance != "priority" {
		t.Errorf("body.balance = %q, want priority", captured.Balance)
	}
	if !captured.EnableAuth {
		t.Error("body.enable_auth = false, want true")
	}
	if len(captured.Providers) != 1 || captured.Providers[0].ProviderID != "prov-abc" {
		t.Errorf("body.upstreams = %+v, want one entry with upstream_id=prov-abc", captured.Providers)
	}
}

func TestModelCreateProviderDefaults(t *testing.T) {
	var captured createModelRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == providerListPath:
			_ = json.NewEncoder(w).Encode([]providerRow{{ID: "prov-xyz", Name: "anthropic-prod"}})
		case r.Method == http.MethodPost:
			_ = json.NewDecoder(r.Body).Decode(&captured)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(sampleModelRow("new-id", "claude-3"))
		default:
			_, _ = w.Write([]byte("[]"))
		}
	}))
	defer srv.Close()

	root, _ := newModelTestRoot(t, "model", "create", "claude-3",
		"--provider", "prov-xyz,model=claude-3-5-sonnet",
		"--server", srv.URL,
	)
	if err := root.Execute(); err != nil {
		t.Fatalf("execute model create defaults: %v", err)
	}
	p := captured.Providers[0]
	if p.Weight != 100 {
		t.Errorf("default weight = %d, want 100", p.Weight)
	}
	if p.Priority != 1 {
		t.Errorf("default priority = %d, want 1", p.Priority)
	}
	if p.Model != "claude-3-5-sonnet" {
		t.Errorf("model = %q, want claude-3-5-sonnet (explicitly specified)", p.Model)
	}
}

func TestModelCreateRequiresProvider(t *testing.T) {
	root, _ := newModelTestRoot(t, "model", "create", "gpt-4o", "--server", "http://127.0.0.1:19531")
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "--provider") {
		t.Fatalf("expected --provider required error, got %v", err)
	}
}

func TestModelCreateProviderRequiresModel(t *testing.T) {
	root, _ := newModelTestRoot(t, "model", "create", "gpt-4o",
		"--provider", "prov-abc",
		"--server", "http://127.0.0.1:19531",
	)
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "model is required") {
		t.Fatalf("expected model required error, got %v", err)
	}
}

func TestModelCreateServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"route model already exists"}`))
	}))
	defer srv.Close()

	root, _ := newModelTestRoot(t, "model", "create", "gpt-4o",
		"--provider", "prov-abc,model=gpt-4o",
		"--server", srv.URL,
	)
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "409") {
		t.Fatalf("expected 409 error, got %v", err)
	}
}

// --- update ---

func TestModelUpdateScalarOnlySkipsGet(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.Method == http.MethodGet {
			// resolve is needed for the PUT path; return one row
			_ = json.NewEncoder(w).Encode([]modelRow{sampleModelRow("route-id-1", "gpt-4o")})
			return
		}
		_ = json.NewEncoder(w).Encode(sampleModelRow("route-id-1", "gpt-4o"))
	}))
	defer srv.Close()

	root, _ := newModelTestRoot(t, "model", "update", "gpt-4o",
		"--balance", "priority",
		"--server", srv.URL,
	)
	if err := root.Execute(); err != nil {
		t.Fatalf("execute model update scalar: %v", err)
	}
	// putModel does one GET to resolve name→id, then one PUT; no extra GET for mergeProviders
	getCount := 0
	for _, m := range methods {
		if m == http.MethodGet {
			getCount++
		}
	}
	if getCount != 1 {
		t.Errorf("GET count = %d, want exactly 1 (for name resolution)", getCount)
	}
}

func TestModelUpdateAddProvider(t *testing.T) {
	var putBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == providerListPath:
			_ = json.NewEncoder(w).Encode([]providerRow{{ID: "prov-new", Name: "openai-prod"}})
		case r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]modelRow{sampleModelRow("route-id-1", "gpt-4o")})
		default:
			_ = json.NewDecoder(r.Body).Decode(&putBody)
			_ = json.NewEncoder(w).Encode(sampleModelRow("route-id-1", "gpt-4o"))
		}
	}))
	defer srv.Close()

	root, _ := newModelTestRoot(t, "model", "update", "gpt-4o",
		"--add-provider", "prov-new,model=gpt-4o,weight=50",
		"--server", srv.URL,
	)
	if err := root.Execute(); err != nil {
		t.Fatalf("execute model update add-provider: %v", err)
	}
	upstreams, ok := putBody["upstreams"].([]any)
	if !ok {
		t.Fatalf("PUT body missing upstreams, got %+v", putBody)
	}
	// original had 1 provider, we added 1, expect 2
	if len(upstreams) != 2 {
		t.Errorf("upstreams count = %d, want 2", len(upstreams))
	}
}

func TestModelUpdateRemoveProvider(t *testing.T) {
	var putBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			row := sampleModelRow("route-id-1", "gpt-4o")
			row.Providers = append(row.Providers, modelProvider{
				ID: "rp-2", ProviderID: "prov-xyz", Model: "gpt-4o", Weight: 100, Priority: 1, Enabled: true,
			})
			_ = json.NewEncoder(w).Encode([]modelRow{row})
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&putBody)
		_ = json.NewEncoder(w).Encode(sampleModelRow("route-id-1", "gpt-4o"))
	}))
	defer srv.Close()

	root, _ := newModelTestRoot(t, "model", "update", "gpt-4o",
		"--remove-provider", "prov-xyz",
		"--server", srv.URL,
	)
	if err := root.Execute(); err != nil {
		t.Fatalf("execute model update remove-provider: %v", err)
	}
	upstreams, ok := putBody["upstreams"].([]any)
	if !ok {
		t.Fatalf("PUT body missing upstreams, got %+v", putBody)
	}
	if len(upstreams) != 1 {
		t.Errorf("upstreams count = %d, want 1 after removal", len(upstreams))
	}
}

func TestModelUpdateAddProviderAlreadyBound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == providerListPath:
			// prov-abc exists, no static models — validation skipped, "already bound" check fires next
			_ = json.NewEncoder(w).Encode([]providerRow{{ID: "prov-abc", Name: "openai-prod"}})
		default:
			_ = json.NewEncoder(w).Encode([]modelRow{sampleModelRow("route-id-1", "gpt-4o")})
		}
	}))
	defer srv.Close()

	// sampleModelRow already has prov-abc bound
	root, _ := newModelTestRoot(t, "model", "update", "gpt-4o",
		"--add-provider", "prov-abc,model=gpt-4o",
		"--server", srv.URL,
	)
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "already bound") {
		t.Fatalf("expected already bound error, got %v", err)
	}
}

func TestModelUpdateRemoveProviderNotBound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]modelRow{sampleModelRow("route-id-1", "gpt-4o")})
	}))
	defer srv.Close()

	root, _ := newModelTestRoot(t, "model", "update", "gpt-4o",
		"--remove-provider", "prov-nonexistent",
		"--server", srv.URL,
	)
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "not bound") {
		t.Fatalf("expected not bound error, got %v", err)
	}
}

func TestModelUpdateFullReplaceProvider(t *testing.T) {
	var putBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == providerListPath:
			_ = json.NewEncoder(w).Encode([]providerRow{
				{ID: "prov-a", Name: "openai-prod"},
				{ID: "prov-b", Name: "anthropic-prod"},
			})
		case r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]modelRow{sampleModelRow("route-id-1", "gpt-4o")})
		default:
			_ = json.NewDecoder(r.Body).Decode(&putBody)
			_ = json.NewEncoder(w).Encode(sampleModelRow("route-id-1", "gpt-4o"))
		}
	}))
	defer srv.Close()

	root, _ := newModelTestRoot(t, "model", "update", "gpt-4o",
		"--provider", "prov-a,model=gpt-4o,weight=70",
		"--provider", "prov-b,model=gpt-4o,weight=30",
		"--server", srv.URL,
	)
	if err := root.Execute(); err != nil {
		t.Fatalf("execute model update full replace: %v", err)
	}
	upstreams, ok := putBody["upstreams"].([]any)
	if !ok {
		t.Fatalf("PUT body missing upstreams, got %+v", putBody)
	}
	if len(upstreams) != 2 {
		t.Errorf("upstreams count = %d, want 2", len(upstreams))
	}
}

func TestModelUpdateFullReplaceMutuallyExclusiveWithAdd(t *testing.T) {
	root, _ := newModelTestRoot(t, "model", "update", "gpt-4o",
		"--provider", "prov-a,model=gpt-4o",
		"--add-provider", "prov-b,model=gpt-4o",
		"--server", "http://127.0.0.1:19531",
	)
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "--provider cannot be used together") {
		t.Fatalf("expected mutual exclusion error, got %v", err)
	}
}

func TestModelUpdateFullReplaceMutuallyExclusiveWithRemove(t *testing.T) {
	root, _ := newModelTestRoot(t, "model", "update", "gpt-4o",
		"--provider", "prov-a,model=gpt-4o",
		"--remove-provider", "prov-b",
		"--server", "http://127.0.0.1:19531",
	)
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "--provider cannot be used together") {
		t.Fatalf("expected mutual exclusion error, got %v", err)
	}
}

func TestModelUpdateNoFlags(t *testing.T) {
	root, _ := newModelTestRoot(t, "model", "update", "gpt-4o", "--server", "http://127.0.0.1:19531")
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "at least one update flag") {
		t.Fatalf("expected missing flags error, got %v", err)
	}
	if !strings.Contains(err.Error(), "Usage") {
		t.Fatalf("expected Usage hint in error, got %q", err.Error())
	}
}

// --- rm ---

func TestModelRemoveRequest(t *testing.T) {
	var method, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode([]modelRow{sampleModelRow("route-id-1", "gpt-4o")})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	root, output := newModelTestRoot(t, "model", "rm", "gpt-4o", "--server", srv.URL)
	if err := root.Execute(); err != nil {
		t.Fatalf("execute model rm: %v", err)
	}
	if method != http.MethodDelete {
		t.Fatalf("final method = %q, want DELETE", method)
	}
	if path != fmt.Sprintf(modelItemPath, "route-id-1") {
		t.Fatalf("path = %q, want %q", path, fmt.Sprintf(modelItemPath, "route-id-1"))
	}
	if !strings.Contains(output.String(), "gpt-4o") || !strings.Contains(output.String(), "route-id-1") {
		t.Fatalf("output = %q, want deleted route info", output.String())
	}
}

func TestModelRemoveNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()

	root, _ := newModelTestRoot(t, "model", "rm", "nonexistent", "--server", srv.URL)
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found error, got %v", err)
	}
}

// --- validateProviderModelBindings ---

// newProviderModelServer creates a test server that serves provider list on
// /api/v1/upstreams and accepts POST on /api/v1/routes.
func newProviderModelServer(t *testing.T, providers []providerRow) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == providerListPath:
			_ = json.NewEncoder(w).Encode(providers)
		case r.Method == http.MethodPost && r.URL.Path == modelListPath:
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(sampleModelRow("new-id", "gpt-4o"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestModelCreateRejectsUnknownProvider(t *testing.T) {
	srv := newProviderModelServer(t, []providerRow{})
	defer srv.Close()

	root, _ := newModelTestRoot(t, "model", "create", "gpt-4o",
		"--provider", "nonexistent-id,model=gpt-4o",
		"--server", srv.URL,
	)
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected provider not found error, got %v", err)
	}
}

func TestModelCreateRejectsModelNotInProviderList(t *testing.T) {
	providers := []providerRow{{
		ID:         "prov-abc",
		Name:       "openai-prod",
		ModelsJSON: json.RawMessage(`["gpt-4o","gpt-4o-mini"]`),
	}}
	srv := newProviderModelServer(t, providers)
	defer srv.Close()

	root, _ := newModelTestRoot(t, "model", "create", "gpt-4o",
		"--provider", "prov-abc,model=gpt-999",
		"--server", srv.URL,
	)
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "gpt-999") {
		t.Fatalf("expected model not available error, got %v", err)
	}
	if !strings.Contains(err.Error(), "openai-prod") {
		t.Fatalf("error should mention provider name, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "gpt-4o") {
		t.Fatalf("error should list available models, got %q", err.Error())
	}
}

func TestModelCreateSkipsValidationWhenNoStaticModels(t *testing.T) {
	providers := []providerRow{{
		ID:        "prov-abc",
		Name:      "openai-prod",
		ModelsURL: "https://api.openai.com/v1/models",
		// ModelsJSON empty — dynamic discovery
	}}
	srv := newProviderModelServer(t, providers)
	defer srv.Close()

	root, _ := newModelTestRoot(t, "model", "create", "gpt-4o",
		"--provider", "prov-abc,model=any-model-name",
		"--server", srv.URL,
	)
	if err := root.Execute(); err != nil {
		t.Fatalf("should skip model validation when models_url set, got: %v", err)
	}
}

func TestModelCreatePassesWhenModelInList(t *testing.T) {
	providers := []providerRow{{
		ID:         "prov-abc",
		Name:       "openai-prod",
		ModelsJSON: json.RawMessage(`["gpt-4o","gpt-4o-mini"]`),
	}}
	srv := newProviderModelServer(t, providers)
	defer srv.Close()

	root, _ := newModelTestRoot(t, "model", "create", "gpt-4o",
		"--provider", "prov-abc,model=gpt-4o",
		"--server", srv.URL,
	)
	if err := root.Execute(); err != nil {
		t.Fatalf("unexpected error for valid model: %v", err)
	}
}

func TestModelUpdateAddProviderRejectsModelNotInList(t *testing.T) {
	providers := []providerRow{{
		ID:         "prov-new",
		Name:       "anthropic-prod",
		ModelsJSON: json.RawMessage(`["claude-3-5-sonnet","claude-3-haiku"]`),
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == providerListPath:
			_ = json.NewEncoder(w).Encode(providers)
		case r.Method == http.MethodGet && r.URL.Path == modelListPath:
			_ = json.NewEncoder(w).Encode([]modelRow{sampleModelRow("route-id-1", "gpt-4o")})
		}
	}))
	defer srv.Close()

	root, _ := newModelTestRoot(t, "model", "update", "gpt-4o",
		"--add-provider", "prov-new,model=claude-3-opus",
		"--server", srv.URL,
	)
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "claude-3-opus") {
		t.Fatalf("expected model not available error, got %v", err)
	}
}

func TestValidateProviderModelBindingsSkipsOnServerError(t *testing.T) {
	// When server is unreachable, validation should be skipped (not block the command).
	err := validateProviderModelBindings(
		context.Background(),
		&http.Client{Timeout: 10 * time.Millisecond},
		"http://127.0.0.1:1", // unreachable
		[]createModelProvider{{ProviderID: "x", Model: "m"}},
	)
	if err != nil {
		t.Fatalf("expected nil when server unreachable, got %v", err)
	}
}

// --- parseOneProviderSpec (pure function) ---

func TestParseProviderSpecIDOnly(t *testing.T) {
	_, err := parseOneProviderSpec("prov-abc")
	if err == nil || !strings.Contains(err.Error(), "model is required") {
		t.Fatalf("expected model required error, got %v", err)
	}
}

func TestParseProviderSpecWithModel(t *testing.T) {
	p, err := parseOneProviderSpec("prov-abc,model=gpt-4o")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.ProviderID != "prov-abc" || p.Model != "gpt-4o" || p.Weight != 100 || p.Priority != 1 || p.Enabled != nil {
		t.Errorf("parsed = %+v, want prov-abc/gpt-4o/weight=100/priority=1/enabled=nil", p)
	}
}

func TestParseProviderSpecAllFields(t *testing.T) {
	p, err := parseOneProviderSpec("prov-abc,model=gpt-4o-mini,weight=50,priority=2,enabled=false")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Model != "gpt-4o-mini" {
		t.Errorf("model = %q, want gpt-4o-mini", p.Model)
	}
	if p.Weight != 50 {
		t.Errorf("weight = %d, want 50", p.Weight)
	}
	if p.Priority != 2 {
		t.Errorf("priority = %d, want 2", p.Priority)
	}
	if p.Enabled == nil || *p.Enabled {
		t.Errorf("enabled = %v, want *false", p.Enabled)
	}
}

func TestParseProviderSpecEnabledTrue(t *testing.T) {
	p, err := parseOneProviderSpec("prov-abc,model=gpt-4o,enabled=true")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Enabled == nil || !*p.Enabled {
		t.Errorf("enabled = %v, want *true", p.Enabled)
	}
}

func TestParseProviderSpecEmptyID(t *testing.T) {
	_, err := parseOneProviderSpec(",model=gpt-4o")
	if err == nil || !strings.Contains(err.Error(), "provider ID cannot be empty") {
		t.Fatalf("expected empty ID error, got %v", err)
	}
}

func TestParseProviderSpecInvalidWeight(t *testing.T) {
	_, err := parseOneProviderSpec("prov-abc,model=gpt-4o,weight=abc")
	if err == nil || !strings.Contains(err.Error(), "weight") {
		t.Fatalf("expected weight error, got %v", err)
	}
}

func TestParseProviderSpecInvalidEnabled(t *testing.T) {
	_, err := parseOneProviderSpec("prov-abc,model=gpt-4o,enabled=yes")
	if err == nil || !strings.Contains(err.Error(), "enabled") {
		t.Fatalf("expected enabled error, got %v", err)
	}
}

func TestParseProviderSpecUnknownKey(t *testing.T) {
	_, err := parseOneProviderSpec("prov-abc,model=gpt-4o,foo=bar")
	if err == nil || !strings.Contains(err.Error(), "unknown option") {
		t.Fatalf("expected unknown option error, got %v", err)
	}
}

func TestParseProviderSpecMissingEquals(t *testing.T) {
	_, err := parseOneProviderSpec("prov-abc,weight")
	if err == nil || !strings.Contains(err.Error(), "key=value") {
		t.Fatalf("expected key=value error, got %v", err)
	}
}

// --- mergeProviders (pure function) ---

func TestMergeProvidersAdd(t *testing.T) {
	existing := []modelProvider{
		{ProviderID: "prov-a", Model: "gpt-4o", Weight: 100, Priority: 1, Enabled: true},
	}
	result, err := mergeProviders(existing, []string{"prov-b,model=gpt-4o,weight=50"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("len = %d, want 2", len(result))
	}
}

func TestMergeProvidersRemove(t *testing.T) {
	existing := []modelProvider{
		{ProviderID: "prov-a", Model: "gpt-4o", Weight: 100, Priority: 1, Enabled: true},
		{ProviderID: "prov-b", Model: "gpt-4o", Weight: 50, Priority: 1, Enabled: true},
	}
	result, err := mergeProviders(existing, nil, []string{"prov-b"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 || result[0].ProviderID != "prov-a" {
		t.Fatalf("result = %+v, want only prov-a", result)
	}
}

func TestMergeProvidersAddAlreadyBound(t *testing.T) {
	existing := []modelProvider{
		{ProviderID: "prov-a", Model: "gpt-4o", Weight: 100, Priority: 1, Enabled: true},
	}
	_, err := mergeProviders(existing, []string{"prov-a,model=gpt-4o"}, nil)
	if err == nil || !strings.Contains(err.Error(), "already bound") {
		t.Fatalf("expected already bound error, got %v", err)
	}
}

func TestMergeProvidersRemoveNotBound(t *testing.T) {
	existing := []modelProvider{
		{ProviderID: "prov-a", Model: "gpt-4o", Weight: 100, Priority: 1, Enabled: true},
	}
	_, err := mergeProviders(existing, nil, []string{"prov-x"})
	if err == nil || !strings.Contains(err.Error(), "not bound") {
		t.Fatalf("expected not bound error, got %v", err)
	}
}

// --- writeModels (pure function) ---

func TestWriteModelsColumnAlignment(t *testing.T) {
	models := []modelRow{
		{ID: "short", Model: "gpt-4o", Balance: "weighted", Enabled: true, Providers: []modelProvider{{}, {}}},
		{ID: "a-much-longer-id", Model: "claude-3-5-sonnet", Balance: "priority", Enabled: false},
	}
	var buf bytes.Buffer
	if err := writeModels(&buf, models, time.Now()); err != nil {
		t.Fatalf("writeModels: %v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %d, want 3 (header + 2 rows)", len(lines))
	}
	// Each line should contain the expected values
	if !strings.Contains(lines[1], "short") || !strings.Contains(lines[1], "gpt-4o") {
		t.Errorf("line 1 = %q, missing expected values", lines[1])
	}
	if !strings.Contains(lines[2], "a-much-longer-id") || !strings.Contains(lines[2], "claude-3-5-sonnet") {
		t.Errorf("line 2 = %q, missing expected values", lines[2])
	}
}

func TestWriteModelsProvidersCount(t *testing.T) {
	models := []modelRow{
		{
			ID:    "r1",
			Model: "gpt-4o",
			Providers: []modelProvider{
				{ProviderID: "a"},
				{ProviderID: "b"},
				{ProviderID: "c"},
			},
		},
	}
	var buf bytes.Buffer
	_ = writeModels(&buf, models, time.Now())
	if !strings.Contains(buf.String(), "3") {
		t.Errorf("output should contain provider count 3: %q", buf.String())
	}
}

// --- writeModel (pure function) ---

func TestWriteModelAllFields(t *testing.T) {
	ep := true
	m := modelRow{
		ID:            "route-id-1",
		Model:         "gpt-4o",
		Balance:       "weighted",
		EnableAuth:    true,
		EnablePayload: &ep,
		Enabled:       true,
		CreatedAt:     "2026-08-01T10:00:00Z",
		UpdatedAt:     "2026-08-07T10:00:00Z",
		Providers: []modelProvider{
			{ProviderID: "prov-abc", Model: "gpt-4o", Weight: 100, Priority: 1, Enabled: true},
		},
	}
	var buf bytes.Buffer
	if err := writeModel(&buf, m); err != nil {
		t.Fatalf("writeModel: %v", err)
	}
	for _, want := range []string{
		"ID:", "route-id-1",
		"Model:", "gpt-4o",
		"Balance:", "weighted",
		"Auth:", "true",
		"Payload log:", "true",
		"Enabled:", "true",
		"Created:", "2026-08-01T10:00:00Z",
		"Updated:", "2026-08-07T10:00:00Z",
		"Providers (1):",
		"prov-abc",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\nfull output:\n%s", want, buf.String())
		}
	}
}

func TestWriteModelNoProviders(t *testing.T) {
	m := modelRow{ID: "r1", Model: "gpt-4o", Balance: "weighted"}
	var buf bytes.Buffer
	if err := writeModel(&buf, m); err != nil {
		t.Fatalf("writeModel: %v", err)
	}
	if !strings.Contains(buf.String(), "none") {
		t.Errorf("output should show 'none' for empty providers: %q", buf.String())
	}
}

// --- balance strategy validation ---

func TestValidateModelBalanceAcceptsValidStrategies(t *testing.T) {
	for _, s := range []string{"weighted", "priority", "cooldown", "latency"} {
		if err := validateModelBalance(s); err != nil {
			t.Errorf("validateModelBalance(%q) returned unexpected error: %v", s, err)
		}
	}
}

func TestValidateModelBalanceRejectsUnknown(t *testing.T) {
	err := validateModelBalance("round-robin")
	if err == nil {
		t.Fatal("expected error for unknown strategy")
	}
	if !strings.Contains(err.Error(), "round-robin") {
		t.Errorf("error should mention the invalid value: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "--balance strategies") {
		t.Errorf("error should list available strategies: %q", err.Error())
	}
}

func TestFormatAvailableBalanceStrategiesListsAll(t *testing.T) {
	out := formatAvailableBalanceStrategies()
	for _, want := range []string{"weighted", "priority", "cooldown", "latency"} {
		if !strings.Contains(out, want) {
			t.Errorf("formatted strategies missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "--balance strategies") {
		t.Errorf("formatted strategies missing header:\n%s", out)
	}
}

func TestCompleteModelBalanceReturnsAll(t *testing.T) {
	completions, directive := completeModelBalance(nil, nil, "")
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive = %v, want NoFileComp", directive)
	}
	if len(completions) != len(modelBalanceStrategies) {
		t.Errorf("completions count = %d, want %d", len(completions), len(modelBalanceStrategies))
	}
}

func TestCompleteModelBalanceFiltersPrefix(t *testing.T) {
	completions, _ := completeModelBalance(nil, nil, "p")
	if len(completions) != 1 || !strings.HasPrefix(completions[0], "priority") {
		t.Errorf("completions for prefix 'p' = %v, want [priority...]", completions)
	}
}

func TestModelCreateRejectsInvalidBalance(t *testing.T) {
	root, _ := newModelTestRoot(t, "model", "create", "gpt-4o",
		"--provider", "prov-abc,model=gpt-4o",
		"--balance", "round-robin",
		"--server", "http://127.0.0.1:19531",
	)
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "round-robin") {
		t.Fatalf("expected unknown balance error, got %v", err)
	}
	if !strings.Contains(err.Error(), "--balance strategies") {
		t.Fatalf("error should list available strategies, got %q", err.Error())
	}
}

func TestModelUpdateRejectsInvalidBalance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]modelRow{sampleModelRow("route-id-1", "gpt-4o")})
	}))
	defer srv.Close()

	root, _ := newModelTestRoot(t, "model", "update", "gpt-4o",
		"--balance", "round-robin",
		"--server", srv.URL,
	)
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "round-robin") {
		t.Fatalf("expected unknown balance error, got %v", err)
	}
}

// --- normalizeModelServer ---

func TestNormalizeModelServerAddsScheme(t *testing.T) {
	got, err := normalizeModelServer("127.0.0.1:19531")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "http://127.0.0.1:19531" {
		t.Errorf("got %q, want http://127.0.0.1:19531", got)
	}
}

func TestNormalizeModelServerStripsTrailingSlash(t *testing.T) {
	got, err := normalizeModelServer("http://127.0.0.1:19531/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.HasSuffix(got, "/") {
		t.Errorf("got trailing slash: %q", got)
	}
}

func TestNormalizeModelServerRejectsEmpty(t *testing.T) {
	_, err := normalizeModelServer("")
	if err == nil {
		t.Fatal("expected error for empty address")
	}
}

func TestNormalizeModelServerRejectsQueryString(t *testing.T) {
	_, err := normalizeModelServer("http://127.0.0.1:19531?foo=bar")
	if err == nil || !strings.Contains(err.Error(), "query") {
		t.Fatalf("expected query error, got %v", err)
	}
}

// --- modelConnectionError ---

func TestModelConnectionErrorTimeout(t *testing.T) {
	err := modelConnectionError(context.DeadlineExceeded, "http://127.0.0.1:19531")
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("expected timeout message, got %q", err.Error())
	}
}

func TestModelConnectionErrorUnreachable(t *testing.T) {
	err := modelConnectionError(fmt.Errorf("connection refused"), "http://127.0.0.1:19531")
	if !strings.Contains(err.Error(), "cannot reach") {
		t.Errorf("expected unreachable message, got %q", err.Error())
	}
}
