package resource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
	"github.com/spf13/cobra"
)

const (
	defaultModelServer = "http://127.0.0.1:19531"
	modelListPath      = "/api/v1/routes"
	modelItemPath      = "/api/v1/routes/%s"
	modelHTTPTimeout   = 10 * time.Second
	maxModelBodyBytes  = 10 << 20
)

var modelHTTPClient = &http.Client{Timeout: modelHTTPTimeout}

// modelBalanceStrategies is the authoritative list of supported load balance strategies.
// Kept in sync with storage.ModelBalance constants in the backend.
var modelBalanceStrategies = []struct {
	ID          string
	Description string
}{
	{"weighted", "Distribute requests across providers by weight"},
	{"priority", "Route to highest-priority provider first, fallback on failure"},
	{"cooldown", "Skip providers in cooldown after errors, recover automatically"},
	{"latency", "Route to the lowest-latency provider"},
}

func validateModelBalance(balance string) error {
	for _, s := range modelBalanceStrategies {
		if s.ID == balance {
			return nil
		}
	}
	return fmt.Errorf("unknown balance strategy %q\n\n%s", balance, formatAvailableBalanceStrategies())
}

func formatAvailableBalanceStrategies() string {
	maxID := 0
	for _, s := range modelBalanceStrategies {
		if w := runewidth.StringWidth(s.ID); w > maxID {
			maxID = w
		}
	}
	var b strings.Builder
	b.WriteString("--balance strategies:\n")
	for _, s := range modelBalanceStrategies {
		padding := strings.Repeat(" ", maxID-runewidth.StringWidth(s.ID)+2)
		b.WriteString("  ")
		b.WriteString(s.ID)
		b.WriteString(padding)
		b.WriteString(s.Description)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// appendBalanceStrategiesHelp wraps the default help function to append the
// available balance strategies after the flags section.
func appendBalanceStrategiesHelp(cmd *cobra.Command) {
	defaultHelp := cmd.HelpFunc()
	cmd.SetHelpFunc(func(c *cobra.Command, args []string) {
		defaultHelp(c, args)
		_, _ = fmt.Fprintf(c.OutOrStdout(), "\n%s\n", formatAvailableBalanceStrategies())
	})
}

func completeModelBalance(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	toComplete = strings.ToLower(strings.TrimSpace(toComplete))
	out := make([]string, 0, len(modelBalanceStrategies))
	for _, s := range modelBalanceStrategies {
		if toComplete != "" && !strings.HasPrefix(s.ID, toComplete) {
			continue
		}
		out = append(out, fmt.Sprintf("%s\t%s", s.ID, s.Description))
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

type modelRow struct {
	ID            string          `json:"id"`
	Model         string          `json:"model"`
	Balance       string          `json:"balance"`
	EnableAuth    bool            `json:"enable_auth"`
	EnablePayload *bool           `json:"enable_payload,omitempty"`
	Enabled       bool            `json:"enabled"`
	Providers     []modelProvider `json:"upstreams,omitempty"`
	CreatedAt     string          `json:"created_at,omitempty"`
	UpdatedAt     string          `json:"updated_at,omitempty"`
}

type modelProvider struct {
	ID         string `json:"id"`
	RouteID    string `json:"route_id"`
	ProviderID string `json:"upstream_id"`
	Model      string `json:"model"`
	Weight     int32  `json:"weight"`
	Priority   int32  `json:"priority"`
	Enabled    bool   `json:"enabled"`
}

type createModelRequest struct {
	Model         string                `json:"model"`
	Balance       string                `json:"balance"`
	EnableAuth    bool                  `json:"enable_auth"`
	EnablePayload *bool                 `json:"enable_payload,omitempty"`
	Providers     []createModelProvider `json:"upstreams"`
}

type createModelProvider struct {
	ProviderID string `json:"upstream_id"`
	Model      string `json:"model"`
	Weight     int32  `json:"weight"`
	Priority   int32  `json:"priority"`
	Enabled    *bool  `json:"enabled,omitempty"`
}

// ModelCmd builds the top-level model management command.
func ModelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "model",
		Aliases: []string{"mod"},
		Short:   "Manage route models",
	}
	cmd.AddCommand(modelListCmd())
	cmd.AddCommand(modelShowCmd())
	cmd.AddCommand(modelCreateCmd())
	cmd.AddCommand(modelUpdateCmd())
	cmd.AddCommand(modelRemoveCmd())
	return cmd
}

func modelListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "ls",
		Short:         "List route models",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			server, err := cmd.Flags().GetString("server")
			if err != nil {
				return fmt.Errorf("read server flag: %w", err)
			}
			models, err := fetchModels(cmd.Context(), modelHTTPClient, server)
			if err != nil {
				return err
			}
			return writeModels(cmd.OutOrStdout(), models, time.Now())
		},
	}
	cmd.Flags().String("server", defaultModelServer, "Nyro control-plane address")
	return cmd
}

func modelShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "show <model-or-id>",
		Short:         "Show route model details",
		Args:          modelExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			server, err := cmd.Flags().GetString("server")
			if err != nil {
				return fmt.Errorf("read server flag: %w", err)
			}
			models, err := fetchModels(cmd.Context(), modelHTTPClient, server)
			if err != nil {
				return err
			}
			found := findModel(models, args[0])
			if found == nil {
				return fmt.Errorf("route model %q not found", args[0])
			}
			return writeModel(cmd.OutOrStdout(), *found)
		},
	}
	cmd.Flags().String("server", defaultModelServer, "Nyro control-plane address")
	return cmd
}

func modelCreateCmd() *cobra.Command {
	var (
		providerSpecs []string
		balance       string
		enableAuth    bool
		enablePayload bool
	)
	cmd := &cobra.Command{
		Use:           "create <model>",
		Short:         "Create a route model",
		Args:          modelExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			server, err := cmd.Flags().GetString("server")
			if err != nil {
				return fmt.Errorf("read server flag: %w", err)
			}
			if len(providerSpecs) == 0 {
				return fmt.Errorf(
					"at least one --provider is required\n\n%s",
					formatModelCreateHints(cmd),
				)
			}
			providers, err := parseProviderSpecs(providerSpecs)
			if err != nil {
				return fmt.Errorf("%w\n\n%s", err, formatModelCreateHints(cmd))
			}
			if balance == "" {
				balance = "weighted"
			}
			if err := validateModelBalance(balance); err != nil {
				return err
			}
			if err := validateProviderModelBindings(cmd.Context(), modelHTTPClient, server, providers); err != nil {
				return err
			}
			var ep *bool
			if cmd.Flags().Changed("enable-payload") {
				ep = &enablePayload
			}
			req := createModelRequest{
				Model:         strings.TrimSpace(args[0]),
				Balance:       balance,
				EnableAuth:    enableAuth,
				EnablePayload: ep,
				Providers:     providers,
			}
			created, err := postModel(cmd.Context(), modelHTTPClient, server, req)
			if err != nil {
				return err
			}
			return writeModel(cmd.OutOrStdout(), created)
		},
	}
	cmd.Flags().String("server", defaultModelServer, "Nyro control-plane address")
	cmd.Flags().StringArrayVar(&providerSpecs, "provider", nil, "Provider binding spec (model required, repeatable)")
	cmd.Flags().StringVar(&balance, "balance", "weighted", "Load balance strategy (weighted|priority|cooldown|latency)")
	cmd.Flags().BoolVar(&enableAuth, "enable-auth", false, "Enable route authentication")
	cmd.Flags().BoolVar(&enablePayload, "enable-payload", false, "Enable payload logging")
	_ = cmd.RegisterFlagCompletionFunc("balance", completeModelBalance)
	appendBalanceStrategiesHelp(cmd)
	return cmd
}

func modelUpdateCmd() *cobra.Command {
	var (
		name          string
		balance       string
		enableAuth    bool
		enablePayload bool
		enabled       bool
		providerSpecs []string
		addSpecs      []string
		removeIDs     []string
	)
	cmd := &cobra.Command{
		Use:           "update <model-or-id>",
		Short:         "Update a route model",
		Args:          modelExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			server, err := cmd.Flags().GetString("server")
			if err != nil {
				return fmt.Errorf("read server flag: %w", err)
			}

			hasScalar := cmd.Flags().Changed("name") ||
				cmd.Flags().Changed("balance") ||
				cmd.Flags().Changed("enable-auth") ||
				cmd.Flags().Changed("enable-payload") ||
				cmd.Flags().Changed("enabled")
			hasFullReplace := len(providerSpecs) > 0
			hasIncremental := len(addSpecs) > 0 || len(removeIDs) > 0

			if !hasScalar && !hasFullReplace && !hasIncremental {
				return modelUpdateMissingFlagsError(cmd)
			}
			if hasFullReplace && hasIncremental {
				return errors.New(
					"--provider cannot be used together with --add-provider or --remove-provider",
				)
			}

			body := map[string]any{}

			if cmd.Flags().Changed("name") {
				n := strings.TrimSpace(name)
				if n == "" {
					return errors.New("model name cannot be empty")
				}
				body["model"] = n
			}
			if cmd.Flags().Changed("balance") {
				if err := validateModelBalance(balance); err != nil {
					return err
				}
				body["balance"] = balance
			}
			if cmd.Flags().Changed("enable-auth") {
				body["enable_auth"] = enableAuth
			}
			if cmd.Flags().Changed("enable-payload") {
				body["enable_payload"] = enablePayload
			}
			if cmd.Flags().Changed("enabled") {
				body["enabled"] = enabled
			}

			if hasFullReplace {
				parsed, parseErr := parseProviderSpecs(providerSpecs)
				if parseErr != nil {
					return parseErr
				}
				if err := validateProviderModelBindings(cmd.Context(), modelHTTPClient, server, parsed); err != nil {
					return err
				}
				body["upstreams"] = parsed
			} else if hasIncremental {
				baseServer, err := normalizeModelServer(server)
				if err != nil {
					return err
				}
				if len(addSpecs) > 0 {
					parsed, parseErr := parseProviderSpecs(addSpecs)
					if parseErr != nil {
						return parseErr
					}
					if err := validateProviderModelBindings(cmd.Context(), modelHTTPClient, baseServer, parsed); err != nil {
						return err
					}
				}
				existing, err := resolveModel(cmd.Context(), modelHTTPClient, baseServer, args[0])
				if err != nil {
					return err
				}
				merged, err := mergeProviders(existing.Providers, addSpecs, removeIDs)
				if err != nil {
					return err
				}
				body["upstreams"] = merged
			}

			updated, err := putModel(cmd.Context(), modelHTTPClient, server, args[0], body)
			if err != nil {
				return err
			}
			return writeModel(cmd.OutOrStdout(), updated)
		},
	}
	cmd.Flags().String("server", defaultModelServer, "Nyro control-plane address")
	cmd.Flags().StringVar(&name, "name", "", "New route model name")
	cmd.Flags().StringVar(&balance, "balance", "", "Load balance strategy (weighted|priority|cooldown|latency)")
	cmd.Flags().BoolVar(&enableAuth, "enable-auth", false, "Enable route authentication")
	cmd.Flags().BoolVar(&enablePayload, "enable-payload", false, "Enable payload logging")
	cmd.Flags().BoolVar(&enabled, "enabled", true, "Enable or disable route")
	cmd.Flags().StringArrayVar(&providerSpecs, "provider", nil, "Set all provider bindings, replacing any existing ones")
	cmd.Flags().StringArrayVar(&addSpecs, "add-provider", nil, "Add a binding without affecting existing ones")
	cmd.Flags().StringArrayVar(&removeIDs, "remove-provider", nil, "Remove a binding by provider ID")
	_ = cmd.RegisterFlagCompletionFunc("balance", completeModelBalance)
	appendBalanceStrategiesHelp(cmd)
	return cmd
}

func modelRemoveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "rm <model-or-id>",
		Aliases:       []string{"remove"},
		Short:         "Remove a route model",
		Args:          modelExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			server, err := cmd.Flags().GetString("server")
			if err != nil {
				return fmt.Errorf("read server flag: %w", err)
			}
			removed, err := removeModel(cmd.Context(), modelHTTPClient, server, args[0])
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Deleted route model %q (%s)\n", removed.Model, removed.ID)
			return err
		},
	}
	cmd.Flags().String("server", defaultModelServer, "Nyro control-plane address")
	return cmd
}

// parseProviderSpecs parses a slice of --provider flag values into createModelProvider entries.
// Each spec has the format: <provider-id>,model=<m>[,weight=<w>][,priority=<p>][,enabled=false]
// The model key is required; no default is applied.
func parseProviderSpecs(specs []string) ([]createModelProvider, error) {
	out := make([]createModelProvider, 0, len(specs))
	for _, spec := range specs {
		p, err := parseOneProviderSpec(spec)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func parseOneProviderSpec(spec string) (createModelProvider, error) {
	parts := strings.Split(spec, ",")
	id := strings.TrimSpace(parts[0])
	if id == "" {
		return createModelProvider{}, fmt.Errorf("provider spec %q: provider ID cannot be empty", spec)
	}
	p := createModelProvider{
		ProviderID: id,
		Weight:     100,
		Priority:   1,
	}
	for _, kv := range parts[1:] {
		kv = strings.TrimSpace(kv)
		if kv == "" {
			continue
		}
		key, val, ok := strings.Cut(kv, "=")
		if !ok {
			return createModelProvider{}, fmt.Errorf("provider spec %q: invalid option %q (expected key=value)", spec, kv)
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "model":
			if val == "" {
				return createModelProvider{}, fmt.Errorf("provider spec %q: model cannot be empty", spec)
			}
			p.Model = val
		case "weight":
			var w int32
			if _, err := fmt.Sscanf(val, "%d", &w); err != nil || w < 0 {
				return createModelProvider{}, fmt.Errorf("provider spec %q: weight must be a non-negative integer", spec)
			}
			p.Weight = w
		case "priority":
			var pr int32
			if _, err := fmt.Sscanf(val, "%d", &pr); err != nil || pr < 0 {
				return createModelProvider{}, fmt.Errorf("provider spec %q: priority must be a non-negative integer", spec)
			}
			p.Priority = pr
		case "enabled":
			switch val {
			case "true":
				t := true
				p.Enabled = &t
			case "false":
				f := false
				p.Enabled = &f
			default:
				return createModelProvider{}, fmt.Errorf("provider spec %q: enabled must be true or false", spec)
			}
		default:
			return createModelProvider{}, fmt.Errorf("provider spec %q: unknown option %q", spec, key)
		}
	}
	if p.Model == "" {
		return createModelProvider{}, fmt.Errorf("provider spec %q: model is required (e.g. %s,model=<model-name>)", spec, id)
	}
	return p, nil
}

// validateProviderModelBindings fetches the upstream list once and checks that each provider
// in specs exists and, if it has a static models list, the requested model is in that list.
// When a provider uses models_url (dynamic discovery), model validation is skipped.
// If the server is unreachable the validation is skipped so the actual create/update call
// surfaces the connection error with consistent messaging.
func validateProviderModelBindings(ctx context.Context, client *http.Client, server string, specs []createModelProvider) error {
	if len(specs) == 0 {
		return nil
	}
	providers, err := fetchProviders(ctx, client, server)
	if err != nil {
		return nil
	}
	for _, spec := range specs {
		var found *providerRow
		for i := range providers {
			if providers[i].ID == spec.ProviderID {
				found = &providers[i]
				break
			}
		}
		if found == nil {
			return fmt.Errorf("provider %q not found", spec.ProviderID)
		}
		models := providerModels(*found)
		if len(models) == 0 {
			continue
		}
		valid := false
		for _, m := range models {
			if m == spec.Model {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf(
				"model %q is not available on provider %q\n\nAvailable models:\n%s",
				spec.Model,
				found.Name,
				formatProviderModelList(models),
			)
		}
	}
	return nil
}

func formatProviderModelList(models []string) string {
	var b strings.Builder
	for _, m := range models {
		b.WriteString("  ")
		b.WriteString(m)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// mergeProviders applies --add-provider and --remove-provider on top of the existing provider list.
func mergeProviders(existing []modelProvider, addSpecs, removeIDs []string) ([]createModelProvider, error) {
	current := make([]createModelProvider, 0, len(existing))
	for _, p := range existing {
		var ep *bool
		if !p.Enabled {
			f := false
			ep = &f
		}
		current = append(current, createModelProvider{
			ProviderID: p.ProviderID,
			Model:      p.Model,
			Weight:     p.Weight,
			Priority:   p.Priority,
			Enabled:    ep,
		})
	}

	for _, rid := range removeIDs {
		rid = strings.TrimSpace(rid)
		found := false
		for _, p := range current {
			if p.ProviderID == rid {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("provider %q is not bound to this route", rid)
		}
		next := current[:0]
		for _, p := range current {
			if p.ProviderID != rid {
				next = append(next, p)
			}
		}
		current = next
	}

	for _, spec := range addSpecs {
		p, err := parseOneProviderSpec(spec)
		if err != nil {
			return nil, err
		}
		for _, existing := range current {
			if existing.ProviderID == p.ProviderID {
				return nil, fmt.Errorf("provider %q is already bound to this route", p.ProviderID)
			}
		}
		current = append(current, p)
	}

	return current, nil
}

func fetchModels(ctx context.Context, client *http.Client, server string) ([]modelRow, error) {
	baseServer, err := normalizeModelServer(server)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseServer+modelListPath, nil)
	if err != nil {
		return nil, fmt.Errorf("build model list request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, modelConnectionError(err, baseServer)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxModelBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("read model list response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, modelStatusError(resp.StatusCode, body)
	}
	var models []modelRow
	if err := json.Unmarshal(body, &models); err != nil {
		return nil, fmt.Errorf("decode model list response: %w", err)
	}
	return models, nil
}

func resolveModel(ctx context.Context, client *http.Client, server, nameOrID string) (*modelRow, error) {
	models, err := fetchModels(ctx, client, server)
	if err != nil {
		return nil, err
	}
	found := findModel(models, nameOrID)
	if found == nil {
		return nil, fmt.Errorf("route model %q not found", nameOrID)
	}
	return found, nil
}

func findModel(models []modelRow, nameOrID string) *modelRow {
	for i := range models {
		if models[i].ID == nameOrID {
			return &models[i]
		}
	}
	for i := range models {
		if models[i].Model == nameOrID {
			return &models[i]
		}
	}
	return nil
}

func postModel(ctx context.Context, client *http.Client, server string, req createModelRequest) (modelRow, error) {
	baseServer, err := normalizeModelServer(server)
	if err != nil {
		return modelRow{}, err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return modelRow{}, fmt.Errorf("encode model create request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseServer+modelListPath, bytes.NewReader(body))
	if err != nil {
		return modelRow{}, fmt.Errorf("build model create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(httpReq)
	if err != nil {
		return modelRow{}, modelConnectionError(err, baseServer)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxModelBodyBytes))
	if err != nil {
		return modelRow{}, fmt.Errorf("read model create response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return modelRow{}, modelStatusError(resp.StatusCode, respBody)
	}
	var created modelRow
	if err := json.Unmarshal(respBody, &created); err != nil {
		return modelRow{}, fmt.Errorf("decode model create response: %w", err)
	}
	return created, nil
}

func putModel(ctx context.Context, client *http.Client, server, nameOrID string, input map[string]any) (modelRow, error) {
	baseServer, err := normalizeModelServer(server)
	if err != nil {
		return modelRow{}, err
	}
	existing, err := resolveModel(ctx, client, baseServer, nameOrID)
	if err != nil {
		return modelRow{}, err
	}
	body, err := json.Marshal(input)
	if err != nil {
		return modelRow{}, fmt.Errorf("encode model update request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, baseServer+fmt.Sprintf(modelItemPath, existing.ID), bytes.NewReader(body))
	if err != nil {
		return modelRow{}, fmt.Errorf("build model update request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(httpReq)
	if err != nil {
		return modelRow{}, modelConnectionError(err, baseServer)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxModelBodyBytes))
	if err != nil {
		return modelRow{}, fmt.Errorf("read model update response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return modelRow{}, modelStatusError(resp.StatusCode, respBody)
	}
	var updated modelRow
	if err := json.Unmarshal(respBody, &updated); err != nil {
		return modelRow{}, fmt.Errorf("decode model update response: %w", err)
	}
	return updated, nil
}

func removeModel(ctx context.Context, client *http.Client, server, nameOrID string) (modelRow, error) {
	baseServer, err := normalizeModelServer(server)
	if err != nil {
		return modelRow{}, err
	}
	existing, err := resolveModel(ctx, client, baseServer, nameOrID)
	if err != nil {
		return modelRow{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, baseServer+fmt.Sprintf(modelItemPath, existing.ID), nil)
	if err != nil {
		return modelRow{}, fmt.Errorf("build model delete request: %w", err)
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return modelRow{}, modelConnectionError(err, baseServer)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		return *existing, nil
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxModelBodyBytes))
	return modelRow{}, modelStatusError(resp.StatusCode, respBody)
}

func normalizeModelServer(server string) (string, error) {
	server = strings.TrimSpace(server)
	if server == "" {
		return "", errors.New("server address cannot be empty")
	}
	if !strings.Contains(server, "://") {
		server = "http://" + server
	}
	parsed, err := url.Parse(server)
	if err != nil {
		return "", fmt.Errorf("invalid server address: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("invalid server address: unsupported scheme %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", errors.New("invalid server address: missing host")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("invalid server address: query and fragment are not allowed")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return strings.TrimRight(parsed.String(), "/"), nil
}

func modelConnectionError(err error, server string) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("nyro server did not respond (request timed out)")
	}
	parsed, parseErr := url.Parse(server)
	display := server
	if parseErr == nil && parsed.Host != "" {
		display = parsed.Host
	}
	return fmt.Errorf("cannot reach nyro server at %s", display)
}

func modelStatusError(statusCode int, body []byte) error {
	var response struct {
		Error string `json:"error"`
	}
	message := ""
	if json.Unmarshal(body, &response) == nil {
		message = strings.TrimSpace(response.Error)
	}
	if message == "" {
		message = strings.TrimSpace(string(body))
	}
	if message == "" {
		message = http.StatusText(statusCode)
	}
	return fmt.Errorf("server returned %d: %s", statusCode, message)
}

// modelExactArgs returns a PositionalArgs validator that appends Usage on failure.
func modelExactArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := cobra.ExactArgs(n)(cmd, args); err != nil {
			return fmt.Errorf("%w\n\nUsage:\n  %s", err, cmd.UseLine())
		}
		return nil
	}
}

func modelUpdateMissingFlagsError(cmd *cobra.Command) error {
	return fmt.Errorf(
		"at least one update flag is required\n\nUsage:\n  %s\n\nFlags:\n%s",
		cmd.UseLine(),
		strings.TrimRight(cmd.LocalFlags().FlagUsages(), "\n"),
	)
}

func formatModelCreateHints(cmd *cobra.Command) string {
	return strings.TrimRight(fmt.Sprintf(
		"Arguments:\n"+
			"  model      Route model name (must be unique)\n\n"+
			"Flags:\n%s",
		cmd.LocalFlags().FlagUsages(),
	), "\n")
}

func writeModels(w io.Writer, models []modelRow, now time.Time) error {
	rows := make([][7]string, 0, len(models)+1)
	rows = append(rows, [7]string{"ID", "MODEL", "BALANCE", "AUTH", "PROVIDERS", "ENABLED", "UPDATED"})
	for _, m := range models {
		rows = append(rows, [7]string{
			m.ID,
			m.Model,
			valueOrDash(m.Balance),
			fmt.Sprintf("%t", m.EnableAuth),
			fmt.Sprintf("%d", len(m.Providers)),
			fmt.Sprintf("%t", m.Enabled),
			humanizeProviderTime(m.UpdatedAt, now),
		})
	}

	var widths [7]int
	for _, row := range rows {
		for col, val := range row {
			if w := runewidth.StringWidth(val); w > widths[col] {
				widths[col] = w
			}
		}
	}

	const padding = 3
	for _, row := range rows {
		for col, val := range row {
			if _, err := io.WriteString(w, val); err != nil {
				return err
			}
			if col == len(row)-1 {
				continue
			}
			spaces := widths[col] - runewidth.StringWidth(val) + padding
			if _, err := io.WriteString(w, strings.Repeat(" ", spaces)); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(w, "\n"); err != nil {
			return err
		}
	}
	return nil
}

func writeModel(w io.Writer, m modelRow) error {
	enablePayload := "false"
	if m.EnablePayload != nil {
		enablePayload = fmt.Sprintf("%t", *m.EnablePayload)
	}

	fields := [][2]string{
		{"ID", m.ID},
		{"Model", m.Model},
		{"Balance", valueOrDash(m.Balance)},
		{"Auth", fmt.Sprintf("%t", m.EnableAuth)},
		{"Payload log", enablePayload},
		{"Enabled", fmt.Sprintf("%t", m.Enabled)},
		{"Created", valueOrDash(m.CreatedAt)},
		{"Updated", valueOrDash(m.UpdatedAt)},
	}
	for _, field := range fields {
		if _, err := fmt.Fprintf(w, "%-13s %s\n", field[0]+":", field[1]); err != nil {
			return err
		}
	}

	if len(m.Providers) == 0 {
		_, err := fmt.Fprintf(w, "\nProviders:    none\n")
		return err
	}

	if _, err := fmt.Fprintf(w, "\nProviders (%d):\n", len(m.Providers)); err != nil {
		return err
	}

	pRows := make([][6]string, 0, len(m.Providers)+1)
	pRows = append(pRows, [6]string{"PROVIDER ID", "MODEL", "WEIGHT", "PRIORITY", "ENABLED"})
	for _, p := range m.Providers {
		pRows = append(pRows, [6]string{
			p.ProviderID,
			p.Model,
			fmt.Sprintf("%d", p.Weight),
			fmt.Sprintf("%d", p.Priority),
			fmt.Sprintf("%t", p.Enabled),
		})
	}

	var pWidths [6]int
	for _, row := range pRows {
		for col, val := range row {
			if w := runewidth.StringWidth(val); w > pWidths[col] {
				pWidths[col] = w
			}
		}
	}

	const padding = 3
	for _, row := range pRows {
		if _, err := io.WriteString(w, "  "); err != nil {
			return err
		}
		for col, val := range row {
			if _, err := io.WriteString(w, val); err != nil {
				return err
			}
			if col == len(row)-1 {
				continue
			}
			spaces := pWidths[col] - runewidth.StringWidth(val) + padding
			if _, err := io.WriteString(w, strings.Repeat(" ", spaces)); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(w, "\n"); err != nil {
			return err
		}
	}
	return nil
}
