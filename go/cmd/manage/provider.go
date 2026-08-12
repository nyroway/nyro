// Package manage implements top-level CLI commands for managing nyro
// control-plane resources through the Admin API.
package manage

import (
	"bufio"
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

	"github.com/nyroway/nyro/go/internal/protocol/llm/spec"
)

const (
	defaultProviderServer     = "http://127.0.0.1:19531"
	providerListPath          = "/api/v1/upstreams"
	providerPresetsPath       = "/api/v1/provider-presets"
	providerDraftTestPath     = "/api/v1/upstreams/test-draft/stream"
	providerEditDraftTestPath = "/api/v1/upstreams/%s/test-draft/stream"
	providerItemPath          = "/api/v1/upstreams/%s"
	providerHTTPTimeout       = 10 * time.Second
	providerTestTimeout       = 45 * time.Second
	maxProviderBodyBytes      = 10 << 20
)

var (
	providerHTTPClient     = &http.Client{Timeout: providerHTTPTimeout}
	providerTestHTTPClient = &http.Client{Timeout: providerTestTimeout}
)

type providerRow struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Provider        string          `json:"provider"`
	Protocol        string          `json:"protocol"`
	BaseURL         string          `json:"base_url"`
	CredentialsJSON json.RawMessage `json:"credentials"`
	ModelsJSON      json.RawMessage `json:"models"`
	ModelsURL       string          `json:"models_url"`
	ProxyURL        string          `json:"proxy_url"`
	Enabled         bool            `json:"enabled"`
	CreatedAt       string          `json:"created_at"`
	UpdatedAt       string          `json:"updated_at"`
}

type providerPreset struct {
	ID              string                    `json:"id"`
	Name            string                    `json:"name"`
	DefaultProtocol string                    `json:"default_protocol"`
	Protocols       []providerPresetProtocol  `json:"protocols"`
	Credentials     providerPresetCredentials `json:"credentials"`
	ModelsURL       string                    `json:"models_url"`
}

type providerPresetProtocol struct {
	ID      string `json:"id"`
	BaseURL string `json:"base_url"`
}

type providerPresetCredentials struct {
	Fields []providerCredentialField `json:"fields"`
}

type providerCredentialField struct {
	Name    string `json:"name"`
	Default string `json:"default"`
}

type createProviderRequest struct {
	Name        string            `json:"name"`
	Provider    string            `json:"provider"`
	Protocol    string            `json:"protocol,omitempty"`
	BaseURL     string            `json:"base_url,omitempty"`
	Credentials map[string]string `json:"credentials"`
	Models      []string          `json:"models,omitempty"`
	ModelsURL   string            `json:"models_url,omitempty"`
	ProxyURL    string            `json:"proxy_url,omitempty"`
	Enabled     bool              `json:"enabled"`
}

type providerHealthEvent struct {
	Type    string `json:"type"`
	Check   string `json:"check"`
	Status  string `json:"status"`
	Message string `json:"message"`
	Error   string `json:"error"`
	Success *bool  `json:"success"`
}

// ProviderCmd builds the top-level provider management command.
func ProviderCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "provider",
		Aliases: []string{"prov"},
		Short:   "Manage providers",
	}
	cmd.AddCommand(providerListCmd())
	cmd.AddCommand(providerShowCmd())
	cmd.AddCommand(providerCreateCmd())
	cmd.AddCommand(providerUpdateCmd())
	cmd.AddCommand(providerRemoveCmd())
	return cmd
}

func providerListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "ls",
		Short:         "List providers",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			server, err := cmd.Flags().GetString("server")
			if err != nil {
				return fmt.Errorf("read server flag: %w", err)
			}
			providers, err := fetchProviders(cmd.Context(), providerHTTPClient, server)
			if err != nil {
				return err
			}
			return writeProviders(cmd.OutOrStdout(), providers, time.Now())
		},
	}
	cmd.Flags().String("server", defaultProviderServer, "Nyro control-plane address")
	return cmd
}

func providerShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "show <name-or-id>",
		Short:         "Show provider details",
		Args:          providerExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			server, err := cmd.Flags().GetString("server")
			if err != nil {
				return fmt.Errorf("read server flag: %w", err)
			}
			providers, err := fetchProviders(cmd.Context(), providerHTTPClient, server)
			if err != nil {
				return err
			}
			found := findProvider(providers, args[0])
			if found == nil {
				return fmt.Errorf("provider %q not found", args[0])
			}
			return writeProvider(cmd.OutOrStdout(), *found)
		},
	}
	cmd.Flags().String("server", defaultProviderServer, "Nyro control-plane address")
	return cmd
}

func providerCreateCmd() *cobra.Command {
	var (
		name      string
		protocol  string
		baseURL   string
		modelsURL string
		models    []string
		proxyURL  string
		enabled   bool
	)
	cmd := &cobra.Command{
		Use:           "create <provider> <api-key>",
		Short:         "Create a provider",
		Args:          providerCreateArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) != 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			server, _ := cmd.Flags().GetString("server")
			completions, err := completeCreateProviders(cmd.Context(), providerHTTPClient, server, toComplete)
			if err != nil {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			return completions, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[1]) == "" {
				return fmt.Errorf("api-key cannot be empty\n\n%s", formatCreateParameterHints(cmd))
			}
			server, err := cmd.Flags().GetString("server")
			if err != nil {
				return fmt.Errorf("read server flag: %w", err)
			}
			if cmd.Flags().Changed("model") && cmd.Flags().Changed("models-url") {
				return fmt.Errorf(
					"--model and --models-url are mutually exclusive\n\n%s",
					formatCreateParameterHints(cmd),
				)
			}
			options := createProviderOptions{
				Provider:         args[0],
				APIKey:           args[1],
				Name:             name,
				Protocol:         protocol,
				BaseURL:          baseURL,
				ModelsURL:        modelsURL,
				Models:           models,
				ProxyURL:         proxyURL,
				Enabled:          enabled,
				ModelsChanged:    cmd.Flags().Changed("model"),
				ModelsURLChanged: cmd.Flags().Changed("models-url"),
			}
			created, err := createProvider(
				cmd.Context(),
				providerHTTPClient,
				providerTestHTTPClient,
				server,
				options,
				cmd.OutOrStdout(),
			)
			if err != nil {
				err = redactProviderSecret(err, args[1])
				if shouldAttachCreateParameterHints(err) {
					return fmt.Errorf("%w\n\n%s", err, formatCreateParameterHints(cmd))
				}
				return err
			}
			return writeProvider(cmd.OutOrStdout(), created)
		},
	}
	cmd.Flags().String("server", defaultProviderServer, "Nyro control-plane address")
	cmd.Flags().StringVar(&name, "name", "", "Provider instance name (default: preset name)")
	cmd.Flags().StringVar(&protocol, "protocol", "", "Protocol override (default: preset; see Available protocols)")
	cmd.Flags().StringVar(&baseURL, "base-url", "", "Base URL override (default: preset URL for the protocol)")
	cmd.Flags().StringVar(&modelsURL, "models-url", "", "Model discovery URL (default: preset models URL)")
	cmd.Flags().StringSliceVar(&models, "model", nil, "Static model ID, e.g. gpt-4o (exclusive with --models-url)")
	cmd.Flags().StringVar(&proxyURL, "proxy-url", "", "Outbound proxy URL (optional), e.g. http://127.0.0.1:7890")
	cmd.Flags().BoolVar(&enabled, "enabled", true, "Enable provider after creation")
	registerProtocolFlagCompletion(cmd, true)
	return cmd
}

func providerUpdateCmd() *cobra.Command {
	var (
		name      string
		apiKey    string
		protocol  string
		baseURL   string
		modelsURL string
		models    []string
		proxyURL  string
		enabled   bool
	)
	cmd := &cobra.Command{
		Use:           "update <name-or-id>",
		Short:         "Update a provider",
		Args:          providerExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			server, err := cmd.Flags().GetString("server")
			if err != nil {
				return fmt.Errorf("read server flag: %w", err)
			}
			if cmd.Flags().Changed("model") && cmd.Flags().Changed("models-url") {
				return errors.New("--model and --models-url are mutually exclusive")
			}
			options := updateProviderOptions{
				NameOrID:         args[0],
				Name:             name,
				APIKey:           apiKey,
				Protocol:         protocol,
				BaseURL:          baseURL,
				ModelsURL:        modelsURL,
				Models:           models,
				ProxyURL:         proxyURL,
				Enabled:          enabled,
				NameChanged:      cmd.Flags().Changed("name"),
				APIKeyChanged:    cmd.Flags().Changed("api-key"),
				ProtocolChanged:  cmd.Flags().Changed("protocol"),
				BaseURLChanged:   cmd.Flags().Changed("base-url"),
				ModelsChanged:    cmd.Flags().Changed("model"),
				ModelsURLChanged: cmd.Flags().Changed("models-url"),
				ProxyURLChanged:  cmd.Flags().Changed("proxy-url"),
				EnabledChanged:   cmd.Flags().Changed("enabled"),
			}
			if !options.hasChanges() {
				return updateMissingFlagsError(cmd.Context(), providerHTTPClient, server, args[0], cmd)
			}
			if options.APIKeyChanged && strings.TrimSpace(options.APIKey) == "" {
				return errors.New("api-key cannot be empty")
			}
			updated, err := updateProvider(
				cmd.Context(),
				providerHTTPClient,
				providerTestHTTPClient,
				server,
				options,
				cmd.OutOrStdout(),
			)
			if err != nil {
				return redactProviderSecret(err, options.APIKey)
			}
			return writeProvider(cmd.OutOrStdout(), updated)
		},
	}
	cmd.Flags().String("server", defaultProviderServer, "Nyro control-plane address")
	cmd.Flags().StringVar(&name, "name", "", "Provider instance name")
	cmd.Flags().StringVar(&apiKey, "api-key", "", "Provider API key")
	cmd.Flags().StringVar(&protocol, "protocol", "", "Provider protocol (see Available protocols)")
	cmd.Flags().StringVar(&baseURL, "base-url", "", "Provider base URL")
	cmd.Flags().StringVar(&modelsURL, "models-url", "", "Model discovery URL, e.g. https://api.openai.com/v1/models")
	cmd.Flags().StringSliceVar(&models, "model", nil, "Static model ID, e.g. gpt-4o (exclusive with --models-url)")
	cmd.Flags().StringVar(&proxyURL, "proxy-url", "", "Outbound proxy URL, e.g. http://127.0.0.1:7890")
	cmd.Flags().BoolVar(&enabled, "enabled", true, "Enable provider")
	registerProtocolFlagCompletion(cmd, false)
	return cmd
}

func providerRemoveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "rm <name-or-id>",
		Aliases:       []string{"remove"},
		Short:         "Remove a provider",
		Args:          providerExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			server, err := cmd.Flags().GetString("server")
			if err != nil {
				return fmt.Errorf("read server flag: %w", err)
			}
			removed, err := removeProvider(cmd.Context(), providerHTTPClient, server, args[0])
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Deleted provider %q (%s)\n", removed.Name, removed.ID)
			return err
		},
	}
	cmd.Flags().String("server", defaultProviderServer, "Nyro control-plane address")
	return cmd
}

type createProviderOptions struct {
	Provider         string
	APIKey           string
	Name             string
	Protocol         string
	BaseURL          string
	ModelsURL        string
	Models           []string
	ProxyURL         string
	Enabled          bool
	ModelsChanged    bool
	ModelsURLChanged bool
}

type updateProviderOptions struct {
	NameOrID         string
	Name             string
	APIKey           string
	Protocol         string
	BaseURL          string
	ModelsURL        string
	Models           []string
	ProxyURL         string
	Enabled          bool
	NameChanged      bool
	APIKeyChanged    bool
	ProtocolChanged  bool
	BaseURLChanged   bool
	ModelsChanged    bool
	ModelsURLChanged bool
	ProxyURLChanged  bool
	EnabledChanged   bool
}

func (o updateProviderOptions) hasChanges() bool {
	return o.NameChanged ||
		o.APIKeyChanged ||
		o.ProtocolChanged ||
		o.BaseURLChanged ||
		o.ModelsChanged ||
		o.ModelsURLChanged ||
		o.ProxyURLChanged ||
		o.EnabledChanged
}

func createProvider(
	ctx context.Context,
	client *http.Client,
	testClient *http.Client,
	server string,
	options createProviderOptions,
	output io.Writer,
) (providerRow, error) {
	baseServer, err := normalizeProviderServer(server)
	if err != nil {
		return providerRow{}, err
	}
	presets, err := fetchProviderPresets(ctx, client, baseServer)
	if err != nil {
		return providerRow{}, err
	}
	input, err := buildCreateProviderRequest(presets, options)
	if err != nil {
		return providerRow{}, err
	}
	if err := testProviderDraft(ctx, testClient, baseServer, providerDraftTestPath, input, output); err != nil {
		return providerRow{}, err
	}
	return postProvider(ctx, client, baseServer, input)
}

func updateProvider(
	ctx context.Context,
	client *http.Client,
	testClient *http.Client,
	server string,
	options updateProviderOptions,
	output io.Writer,
) (providerRow, error) {
	baseServer, err := normalizeProviderServer(server)
	if err != nil {
		return providerRow{}, err
	}
	existing, err := resolveProvider(ctx, client, baseServer, options.NameOrID)
	if err != nil {
		return providerRow{}, err
	}
	if options.ProtocolChanged {
		presets, presetErr := fetchProviderPresets(ctx, client, baseServer)
		if presetErr != nil {
			return providerRow{}, presetErr
		}
		normalized, protocolErr := normalizeUpdateProtocol(options.Protocol, presets, *existing)
		if protocolErr != nil {
			return providerRow{}, protocolErr
		}
		options.Protocol = normalized
	}
	draft, updateBody, err := buildUpdateProviderRequest(*existing, options)
	if err != nil {
		return providerRow{}, err
	}
	testPath := fmt.Sprintf(providerEditDraftTestPath, existing.ID)
	if err := testProviderDraft(ctx, testClient, baseServer, testPath, draft, output); err != nil {
		return providerRow{}, err
	}
	return putProvider(ctx, client, baseServer, existing.ID, updateBody)
}

func removeProvider(ctx context.Context, client *http.Client, server, nameOrID string) (providerRow, error) {
	baseServer, err := normalizeProviderServer(server)
	if err != nil {
		return providerRow{}, err
	}
	existing, err := resolveProvider(ctx, client, baseServer, nameOrID)
	if err != nil {
		return providerRow{}, err
	}
	if err := deleteProvider(ctx, client, baseServer, existing.ID); err != nil {
		return providerRow{}, err
	}
	return *existing, nil
}

func resolveProvider(ctx context.Context, client *http.Client, server, nameOrID string) (*providerRow, error) {
	providers, err := fetchProviders(ctx, client, server)
	if err != nil {
		return nil, err
	}
	found := findProvider(providers, nameOrID)
	if found == nil {
		return nil, fmt.Errorf("provider %q not found", nameOrID)
	}
	return found, nil
}

func redactProviderSecret(err error, secret string) error {
	if err == nil || secret == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), secret, "[REDACTED]"))
}

// providerExactArgs 在参数个数错误时附带 Usage，避免 SilenceUsage 导致只剩一句笼统报错。
func providerExactArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := cobra.ExactArgs(n)(cmd, args); err != nil {
			return fmt.Errorf("%w\n\nUsage:\n  %s", err, cmd.UseLine())
		}
		return nil
	}
}

// providerCreateArgs 在 create 参数不足时动态拉取可用厂商/协议列表，并附带参数说明。
func providerCreateArgs(cmd *cobra.Command, args []string) error {
	err := cobra.ExactArgs(2)(cmd, args)
	if err == nil {
		return nil
	}
	message := fmt.Sprintf("%v\n\nUsage:\n  %s\n\n%s", err, cmd.UseLine(), formatCreateParameterHints(cmd))
	server, flagErr := cmd.Flags().GetString("server")
	if flagErr != nil {
		return errors.New(message)
	}
	return withAvailableCreateHints(cmd.Context(), providerHTTPClient, server, message, args)
}

func formatCreateParameterHints(cmd *cobra.Command) string {
	return strings.TrimRight(fmt.Sprintf(
		"Arguments:\n"+
			"  provider   Provider preset ID (see Available providers)\n"+
			"  api-key    Provider API key (required, no default)\n\n"+
			"Flags:\n%s",
		cmd.LocalFlags().FlagUsages(),
	), "\n")
}

func updateMissingFlagsError(ctx context.Context, client *http.Client, server, nameOrID string, cmd *cobra.Command) error {
	message := fmt.Sprintf(
		"at least one update flag is required\n\nUsage:\n  %s\n\nArguments:\n  name-or-id   Provider instance name or ID\n\nFlags:\n%s",
		cmd.UseLine(),
		strings.TrimRight(cmd.LocalFlags().FlagUsages(), "\n"),
	)
	existing, err := resolveProvider(ctx, client, server, nameOrID)
	if err != nil {
		return fmt.Errorf("%s\n\n%v", message, err)
	}
	baseServer, err := normalizeProviderServer(server)
	if err != nil {
		return fmt.Errorf(
			"%s\n\nCurrent protocol: %s\n\nAvailable protocols: unavailable (%v)\n\n%s",
			message,
			valueOrDash(existing.Protocol),
			err,
			formatAvailableProtocols(selectableProtocolOptions()),
		)
	}
	presets, err := fetchProviderPresets(ctx, client, baseServer)
	if err != nil {
		return fmt.Errorf(
			"%s\n\nCurrent protocol: %s\n\nAvailable protocols: unavailable (%v)\n\n%s",
			message,
			valueOrDash(existing.Protocol),
			err,
			formatAvailableProtocols(selectableProtocolOptions()),
		)
	}
	return fmt.Errorf(
		"%s\n\nCurrent protocol: %s\n\n%s",
		message,
		valueOrDash(existing.Protocol),
		formatAvailableProtocols(protocolOptionsForProvider(presets, existing.Provider)),
	)
}

func normalizeUpdateProtocol(protocol string, presets []providerPreset, existing providerRow) (string, error) {
	protocol = strings.TrimSpace(protocol)
	options := protocolOptionsForProvider(presets, existing.Provider)
	if protocol == "" {
		return "", fmt.Errorf(
			"protocol cannot be empty\n\nCurrent protocol: %s\n\n%s",
			valueOrDash(existing.Protocol),
			formatAvailableProtocols(options),
		)
	}
	parsed, err := spec.ParseProtocol(protocol)
	if err != nil {
		return "", fmt.Errorf(
			"unknown protocol %q\n\nCurrent protocol: %s\n\n%s",
			protocol,
			valueOrDash(existing.Protocol),
			formatAvailableProtocols(options),
		)
	}
	normalized := parsed.String()
	if err := validateProviderProtocol(presets, existing.Provider, normalized); err != nil {
		return "", fmt.Errorf(
			"%w\n\nCurrent protocol: %s",
			err,
			valueOrDash(existing.Protocol),
		)
	}
	return normalized, nil
}

func shouldAttachCreateParameterHints(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	for _, marker := range []string{
		"unknown provider",
		"unknown protocol",
		"not supported by provider",
		"base-url is required",
		"mutually exclusive",
		"api-key cannot be empty",
		"--model cannot be empty",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func withAvailableCreateHints(ctx context.Context, client *http.Client, server, message string, args []string) error {
	baseServer, err := normalizeProviderServer(server)
	if err != nil {
		return fmt.Errorf("%s\n\nAvailable providers: unavailable (%v)\n\n%s", message, err, formatAvailableProtocols(selectableProtocolOptions()))
	}
	presets, err := fetchProviderPresets(ctx, client, baseServer)
	if err != nil {
		return fmt.Errorf("%s\n\nAvailable providers: unavailable (%v)\n\n%s", message, err, formatAvailableProtocols(selectableProtocolOptions()))
	}
	providerID := ""
	if len(args) > 0 {
		providerID = args[0]
	}
	return fmt.Errorf(
		"%s\n\n%s\n\n%s",
		message,
		formatAvailableProviders(presets),
		formatAvailableProtocols(protocolOptionsForProvider(presets, providerID)),
	)
}

func registerProtocolFlagCompletion(cmd *cobra.Command, providerFromArgs bool) {
	_ = cmd.RegisterFlagCompletionFunc("protocol", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		server, _ := cmd.Flags().GetString("server")
		providerID := ""
		if providerFromArgs && len(args) > 0 {
			providerID = args[0]
		} else if !providerFromArgs && len(args) > 0 {
			if existing, err := resolveProvider(cmd.Context(), providerHTTPClient, server, args[0]); err == nil {
				providerID = existing.Provider
			}
		}
		completions, err := completeProviderProtocols(cmd.Context(), providerHTTPClient, server, providerID, toComplete)
		if err != nil {
			return completeSelectableProtocols(toComplete), cobra.ShellCompDirectiveNoFileComp
		}
		return completions, cobra.ShellCompDirectiveNoFileComp
	})
}

type protocolOption struct {
	ID   string
	Name string
}

func selectableProtocolOptions() []protocolOption {
	infos := spec.Protocols()
	out := make([]protocolOption, 0, len(infos))
	for _, info := range infos {
		if !info.Selectable {
			continue
		}
		out = append(out, protocolOption{ID: string(info.ID), Name: info.DisplayName})
	}
	return out
}

func protocolDisplayName(id string) string {
	if parsed, err := spec.ParseProtocol(id); err == nil {
		return parsed.DisplayName()
	}
	return id
}

func protocolOptionsForProvider(presets []providerPreset, providerID string) []protocolOption {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	if providerID == "" || providerID == "custom" {
		return selectableProtocolOptions()
	}
	for _, preset := range presets {
		if !strings.EqualFold(preset.ID, providerID) {
			continue
		}
		out := make([]protocolOption, 0, len(preset.Protocols))
		seen := make(map[string]struct{}, len(preset.Protocols))
		for _, protocol := range preset.Protocols {
			id := strings.TrimSpace(protocol.ID)
			if id == "" {
				continue
			}
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, protocolOption{ID: id, Name: protocolDisplayName(id)})
		}
		if len(out) > 0 {
			return out
		}
		break
	}
	return selectableProtocolOptions()
}

func formatAvailableProtocols(options []protocolOption) string {
	maxID := 0
	for _, option := range options {
		if width := runewidth.StringWidth(option.ID); width > maxID {
			maxID = width
		}
	}
	var builder strings.Builder
	builder.WriteString("Available protocols:\n")
	for _, option := range options {
		name := strings.TrimSpace(option.Name)
		if name == "" {
			name = option.ID
		}
		padding := strings.Repeat(" ", maxID-runewidth.StringWidth(option.ID)+2)
		builder.WriteString("  ")
		builder.WriteString(option.ID)
		builder.WriteString(padding)
		builder.WriteString(name)
		builder.WriteByte('\n')
	}
	return strings.TrimRight(builder.String(), "\n")
}

func normalizeProviderProtocol(protocol string) (string, error) {
	protocol = strings.TrimSpace(protocol)
	if protocol == "" {
		return "", errors.New("protocol cannot be empty")
	}
	parsed, err := spec.ParseProtocol(protocol)
	if err != nil {
		return "", fmt.Errorf("unknown protocol %q\n\n%s", protocol, formatAvailableProtocols(selectableProtocolOptions()))
	}
	return parsed.String(), nil
}

func protocolOptionSupported(options []protocolOption, protocol string) bool {
	for _, option := range options {
		if option.ID == protocol {
			return true
		}
	}
	return false
}

func validateProviderProtocol(presets []providerPreset, providerID, protocol string) error {
	options := protocolOptionsForProvider(presets, providerID)
	if protocolOptionSupported(options, protocol) {
		return nil
	}
	return fmt.Errorf(
		"protocol %q is not supported by provider %q\n\n%s",
		protocol,
		providerID,
		formatAvailableProtocols(options),
	)
}

func completeProviderProtocols(ctx context.Context, client *http.Client, server, providerID, toComplete string) ([]string, error) {
	baseServer, err := normalizeProviderServer(server)
	if err != nil {
		return nil, err
	}
	presets, err := fetchProviderPresets(ctx, client, baseServer)
	if err != nil {
		return nil, err
	}
	return filterProtocolCompletions(protocolOptionsForProvider(presets, providerID), toComplete), nil
}

func completeSelectableProtocols(toComplete string) []string {
	return filterProtocolCompletions(selectableProtocolOptions(), toComplete)
}

func filterProtocolCompletions(options []protocolOption, toComplete string) []string {
	toComplete = strings.ToLower(strings.TrimSpace(toComplete))
	out := make([]string, 0, len(options))
	for _, option := range options {
		if toComplete != "" && !strings.HasPrefix(strings.ToLower(option.ID), toComplete) {
			continue
		}
		if option.Name == "" || strings.EqualFold(option.Name, option.ID) {
			out = append(out, option.ID)
			continue
		}
		out = append(out, fmt.Sprintf("%s\t%s", option.ID, option.Name))
	}
	return out
}

func completeCreateProviders(ctx context.Context, client *http.Client, server, toComplete string) ([]string, error) {
	baseServer, err := normalizeProviderServer(server)
	if err != nil {
		return nil, err
	}
	presets, err := fetchProviderPresets(ctx, client, baseServer)
	if err != nil {
		return nil, err
	}
	toComplete = strings.ToLower(strings.TrimSpace(toComplete))
	out := make([]string, 0, len(presets)+1)
	for _, preset := range availableCreateProviders(presets) {
		id := strings.TrimSpace(preset.ID)
		if id == "" {
			continue
		}
		if toComplete != "" && !strings.HasPrefix(strings.ToLower(id), toComplete) {
			continue
		}
		name := strings.TrimSpace(preset.Name)
		if name == "" || strings.EqualFold(name, id) {
			out = append(out, id)
			continue
		}
		out = append(out, fmt.Sprintf("%s\t%s", id, name))
	}
	return out, nil
}

func availableCreateProviders(presets []providerPreset) []providerPreset {
	out := make([]providerPreset, 0, len(presets)+1)
	hasCustom := false
	for _, preset := range presets {
		id := strings.TrimSpace(preset.ID)
		if id == "" {
			continue
		}
		if strings.EqualFold(id, "custom") {
			hasCustom = true
		}
		out = append(out, preset)
	}
	if !hasCustom {
		out = append(out, providerPreset{ID: "custom", Name: "Custom"})
	}
	return out
}

func formatAvailableProviders(presets []providerPreset) string {
	providers := availableCreateProviders(presets)
	maxID := 0
	for _, preset := range providers {
		if width := runewidth.StringWidth(preset.ID); width > maxID {
			maxID = width
		}
	}
	var builder strings.Builder
	builder.WriteString("Available providers:\n")
	for _, preset := range providers {
		name := strings.TrimSpace(preset.Name)
		if name == "" {
			name = preset.ID
		}
		padding := strings.Repeat(" ", maxID-runewidth.StringWidth(preset.ID)+2)
		builder.WriteString("  ")
		builder.WriteString(preset.ID)
		builder.WriteString(padding)
		builder.WriteString(name)
		builder.WriteByte('\n')
	}
	return strings.TrimRight(builder.String(), "\n")
}

func fetchProviderPresets(ctx context.Context, client *http.Client, server string) ([]providerPreset, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server+providerPresetsPath, nil)
	if err != nil {
		return nil, fmt.Errorf("build provider presets request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, providerConnectionError(err, server)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("read provider presets response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, providerStatusError(resp.StatusCode, body)
	}
	var presets []providerPreset
	if err := json.Unmarshal(body, &presets); err != nil {
		return nil, fmt.Errorf("decode provider presets response: %w", err)
	}
	return presets, nil
}

func buildCreateProviderRequest(presets []providerPreset, options createProviderOptions) (createProviderRequest, error) {
	providerID := strings.ToLower(strings.TrimSpace(options.Provider))
	if providerID == "" {
		return createProviderRequest{}, errors.New("provider cannot be empty")
	}
	var preset *providerPreset
	for i := range presets {
		if strings.EqualFold(presets[i].ID, providerID) {
			preset = &presets[i]
			break
		}
	}
	if preset == nil && providerID != "custom" {
		return createProviderRequest{}, fmt.Errorf("unknown provider %q\n\n%s", options.Provider, formatAvailableProviders(presets))
	}

	input := createProviderRequest{
		Name:        strings.TrimSpace(options.Name),
		Provider:    providerID,
		Protocol:    strings.TrimSpace(options.Protocol),
		BaseURL:     strings.TrimRight(strings.TrimSpace(options.BaseURL), "/"),
		Credentials: map[string]string{"api_key": options.APIKey},
		ModelsURL:   strings.TrimSpace(options.ModelsURL),
		ProxyURL:    strings.TrimSpace(options.ProxyURL),
		Enabled:     options.Enabled,
	}
	if input.Protocol != "" {
		normalized, err := normalizeProviderProtocol(input.Protocol)
		if err != nil {
			return createProviderRequest{}, err
		}
		input.Protocol = normalized
	}
	if preset != nil {
		if input.Name == "" {
			input.Name = preset.Name
		}
		if input.Protocol == "" {
			input.Protocol = preset.DefaultProtocol
		}
		for _, field := range preset.Credentials.Fields {
			if field.Default != "" {
				input.Credentials[field.Name] = field.Default
			}
		}
		input.Credentials["api_key"] = options.APIKey
		if err := validateProviderProtocol(presets, providerID, input.Protocol); err != nil {
			return createProviderRequest{}, err
		}
		for _, candidate := range preset.Protocols {
			if candidate.ID == input.Protocol {
				if input.BaseURL == "" {
					input.BaseURL = strings.TrimRight(candidate.BaseURL, "/")
				}
				break
			}
		}
		if input.BaseURL == "" {
			return createProviderRequest{}, fmt.Errorf("base URL is not configured for protocol %q", input.Protocol)
		}
		if !options.ModelsChanged && !options.ModelsURLChanged {
			input.ModelsURL = preset.ModelsURL
		}
	} else {
		if input.Name == "" {
			input.Name = "custom"
		}
		if input.Protocol == "" {
			input.Protocol = "openai-chatcompletions"
		}
		if err := validateProviderProtocol(presets, providerID, input.Protocol); err != nil {
			return createProviderRequest{}, err
		}
		if input.BaseURL == "" {
			return createProviderRequest{}, errors.New("--base-url is required for provider \"custom\"")
		}
	}
	if options.ModelsChanged {
		input.ModelsURL = ""
		input.Models = normalizeProviderModels(options.Models)
		if len(input.Models) == 0 {
			return createProviderRequest{}, errors.New("--model cannot be empty")
		}
	}
	return input, nil
}

func normalizeProviderModels(models []string) []string {
	seen := make(map[string]struct{}, len(models))
	out := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, exists := seen[model]; exists {
			continue
		}
		seen[model] = struct{}{}
		out = append(out, model)
	}
	return out
}

func buildUpdateProviderRequest(existing providerRow, options updateProviderOptions) (createProviderRequest, map[string]any, error) {
	credentials := providerCredentials(existing)
	draft := createProviderRequest{
		Name:        existing.Name,
		Provider:    existing.Provider,
		Protocol:    existing.Protocol,
		BaseURL:     existing.BaseURL,
		Credentials: credentials,
		Models:      providerModels(existing),
		ModelsURL:   existing.ModelsURL,
		ProxyURL:    existing.ProxyURL,
		Enabled:     existing.Enabled,
	}
	updateBody := map[string]any{}

	if options.NameChanged {
		name := strings.TrimSpace(options.Name)
		if name == "" {
			return createProviderRequest{}, nil, errors.New("name cannot be empty")
		}
		draft.Name = name
		updateBody["name"] = name
	}
	if options.ProtocolChanged {
		protocol := strings.TrimSpace(options.Protocol)
		if protocol == "" {
			return createProviderRequest{}, nil, errors.New("protocol cannot be empty")
		}
		draft.Protocol = protocol
		updateBody["protocol"] = protocol
	}
	if options.BaseURLChanged {
		baseURL := strings.TrimRight(strings.TrimSpace(options.BaseURL), "/")
		if baseURL == "" {
			return createProviderRequest{}, nil, errors.New("base-url cannot be empty")
		}
		draft.BaseURL = baseURL
		updateBody["base_url"] = baseURL
	}
	if options.ProxyURLChanged {
		proxyURL := strings.TrimSpace(options.ProxyURL)
		draft.ProxyURL = proxyURL
		updateBody["proxy_url"] = proxyURL
	}
	if options.EnabledChanged {
		draft.Enabled = options.Enabled
		updateBody["enabled"] = options.Enabled
	}
	if options.APIKeyChanged {
		credentials["api_key"] = options.APIKey
		draft.Credentials = credentials
		updateBody["credentials"] = credentials
	}
	if options.ModelsChanged {
		models := normalizeProviderModels(options.Models)
		if len(models) == 0 {
			return createProviderRequest{}, nil, errors.New("--model cannot be empty")
		}
		draft.Models = models
		draft.ModelsURL = ""
		updateBody["models"] = models
		updateBody["models_url"] = ""
	}
	if options.ModelsURLChanged {
		modelsURL := strings.TrimSpace(options.ModelsURL)
		draft.ModelsURL = modelsURL
		draft.Models = nil
		updateBody["models_url"] = modelsURL
		updateBody["models"] = []string{}
	}
	// Reached only when the existing record itself has no base_url; explicit --base-url ""
	// is already caught above by the BaseURLChanged branch.
	if draft.BaseURL == "" {
		return createProviderRequest{}, nil, errors.New("base-url cannot be empty")
	}
	return draft, updateBody, nil
}

func providerCredentials(provider providerRow) map[string]string {
	out := map[string]string{}
	if len(provider.CredentialsJSON) == 0 || string(provider.CredentialsJSON) == "null" {
		return out
	}
	_ = json.Unmarshal(provider.CredentialsJSON, &out)
	return out
}

func providerModels(provider providerRow) []string {
	if len(provider.ModelsJSON) == 0 || string(provider.ModelsJSON) == "null" {
		return nil
	}
	var models []string
	if err := json.Unmarshal(provider.ModelsJSON, &models); err != nil {
		return nil
	}
	return models
}

func testProviderDraft(
	ctx context.Context,
	client *http.Client,
	server string,
	path string,
	input createProviderRequest,
	output io.Writer,
) error {
	body, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("encode provider test request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build provider test request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		return providerConnectionError(err, server)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxProviderBodyBytes))
		return providerStatusError(resp.StatusCode, responseBody)
	}

	scanner := bufio.NewScanner(io.LimitReader(resp.Body, maxProviderBodyBytes))
	scanner.Buffer(make([]byte, 64*1024), maxProviderBodyBytes)
	var dataLines []string
	completed := false
	succeeded := false
	for scanner.Scan() {
		line := scanner.Text()
		if line != "" {
			if strings.HasPrefix(line, "data:") {
				dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
			continue
		}
		if len(dataLines) == 0 {
			continue
		}
		var event providerHealthEvent
		if err := json.Unmarshal([]byte(strings.Join(dataLines, "\n")), &event); err != nil {
			return fmt.Errorf("decode provider test event: %w", err)
		}
		dataLines = dataLines[:0]
		if event.Type == "check" && event.Status != "running" {
			message := event.Message
			if event.Status == "failed" {
				message = event.Error
			}
			if _, err := fmt.Fprintf(output, "%s %s: %s\n", healthStatusMark(event.Status), event.Check, message); err != nil {
				return err
			}
		}
		if event.Type == "complete" {
			completed = true
			succeeded = event.Success != nil && *event.Success
			if !succeeded {
				message := event.Error
				if message == "" {
					message = "provider test failed"
				}
				return errors.New(message)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read provider test response: %w", err)
	}
	if !completed || !succeeded {
		return errors.New("provider test did not return a successful completion event")
	}
	return nil
}

func healthStatusMark(status string) string {
	if status == "passed" {
		return "✓"
	}
	return "✗"
}

func postProvider(ctx context.Context, client *http.Client, server string, input createProviderRequest) (providerRow, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return providerRow{}, fmt.Errorf("encode provider create request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server+providerListPath, bytes.NewReader(body))
	if err != nil {
		return providerRow{}, fmt.Errorf("build provider create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return providerRow{}, providerConnectionError(err, server)
	}
	defer func() { _ = resp.Body.Close() }()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderBodyBytes))
	if err != nil {
		return providerRow{}, fmt.Errorf("read provider create response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return providerRow{}, providerStatusError(resp.StatusCode, responseBody)
	}
	var created providerRow
	if err := json.Unmarshal(responseBody, &created); err != nil {
		return providerRow{}, fmt.Errorf("decode provider create response: %w", err)
	}
	return created, nil
}

func putProvider(ctx context.Context, client *http.Client, server, id string, input map[string]any) (providerRow, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return providerRow{}, fmt.Errorf("encode provider update request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, server+fmt.Sprintf(providerItemPath, id), bytes.NewReader(body))
	if err != nil {
		return providerRow{}, fmt.Errorf("build provider update request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return providerRow{}, providerConnectionError(err, server)
	}
	defer func() { _ = resp.Body.Close() }()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderBodyBytes))
	if err != nil {
		return providerRow{}, fmt.Errorf("read provider update response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return providerRow{}, providerStatusError(resp.StatusCode, responseBody)
	}
	var updated providerRow
	if err := json.Unmarshal(responseBody, &updated); err != nil {
		return providerRow{}, fmt.Errorf("decode provider update response: %w", err)
	}
	return updated, nil
}

func deleteProvider(ctx context.Context, client *http.Client, server, id string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, server+fmt.Sprintf(providerItemPath, id), nil)
	if err != nil {
		return fmt.Errorf("build provider delete request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return providerConnectionError(err, server)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		return nil
	}
	responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxProviderBodyBytes))
	return providerStatusError(resp.StatusCode, responseBody)
}

func providerConnectionError(err error, server string) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("nyro server did not respond (request timed out)")
	}
	return fmt.Errorf("cannot reach nyro server at %s", displayProviderServer(server))
}

func providerStatusError(statusCode int, body []byte) error {
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

func fetchProviders(ctx context.Context, client *http.Client, server string) ([]providerRow, error) {
	baseURL, err := normalizeProviderServer(server)
	if err != nil {
		return nil, err
	}

	requestURL := baseURL + providerListPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build provider request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, providerConnectionError(err, baseURL)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("read providers response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = resp.Status
		}
		return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode, message)
	}

	var providers []providerRow
	if err := json.Unmarshal(body, &providers); err != nil {
		return nil, fmt.Errorf("decode providers response: %w", err)
	}
	return providers, nil
}

func normalizeProviderServer(server string) (string, error) {
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

func displayProviderServer(server string) string {
	parsed, err := url.Parse(server)
	if err == nil && parsed.Host != "" {
		return parsed.Host
	}
	return server
}

func writeProviders(w io.Writer, providers []providerRow, now time.Time) error {
	rows := make([][6]string, 0, len(providers)+1)
	rows = append(rows, [6]string{"ID", "NAME", "PROVIDER", "PROTOCOL", "ENABLED", "UPDATED"})
	for _, provider := range providers {
		rows = append(rows, [6]string{
			provider.ID,
			provider.Name,
			provider.Provider,
			provider.Protocol,
			fmt.Sprintf("%t", provider.Enabled),
			humanizeProviderTime(provider.UpdatedAt, now),
		})
	}

	var widths [6]int
	for _, row := range rows {
		for column, value := range row {
			if width := runewidth.StringWidth(value); width > widths[column] {
				widths[column] = width
			}
		}
	}

	const padding = 3
	for _, row := range rows {
		for column, value := range row {
			if _, err := io.WriteString(w, value); err != nil {
				return err
			}
			if column == len(row)-1 {
				continue
			}
			spaces := widths[column] - runewidth.StringWidth(value) + padding
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

func findProvider(providers []providerRow, nameOrID string) *providerRow {
	for i := range providers {
		if providers[i].ID == nameOrID {
			return &providers[i]
		}
	}
	for i := range providers {
		if providers[i].Name == nameOrID {
			return &providers[i]
		}
	}
	return nil
}

func writeProvider(w io.Writer, provider providerRow) error {
	credentials := "-"
	if value := strings.TrimSpace(string(provider.CredentialsJSON)); value != "" && value != "null" && value != "{}" {
		credentials = "configured"
	}
	models := strings.TrimSpace(string(provider.ModelsJSON))
	if models == "" || models == "null" {
		models = "-"
	}
	fields := [][2]string{
		{"ID", provider.ID},
		{"Name", provider.Name},
		{"Provider", provider.Provider},
		{"Protocol", provider.Protocol},
		{"Base URL", valueOrDash(provider.BaseURL)},
		{"Credentials", credentials},
		{"Models", models},
		{"Models URL", valueOrDash(provider.ModelsURL)},
		{"Proxy URL", valueOrDash(provider.ProxyURL)},
		{"Enabled", fmt.Sprintf("%t", provider.Enabled)},
		{"Created", valueOrDash(provider.CreatedAt)},
		{"Updated", valueOrDash(provider.UpdatedAt)},
	}
	for _, field := range fields {
		if _, err := fmt.Fprintf(w, "%-12s %s\n", field[0]+":", field[1]); err != nil {
			return err
		}
	}
	return nil
}

func valueOrDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func humanizeProviderTime(value string, now time.Time) string {
	if value == "" {
		return "-"
	}
	updatedAt, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return value
	}
	elapsed := now.Sub(updatedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	switch {
	case elapsed < time.Minute:
		return "just now"
	case elapsed < time.Hour:
		return pluralDuration(int(elapsed.Minutes()), "minute")
	case elapsed < 24*time.Hour:
		return pluralDuration(int(elapsed.Hours()), "hour")
	case elapsed < 30*24*time.Hour:
		return pluralDuration(int(elapsed/(24*time.Hour)), "day")
	default:
		return pluralDuration(int(elapsed/(30*24*time.Hour)), "month")
	}
}

func pluralDuration(value int, unit string) string {
	if value == 1 {
		return fmt.Sprintf("1 %s ago", unit)
	}
	return fmt.Sprintf("%d %ss ago", value, unit)
}
