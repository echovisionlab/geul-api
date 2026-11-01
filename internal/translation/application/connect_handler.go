package application

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/translation"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

// ConnectHandler serves the Translation Connect RPCs. Each method translates
// the request into a use-case command or query, and the result into the
// response; errs.Wrap maps use-case errors to Connect codes.
type ConnectHandler struct {
	managev1connect.UnimplementedTranslationServiceHandler
	translations *Service
}

// NewConnectHandler returns the Connect adapter of translations.
func NewConnectHandler(translations *Service) *ConnectHandler {
	return &ConnectHandler{translations: translations}
}

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

func (h *ConnectHandler) ListTranslationLocales(ctx context.Context, _ *connect.Request[managev1.ListTranslationLocalesRequest]) (*connect.Response[managev1.ListTranslationLocalesResponse], error) {
	locales, err := h.translations.ListTranslationLocales(ctx)
	if err != nil {
		return nil, errs.Wrap(err)
	}
	messages := make([]*managev1.TranslationLocale, 0, len(locales))
	for _, locale := range locales {
		messages = append(messages, localeMessage(locale))
	}
	return respond(&managev1.ListTranslationLocalesResponse{Locales: messages}, nil)
}

func (h *ConnectHandler) GetTranslationOverview(ctx context.Context, _ *connect.Request[managev1.GetTranslationOverviewRequest]) (*connect.Response[managev1.GetTranslationOverviewResponse], error) {
	overview, err := h.translations.GetTranslationOverview(ctx)
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return respond(overviewMessage(overview), nil)
}

func (h *ConnectHandler) GetTranslationSettings(ctx context.Context, _ *connect.Request[managev1.GetTranslationSettingsRequest]) (*connect.Response[managev1.GetTranslationSettingsResponse], error) {
	view, err := h.translations.GetTranslationSettings(ctx)
	return respond(&managev1.GetTranslationSettingsResponse{
		Settings: settingsMessage(view.Settings), GenerationEnabled: view.GenerationEnabled, GenerationDisabledReason: view.GenerationDisabledReason,
	}, err)
}

func (h *ConnectHandler) UpdateTranslationSettings(ctx context.Context, req *connect.Request[managev1.UpdateTranslationSettingsRequest]) (*connect.Response[managev1.UpdateTranslationSettingsResponse], error) {
	settings, err := h.translations.UpdateTranslationSettings(ctx, UpdateTranslationSettingsCommand{
		Settings: settingsFromMessage(req.Msg.Settings), BaseSettings: settingsFromMessage(req.Msg.BaseSettings),
		UpdateMask: req.Msg.UpdateMask,
	})
	return respond(&managev1.UpdateTranslationSettingsResponse{Settings: settingsMessage(settings)}, err)
}

func (h *ConnectHandler) ListTranslationJobs(ctx context.Context, req *connect.Request[managev1.ListTranslationJobsRequest]) (*connect.Response[managev1.ListTranslationJobsResponse], error) {
	page, err := h.translations.ListTranslationJobs(ctx, ListTranslationJobsQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(),
		Filters: req.Msg.Filters, Sorts: req.Msg.Sorts,
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return respond(&managev1.ListTranslationJobsResponse{Jobs: JobMessages(page.Jobs), Pagination: PaginationMessage(page.Pagination)}, nil)
}

func (h *ConnectHandler) ListEntityTranslations(ctx context.Context, req *connect.Request[managev1.ListEntityTranslationsRequest]) (*connect.Response[managev1.ListEntityTranslationsResponse], error) {
	translations, err := h.translations.ListEntityTranslations(ctx, ListEntityTranslationsQuery{Target: TargetFromMessage(req.Msg.Target)})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return respond(&managev1.ListEntityTranslationsResponse{Entries: EntryMessages(translations.Entries), SourceLocale: translations.SourceLocale}, nil)
}

func (h *ConnectHandler) GetEntityTranslation(ctx context.Context, req *connect.Request[managev1.GetEntityTranslationRequest]) (*connect.Response[managev1.GetEntityTranslationResponse], error) {
	entry, err := h.translations.GetEntityTranslation(ctx, GetEntityTranslationQuery{Target: TargetFromMessage(req.Msg.Target), Locale: req.Msg.Locale})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return respond(&managev1.GetEntityTranslationResponse{Entry: EntryMessage(entry)}, nil)
}

func (h *ConnectHandler) SetEntitySourceLocale(ctx context.Context, req *connect.Request[managev1.SetEntitySourceLocaleRequest]) (*connect.Response[managev1.SetEntitySourceLocaleResponse], error) {
	change, err := h.translations.SetEntitySourceLocale(ctx, SetEntitySourceLocaleCommand{
		Target: TargetFromMessage(req.Msg.Target), SourceLocale: req.Msg.SourceLocale, ExpectedDocumentRevision: req.Msg.GetExpectedDocumentRevision(),
	})
	return respond(&managev1.SetEntitySourceLocaleResponse{
		DocumentRevision: change.DocumentRevision, Changed: change.Changed,
		SourceChanged: change.Changed, ChangedLocales: change.ChangedLocales,
	}, err)
}

func (h *ConnectHandler) RegenerateEntityTranslations(ctx context.Context, req *connect.Request[managev1.RegenerateEntityTranslationsRequest]) (*connect.Response[managev1.RegenerateEntityTranslationsResponse], error) {
	jobs, err := h.translations.RegenerateEntityTranslations(ctx, RegenerateEntityTranslationsCommand{
		Target: TargetFromMessage(req.Msg.Target), Locales: req.Msg.GetLocales(),
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return respond(&managev1.RegenerateEntityTranslationsResponse{Jobs: JobMessages(jobs)}, nil)
}

func (h *ConnectHandler) CancelTranslationJob(ctx context.Context, req *connect.Request[managev1.CancelTranslationJobRequest]) (*connect.Response[managev1.CancelTranslationJobResponse], error) {
	err := h.translations.CancelTranslationJob(ctx, CancelTranslationJobCommand{JobID: req.Msg.GetJobId()})
	return respond(&managev1.CancelTranslationJobResponse{}, err)
}

func (h *ConnectHandler) ListTranslationProviders(ctx context.Context, req *connect.Request[managev1.ListTranslationProvidersRequest]) (*connect.Response[managev1.ListTranslationProvidersResponse], error) {
	page, err := h.translations.ListTranslationProviders(ctx, ListTranslationProvidersQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(), Filters: req.Msg.Filters,
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	providers := make([]*managev1.TranslationProvider, 0, len(page.Providers))
	for index := range page.Providers {
		providers = append(providers, providerMessage(ctx, &page.Providers[index]))
	}
	return respond(&managev1.ListTranslationProvidersResponse{Providers: providers, Pagination: PaginationMessage(page.Pagination)}, nil)
}

func (h *ConnectHandler) CreateTranslationProvider(ctx context.Context, req *connect.Request[managev1.CreateTranslationProviderRequest]) (*connect.Response[managev1.CreateTranslationProviderResponse], error) {
	provider, err := h.translations.CreateTranslationProvider(ctx, CreateTranslationProviderCommand{
		Name: req.Msg.Name, Type: providerTypeFromMessage(req.Msg.Type), IsActive: req.Msg.IsActive, Priority: req.Msg.GetPriority(),
		LLMConfig: llmConfigInput(req.Msg.GetLlmConfig()), DeepLConfig: deepLConfigInput(req.Msg.GetDeeplConfig()),
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return respond(&managev1.CreateTranslationProviderResponse{Provider: providerMessage(ctx, &provider)}, nil)
}

func (h *ConnectHandler) UpdateTranslationProvider(ctx context.Context, req *connect.Request[managev1.UpdateTranslationProviderRequest]) (*connect.Response[managev1.UpdateTranslationProviderResponse], error) {
	command := UpdateTranslationProviderCommand{
		ID: req.Msg.Id, Name: req.Msg.Name, IsActive: req.Msg.IsActive, Priority: req.Msg.Priority,
		LLMConfig: llmConfigInput(req.Msg.GetLlmConfig()), DeepLConfig: deepLConfigInput(req.Msg.GetDeeplConfig()),
	}
	if req.Msg.Type != nil {
		providerType := providerTypeFromMessage(*req.Msg.Type)
		command.Type = &providerType
	}
	provider, err := h.translations.UpdateTranslationProvider(ctx, command)
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return respond(&managev1.UpdateTranslationProviderResponse{Provider: providerMessage(ctx, &provider)}, nil)
}

func (h *ConnectHandler) DeleteTranslationProvider(ctx context.Context, req *connect.Request[managev1.DeleteTranslationProviderRequest]) (*connect.Response[managev1.DeleteTranslationProviderResponse], error) {
	err := h.translations.DeleteTranslationProvider(ctx, DeleteTranslationProviderCommand{ID: req.Msg.Id})
	return respond(&managev1.DeleteTranslationProviderResponse{}, err)
}

func (h *ConnectHandler) ExportEntityTranslationXLIFF(ctx context.Context, req *connect.Request[managev1.ExportEntityTranslationXLIFFRequest]) (*connect.Response[managev1.ExportEntityTranslationXLIFFResponse], error) {
	exported, err := h.translations.ExportEntityTranslationXLIFF(ctx, ExportTranslationXLIFFCommand{
		Target: TargetFromMessage(req.Msg.Target), TargetLocale: req.Msg.TargetLocale,
		Mode: InterchangeModeFromMessage(req.Msg.Mode), UnitHandles: req.Msg.UnitHandles,
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return respond(XLIFFExportMessage(exported), nil)
}

func (h *ConnectHandler) ImportEntityTranslationXLIFF(ctx context.Context, req *connect.Request[managev1.ImportEntityTranslationXLIFFRequest]) (*connect.Response[managev1.ImportEntityTranslationXLIFFResponse], error) {
	imported, err := h.translations.ImportEntityTranslationXLIFF(ctx, ImportTranslationXLIFFCommand{
		Target: TargetFromMessage(req.Msg.Target), TargetLocale: req.Msg.TargetLocale,
		Mode: InterchangeModeFromMessage(req.Msg.Mode), FileID: req.Msg.FileId,
		ExpectedTargetRevision: req.Msg.ExpectedTargetRevision,
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return respond(XLIFFImportMessage(imported), nil)
}

// TargetFromMessage is the target a request names; an unsupported entity
// type has no kind, which the use cases reject.
func TargetFromMessage(target *managev1.TranslationTarget) *Target {
	if target == nil {
		return nil
	}
	var kind string
	if definition, ok := translation.DefinitionForProto(target.EntityType); ok {
		kind = string(definition.Kind)
	}
	return &Target{EntityType: kind, EntityID: target.EntityId}
}

func targetMessage(entityType, entityID string) *managev1.TranslationTarget {
	definition, _ := translation.DefinitionForKind(entityType)
	return &managev1.TranslationTarget{EntityType: definition.Proto, EntityId: entityID}
}

// InterchangeModeFromMessage is the XLIFF mode a request names.
func InterchangeModeFromMessage(mode managev1.TranslationInterchangeMode) InterchangeMode {
	switch mode {
	case managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH:
		return InterchangeModePatch
	case managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE:
		return InterchangeModeReplace
	default:
		return InterchangeModeUnspecified
	}
}

// InterchangeModeMessage is the wire value of an XLIFF mode.
func InterchangeModeMessage(mode InterchangeMode) managev1.TranslationInterchangeMode {
	switch mode {
	case InterchangeModePatch:
		return managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH
	case InterchangeModeReplace:
		return managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE
	default:
		return managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_UNSPECIFIED
	}
}

// XLIFFExportMessage is the wire form of an XLIFF export.
func XLIFFExportMessage(exported XLIFFExport) *managev1.ExportEntityTranslationXLIFFResponse {
	return &managev1.ExportEntityTranslationXLIFFResponse{
		Artifact: exported.Artifact, SourceLocale: exported.SourceLocale, TargetLocale: exported.TargetLocale,
		TargetRevision: exported.TargetRevision, Mode: InterchangeModeMessage(exported.Mode),
	}
}

// XLIFFImportMessage is the wire form of an XLIFF import.
func XLIFFImportMessage(imported XLIFFImport) *managev1.ImportEntityTranslationXLIFFResponse {
	return &managev1.ImportEntityTranslationXLIFFResponse{
		TargetRevision: imported.TargetRevision, Changed: imported.Changed,
		AffectedUnitHandles: imported.AffectedUnitHandles,
	}
}

// PaginationMessage is the wire form of a page position.
func PaginationMessage(pagination Pagination) *commonv1.PaginationResponse {
	return &commonv1.PaginationResponse{
		Total: pagination.Total, Limit: pagination.Limit, Offset: pagination.Offset, HasMore: pagination.HasMore,
	}
}

// JobMessages is the wire form of jobs.
func JobMessages(jobs []Job) []*managev1.TranslationJob {
	messages := make([]*managev1.TranslationJob, 0, len(jobs))
	for index := range jobs {
		messages = append(messages, JobMessage(jobs[index]))
	}
	return messages
}

// JobMessage is the wire form of a job.
func JobMessage(job Job) *managev1.TranslationJob {
	message := &managev1.TranslationJob{
		Id:                    job.ID,
		Target:                targetMessage(job.EntityType, job.EntityID),
		TargetLocale:          job.TargetLocale,
		SourceLocale:          job.SourceLocale,
		RequestArtifactDigest: job.RequestArtifactDigest,
		Status:                jobStatusMessage(job.Status),
		OperationId:           job.OperationID,
		RequestedAt:           timestamppb.New(job.RequestedAt),
	}
	message.RequestedByMemberId = job.RequestedByMemberID
	if job.Provider != nil {
		message.Provider = job.Provider
	}
	if job.Model != nil {
		message.Model = job.Model
	}
	if job.StartedAt != nil {
		message.StartedAt = timestamppb.New(*job.StartedAt)
	}
	return message
}

func jobStatusMessage(status JobStatus) managev1.TranslationJobStatus {
	switch status {
	case JobQueued:
		return managev1.TranslationJobStatus_TRANSLATION_JOB_STATUS_QUEUED
	case JobRunning:
		return managev1.TranslationJobStatus_TRANSLATION_JOB_STATUS_RUNNING
	default:
		return managev1.TranslationJobStatus_TRANSLATION_JOB_STATUS_UNSPECIFIED
	}
}

// EntryMessages is the wire form of locale entries.
func EntryMessages(entries []Entry) []*managev1.TranslationEntry {
	messages := make([]*managev1.TranslationEntry, 0, len(entries))
	for _, entry := range entries {
		messages = append(messages, EntryMessage(entry))
	}
	return messages
}

// EntryMessage is the wire form of a locale entry.
func EntryMessage(entry Entry) *managev1.TranslationEntry {
	message := &managev1.TranslationEntry{
		Target:      targetMessage(entry.EntityType, entry.EntityID),
		Locale:      entry.Locale,
		UpdatedAt:   timestamppb.New(entry.UpdatedAt),
		Title:       entry.Title,
		Summary:     entry.Summary,
		ContentHtml: entry.ContentHTML,
		ContentText: entry.ContentText,
		OgAsset:     entry.OgAsset,
	}
	if len(entry.ContentJSON) > 0 {
		message.ContentJson = entry.ContentJSON
	}
	return message
}

func localeMessage(locale Locale) *managev1.TranslationLocale {
	return &managev1.TranslationLocale{
		Code:                      locale.Code,
		DisplayName:               locale.DisplayName,
		Enabled:                   locale.Enabled,
		IsPublic:                  locale.IsPublic,
		Dir:                       locale.Dir,
		MachineTranslationAllowed: locale.MachineTranslationAllowed,
		SortOrder:                 locale.SortOrder,
		FontProfile:               locale.FontProfile,
	}
}

func overviewMessage(overview Overview) *managev1.GetTranslationOverviewResponse {
	response := &managev1.GetTranslationOverviewResponse{
		Stats: &managev1.TranslationOverviewStats{
			TotalLocales:    int32(overview.Stats.TotalLocales),
			EnabledLocales:  int32(overview.Stats.EnabledLocales),
			PublicLocales:   int32(overview.Stats.PublicLocales),
			SourceEntities:  int32(overview.Stats.SourceEntities),
			ActiveJobs:      int32(overview.Stats.ActiveJobs),
			ExistingEntries: int32(overview.Stats.ExistingEntries),
		},
		LocaleHealth: make([]*managev1.TranslationLocaleHealth, 0, len(overview.LocaleHealth)),
		EntityHealth: make([]*managev1.TranslationEntityHealth, 0, len(overview.EntityHealth)),
	}
	for _, health := range overview.LocaleHealth {
		item := &managev1.TranslationLocaleHealth{
			Locale:          localeMessage(health.Locale),
			ExistingEntries: int32(health.ExistingEntries),
			ActiveJobs:      int32(health.ActiveJobs),
		}
		if health.LastTargetUpdateAt != nil {
			item.LastTargetUpdateAt = timestamppb.New(*health.LastTargetUpdateAt)
		}
		response.LocaleHealth = append(response.LocaleHealth, item)
	}
	for _, health := range overview.EntityHealth {
		definition, _ := translation.DefinitionForKind(health.EntityType)
		item := &managev1.TranslationEntityHealth{
			EntityType:      definition.Proto,
			SourceEntities:  int32(health.SourceEntities),
			ExistingEntries: int32(health.ExistingEntries),
			ActiveJobs:      int32(health.ActiveJobs),
		}
		if health.LastSourceUpdateAt != nil {
			item.LastSourceUpdateAt = timestamppb.New(*health.LastSourceUpdateAt)
		}
		response.EntityHealth = append(response.EntityHealth, item)
	}
	return response
}

func settingsMessage(settings translation.RuntimeSettings) *managev1.TranslationSettings {
	if settings.DefaultLocale == "" {
		return nil
	}
	message := &managev1.TranslationSettings{
		DefaultLocale:  settings.DefaultLocale,
		ProtectedTerms: append([]string(nil), settings.ProtectedTerms...),
	}
	if settings.UpdatedAt != nil {
		message.UpdatedAt = timestamppb.New(settings.UpdatedAt.UTC())
	}
	return message
}

func settingsFromMessage(message *managev1.TranslationSettings) *translation.RuntimeSettings {
	if message == nil {
		return nil
	}
	return &translation.RuntimeSettings{
		DefaultLocale:  message.DefaultLocale,
		ProtectedTerms: append([]string(nil), message.ProtectedTerms...),
	}
}

// providerTypeFromMessage is the stored provider type a request names; an
// unspecified or unknown type is empty, which the use cases reject.
func providerTypeFromMessage(providerType managev1.TranslationProviderType) ProviderType {
	switch providerType {
	case managev1.TranslationProviderType_TRANSLATION_PROVIDER_TYPE_LLM:
		return ProviderTypeLLM
	case managev1.TranslationProviderType_TRANSLATION_PROVIDER_TYPE_DEEPL:
		return ProviderTypeDeepL
	default:
		return ""
	}
}

func providerTypeMessage(providerType ProviderType) managev1.TranslationProviderType {
	switch providerType {
	case ProviderTypeLLM:
		return managev1.TranslationProviderType_TRANSLATION_PROVIDER_TYPE_LLM
	case ProviderTypeDeepL:
		return managev1.TranslationProviderType_TRANSLATION_PROVIDER_TYPE_DEEPL
	default:
		return managev1.TranslationProviderType_TRANSLATION_PROVIDER_TYPE_UNSPECIFIED
	}
}

// llmPresetFromMessage is the stored preset a request names: empty when
// unspecified, and a value no preset matches when unknown, so an unknown
// preset neither inherits the stored one nor passes as a preset.
func llmPresetFromMessage(preset managev1.TranslationLLMProviderPreset) LLMPreset {
	switch preset {
	case managev1.TranslationLLMProviderPreset_TRANSLATION_LLM_PROVIDER_PRESET_UNSPECIFIED:
		return ""
	case managev1.TranslationLLMProviderPreset_TRANSLATION_LLM_PROVIDER_PRESET_GEMINI:
		return LLMPresetGemini
	case managev1.TranslationLLMProviderPreset_TRANSLATION_LLM_PROVIDER_PRESET_OPENAI_COMPATIBLE:
		return LLMPresetOpenAICompatible
	default:
		return LLMPreset(preset.String())
	}
}

func llmPresetMessage(preset LLMPreset) managev1.TranslationLLMProviderPreset {
	switch preset {
	case LLMPresetGemini:
		return managev1.TranslationLLMProviderPreset_TRANSLATION_LLM_PROVIDER_PRESET_GEMINI
	case LLMPresetOpenAICompatible:
		return managev1.TranslationLLMProviderPreset_TRANSLATION_LLM_PROVIDER_PRESET_OPENAI_COMPATIBLE
	default:
		return managev1.TranslationLLMProviderPreset_TRANSLATION_LLM_PROVIDER_PRESET_UNSPECIFIED
	}
}

func llmConfigInput(config *managev1.LLMTranslationProviderConfig) *LLMConfigInput {
	if config == nil {
		return nil
	}
	return &LLMConfigInput{
		APIKey:                        config.ApiKey,
		Preset:                        llmPresetFromMessage(config.Preset),
		APIBaseURL:                    config.GetApiBaseUrl(),
		Model:                         config.Model,
		InputTokenPriceUSDPerMillion:  config.InputTokenPriceUsdPerMillion,
		OutputTokenPriceUSDPerMillion: config.OutputTokenPriceUsdPerMillion,
		MaxContextTokens:              config.MaxContextTokens,
		MaxOutputTokens:               config.MaxOutputTokens,
		SupportsJSONMode:              config.SupportsJsonMode,
		Temperature:                   config.Temperature,
	}
}

func deepLConfigInput(config *managev1.DeepLTranslationProviderConfig) *DeepLConfigInput {
	if config == nil {
		return nil
	}
	return &DeepLConfigInput{APIKey: config.ApiKey, APIBaseURL: config.GetApiBaseUrl()}
}

// providerMessage is the wire form of a provider without its secrets. A
// stored config that does not decode is logged and left out.
func providerMessage(ctx context.Context, provider *Provider) *managev1.TranslationProvider {
	message := &managev1.TranslationProvider{
		Id:        provider.ID,
		Name:      provider.Name,
		Type:      providerTypeMessage(provider.Type),
		IsActive:  provider.IsActive,
		Priority:  int32(provider.Priority),
		CreatedAt: timestamppb.New(provider.CreatedAt.UTC()),
		UpdatedAt: timestamppb.New(provider.UpdatedAt.UTC()),
	}
	switch provider.Type {
	case ProviderTypeLLM:
		cfg, err := provider.LLMConfig()
		if err != nil {
			logUndecodableProviderConfig(ctx, provider.ID, err)
			break
		}
		out := &managev1.LLMTranslationProviderConfig{
			Preset:                        llmPresetMessage(cfg.Preset),
			Model:                         cfg.Model,
			SupportsJsonMode:              cfg.SupportsJSONMode,
			InputTokenPriceUsdPerMillion:  cfg.InputTokenPriceUSDPerMillion,
			OutputTokenPriceUsdPerMillion: cfg.OutputTokenPriceUSDPerMillion,
			MaxContextTokens:              cfg.MaxContextTokens,
			MaxOutputTokens:               cfg.MaxOutputTokens,
			Temperature:                   cfg.Temperature,
		}
		if cfg.APIBaseURL != "" {
			out.ApiBaseUrl = &cfg.APIBaseURL
		}
		message.Config = &managev1.TranslationProvider_LlmConfig{LlmConfig: out}
	case ProviderTypeDeepL:
		cfg, err := provider.DeepLConfig()
		if err != nil {
			logUndecodableProviderConfig(ctx, provider.ID, err)
			break
		}
		out := &managev1.DeepLTranslationProviderConfig{}
		if cfg.APIBaseURL != "" {
			out.ApiBaseUrl = &cfg.APIBaseURL
		}
		message.Config = &managev1.TranslationProvider_DeeplConfig{DeeplConfig: out}
	}
	return message
}

func logUndecodableProviderConfig(ctx context.Context, providerID string, err error) {
	slog.ErrorContext(ctx, "Stored translation provider config decode failed; returning provider without config", "domain", "translation", "event", "translation.provider.config_decode_failed", "outcome", "failed", failurecause.Attr(err), "provider_id", providerID)
}
