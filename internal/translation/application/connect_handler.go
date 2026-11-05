package application

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/mediaasset"

	"github.com/echovisionlab/geul-api/internal/mediaauth"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// ConnectHandler serves the Translation Connect RPCs. Each method translates
// the request into a use-case command or query, and the result into the
// response; errs.Wrap maps use-case errors to Connect codes.
type ConnectHandler struct {
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
		Settings: settingsMessage(view.Settings),
	}, err)
}

func (h *ConnectHandler) UpdateTranslationSettings(ctx context.Context, req *connect.Request[managev1.UpdateTranslationSettingsRequest]) (*connect.Response[managev1.UpdateTranslationSettingsResponse], error) {
	settings, err := h.translations.UpdateTranslationSettings(ctx, UpdateTranslationSettingsCommand{
		Settings: settingsFromMessage(req.Msg.Settings), UpdateMask: req.Msg.UpdateMask,
	})
	return respond(&managev1.UpdateTranslationSettingsResponse{Settings: settingsMessage(settings)}, err)
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
		Artifact: mediaauth.ReferenceProto(exported.Artifact), SourceLocale: exported.SourceLocale, TargetLocale: exported.TargetLocale,
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
		OgAsset:     mediaasset.ReferenceProto(entry.OgAsset),
	}
	if len(entry.ContentJSON) > 0 {
		message.ContentJson = entry.ContentJSON
	}
	return message
}

func localeMessage(locale Locale) *managev1.TranslationLocale {
	return &managev1.TranslationLocale{
		Code:        locale.Code,
		DisplayName: locale.DisplayName,
		Enabled:     locale.Enabled,
		IsPublic:    locale.IsPublic,
		Dir:         locale.Dir,
		SortOrder:   locale.SortOrder,
		FontProfile: locale.FontProfile,
	}
}

func overviewMessage(overview Overview) *managev1.GetTranslationOverviewResponse {
	response := &managev1.GetTranslationOverviewResponse{
		Stats: &managev1.TranslationOverviewStats{
			TotalLocales:    int32(overview.Stats.TotalLocales),
			EnabledLocales:  int32(overview.Stats.EnabledLocales),
			PublicLocales:   int32(overview.Stats.PublicLocales),
			SourceEntities:  int32(overview.Stats.SourceEntities),
			ExistingEntries: int32(overview.Stats.ExistingEntries),
		},
		LocaleHealth: make([]*managev1.TranslationLocaleHealth, 0, len(overview.LocaleHealth)),
		EntityHealth: make([]*managev1.TranslationEntityHealth, 0, len(overview.EntityHealth)),
	}
	for _, health := range overview.LocaleHealth {
		item := &managev1.TranslationLocaleHealth{
			Locale:          localeMessage(health.Locale),
			ExistingEntries: int32(health.ExistingEntries),
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
		DefaultLocale: settings.DefaultLocale,
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
		DefaultLocale: message.DefaultLocale,
	}
}
