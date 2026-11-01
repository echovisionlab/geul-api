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
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const translationProviderUnavailableMessage = "translation generation requires an active translation provider"
const defaultTranslationLLMGeminiModel = "gemini-2.5-flash-lite"

var errTranslationProviderUnavailable = errors.New(translationProviderUnavailableMessage)

type TranslationGeneratorResolver interface {
	Resolve(ctx context.Context) (translation.Generator, error)
	ResolveAll(ctx context.Context) ([]translation.Generator, error)
	HasAvailableProvider(ctx context.Context) (bool, error)
}

type dbTranslationGeneratorResolver struct {
	db *gorm.DB
}

func newDBTranslationGeneratorResolver(db *gorm.DB) TranslationGeneratorResolver {
	return dbTranslationGeneratorResolver{db: db}
}

func (r dbTranslationGeneratorResolver) Resolve(ctx context.Context) (translation.Generator, error) {
	generator, err := loadActiveTranslationGenerator(ctx, r.db)
	return generator, err
}

func (r dbTranslationGeneratorResolver) ResolveAll(ctx context.Context) ([]translation.Generator, error) {
	generators, err := loadActiveTranslationGenerators(ctx, r.db)
	return generators, err
}

func (r dbTranslationGeneratorResolver) HasAvailableProvider(ctx context.Context) (bool, error) {
	generators, err := loadActiveTranslationGenerators(ctx, r.db)
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
	Providers  []*managev1.TranslationProvider
	Pagination *commonv1.PaginationResponse
}

// ListTranslationProviders lists translation providers without secrets.
func (s *Service) ListTranslationProviders(ctx context.Context, query ListTranslationProvidersQuery) (ProviderPage, error) {
	can, canErr := policyv1.TranslationProvider.List()
	if err := s.requireTranslationPlatformCan(ctx, can, canErr); err != nil {
		return ProviderPage{}, err
	}

	db := s.db.WithContext(ctx).Model(&model.TranslationProviderConfig{})
	for _, filter := range query.Filters {
		if filter == nil {
			continue
		}
		if filter.GetField() == "is_active" && filter.GetValue() == "true" {
			db = db.Where("is_active = ?", true)
		}
	}

	var total int64
	if err := db.Count(&total).Error; err != nil {
		return ProviderPage{}, err
	}

	pagination := queryutil.ExtractPaginationRaw(query.Limit, query.Offset, queryutil.DefaultPaginationConfig)
	var providers []model.TranslationProviderConfig
	if err := pagination.Apply(db.Order("priority ASC, created_at ASC")).Find(&providers).Error; err != nil {
		return ProviderPage{}, err
	}

	page := ProviderPage{
		Providers:  make([]*managev1.TranslationProvider, 0, len(providers)),
		Pagination: pagination.BuildResponse(total),
	}
	for i := range providers {
		page.Providers = append(page.Providers, toProtoTranslationProvider(ctx, &providers[i], false))
	}
	return page, nil
}

// CreateTranslationProviderCommand registers a translation provider. The
// config of the chosen Type is required.
type CreateTranslationProviderCommand struct {
	Name        string
	Type        managev1.TranslationProviderType
	IsActive    bool
	Priority    int32
	LLMConfig   *managev1.LLMTranslationProviderConfig
	DeepLConfig *managev1.DeepLTranslationProviderConfig
}

// CreateTranslationProvider registers a provider after proving its config
// builds a generator.
func (s *Service) CreateTranslationProvider(ctx context.Context, command CreateTranslationProviderCommand) (_ *managev1.TranslationProvider, retErr error) {
	// The boundary redacts the API keys as sent; failures can quote them
	// trimmed.
	defer failurecause.AnnotatePrivate(&retErr,
		strings.TrimSpace(command.LLMConfig.GetApiKey()),
		strings.TrimSpace(command.DeepLConfig.GetApiKey()),
	)
	provider, err := mutation.Run(ctx, s.mutations, mutation.Spec[*model.TranslationProviderConfig]{
		Access: mutation.FreshAdminCan(policyv1.TranslationProvider.Create()),
		Change: func(ctx context.Context, tx *gorm.DB) (*model.TranslationProviderConfig, error) {
			return createTranslationProvider(ctx, tx, command)
		},
		Audit: func(provider *model.TranslationProviderConfig) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditTranslationProviderCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewTranslationProviderCreatedAuditRecord(metadata, provider.ID)
			})
		},
	})
	if err != nil {
		return nil, err
	}
	return toProtoTranslationProvider(ctx, provider, false), nil
}

func createTranslationProvider(ctx context.Context, tx *gorm.DB, command CreateTranslationProviderCommand) (*model.TranslationProviderConfig, error) {
	name := strings.TrimSpace(command.Name)
	if name == "" {
		return nil, domainerr.Required("name")
	}
	if len(name) > 255 {
		return nil, domainerr.InvalidField("name", "must be at most 255 characters")
	}
	providerType := protoToModelTranslationProviderType(command.Type)
	if providerType == "" {
		return nil, domainerr.InvalidField("type", "invalid translation provider type")
	}
	config, err := buildTranslationProviderConfig(ctx, providerType, command.LLMConfig, command.DeepLConfig, nil)
	if err != nil {
		return nil, domainerr.InvalidField("config", err.Error())
	}
	now := dbtime.Now()
	provider := &model.TranslationProviderConfig{
		Name:      name,
		Type:      providerType,
		IsActive:  command.IsActive,
		Priority:  int(command.Priority),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := provider.SetConfig(config); err != nil {
		return nil, fmt.Errorf("failed to set translation provider config: %w", err)
	}
	if _, err := newTranslationGeneratorFromProvider(provider); err != nil {
		return nil, domainerr.InvalidField("config", err.Error())
	}
	if err := tx.Omit("ID").Clauses(clause.Returning{}).Create(provider).Error; err != nil {
		return nil, err
	}
	return provider, nil
}

// UpdateTranslationProviderCommand changes the set fields of a provider. A
// config without an API key keeps the stored key of the same type.
type UpdateTranslationProviderCommand struct {
	ID          string
	Name        *string
	Type        *managev1.TranslationProviderType
	IsActive    *bool
	Priority    *int32
	LLMConfig   *managev1.LLMTranslationProviderConfig
	DeepLConfig *managev1.DeepLTranslationProviderConfig
}

type providerUpdate struct {
	provider      model.TranslationProviderConfig
	changedFields []string
}

// UpdateTranslationProvider applies the set fields and audits the fields
// that changed.
func (s *Service) UpdateTranslationProvider(ctx context.Context, command UpdateTranslationProviderCommand) (_ *managev1.TranslationProvider, retErr error) {
	// The boundary redacts the API keys as sent; failures can quote them
	// trimmed.
	defer failurecause.AnnotatePrivate(&retErr,
		strings.TrimSpace(command.LLMConfig.GetApiKey()),
		strings.TrimSpace(command.DeepLConfig.GetApiKey()),
	)
	providerID := strings.TrimSpace(command.ID)
	var provider model.TranslationProviderConfig
	updated, err := mutation.Run(ctx, s.mutations, mutation.Spec[providerUpdate]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			return tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&provider, "id = ?", providerID).Error
		},
		Access: mutation.FreshAdminCan(policyv1.TranslationProvider.Update()),
		Change: func(ctx context.Context, tx *gorm.DB) (providerUpdate, error) {
			return updateTranslationProvider(ctx, tx, provider, command)
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
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, domainerr.ResourceNotFound("translation_provider", command.ID)
	}
	if err != nil {
		return nil, err
	}
	return toProtoTranslationProvider(ctx, &updated.provider, false), nil
}

func updateTranslationProvider(
	ctx context.Context,
	tx *gorm.DB,
	provider model.TranslationProviderConfig,
	command UpdateTranslationProviderCommand,
) (providerUpdate, error) {
	requested, err := requestedTranslationProvider(ctx, provider, command)
	if err != nil {
		return providerUpdate{}, err
	}
	updates := structured.Fields{}
	changedFields := make([]string, 0, 5)
	if command.Name != nil && provider.Name != requested.Name {
		updates["name"] = requested.Name
		provider.Name = requested.Name
		changedFields = append(changedFields, "name")
	}
	if command.IsActive != nil && provider.IsActive != requested.IsActive {
		updates["is_active"] = requested.IsActive
		provider.IsActive = requested.IsActive
		changedFields = append(changedFields, "active")
	}
	if command.Priority != nil && provider.Priority != requested.Priority {
		updates["priority"] = requested.Priority
		provider.Priority = requested.Priority
		changedFields = append(changedFields, "priority")
	}
	if command.Type != nil && provider.Type != requested.Type {
		updates["type"] = requested.Type
		provider.Type = requested.Type
		changedFields = append(changedFields, "type")
	}
	if requested.configChanged && !configJSONSemanticallyEqual(provider.Config.RawMessage, requested.Config.RawMessage) {
		updates["config"] = requested.Config
		provider.Config = requested.Config
		changedFields = append(changedFields, "config")
	}
	if len(changedFields) == 0 {
		return providerUpdate{provider: provider}, nil
	}
	updates["updated_at"] = tx.NowFunc().UTC()
	if err := tx.Model(&provider).Clauses(clause.Returning{Columns: []clause.Column{{Name: "updated_at"}}}).Updates(updates).Error; err != nil {
		return providerUpdate{}, err
	}
	return providerUpdate{provider: provider, changedFields: changedFields}, nil
}

type requestedProvider struct {
	model.TranslationProviderConfig
	configChanged bool
}

// requestedTranslationProvider validates the command against the stored
// provider and returns the provider the command asks for.
func requestedTranslationProvider(
	ctx context.Context,
	provider model.TranslationProviderConfig,
	command UpdateTranslationProviderCommand,
) (requestedProvider, error) {
	requested := requestedProvider{TranslationProviderConfig: provider}
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
		nextType := protoToModelTranslationProviderType(*command.Type)
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
	var provider model.TranslationProviderConfig
	_, err := mutation.Run(ctx, s.mutations, mutation.Spec[struct{}]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			return tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&provider, "id = ?", providerID).Error
		},
		Access: mutation.FreshAdminCan(policyv1.TranslationProvider.Delete()),
		Change: func(_ context.Context, tx *gorm.DB) (struct{}, error) {
			return struct{}{}, tx.Delete(&provider).Error
		},
		Audit: func(struct{}) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditTranslationProviderDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewTranslationProviderDeletedAuditRecord(metadata, provider.ID)
			})
		},
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
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

func loadActiveTranslationGenerator(
	ctx context.Context,
	db *gorm.DB,
) (translation.Generator, error) {
	generators, err := loadActiveTranslationGenerators(ctx, db)
	if err != nil {
		return nil, err
	}
	return generators[0], nil
}

func loadActiveTranslationGenerators(
	ctx context.Context,
	db *gorm.DB,
) ([]translation.Generator, error) {
	var providers []model.TranslationProviderConfig
	if err := db.WithContext(ctx).
		Where("is_active = ?", true).
		Order("priority ASC, created_at ASC").
		Find(&providers).Error; err != nil {
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

func hasAvailableTranslationProvider(ctx context.Context, db *gorm.DB) (bool, error) {
	_, err := loadActiveTranslationGenerator(ctx, db)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, errTranslationProviderUnavailable) {
		return false, nil
	}
	return false, err
}

func newTranslationGeneratorFromProvider(provider *model.TranslationProviderConfig) (translation.Generator, error) {
	if provider == nil {
		return nil, errTranslationProviderUnavailable
	}
	switch provider.Type {
	case model.TranslationProviderTypeLLM:
		cfg, err := provider.GetLLMConfig()
		if err != nil {
			return nil, err
		}
		textProvider, err := newTranslationLLMTextProvider(cfg)
		if err != nil {
			return nil, err
		}
		return translation.NewAIGenerator(textProvider), nil
	case model.TranslationProviderTypeDeepL:
		cfg, err := provider.GetDeepLConfig()
		if err != nil {
			return nil, err
		}
		return translation.NewDeepLGenerator(cfg.APIKey, cfg.APIBaseURL)
	default:
		return nil, fmt.Errorf("unsupported translation provider type %q", provider.Type)
	}
}

func newTranslationLLMTextProvider(cfg *model.LLMTranslationProviderConfig) (llm.Provider, error) {
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
		preset = model.TranslationLLMProviderPresetGemini
	}
	switch preset {
	case model.TranslationLLMProviderPresetGemini:
		if modelName == "" && preset == model.TranslationLLMProviderPresetGemini {
			modelName = defaultTranslationLLMGeminiModel
		}
		return llmproviders.NewGeminiProvider(llmproviders.GeminiConfig{
			APIKey: apiKey, Model: modelName, Temperature: float32PtrFromFloat64Ptr(cfg.Temperature),
		})
	case model.TranslationLLMProviderPresetOpenAICompatible:
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
	provider *model.TranslationProviderConfig,
	typeChanged bool,
) *model.TranslationProviderConfig {
	if typeChanged {
		return nil
	}
	return provider
}

type llmTranslationProviderValues struct {
	apiKey           string
	preset           model.TranslationLLMProviderPreset
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
	config *managev1.LLMTranslationProviderConfig,
	existing *model.TranslationProviderConfig,
) llmTranslationProviderValues {
	values := llmTranslationProviderValues{
		apiKey:           strings.TrimSpace(config.ApiKey),
		preset:           protoToModelTranslationLLMProviderPreset(config.Preset),
		baseURL:          strings.TrimSpace(config.GetApiBaseUrl()),
		modelName:        strings.TrimSpace(config.Model),
		inputPrice:       config.InputTokenPriceUsdPerMillion,
		outputPrice:      config.OutputTokenPriceUsdPerMillion,
		maxContextTokens: config.MaxContextTokens,
		maxOutputTokens:  config.MaxOutputTokens,
		temperature:      config.Temperature,
	}
	if values.preset == "" {
		values.preset = model.TranslationLLMProviderPresetGemini
	}
	if values.apiKey != "" || existing == nil || existing.Type != model.TranslationProviderTypeLLM {
		return values
	}
	current, err := existing.GetLLMConfig()
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
	requested *managev1.LLMTranslationProviderConfig,
	current *model.LLMTranslationProviderConfig,
) {
	if values.modelName == "" {
		values.modelName = strings.TrimSpace(current.Model)
	}
	if values.baseURL == "" {
		values.baseURL = strings.TrimSpace(current.APIBaseURL)
	}
	if requested.Preset == managev1.TranslationLLMProviderPreset_TRANSLATION_LLM_PROVIDER_PRESET_UNSPECIFIED && current.Preset != "" {
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
	if values.preset != model.TranslationLLMProviderPresetGemini {
		return fmt.Errorf("LLM model is required")
	}
	values.modelName = defaultTranslationLLMGeminiModel
	return nil
}

func buildTranslationProviderConfig(
	ctx context.Context,
	providerType model.TranslationProviderType,
	llmConfig *managev1.LLMTranslationProviderConfig,
	deeplConfig *managev1.DeepLTranslationProviderConfig,
	existing *model.TranslationProviderConfig,
) (structured.Value, error) {
	switch providerType {
	case model.TranslationProviderTypeLLM:
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
		cfg := &model.LLMTranslationProviderConfig{
			APIKey:                        values.apiKey,
			Preset:                        values.preset,
			APIBaseURL:                    values.baseURL,
			Model:                         values.modelName,
			InputTokenPriceUSDPerMillion:  values.inputPrice,
			OutputTokenPriceUSDPerMillion: values.outputPrice,
			MaxContextTokens:              values.maxContextTokens,
			MaxOutputTokens:               values.maxOutputTokens,
			SupportsJSONMode:              llmConfig.SupportsJsonMode,
			Temperature:                   values.temperature,
		}
		if _, err := newTranslationLLMTextProvider(cfg); err != nil {
			return nil, err
		}
		return cfg, nil
	case model.TranslationProviderTypeDeepL:
		if deeplConfig == nil {
			return nil, fmt.Errorf("DeepL config is required")
		}
		apiKey := strings.TrimSpace(deeplConfig.ApiKey)
		baseURL := strings.TrimSpace(deeplConfig.GetApiBaseUrl())
		apiKey, baseURL = inheritDeepLProviderConfig(ctx, existing, providerType, apiKey, baseURL)
		if apiKey == "" {
			return nil, fmt.Errorf("DeepL API key is required")
		}
		if baseURL == "" {
			baseURL = translation.DefaultDeepLAPIBaseURL
		}
		return &model.DeepLTranslationProviderConfig{APIKey: apiKey, APIBaseURL: baseURL}, nil
	default:
		return nil, fmt.Errorf("unknown translation provider type: %s", providerType)
	}
}

func inheritDeepLProviderConfig(
	ctx context.Context,
	existing *model.TranslationProviderConfig,
	providerType model.TranslationProviderType,
	apiKey string,
	baseURL string,
) (string, string) {
	if existing == nil || existing.Type != providerType {
		return apiKey, baseURL
	}
	current, err := existing.GetDeepLConfig()
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

func protoToModelTranslationLLMProviderPreset(
	preset managev1.TranslationLLMProviderPreset,
) model.TranslationLLMProviderPreset {
	switch preset {
	case managev1.TranslationLLMProviderPreset_TRANSLATION_LLM_PROVIDER_PRESET_GEMINI:
		return model.TranslationLLMProviderPresetGemini
	case managev1.TranslationLLMProviderPreset_TRANSLATION_LLM_PROVIDER_PRESET_OPENAI_COMPATIBLE:
		return model.TranslationLLMProviderPresetOpenAICompatible
	default:
		return ""
	}
}

func modelToProtoTranslationLLMProviderPreset(
	preset model.TranslationLLMProviderPreset,
) managev1.TranslationLLMProviderPreset {
	switch preset {
	case model.TranslationLLMProviderPresetGemini:
		return managev1.TranslationLLMProviderPreset_TRANSLATION_LLM_PROVIDER_PRESET_GEMINI
	case model.TranslationLLMProviderPresetOpenAICompatible:
		return managev1.TranslationLLMProviderPreset_TRANSLATION_LLM_PROVIDER_PRESET_OPENAI_COMPATIBLE
	default:
		return managev1.TranslationLLMProviderPreset_TRANSLATION_LLM_PROVIDER_PRESET_UNSPECIFIED
	}
}

func protoToModelTranslationProviderType(t managev1.TranslationProviderType) model.TranslationProviderType {
	switch t {
	case managev1.TranslationProviderType_TRANSLATION_PROVIDER_TYPE_LLM:
		return model.TranslationProviderTypeLLM
	case managev1.TranslationProviderType_TRANSLATION_PROVIDER_TYPE_DEEPL:
		return model.TranslationProviderTypeDeepL
	default:
		return ""
	}
}

func modelToProtoTranslationProviderType(t model.TranslationProviderType) managev1.TranslationProviderType {
	switch t {
	case model.TranslationProviderTypeLLM:
		return managev1.TranslationProviderType_TRANSLATION_PROVIDER_TYPE_LLM
	case model.TranslationProviderTypeDeepL:
		return managev1.TranslationProviderType_TRANSLATION_PROVIDER_TYPE_DEEPL
	default:
		return managev1.TranslationProviderType_TRANSLATION_PROVIDER_TYPE_UNSPECIFIED
	}
}

func toProtoTranslationProvider(ctx context.Context, provider *model.TranslationProviderConfig, includeSecrets bool) *managev1.TranslationProvider {
	if provider == nil {
		return nil
	}
	proto := &managev1.TranslationProvider{
		Id:       provider.ID,
		Name:     provider.Name,
		Type:     modelToProtoTranslationProviderType(provider.Type),
		IsActive: provider.IsActive,
		Priority: int32(provider.Priority),
	}
	proto.CreatedAt = timestamppb.New(provider.CreatedAt.UTC())
	proto.UpdatedAt = timestamppb.New(provider.UpdatedAt.UTC())

	switch provider.Type {
	case model.TranslationProviderTypeLLM:
		if cfg, err := provider.GetLLMConfig(); err == nil {
			out := &managev1.LLMTranslationProviderConfig{
				Preset:           modelToProtoTranslationLLMProviderPreset(cfg.Preset),
				Model:            cfg.Model,
				SupportsJsonMode: cfg.SupportsJSONMode,
			}
			if cfg.APIBaseURL != "" {
				out.ApiBaseUrl = &cfg.APIBaseURL
			}
			out.InputTokenPriceUsdPerMillion = cfg.InputTokenPriceUSDPerMillion
			out.OutputTokenPriceUsdPerMillion = cfg.OutputTokenPriceUSDPerMillion
			out.MaxContextTokens = cfg.MaxContextTokens
			out.MaxOutputTokens = cfg.MaxOutputTokens
			out.Temperature = cfg.Temperature
			if includeSecrets {
				out.ApiKey = cfg.APIKey
			}
			proto.Config = &managev1.TranslationProvider_LlmConfig{LlmConfig: out}
		} else {
			slog.ErrorContext(ctx, "Stored translation provider config decode failed; returning provider without config", "domain", "translation", "event", "translation.provider.config_decode_failed", "outcome", "failed", failurecause.Attr(err), "provider_id", provider.ID)
		}
	case model.TranslationProviderTypeDeepL:
		if cfg, err := provider.GetDeepLConfig(); err == nil {
			out := &managev1.DeepLTranslationProviderConfig{}
			if cfg.APIBaseURL != "" {
				out.ApiBaseUrl = &cfg.APIBaseURL
			}
			if includeSecrets {
				out.ApiKey = cfg.APIKey
			}
			proto.Config = &managev1.TranslationProvider_DeeplConfig{DeeplConfig: out}
		} else {
			slog.ErrorContext(ctx, "Stored translation provider config decode failed; returning provider without config", "domain", "translation", "event", "translation.provider.config_decode_failed", "outcome", "failed", failurecause.Attr(err), "provider_id", provider.ID)
		}
	}
	return proto
}
