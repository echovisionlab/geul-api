package application

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
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

// Publisher carries the commands and signals of the Translation use cases.
type Publisher interface {
	// The job and its durable queue command must become visible together.
	EnqueueTranslationGenerateWithDB(context.Context, *gorm.DB, *managev1.TranslationGenerateEvent) error
	PublishTranslationLifecycle(ctx context.Context, event *managev1.TranslationLifecycleEvent) error
	// content.updated is scheduled only in the owning mutation transaction.
	contentsignal.Publisher
}

// Deps are the collaborators of the Translation use cases. Every field except
// CDNDomain is required; NewService rejects a missing one.
type Deps struct {
	DB            *gorm.DB
	SpiceDB       *auth.SpiceDBClient
	Audit         domainaudit.Appender
	Publisher     Publisher
	ContentBlocks *contentblock.Store
	OGPlanner     *ogpostgres.Planner
	OGRefresher   *ogpostgres.Refresher
	// Domains reaches each owning domain's translation port.
	Domains DomainRegistry
	// XLIFFFiles stores XLIFF exports and reads verified XLIFF uploads.
	XLIFFFiles TranslationXLIFFFiles
	// InterchangeDomains applies imported XLIFF targets in the owning domain.
	InterchangeDomains TranslationInterchangeDomains
	// CDNDomain prefixes OG asset URLs; empty keeps them relative.
	CDNDomain string
}

func (d Deps) validate() error {
	return requireDeps("translation", map[string]bool{
		"DB": d.DB == nil, "SpiceDB": d.SpiceDB == nil, "Audit": d.Audit == nil,
		"Publisher": d.Publisher == nil, "ContentBlocks": d.ContentBlocks == nil,
		"OGPlanner": d.OGPlanner == nil, "OGRefresher": d.OGRefresher == nil,
		"Domains": d.Domains == nil, "XLIFFFiles": d.XLIFFFiles == nil,
		"InterchangeDomains": d.InterchangeDomains == nil,
	})
}

func requireDeps(name string, absent map[string]bool) error {
	var missing []string
	for field, isAbsent := range absent {
		if isAbsent {
			missing = append(missing, field)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New(name + " dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the Translation use cases: locales, settings, providers,
// entity translations, jobs and XLIFF interchange. Connect and MCP adapters
// translate their requests into its commands and queries.
type Service struct {
	db                 *gorm.DB
	publisher          Publisher
	spiceDB            *auth.SpiceDBClient
	cdnDomain          string
	now                func() time.Time
	contentBlocks      *contentblock.Store
	ogPlanner          *ogpostgres.Planner
	ogRefresher        *ogpostgres.Refresher
	metrics            translationMetrics
	domains            DomainRegistry
	xliffFiles         TranslationXLIFFFiles
	interchangeDomains TranslationInterchangeDomains
	mutations          *mutation.Runner
}

// NewService returns the Translation use cases over deps.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewRunner(deps.DB, deps.SpiceDB, identitystate.PostgresFence{}, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{
		db: deps.DB, publisher: deps.Publisher, spiceDB: deps.SpiceDB, cdnDomain: deps.CDNDomain,
		now: dbtime.Now, contentBlocks: deps.ContentBlocks, ogPlanner: deps.OGPlanner,
		ogRefresher: deps.OGRefresher, metrics: newTranslationMetrics(), domains: deps.Domains,
		xliffFiles: deps.XLIFFFiles, interchangeDomains: deps.InterchangeDomains, mutations: mutations,
	}, nil
}

// ListTranslationLocales lists every runtime locale to an administrator and
// the enabled ones to any other authenticated principal.
func (s *Service) ListTranslationLocales(ctx context.Context) ([]*managev1.TranslationLocale, error) {
	user := auth.GetUser(ctx)
	isAdmin := false
	if user != nil && user.Authenticated {
		var err error
		isAdmin, err = checkSpiceDBAdmin(ctx, user, s.spiceDB)
		if err != nil {
			return nil, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
		}
	}
	if !isAdmin && (user == nil || !user.Authenticated) {
		return nil, domainerr.AuthenticationRequired()
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
		return nil, err
	}

	result := make([]*managev1.TranslationLocale, 0, len(locales))
	for _, locale := range locales {
		result = append(result, toProtoTranslationLocale(locale))
	}
	return result, nil
}

// Overview is the administrator's translation coverage summary.
type Overview struct {
	Stats        *managev1.TranslationOverviewStats
	LocaleHealth []*managev1.TranslationLocaleHealth
	EntityHealth []*managev1.TranslationEntityHealth
}

// GetTranslationOverview summarizes locale and entity translation coverage.
func (s *Service) GetTranslationOverview(ctx context.Context) (Overview, error) {
	if err := requireTranslationAdmin(ctx, s.spiceDB); err != nil {
		return Overview{}, err
	}
	stats, err := s.getTranslationOverviewStats(ctx)
	if err != nil {
		return Overview{}, err
	}
	localeHealth, err := s.listTranslationLocaleHealth(ctx)
	if err != nil {
		return Overview{}, err
	}
	entityHealth, err := s.listTranslationEntityHealth(ctx)
	if err != nil {
		return Overview{}, err
	}
	return Overview{Stats: stats, LocaleHealth: localeHealth, EntityHealth: entityHealth}, nil
}

// SettingsView is the runtime translation settings and whether a provider
// can generate translations now.
type SettingsView struct {
	Settings                 *managev1.TranslationSettings
	GenerationEnabled        bool
	GenerationDisabledReason *string
}

// GetTranslationSettings reads the runtime translation settings.
func (s *Service) GetTranslationSettings(ctx context.Context) (SettingsView, error) {
	can, canErr := policyv1.TranslationSettings.View()
	if err := s.requireTranslationPlatformCan(ctx, can, canErr); err != nil {
		return SettingsView{}, err
	}
	settings, err := loadTranslationRuntimeSettings(ctx, s.db)
	if err != nil {
		return SettingsView{}, err
	}
	generationEnabled, err := hasAvailableTranslationProvider(ctx, s.db)
	if err != nil {
		return SettingsView{}, err
	}
	view := SettingsView{Settings: toProtoTranslationSettings(settings), GenerationEnabled: generationEnabled}
	if !generationEnabled {
		reason := translationProviderUnavailableMessage
		view.GenerationDisabledReason = &reason
	}
	return view, nil
}

// UpdateTranslationSettingsCommand patches the runtime translation settings.
// UpdateMask names the fields to change; BaseSettings are the settings the
// caller last read, from which a protected-terms patch is derived.
type UpdateTranslationSettingsCommand struct {
	Settings     *managev1.TranslationSettings
	BaseSettings *managev1.TranslationSettings
	UpdateMask   *fieldmaskpb.FieldMask
}

type settingsUpdate struct {
	settings translationRuntimeSettings
	fields   []string
}

// UpdateTranslationSettings applies the masked settings fields.
func (s *Service) UpdateTranslationSettings(ctx context.Context, command UpdateTranslationSettingsCommand) (*managev1.TranslationSettings, error) {
	var row model.TranslationSettings
	updated, err := mutation.Run(ctx, s.mutations, mutation.Spec[settingsUpdate]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			return tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&row, "id = 1").Error
		},
		Access: mutation.FreshAdminCan(policyv1.TranslationSettings.Update()),
		Change: func(_ context.Context, tx *gorm.DB) (settingsUpdate, error) {
			return updateTranslationRuntimeSettings(tx, &row, command)
		},
		Audit: func(update settingsUpdate) mutation.Audit {
			if len(update.fields) == 0 {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditTranslationSettingsUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewTranslationSettingsUpdatedAuditRecord(metadata, update.fields)
			})
		},
	})
	if err != nil {
		return nil, err
	}
	return toProtoTranslationSettings(updated.settings), nil
}

func updateTranslationRuntimeSettings(tx *gorm.DB, row *model.TranslationSettings, command UpdateTranslationSettingsCommand) (settingsUpdate, error) {
	current, err := normalizeTranslationRuntimeSettings(translationRuntimeSettings{
		DefaultLocale:  row.DefaultLocale,
		ProtectedTerms: append([]string(nil), row.ProtectedTerms...),
		UpdatedAt:      &row.UpdatedAt,
	})
	if err != nil {
		return settingsUpdate{}, err
	}
	requested, err := applyTranslationRuntimeSettingsUpdate(current, command)
	if err != nil {
		return settingsUpdate{}, domainerr.InvalidField("settings", err.Error())
	}
	fields := translationRuntimeSettingsChangedFields(current, requested)
	if len(fields) == 0 {
		return settingsUpdate{settings: current}, nil
	}
	updates := map[string]any{"updated_at": tx.NowFunc().UTC()}
	if current.DefaultLocale != requested.DefaultLocale {
		updates["default_locale"] = requested.DefaultLocale
	}
	if !slices.Equal(current.ProtectedTerms, requested.ProtectedTerms) {
		updates["protected_terms"] = pq.Array(requested.ProtectedTerms)
	}
	if err := tx.Model(row).Clauses(clause.Returning{Columns: []clause.Column{{Name: "updated_at"}}}).Updates(updates).Error; err != nil {
		return settingsUpdate{}, err
	}
	requested.UpdatedAt = &row.UpdatedAt
	return settingsUpdate{settings: requested, fields: fields}, nil
}

func requireTranslationAdmin(ctx context.Context, spiceDB *auth.SpiceDBClient) error {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated {
		return domainerr.AuthenticationRequired()
	}
	isAdmin, err := checkSpiceDBAdmin(ctx, user, spiceDB)
	if err != nil {
		return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if !isAdmin {
		return domainerr.AdminRequired()
	}
	return nil
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

// ListTranslationJobsQuery lists in-flight translation jobs. An
// administrator reads every job; another principal must filter to exactly
// one entity it may read.
type ListTranslationJobsQuery struct {
	// Limit defaults to 20 and is capped at 100.
	Limit   int32
	Offset  int32
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
}

// JobPage is one page of translation jobs.
type JobPage struct {
	Jobs       []*managev1.TranslationJob
	Pagination *commonv1.PaginationResponse
}

// ListTranslationJobs lists translation jobs.
func (s *Service) ListTranslationJobs(ctx context.Context, query ListTranslationJobsQuery) (JobPage, error) {
	if err := s.authorizeTranslationJobList(ctx, query.Filters); err != nil {
		return JobPage{}, err
	}

	var jobs []model.TranslationJob
	var total int64

	db := s.db.WithContext(ctx).Model(&model.TranslationJob{})
	db, err := applyTranslationJobFilters(db, query.Filters)
	if err != nil {
		return JobPage{}, err
	}
	if err := db.Count(&total).Error; err != nil {
		return JobPage{}, err
	}

	pagination := queryutil.ExtractPaginationRaw(query.Limit, query.Offset, queryutil.DefaultPaginationConfig)
	db, err = applyTranslationJobSort(db, query.Sorts)
	if err != nil {
		return JobPage{}, err
	}
	if err := pagination.Apply(db).Find(&jobs).Error; err != nil {
		return JobPage{}, err
	}

	page := JobPage{
		Jobs:       make([]*managev1.TranslationJob, 0, len(jobs)),
		Pagination: pagination.BuildResponse(total),
	}
	for _, job := range jobs {
		if err := validateTranslationJobRequester(&job); err != nil {
			return JobPage{}, err
		}
		page.Jobs = append(page.Jobs, toProtoTranslationJob(job))
	}
	return page, nil
}

func applyTranslationJobSort(query *gorm.DB, sorts []*commonv1.SortSpec) (*gorm.DB, error) {
	query, err := translationJobSortConfig.ApplySort(query, sorts)
	if err != nil {
		return nil, err
	}
	if len(sorts) != 0 {
		// Explicit sort fields can tie. Preserve a unique order across offset
		// pages just as the default sort does.
		query = query.Order("id ASC")
	}
	return query, nil
}
