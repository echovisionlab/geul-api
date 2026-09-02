package application

import (
	"context"
	"slices"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dependencycheck"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/lib/pq"
)

var translationJobSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"requested_at":  "requested_at",
		"updated_at":    "updated_at",
		"target_locale": "target_locale",
		"status":        "status",
	},
	DefaultSort: "updated_at DESC, id ASC",
}

type translationServicePublisher interface {
	PublishTranslationGenerate(ctx context.Context, job *managev1.TranslationGenerateEvent) error
	PublishTranslationLifecycle(ctx context.Context, event *managev1.TranslationLifecycleEvent) error
	PublishContentUpdated(ctx context.Context, event *managev1.ContentUpdatedEvent) error
}

type transactionalTranslationServicePublisher interface {
	EnqueueTranslationGenerateWithDB(context.Context, *gorm.DB, *managev1.TranslationGenerateEvent) error
}

func enqueueTranslationGenerateWithDB(
	ctx context.Context,
	publisher translationServicePublisher,
	tx *gorm.DB,
	job *managev1.TranslationGenerateEvent,
) error {
	if transactionalPublisher, ok := publisher.(transactionalTranslationServicePublisher); ok {
		return transactionalPublisher.EnqueueTranslationGenerateWithDB(ctx, tx, job)
	}
	return publisher.PublishTranslationGenerate(ctx, job)
}

type TranslationService struct {
	managev1connect.UnimplementedTranslationServiceHandler
	db                 *gorm.DB
	publisher          translationServicePublisher
	spiceDB            *auth.SpiceDBClient
	cdnDomain          string
	now                func() time.Time
	auditWriter        domainaudit.Appender
	contentBlocks      *contentblock.Store
	ogPlanner          *og.Planner
	ogRefresher        *og.Refresher
	metrics            translationMetrics
	domains            DomainRegistry
	xliffFiles         TranslationXLIFFFiles
	interchangeDomains TranslationInterchangeDomains
}

type TranslationServiceOption func(*TranslationService)

func WithTranslationServiceContentBlockStore(store *contentblock.Store) TranslationServiceOption {
	return func(service *TranslationService) { service.contentBlocks = store }
}

func WithTranslationServiceDomainRegistry(registry DomainRegistry) TranslationServiceOption {
	return func(service *TranslationService) { service.domains = registry }
}

// NewAuditedTranslationService creates a TranslationService whose durable
// provider and source-locale mutations require an in-transaction Domain Audit
// append.
func NewAuditedTranslationService(
	db *gorm.DB,
	publisher translationServicePublisher,
	cdnDomain string,
	auditWriter domainaudit.Appender,
	spiceDB *auth.SpiceDBClient,
	ogPlanner *og.Planner,
	ogRefresher *og.Refresher,
	options ...TranslationServiceOption,
) *TranslationService {
	if auditWriter == nil {
		panic("translation provider audit writer is required")
	}
	service := NewTranslationService(db, publisher, cdnDomain, spiceDB, ogPlanner, ogRefresher, options...)
	service.auditWriter = auditWriter
	return service
}

func NewTranslationService(
	db *gorm.DB,
	publisher translationServicePublisher,
	cdnDomain string,
	spiceDB *auth.SpiceDBClient,
	ogPlanner *og.Planner,
	ogRefresher *og.Refresher,
	options ...TranslationServiceOption,
) *TranslationService {
	dependencycheck.New("TranslationService").
		RequireNotNil(db, "db").
		RequireNotNil(publisher, "publisher").
		Validate()

	if spiceDB == nil {
		panic("translation SpiceDB client is required")
	}
	if ogPlanner == nil {
		panic("translation OG planner is required")
	}
	if ogRefresher == nil {
		panic("translation OG refresher is required")
	}
	service := &TranslationService{
		db:          db,
		publisher:   publisher,
		spiceDB:     spiceDB,
		cdnDomain:   cdnDomain,
		now:         time.Now,
		ogPlanner:   ogPlanner,
		ogRefresher: ogRefresher,
		metrics:     newTranslationMetrics(),
	}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service
}

func (s *TranslationService) ListTranslationLocales(
	ctx context.Context,
	_ *connect.Request[managev1.ListTranslationLocalesRequest],
) (*connect.Response[managev1.ListTranslationLocalesResponse], error) {
	user := auth.GetUser(ctx)
	isAdmin := false
	if user != nil && user.Authenticated {
		var err error
		isAdmin, err = checkSpiceDBAdmin(ctx, user, s.spiceDB)
		if err != nil {
			return nil, errs.DependencyUnavailable("SpiceDB")
		}
	}
	if !isAdmin && (user == nil || !user.Authenticated) {
		return nil, errs.AuthenticationRequired()
	}

	catalog := localization.NewCatalog(s.db)
	var locales []localization.RuntimeLocale
	var err error
	if isAdmin {
		locales, err = catalog.All(ctx)
	} else {
		locales, err = catalog.Enabled(ctx)
	}
	if err != nil {
		return nil, errs.Internal(err)
	}

	resp := &managev1.ListTranslationLocalesResponse{
		Locales: make([]*managev1.TranslationLocale, 0, len(locales)),
	}
	for _, locale := range locales {
		resp.Locales = append(resp.Locales, toProtoTranslationLocale(locale))
	}
	return connect.NewResponse(resp), nil
}

func (s *TranslationService) GetTranslationOverview(
	ctx context.Context,
	_ *connect.Request[managev1.GetTranslationOverviewRequest],
) (*connect.Response[managev1.GetTranslationOverviewResponse], error) {
	if err := requireTranslationAdmin(ctx, s.spiceDB); err != nil {
		return nil, err
	}

	stats, err := s.getTranslationOverviewStats(ctx)
	if err != nil {
		return nil, err
	}
	localeHealth, err := s.listTranslationLocaleHealth(ctx)
	if err != nil {
		return nil, err
	}
	entityHealth, err := s.listTranslationEntityHealth(ctx)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&managev1.GetTranslationOverviewResponse{
		Stats:        stats,
		LocaleHealth: localeHealth,
		EntityHealth: entityHealth,
	}), nil
}

func (s *TranslationService) GetTranslationSettings(
	ctx context.Context,
	_ *connect.Request[managev1.GetTranslationSettingsRequest],
) (*connect.Response[managev1.GetTranslationSettingsResponse], error) {
	can, canErr := policyv1.TranslationSettings.View()
	if err := s.requireTranslationPlatformCan(ctx, can, canErr); err != nil {
		return nil, err
	}

	settings, err := loadTranslationRuntimeSettings(ctx, s.db)
	if err != nil {
		return nil, errs.Internal(err)
	}
	generationEnabled, err := hasAvailableTranslationProvider(ctx, s.db)
	if err != nil {
		return nil, errs.Internal(err)
	}

	resp := &managev1.GetTranslationSettingsResponse{
		Settings:          toProtoTranslationSettings(settings),
		GenerationEnabled: generationEnabled,
	}
	if !generationEnabled {
		reason := translationProviderUnavailableMessage
		resp.GenerationDisabledReason = &reason
	}
	return connect.NewResponse(resp), nil
}

func (s *TranslationService) UpdateTranslationSettings(
	ctx context.Context,
	req *connect.Request[managev1.UpdateTranslationSettingsRequest],
) (*connect.Response[managev1.UpdateTranslationSettingsResponse], error) {
	settingsCan, err := policyv1.TranslationSettings.Update()
	if err != nil {
		return nil, errs.Internal(err)
	}
	updated, err := s.updateTranslationRuntimeSettings(ctx, settingsCan, req.Msg.Settings)
	if err != nil {
		return nil, errs.Wrap(err)
	}

	return connect.NewResponse(&managev1.UpdateTranslationSettingsResponse{
		Settings: toProtoTranslationSettings(updated),
	}), nil
}

func requireTranslationAdmin(ctx context.Context, spiceDB *auth.SpiceDBClient) error {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated {
		return errs.AuthenticationRequired()
	}
	isAdmin, err := checkSpiceDBAdmin(ctx, user, spiceDB)
	if err != nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	if !isAdmin {
		return errs.AdminRequired()
	}
	return nil
}

func (s *TranslationService) updateTranslationRuntimeSettings(
	ctx context.Context,
	settingsCan policyv1.Can,
	requestedSettings *managev1.TranslationSettings,
) (translationRuntimeSettings, error) {
	var updated translationRuntimeSettings
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row model.TranslationSettings
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, "id = 1").Error; err != nil {
			return err
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, settingsCan); err != nil {
			return err
		}
		requested, err := translationRuntimeSettingsFromProto(requestedSettings)
		if err != nil {
			return errs.InvalidArgument("settings", err.Error())
		}
		current, err := normalizeTranslationRuntimeSettings(translationRuntimeSettings{
			DefaultLocale:  row.DefaultLocale,
			ProtectedTerms: append([]string(nil), row.ProtectedTerms...),
			UpdatedAt:      &row.UpdatedAt,
		})
		if err != nil {
			return err
		}
		fields := translationRuntimeSettingsChangedFields(current, requested)
		if len(fields) == 0 {
			updated = current
			return nil
		}
		now := time.Now().UTC()
		if err := tx.Model(&row).Updates(map[string]any{
			"default_locale":  requested.DefaultLocale,
			"protected_terms": pq.Array(requested.ProtectedTerms),
			"updated_at":      now,
		}).Error; err != nil {
			return err
		}
		if s.auditWriter != nil {
			if err := domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditTranslationSettingsUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewTranslationSettingsUpdatedAuditRecord(metadata, fields)
			}); err != nil {
				return err
			}
		}
		requested.UpdatedAt = &now
		updated = requested
		return nil
	})
	return updated, err
}

func translationRuntimeSettingsChangedFields(current, requested translationRuntimeSettings) []string {
	fields := make([]string, 0, 2)
	if current.DefaultLocale != requested.DefaultLocale {
		fields = append(fields, "default_locale")
	}
	if !slices.Equal(current.ProtectedTerms, requested.ProtectedTerms) {
		fields = append(fields, "protected_terms")
	}
	return fields
}

func (s *TranslationService) ListTranslationJobs(
	ctx context.Context,
	req *connect.Request[managev1.ListTranslationJobsRequest],
) (*connect.Response[managev1.ListTranslationJobsResponse], error) {
	if err := s.authorizeTranslationJobList(ctx, req.Msg.Filters); err != nil {
		return nil, err
	}

	var jobs []model.TranslationJob
	var total int64

	query := s.db.WithContext(ctx).Model(&model.TranslationJob{})
	query, err := applyTranslationJobFilters(query, req.Msg.Filters)
	if err != nil {
		return nil, err
	}
	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}

	limit, offset := queryutil.NormalizePaginationParams(req.Msg.Pagination)
	query, err = translationJobSortConfig.ApplySort(query, req.Msg.Sorts)
	if err != nil {
		return nil, err
	}
	if err := query.Limit(int(limit)).Offset(int(offset)).Find(&jobs).Error; err != nil {
		return nil, errs.Internal(err)
	}

	resp := &managev1.ListTranslationJobsResponse{
		Jobs:       make([]*managev1.TranslationJob, 0, len(jobs)),
		Pagination: &commonv1.PaginationResponse{Total: int32(total), Limit: limit, Offset: offset, HasMore: offset+limit < int32(total)},
	}
	for _, job := range jobs {
		if err := validateTranslationJobRequester(&job); err != nil {
			return nil, errs.Internal(err)
		}
		resp.Jobs = append(resp.Jobs, toProtoTranslationJob(job))
	}
	return connect.NewResponse(resp), nil
}
