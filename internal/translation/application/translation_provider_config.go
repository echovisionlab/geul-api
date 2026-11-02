package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"

	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/translation"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/llm"
	llmproviders "github.com/echovisionlab/geul-api/internal/llm/providers"
	"github.com/echovisionlab/geul-api/internal/mutation"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

const translationProviderUnavailableMessage = "translation generation requires an active translation provider"
const defaultTranslationLLMGeminiModel = "gemini-2.5-flash-lite"

var errTranslationProviderUnavailable = errors.New(translationProviderUnavailableMessage)

type TranslationGeneratorResolver interface {
	Resolve(ctx context.Context) (translation.Generator, error)
	ResolveAll(ctx context.Context) ([]translation.Generator, error)
	HasAvailableProvider(ctx context.Context) (bool, error)
}

// storeTranslationGeneratorResolver builds generators from the active
// providers, read in one snapshot per call.
type storeTranslationGeneratorResolver struct {
	units mutation.UnitOfWork[JobStore]
}

func (r storeTranslationGeneratorResolver) Resolve(ctx context.Context) (translation.Generator, error) {
	generators, err := r.ResolveAll(ctx)
	if err != nil {
		return nil, err
	}
	return generators[0], nil
}

func (r storeTranslationGeneratorResolver) ResolveAll(ctx context.Context) ([]translation.Generator, error) {
	var generators []translation.Generator
	err := r.units.Read(ctx, func(store JobStore) error {
		var err error
		generators, err = loadActiveTranslationGenerators(ctx, store)
		return err
	})
	return generators, err
}

func (r storeTranslationGeneratorResolver) HasAvailableProvider(ctx context.Context) (bool, error) {
	generators, err := r.ResolveAll(ctx)
	if err == nil {
		return len(generators) > 0, nil
	}
	if errors.Is(err, errTranslationProviderUnavailable) {
		return false, nil
	}
	return false, err
}

// ListTranslationProvidersQuery lists translation providers by priority.
type ListTranslationProvidersQuery struct {
	// Limit defaults to 20 and is capped at 100.
	Limit  int32
	Offset int32
	// Filters honors only is_active = true.
	Filters []*commonv1.FilterSpec
}

// ProviderPage is one page of translation providers.
type ProviderPage struct {
	Providers  []Provider
	Pagination Pagination
}

// ListTranslationProviders lists translation providers without secrets.
func (s *Service) ListTranslationProviders(ctx context.Context, query ListTranslationProvidersQuery) (ProviderPage, error) {
	can, canErr := policyv1.TranslationProvider.List()
	if err := s.requireTranslationPlatformCan(ctx, can, canErr); err != nil {
		return ProviderPage{}, err
	}
	list := ProviderListQuery{}
	for _, filter := range query.Filters {
		if filter == nil {
			continue
		}
		if filter.GetField() == "is_active" && filter.GetValue() == "true" {
			list.ActiveOnly = true
		}
	}
	pagination := queryutil.ExtractPaginationRaw(query.Limit, query.Offset, queryutil.DefaultPaginationConfig)
	list.Limit, list.Offset = pagination.Limit, pagination.Offset
	var providers []Provider
	var total int64
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		providers, total, err = store.ListProviders(ctx, list)
		return err
	})
	if err != nil {
		return ProviderPage{}, err
	}
	if providers == nil {
		providers = []Provider{}
	}
	return ProviderPage{Providers: providers, Pagination: paginationOf(pagination, total)}, nil
}

// LLMConfigInput is the LLM config a command sends. An empty APIKey keeps
// the stored key of an LLM provider; Preset is empty when unspecified.
type LLMConfigInput struct {
	APIKey                        string
	Preset                        LLMPreset
	APIBaseURL                    string
	Model                         string
	InputTokenPriceUSDPerMillion  *float64
	OutputTokenPriceUSDPerMillion *float64
	MaxContextTokens              *int32
	MaxOutputTokens               *int32
	SupportsJSONMode              bool
	Temperature                   *float64
}

// DeepLConfigInput is the DeepL config a command sends; empty values keep
// the stored ones of a DeepL provider.
type DeepLConfigInput struct {
	APIKey     string
	APIBaseURL string
}

// CreateTranslationProviderCommand registers a translation provider. The
// config of the chosen Type is required; an unknown Type is empty.
type CreateTranslationProviderCommand struct {
	Name        string
	Type        ProviderType
	IsActive    bool
	Priority    int32
	LLMConfig   *LLMConfigInput
	DeepLConfig *DeepLConfigInput
}

// CreateTranslationProvider registers a provider after proving its config
// builds a generator.
func (s *Service) CreateTranslationProvider(ctx context.Context, command CreateTranslationProviderCommand) (_ Provider, retErr error) {
	// The boundary redacts the API keys as sent; failures can quote them
	// trimmed.
	defer failurecause.AnnotatePrivate(&retErr, commandAPIKeys(command.LLMConfig, command.DeepLConfig)...)
	return mutation.Run(ctx, s.mutations, mutation.StoreSpec[Provider, Store]{
		Access: mutation.FreshAdminCan(policyv1.TranslationProvider.Create()),
		Change: func(ctx context.Context, store Store) (Provider, error) {
			return createTranslationProvider(ctx, store, command)
		},
		Audit: func(provider Provider) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditTranslationProviderCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewTranslationProviderCreatedAuditRecord(metadata, provider.ID)
			})
		},
	})
}

// commandAPIKeys are the API keys a command carries, trimmed as a failure
// can quote them.
func commandAPIKeys(llmConfig *LLMConfigInput, deeplConfig *DeepLConfigInput) []string {
	var llmKey, deeplKey string
	if llmConfig != nil {
		llmKey = strings.TrimSpace(llmConfig.APIKey)
	}
	if deeplConfig != nil {
		deeplKey = strings.TrimSpace(deeplConfig.APIKey)
	}
	return []string{llmKey, deeplKey}
}

func createTranslationProvider(ctx context.Context, store Store, command CreateTranslationProviderCommand) (Provider, error) {
	name := strings.TrimSpace(command.Name)
	if name == "" {
		return Provider{}, domainerr.Required("name")
	}
	if len(name) > 255 {
		return Provider{}, domainerr.InvalidField("name", "must be at most 255 characters")
	}
	providerType := knownProviderType(command.Type)
	if providerType == "" {
		return Provider{}, domainerr.InvalidField("type", "invalid translation provider type")
	}
	config, err := buildTranslationProviderConfig(ctx, providerType, command.LLMConfig, command.DeepLConfig, nil)
	if err != nil {
		return Provider{}, domainerr.InvalidField("config", err.Error())
	}
	now := dbtime.Now()
	provider := Provider{
		Name:      name,
		Type:      providerType,
		IsActive:  command.IsActive,
		Priority:  int(command.Priority),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := provider.SetConfig(config); err != nil {
		return Provider{}, fmt.Errorf("failed to set translation provider config: %w", err)
	}
	if _, err := newTranslationGeneratorFromProvider(&provider); err != nil {
		return Provider{}, domainerr.InvalidField("config", err.Error())
	}
	return store.InsertProvider(ctx, provider)
}

func knownProviderType(providerType ProviderType) ProviderType {
	switch providerType {
	case ProviderTypeLLM, ProviderTypeDeepL:
		return providerType
	default:
		return ""
	}
}

// UpdateTranslationProviderCommand changes the set fields of a provider. A
// config without an API key keeps the stored key of the same type.
type UpdateTranslationProviderCommand struct {
	ID          string
	Name        *string
	Type        *ProviderType
	IsActive    *bool
	Priority    *int32
	LLMConfig   *LLMConfigInput
	DeepLConfig *DeepLConfigInput
}

type providerUpdate struct {
	provider      Provider
	changedFields []string
}

// UpdateTranslationProvider applies the set fields and audits the fields
// that changed.
func (s *Service) UpdateTranslationProvider(ctx context.Context, command UpdateTranslationProviderCommand) (_ Provider, retErr error) {
	// The boundary redacts the API keys as sent; failures can quote them
	// trimmed.
	defer failurecause.AnnotatePrivate(&retErr, commandAPIKeys(command.LLMConfig, command.DeepLConfig)...)
	providerID := strings.TrimSpace(command.ID)
	var provider Provider
	updated, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[providerUpdate, Store]{
		Lock: func(ctx context.Context, store Store) error {
			var err error
			provider, err = store.LockProvider(ctx, providerID)
			return err
		},
		Access: mutation.FreshAdminCan(policyv1.TranslationProvider.Update()),
		Change: func(ctx context.Context, store Store) (providerUpdate, error) {
			return updateTranslationProvider(ctx, store, provider, command)
		},
		Audit: func(update providerUpdate) mutation.Audit {
			if len(update.changedFields) == 0 {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditTranslationProviderUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewTranslationProviderConfigUpdatedAuditRecord(metadata, update.provider.ID, update.changedFields)
			})
		},
	})
	if errors.Is(err, ErrProviderNotFound) {
		return Provider{}, domainerr.ResourceNotFound("translation_provider", command.ID)
	}
	if err != nil {
		return Provider{}, err
	}
	return updated.provider, nil
}

func updateTranslationProvider(
	ctx context.Context,
	store Store,
	provider Provider,
	command UpdateTranslationProviderCommand,
) (providerUpdate, error) {
	requested, err := requestedTranslationProvider(ctx, provider, command)
	if err != nil {
		return providerUpdate{}, err
	}
	var change ProviderChange
	changedFields := make([]string, 0, 5)
	if command.Name != nil && provider.Name != requested.Name {
		change.Name = &requested.Name
		provider.Name = requested.Name
		changedFields = append(changedFields, "name")
	}
	if command.IsActive != nil && provider.IsActive != requested.IsActive {
		change.IsActive = &requested.IsActive
		provider.IsActive = requested.IsActive
		changedFields = append(changedFields, "active")
	}
	if command.Priority != nil && provider.Priority != requested.Priority {
		change.Priority = &requested.Priority
		provider.Priority = requested.Priority
		changedFields = append(changedFields, "priority")
	}
	if command.Type != nil && provider.Type != requested.Type {
		change.Type = &requested.Type
		provider.Type = requested.Type
		changedFields = append(changedFields, "type")
	}
	if requested.configChanged && !configJSONSemanticallyEqual(provider.Config, requested.Config) {
		change.Config = requested.Config
		provider.Config = requested.Config
		changedFields = append(changedFields, "config")
	}
	if len(changedFields) == 0 {
		return providerUpdate{provider: provider}, nil
	}
	updatedAt, err := store.UpdateProvider(ctx, provider.ID, change)
	if err != nil {
		return providerUpdate{}, err
	}
	provider.UpdatedAt = updatedAt
	return providerUpdate{provider: provider, changedFields: changedFields}, nil
}

type requestedProvider struct {
	Provider
	configChanged bool
}

// requestedTranslationProvider validates the command against the stored
// provider and returns the provider the command asks for.
func requestedTranslationProvider(
	ctx context.Context,
	provider Provider,
	command UpdateTranslationProviderCommand,
) (requestedProvider, error) {
	requested := requestedProvider{Provider: provider}
	if command.Name != nil {
		name := strings.TrimSpace(*command.Name)
		if name == "" {
			return requestedProvider{}, domainerr.InvalidField("name", "cannot be empty")
		}
		if len(name) > 255 {
			return requestedProvider{}, domainerr.InvalidField("name", "must be at most 255 characters")
		}
		requested.Name = name
	}
	if command.IsActive != nil {
		requested.IsActive = *command.IsActive
	}
	if command.Priority != nil {
		requested.Priority = int(*command.Priority)
	}
	typeChanged := false
	if command.Type != nil {
		nextType := knownProviderType(*command.Type)
		if nextType == "" {
			return requestedProvider{}, domainerr.InvalidField("type", "invalid translation provider type")
		}
		if nextType != provider.Type {
			typeChanged = true
			requested.Type = nextType
		}
	}
	requested.configChanged = typeChanged || command.LLMConfig != nil || command.DeepLConfig != nil
	if !requested.configChanged {
		return requested, nil
	}
	config, err := buildTranslationProviderConfig(
		ctx, requested.Type, command.LLMConfig, command.DeepLConfig,
		translationProviderExistingConfig(&provider, typeChanged),
	)
	if err != nil {
		return requestedProvider{}, domainerr.InvalidField("config", err.Error())
	}
	candidate := provider
	candidate.Type = requested.Type
	if err := candidate.SetConfig(config); err != nil {
		return requestedProvider{}, fmt.Errorf("failed to set translation provider config: %w", err)
	}
	if _, err := newTranslationGeneratorFromProvider(&candidate); err != nil {
		return requestedProvider{}, domainerr.InvalidField("config", err.Error())
	}
	requested.Config = candidate.Config
	return requested, nil
}

func configJSONSemanticallyEqual(left, right []byte) bool {
	var leftValue any
	var rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

// DeleteTranslationProviderCommand deletes a translation provider.
type DeleteTranslationProviderCommand struct {
	ID string
}

// DeleteTranslationProvider deletes a provider.
func (s *Service) DeleteTranslationProvider(ctx context.Context, command DeleteTranslationProviderCommand) error {
	providerID := strings.TrimSpace(command.ID)
	var provider Provider
	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[struct{}, Store]{
		Lock: func(ctx context.Context, store Store) error {
			var err error
			provider, err = store.LockProvider(ctx, providerID)
			return err
		},
		Access: mutation.FreshAdminCan(policyv1.TranslationProvider.Delete()),
		Change: func(ctx context.Context, store Store) (struct{}, error) {
			return struct{}{}, store.DeleteProvider(ctx, provider.ID)
		},
		Audit: func(struct{}) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditTranslationProviderDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewTranslationProviderDeletedAuditRecord(metadata, provider.ID)
			})
		},
	})
	if errors.Is(err, ErrProviderNotFound) {
		return domainerr.ResourceNotFound("translation_provider", command.ID)
	}
	return err
}

func (s *Service) requireTranslationPlatformCan(
	ctx context.Context,
	can policyv1.Can,
	canErr error,
) error {
	if canErr != nil {
		return canErr
	}
	return authz.RequireAdminCan(ctx, s.spiceDB, can)
}

func loadActiveTranslationGenerators(
	ctx context.Context,
	store JobStore,
) ([]translation.Generator, error) {
	providers, err := store.ActiveProviders(ctx)
	if err != nil {
		return nil, err
	}
	for i := range providers {
		generator, err := newTranslationGeneratorFromProvider(&providers[i])
		if err != nil {
			slog.WarnContext(ctx,
				"Skipping invalid active translation provider",
				"provider_id", providers[i].ID,
				"provider_name", providers[i].Name,
				"provider_type", providers[i].Type,
				"failure_cause", failurecause.Message(err),
			)
			continue
		}
		generators := []translation.Generator{generator}
		for index := i + 1; index < len(providers); index++ {
			nextGenerator, err := newTranslationGeneratorFromProvider(&providers[index])
			if err != nil {
				slog.WarnContext(ctx,
					"Skipping invalid active translation provider",
					"provider_id", providers[index].ID,
					"provider_name", providers[index].Name,
					"provider_type", providers[index].Type,
					"failure_cause", failurecause.Message(err),
				)
				continue
			}
			generators = append(generators, nextGenerator)
		}
		return generators, nil
	}
	return nil, errTranslationProviderUnavailable
}

func hasAvailableTranslationProvider(ctx context.Context, store JobStore) (bool, error) {
	_, err := loadActiveTranslationGenerators(ctx, store)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, errTranslationProviderUnavailable) {
		return false, nil
	}
	return false, err
}

func newTranslationGeneratorFromProvider(provider *Provider) (translation.Generator, error) {
	if provider == nil {
		return nil, errTranslationProviderUnavailable
	}
	switch provider.Type {
	case ProviderTypeLLM:
		cfg, err := provider.LLMConfig()
		if err != nil {
			return nil, err
		}
		textProvider, err := newTranslationLLMTextProvider(cfg)
		if err != nil {
			return nil, err
		}
		return translation.NewAIGenerator(textProvider), nil
	case ProviderTypeDeepL:
		cfg, err := provider.DeepLConfig()
		if err != nil {
			return nil, err
		}
		return translation.NewDeepLGenerator(cfg.APIKey, cfg.APIBaseURL)
	default:
		return nil, fmt.Errorf("unsupported translation provider type %q", provider.Type)
	}
}

func newTranslationLLMTextProvider(cfg *LLMConfig) (llm.Provider, error) {
	if cfg == nil {
		return nil, fmt.Errorf("LLM config is required")
	}
	apiKey := strings.TrimSpace(cfg.APIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("LLM API key is required")
	}
	modelName := strings.TrimSpace(cfg.Model)
	preset := cfg.Preset
	if preset == "" {
		preset = LLMPresetGemini
	}
	switch preset {
	case LLMPresetGemini:
		if modelName == "" && preset == LLMPresetGemini {
			modelName = defaultTranslationLLMGeminiModel
		}
		return llmproviders.NewGeminiProvider(llmproviders.GeminiConfig{
			APIKey: apiKey, Model: modelName, Temperature: float32PtrFromFloat64Ptr(cfg.Temperature),
		})
	case LLMPresetOpenAICompatible:
		return llmproviders.NewOpenAICompatibleProvider(llmproviders.OpenAICompatibleConfig{
			APIKey: apiKey, BaseURL: cfg.APIBaseURL, Model: modelName, SupportsJSONMode: cfg.SupportsJSONMode,
			Temperature: cfg.Temperature, MaxOutputTokens: cfg.MaxOutputTokens,
		})
	default:
		return nil, fmt.Errorf("unsupported LLM provider preset %q", cfg.Preset)
	}
}

func float32PtrFromFloat64Ptr(value *float64) *float32 {
	if value == nil {
		return nil
	}
	next := float32(*value)
	return &next
}

func translationProviderExistingConfig(
	provider *Provider,
	typeChanged bool,
) *Provider {
	if typeChanged {
		return nil
	}
	return provider
}

type llmTranslationProviderValues struct {
	apiKey           string
	preset           LLMPreset
	baseURL          string
	modelName        string
	inputPrice       *float64
	outputPrice      *float64
	maxContextTokens *int32
	maxOutputTokens  *int32
	temperature      *float64
}

func resolveLLMTranslationProviderValues(
	ctx context.Context,
	config *LLMConfigInput,
	existing *Provider,
) llmTranslationProviderValues {
	values := llmTranslationProviderValues{
		apiKey:           strings.TrimSpace(config.APIKey),
		preset:           knownLLMPreset(config.Preset),
		baseURL:          strings.TrimSpace(config.APIBaseURL),
		modelName:        strings.TrimSpace(config.Model),
		inputPrice:       config.InputTokenPriceUSDPerMillion,
		outputPrice:      config.OutputTokenPriceUSDPerMillion,
		maxContextTokens: config.MaxContextTokens,
		maxOutputTokens:  config.MaxOutputTokens,
		temperature:      config.Temperature,
	}
	if values.preset == "" {
		values.preset = LLMPresetGemini
	}
	if values.apiKey != "" || existing == nil || existing.Type != ProviderTypeLLM {
		return values
	}
	current, err := existing.LLMConfig()
	if err != nil {
		slog.ErrorContext(ctx, "Stored translation provider config decode failed; keeping only requested values", "domain", "translation", "event", "translation.provider.config_decode_failed", "outcome", "failed", failurecause.Attr(err), "provider_id", existing.ID)
		return values
	}
	values.apiKey = strings.TrimSpace(current.APIKey)
	assignMissingLLMProviderValues(&values, config, current)
	return values
}

func assignMissingLLMProviderValues(
	values *llmTranslationProviderValues,
	requested *LLMConfigInput,
	current *LLMConfig,
) {
	if values.modelName == "" {
		values.modelName = strings.TrimSpace(current.Model)
	}
	if values.baseURL == "" {
		values.baseURL = strings.TrimSpace(current.APIBaseURL)
	}
	if requested.Preset == "" && current.Preset != "" {
		values.preset = current.Preset
	}
	if values.inputPrice == nil {
		values.inputPrice = current.InputTokenPriceUSDPerMillion
	}
	if values.outputPrice == nil {
		values.outputPrice = current.OutputTokenPriceUSDPerMillion
	}
	if values.maxContextTokens == nil {
		values.maxContextTokens = current.MaxContextTokens
	}
	if values.maxOutputTokens == nil {
		values.maxOutputTokens = current.MaxOutputTokens
	}
	if values.temperature == nil {
		values.temperature = current.Temperature
	}
}

func defaultLLMTranslationModel(values *llmTranslationProviderValues) error {
	if values.modelName != "" {
		return nil
	}
	if values.preset != LLMPresetGemini {
		return fmt.Errorf("LLM model is required")
	}
	values.modelName = defaultTranslationLLMGeminiModel
	return nil
}

func buildTranslationProviderConfig(
	ctx context.Context,
	providerType ProviderType,
	llmConfig *LLMConfigInput,
	deeplConfig *DeepLConfigInput,
	existing *Provider,
) (any, error) {
	switch providerType {
	case ProviderTypeLLM:
		if llmConfig == nil {
			return nil, fmt.Errorf("LLM config is required")
		}
		values := resolveLLMTranslationProviderValues(ctx, llmConfig, existing)
		if values.apiKey == "" {
			return nil, fmt.Errorf("LLM API key is required")
		}
		if err := defaultLLMTranslationModel(&values); err != nil {
			return nil, err
		}
		cfg := &LLMConfig{
			APIKey:                        values.apiKey,
			Preset:                        values.preset,
			APIBaseURL:                    values.baseURL,
			Model:                         values.modelName,
			InputTokenPriceUSDPerMillion:  values.inputPrice,
			OutputTokenPriceUSDPerMillion: values.outputPrice,
			MaxContextTokens:              values.maxContextTokens,
			MaxOutputTokens:               values.maxOutputTokens,
			SupportsJSONMode:              llmConfig.SupportsJSONMode,
			Temperature:                   values.temperature,
		}
		if _, err := newTranslationLLMTextProvider(cfg); err != nil {
			return nil, err
		}
		return cfg, nil
	case ProviderTypeDeepL:
		if deeplConfig == nil {
			return nil, fmt.Errorf("DeepL config is required")
		}
		apiKey := strings.TrimSpace(deeplConfig.APIKey)
		baseURL := strings.TrimSpace(deeplConfig.APIBaseURL)
		apiKey, baseURL = inheritDeepLProviderConfig(ctx, existing, providerType, apiKey, baseURL)
		if apiKey == "" {
			return nil, fmt.Errorf("DeepL API key is required")
		}
		if baseURL == "" {
			baseURL = translation.DefaultDeepLAPIBaseURL
		}
		return &DeepLConfig{APIKey: apiKey, APIBaseURL: baseURL}, nil
	default:
		return nil, fmt.Errorf("unknown translation provider type: %s", providerType)
	}
}

func inheritDeepLProviderConfig(
	ctx context.Context,
	existing *Provider,
	providerType ProviderType,
	apiKey string,
	baseURL string,
) (string, string) {
	if existing == nil || existing.Type != providerType {
		return apiKey, baseURL
	}
	current, err := existing.DeepLConfig()
	if err != nil {
		slog.ErrorContext(ctx, "Stored translation provider config decode failed; keeping only requested values", "domain", "translation", "event", "translation.provider.config_decode_failed", "outcome", "failed", failurecause.Attr(err), "provider_id", existing.ID)
		return apiKey, baseURL
	}
	if apiKey == "" {
		apiKey = strings.TrimSpace(current.APIKey)
	}
	if baseURL == "" {
		baseURL = strings.TrimSpace(current.APIBaseURL)
	}
	return apiKey, baseURL
}

// knownLLMPreset is preset when it is a stored preset, and empty for an
// unspecified or unknown one.
func knownLLMPreset(preset LLMPreset) LLMPreset {
	switch preset {
	case LLMPresetGemini, LLMPresetOpenAICompatible:
		return preset
	default:
		return ""
	}
}
