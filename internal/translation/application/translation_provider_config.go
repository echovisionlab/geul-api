package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/echovisionlab/geul-api/internal/translation"
	"log/slog"
	"reflect"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/llm"
	"github.com/echovisionlab/geul-api/internal/model"
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
	generator, _, err := loadActiveTranslationGenerator(ctx, r.db)
	return generator, err
}

func (r dbTranslationGeneratorResolver) ResolveAll(ctx context.Context) ([]translation.Generator, error) {
	generators, _, err := loadActiveTranslationGenerators(ctx, r.db)
	return generators, err
}

func (r dbTranslationGeneratorResolver) HasAvailableProvider(ctx context.Context) (bool, error) {
	generators, _, err := loadActiveTranslationGenerators(ctx, r.db)
	if err == nil {
		return len(generators) > 0, nil
	}
	if errors.Is(err, errTranslationProviderUnavailable) {
		return false, nil
	}
	return false, err
}

func (s *TranslationService) ListTranslationProviders(
	ctx context.Context,
	req *connect.Request[managev1.ListTranslationProvidersRequest],
) (*connect.Response[managev1.ListTranslationProvidersResponse], error) {
	can, canErr := policyv1.TranslationProvider.List()
	if err := s.requireTranslationPlatformCan(ctx, can, canErr); err != nil {
		return nil, err
	}

	query := s.db.WithContext(ctx).Model(&model.TranslationProviderConfig{})
	for _, filter := range req.Msg.Filters {
		if filter == nil {
			continue
		}
		if filter.GetField() == "is_active" && filter.GetValue() == "true" {
			query = query.Where("is_active = ?", true)
		}
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}

	limit, offset := queryutil.NormalizePaginationParams(req.Msg.Pagination)
	var providers []model.TranslationProviderConfig
	if err := query.
		Order("priority ASC, created_at ASC").
		Limit(int(limit)).
		Offset(int(offset)).
		Find(&providers).Error; err != nil {
		return nil, errs.Internal(err)
	}

	resp := &managev1.ListTranslationProvidersResponse{
		Providers: make([]*managev1.TranslationProvider, 0, len(providers)),
		Pagination: &commonv1.PaginationResponse{
			Total:  int32(total),
			Limit:  limit,
			Offset: offset,
		},
	}
	for i := range providers {
		resp.Providers = append(resp.Providers, toProtoTranslationProvider(&providers[i], false))
	}
	return connect.NewResponse(resp), nil
}

func (s *TranslationService) CreateTranslationProvider(
	ctx context.Context,
	req *connect.Request[managev1.CreateTranslationProviderRequest],
) (*connect.Response[managev1.CreateTranslationProviderResponse], error) {
	providerCan, err := policyv1.TranslationProvider.Create()
	if err != nil {
		return nil, errs.Internal(err)
	}

	var provider *model.TranslationProviderConfig
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, providerCan); err != nil {
			return err
		}
		name := strings.TrimSpace(req.Msg.Name)
		if name == "" {
			return errs.Required("name")
		}
		if len(name) > 255 {
			return errs.InvalidArgument("name", "must be at most 255 characters")
		}

		providerType := protoToModelTranslationProviderType(req.Msg.Type)
		if providerType == "" {
			return errs.InvalidArgument("type", "invalid translation provider type")
		}
		config, err := buildTranslationProviderConfig(providerType, req.Msg.GetLlmConfig(), req.Msg.GetDeeplConfig(), nil)
		if err != nil {
			return errs.InvalidArgument("config", err.Error())
		}

		now := time.Now().UTC()
		provider = &model.TranslationProviderConfig{
			Name:      name,
			Type:      providerType,
			IsActive:  req.Msg.IsActive,
			Priority:  int(req.Msg.GetPriority()),
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := provider.SetConfig(config); err != nil {
			return errs.Internal(fmt.Errorf("failed to set translation provider config: %w", err))
		}
		if _, err := newTranslationGeneratorFromProvider(provider); err != nil {
			return errs.InvalidArgument("config", err.Error())
		}
		if err := tx.Omit("ID").Clauses(clause.Returning{}).Create(provider).Error; err != nil {
			return err
		}
		if s.auditWriter == nil {
			return nil
		}
		return domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditTranslationProviderCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewTranslationProviderCreatedAuditRecord(metadata, provider.ID)
		})
	}); err != nil {
		return nil, errs.Wrap(err)
	}

	return connect.NewResponse(&managev1.CreateTranslationProviderResponse{
		Provider: toProtoTranslationProvider(provider, false),
	}), nil
}

func (s *TranslationService) UpdateTranslationProvider(
	ctx context.Context,
	req *connect.Request[managev1.UpdateTranslationProviderRequest],
) (*connect.Response[managev1.UpdateTranslationProviderResponse], error) {
	providerCan, err := policyv1.TranslationProvider.Update()
	if err != nil {
		return nil, errs.Internal(err)
	}

	providerID := strings.TrimSpace(req.Msg.Id)
	var provider model.TranslationProviderConfig
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&provider, "id = ?", providerID).Error; err != nil {
			return err
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, providerCan); err != nil {
			return err
		}

		requestedName := provider.Name
		requestedActive := provider.IsActive
		requestedPriority := provider.Priority
		if req.Msg.Name != nil {
			name := strings.TrimSpace(*req.Msg.Name)
			if name == "" {
				return errs.InvalidArgument("name", "cannot be empty")
			}
			if len(name) > 255 {
				return errs.InvalidArgument("name", "must be at most 255 characters")
			}
			requestedName = name
		}
		if req.Msg.IsActive != nil {
			requestedActive = *req.Msg.IsActive
		}
		if req.Msg.Priority != nil {
			requestedPriority = int(*req.Msg.Priority)
		}

		effectiveType := provider.Type
		typeChanged := false
		if req.Msg.Type != nil {
			nextType := protoToModelTranslationProviderType(*req.Msg.Type)
			if nextType == "" {
				return errs.InvalidArgument("type", "invalid translation provider type")
			}
			if nextType != provider.Type {
				typeChanged = true
				effectiveType = nextType
			}
		}

		configChanged := typeChanged || req.Msg.GetLlmConfig() != nil || req.Msg.GetDeeplConfig() != nil
		var requestedConfig model.TranslationProviderConfigJSON
		if configChanged {
			config, err := buildTranslationProviderConfig(
				effectiveType,
				req.Msg.GetLlmConfig(),
				req.Msg.GetDeeplConfig(),
				translationProviderExistingConfig(&provider, typeChanged),
			)
			if err != nil {
				return errs.InvalidArgument("config", err.Error())
			}
			candidate := provider
			candidate.Type = effectiveType
			if err := candidate.SetConfig(config); err != nil {
				return errs.Internal(fmt.Errorf("failed to set translation provider config: %w", err))
			}
			if _, err := newTranslationGeneratorFromProvider(&candidate); err != nil {
				return errs.InvalidArgument("config", err.Error())
			}
			requestedConfig = candidate.Config
		}

		updates := structured.Fields{}
		changedFields := make([]string, 0, 5)
		if req.Msg.Name != nil && provider.Name != requestedName {
			updates["name"] = requestedName
			provider.Name = requestedName
			changedFields = append(changedFields, "name")
		}
		if req.Msg.IsActive != nil && provider.IsActive != requestedActive {
			updates["is_active"] = requestedActive
			provider.IsActive = requestedActive
			changedFields = append(changedFields, "active")
		}
		if req.Msg.Priority != nil && provider.Priority != requestedPriority {
			updates["priority"] = requestedPriority
			provider.Priority = requestedPriority
			changedFields = append(changedFields, "priority")
		}
		if req.Msg.Type != nil && provider.Type != effectiveType {
			updates["type"] = effectiveType
			provider.Type = effectiveType
			changedFields = append(changedFields, "type")
		}
		if configChanged && !configJSONSemanticallyEqual(provider.Config.RawMessage, requestedConfig.RawMessage) {
			updates["config"] = requestedConfig
			provider.Config = requestedConfig
			changedFields = append(changedFields, "config")
		}
		if len(changedFields) == 0 {
			return nil
		}
		now := time.Now().UTC()
		updates["updated_at"] = now
		provider.UpdatedAt = now
		if err := tx.Model(&provider).Updates(updates).Error; err != nil {
			return err
		}
		if s.auditWriter == nil {
			return nil
		}
		return domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditTranslationProviderUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewTranslationProviderConfigUpdatedAuditRecord(metadata, provider.ID, changedFields)
		})
	})
	if err != nil {
		if connectErr, ok := err.(*connect.Error); ok {
			return nil, connectErr
		}
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("translation_provider", req.Msg.Id)
		}
		return nil, errs.Internal(err)
	}

	return connect.NewResponse(&managev1.UpdateTranslationProviderResponse{
		Provider: toProtoTranslationProvider(&provider, false),
	}), nil
}

func configJSONSemanticallyEqual(left, right []byte) bool {
	var leftValue any
	var rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func (s *TranslationService) DeleteTranslationProvider(
	ctx context.Context,
	req *connect.Request[managev1.DeleteTranslationProviderRequest],
) (*connect.Response[managev1.DeleteTranslationProviderResponse], error) {
	providerCan, err := policyv1.TranslationProvider.Delete()
	if err != nil {
		return nil, errs.Internal(err)
	}

	providerID := strings.TrimSpace(req.Msg.Id)
	var provider model.TranslationProviderConfig
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&provider, "id = ?", providerID).Error; err != nil {
			return err
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, providerCan); err != nil {
			return err
		}
		if err := tx.Delete(&provider).Error; err != nil {
			return err
		}
		if s.auditWriter == nil {
			return nil
		}
		return domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditTranslationProviderDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewTranslationProviderDeletedAuditRecord(metadata, provider.ID)
		})
	}); err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("translation_provider", req.Msg.Id)
		}
		return nil, errs.Wrap(err)
	}

	return connect.NewResponse(&managev1.DeleteTranslationProviderResponse{}), nil
}

func (s *TranslationService) requireTranslationPlatformCan(
	ctx context.Context,
	can policyv1.Can,
	canErr error,
) error {
	if canErr != nil {
		return errs.Internal(canErr)
	}
	return authz.RequireAdminCan(ctx, s.spiceDB, can)
}

func loadActiveTranslationGenerator(
	ctx context.Context,
	db *gorm.DB,
) (translation.Generator, *model.TranslationProviderConfig, error) {
	generators, providers, err := loadActiveTranslationGenerators(ctx, db)
	if err != nil {
		return nil, nil, err
	}
	return generators[0], providers[0], nil
}

func loadActiveTranslationGenerators(
	ctx context.Context,
	db *gorm.DB,
) ([]translation.Generator, []*model.TranslationProviderConfig, error) {
	var providers []model.TranslationProviderConfig
	if err := db.WithContext(ctx).
		Where("is_active = ?", true).
		Order("priority ASC, created_at ASC").
		Find(&providers).Error; err != nil {
		return nil, nil, err
	}
	for i := range providers {
		generator, err := newTranslationGeneratorFromProvider(&providers[i])
		if err != nil {
			slog.Warn(
				"Skipping invalid active translation provider",
				"provider_id", providers[i].ID,
				"provider_name", providers[i].Name,
				"provider_type", providers[i].Type,
				"error", err,
			)
			continue
		}
		generators := []translation.Generator{generator}
		configs := []*model.TranslationProviderConfig{&providers[i]}
		for index := i + 1; index < len(providers); index++ {
			nextGenerator, err := newTranslationGeneratorFromProvider(&providers[index])
			if err != nil {
				slog.Warn(
					"Skipping invalid active translation provider",
					"provider_id", providers[index].ID,
					"provider_name", providers[index].Name,
					"provider_type", providers[index].Type,
					"error", err,
				)
				continue
			}
			generators = append(generators, nextGenerator)
			configs = append(configs, &providers[index])
		}
		return generators, configs, nil
	}
	return nil, nil, errTranslationProviderUnavailable
}

func hasAvailableTranslationProvider(ctx context.Context, db *gorm.DB) (bool, error) {
	_, _, err := loadActiveTranslationGenerator(ctx, db)
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
		return llm.NewGeminiProvider(llm.GeminiConfig{
			APIKey: apiKey, Model: modelName, Temperature: float32PtrFromFloat64Ptr(cfg.Temperature),
		})
	case model.TranslationLLMProviderPresetOpenAICompatible:
		return llm.NewOpenAICompatibleProvider(llm.OpenAICompatibleConfig{
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
		values := resolveLLMTranslationProviderValues(llmConfig, existing)
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
		apiKey, baseURL = inheritDeepLProviderConfig(existing, providerType, apiKey, baseURL)
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

func toProtoTranslationProvider(provider *model.TranslationProviderConfig, includeSecrets bool) *managev1.TranslationProvider {
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
		}
	}
	return proto
}
