package application

import (
	"context"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
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
	return respond(&managev1.ListTranslationLocalesResponse{Locales: locales}, err)
}

func (h *ConnectHandler) GetTranslationOverview(ctx context.Context, _ *connect.Request[managev1.GetTranslationOverviewRequest]) (*connect.Response[managev1.GetTranslationOverviewResponse], error) {
	overview, err := h.translations.GetTranslationOverview(ctx)
	return respond(&managev1.GetTranslationOverviewResponse{
		Stats: overview.Stats, LocaleHealth: overview.LocaleHealth, EntityHealth: overview.EntityHealth,
	}, err)
}

func (h *ConnectHandler) GetTranslationSettings(ctx context.Context, _ *connect.Request[managev1.GetTranslationSettingsRequest]) (*connect.Response[managev1.GetTranslationSettingsResponse], error) {
	view, err := h.translations.GetTranslationSettings(ctx)
	return respond(&managev1.GetTranslationSettingsResponse{
		Settings: view.Settings, GenerationEnabled: view.GenerationEnabled, GenerationDisabledReason: view.GenerationDisabledReason,
	}, err)
}

func (h *ConnectHandler) UpdateTranslationSettings(ctx context.Context, req *connect.Request[managev1.UpdateTranslationSettingsRequest]) (*connect.Response[managev1.UpdateTranslationSettingsResponse], error) {
	settings, err := h.translations.UpdateTranslationSettings(ctx, UpdateTranslationSettingsCommand{
		Settings: req.Msg.Settings, BaseSettings: req.Msg.BaseSettings, UpdateMask: req.Msg.UpdateMask,
	})
	return respond(&managev1.UpdateTranslationSettingsResponse{Settings: settings}, err)
}

func (h *ConnectHandler) ListTranslationJobs(ctx context.Context, req *connect.Request[managev1.ListTranslationJobsRequest]) (*connect.Response[managev1.ListTranslationJobsResponse], error) {
	page, err := h.translations.ListTranslationJobs(ctx, ListTranslationJobsQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(),
		Filters: req.Msg.Filters, Sorts: req.Msg.Sorts,
	})
	return respond(&managev1.ListTranslationJobsResponse{Jobs: page.Jobs, Pagination: page.Pagination}, err)
}

func (h *ConnectHandler) ListEntityTranslations(ctx context.Context, req *connect.Request[managev1.ListEntityTranslationsRequest]) (*connect.Response[managev1.ListEntityTranslationsResponse], error) {
	translations, err := h.translations.ListEntityTranslations(ctx, ListEntityTranslationsQuery{Target: req.Msg.Target})
	return respond(&managev1.ListEntityTranslationsResponse{Entries: translations.Entries, SourceLocale: translations.SourceLocale}, err)
}

func (h *ConnectHandler) GetEntityTranslation(ctx context.Context, req *connect.Request[managev1.GetEntityTranslationRequest]) (*connect.Response[managev1.GetEntityTranslationResponse], error) {
	entry, err := h.translations.GetEntityTranslation(ctx, GetEntityTranslationQuery{Target: req.Msg.Target, Locale: req.Msg.Locale})
	return respond(&managev1.GetEntityTranslationResponse{Entry: entry}, err)
}

func (h *ConnectHandler) SetEntitySourceLocale(ctx context.Context, req *connect.Request[managev1.SetEntitySourceLocaleRequest]) (*connect.Response[managev1.SetEntitySourceLocaleResponse], error) {
	change, err := h.translations.SetEntitySourceLocale(ctx, SetEntitySourceLocaleCommand{
		Target: req.Msg.Target, SourceLocale: req.Msg.SourceLocale, ExpectedDocumentRevision: req.Msg.GetExpectedDocumentRevision(),
	})
	return respond(&managev1.SetEntitySourceLocaleResponse{
		DocumentRevision: change.DocumentRevision, Changed: change.Changed,
		SourceChanged: change.Changed, ChangedLocales: change.ChangedLocales,
	}, err)
}

func (h *ConnectHandler) RegenerateEntityTranslations(ctx context.Context, req *connect.Request[managev1.RegenerateEntityTranslationsRequest]) (*connect.Response[managev1.RegenerateEntityTranslationsResponse], error) {
	jobs, err := h.translations.RegenerateEntityTranslations(ctx, RegenerateEntityTranslationsCommand{
		Target: req.Msg.Target, Locales: req.Msg.GetLocales(),
	})
	return respond(&managev1.RegenerateEntityTranslationsResponse{Jobs: jobs}, err)
}

func (h *ConnectHandler) CancelTranslationJob(ctx context.Context, req *connect.Request[managev1.CancelTranslationJobRequest]) (*connect.Response[managev1.CancelTranslationJobResponse], error) {
	err := h.translations.CancelTranslationJob(ctx, CancelTranslationJobCommand{JobID: req.Msg.GetJobId()})
	return respond(&managev1.CancelTranslationJobResponse{}, err)
}

func (h *ConnectHandler) ListTranslationProviders(ctx context.Context, req *connect.Request[managev1.ListTranslationProvidersRequest]) (*connect.Response[managev1.ListTranslationProvidersResponse], error) {
	page, err := h.translations.ListTranslationProviders(ctx, ListTranslationProvidersQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(), Filters: req.Msg.Filters,
	})
	return respond(&managev1.ListTranslationProvidersResponse{Providers: page.Providers, Pagination: page.Pagination}, err)
}

func (h *ConnectHandler) CreateTranslationProvider(ctx context.Context, req *connect.Request[managev1.CreateTranslationProviderRequest]) (*connect.Response[managev1.CreateTranslationProviderResponse], error) {
	provider, err := h.translations.CreateTranslationProvider(ctx, CreateTranslationProviderCommand{
		Name: req.Msg.Name, Type: req.Msg.Type, IsActive: req.Msg.IsActive, Priority: req.Msg.GetPriority(),
		LLMConfig: req.Msg.GetLlmConfig(), DeepLConfig: req.Msg.GetDeeplConfig(),
	})
	return respond(&managev1.CreateTranslationProviderResponse{Provider: provider}, err)
}

func (h *ConnectHandler) UpdateTranslationProvider(ctx context.Context, req *connect.Request[managev1.UpdateTranslationProviderRequest]) (*connect.Response[managev1.UpdateTranslationProviderResponse], error) {
	provider, err := h.translations.UpdateTranslationProvider(ctx, UpdateTranslationProviderCommand{
		ID: req.Msg.Id, Name: req.Msg.Name, Type: req.Msg.Type, IsActive: req.Msg.IsActive, Priority: req.Msg.Priority,
		LLMConfig: req.Msg.GetLlmConfig(), DeepLConfig: req.Msg.GetDeeplConfig(),
	})
	return respond(&managev1.UpdateTranslationProviderResponse{Provider: provider}, err)
}

func (h *ConnectHandler) DeleteTranslationProvider(ctx context.Context, req *connect.Request[managev1.DeleteTranslationProviderRequest]) (*connect.Response[managev1.DeleteTranslationProviderResponse], error) {
	err := h.translations.DeleteTranslationProvider(ctx, DeleteTranslationProviderCommand{ID: req.Msg.Id})
	return respond(&managev1.DeleteTranslationProviderResponse{}, err)
}

func (h *ConnectHandler) ExportEntityTranslationXLIFF(ctx context.Context, req *connect.Request[managev1.ExportEntityTranslationXLIFFRequest]) (*connect.Response[managev1.ExportEntityTranslationXLIFFResponse], error) {
	return respond(h.translations.ExportEntityTranslationXLIFF(ctx, ExportTranslationXLIFFCommand{
		Target: req.Msg.Target, TargetLocale: req.Msg.TargetLocale, Mode: req.Msg.Mode, UnitHandles: req.Msg.UnitHandles,
	}))
}

func (h *ConnectHandler) ImportEntityTranslationXLIFF(ctx context.Context, req *connect.Request[managev1.ImportEntityTranslationXLIFFRequest]) (*connect.Response[managev1.ImportEntityTranslationXLIFFResponse], error) {
	return respond(h.translations.ImportEntityTranslationXLIFF(ctx, ImportTranslationXLIFFCommand{
		Target: req.Msg.Target, TargetLocale: req.Msg.TargetLocale, Mode: req.Msg.Mode,
		FileID: req.Msg.FileId, ExpectedTargetRevision: req.Msg.ExpectedTargetRevision,
	}))
}
