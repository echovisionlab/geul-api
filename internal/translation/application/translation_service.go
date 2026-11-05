package application

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/translation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// Deps are the collaborators of the Translation use cases; all are required.
type Deps struct {
	Units   mutation.UnitOfWork[Store]
	SpiceDB mutation.Authorizer
	Fence   identitystate.Fence
	Audit   domainaudit.Appender
	// Plans builds extraction plans by each owning domain's rules.
	Plans DomainPlans
	// XLIFFFiles stores XLIFF exports and reads verified XLIFF uploads.
	XLIFFFiles TranslationXLIFFFiles
}

func (d Deps) validate() error {
	return requireDeps("translation", map[string]bool{
		"Units": d.Units == nil, "SpiceDB": d.SpiceDB == nil, "Fence": d.Fence == nil,
		"Audit": d.Audit == nil, "Plans": d.Plans == nil, "XLIFFFiles": d.XLIFFFiles == nil,
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
	units      mutation.UnitOfWork[Store]
	spiceDB    mutation.Authorizer
	now        func() time.Time
	plans      DomainPlans
	xliffFiles TranslationXLIFFFiles
	mutations  *mutation.StoreRunner[Store]
}

// NewService returns the Translation use cases over deps.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewStoreRunner(deps.Units, deps.SpiceDB, deps.Fence, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{
		units: deps.Units, spiceDB: deps.SpiceDB, now: dbtime.Now,
		plans: deps.Plans, xliffFiles: deps.XLIFFFiles, mutations: mutations,
	}, nil
}

// ListTranslationLocales lists every runtime locale to an administrator and
// the enabled ones to any other authenticated principal.
func (s *Service) ListTranslationLocales(ctx context.Context) ([]Locale, error) {
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
	var locales []Locale
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		locales, err = store.Locales(ctx, !isAdmin)
		return err
	})
	if err != nil {
		return nil, err
	}
	return locales, nil
}

// Overview is the administrator's translation coverage summary.
type Overview struct {
	Stats        OverviewStats
	LocaleHealth []LocaleHealth
	EntityHealth []EntityHealth
}

// GetTranslationOverview summarizes locale and entity translation coverage.
func (s *Service) GetTranslationOverview(ctx context.Context) (Overview, error) {
	if err := requireTranslationAdmin(ctx, s.spiceDB); err != nil {
		return Overview{}, err
	}
	var overview Overview
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		if overview.Stats, err = store.OverviewStats(ctx); err != nil {
			return err
		}
		if overview.LocaleHealth, err = store.LocaleHealth(ctx); err != nil {
			return err
		}
		overview.EntityHealth, err = store.EntityHealth(ctx)
		return err
	})
	if err != nil {
		return Overview{}, err
	}
	return overview, nil
}

// SettingsView is the runtime translation settings and whether a provider
// can generate translations now.
type SettingsView struct {
	Settings translation.RuntimeSettings
}

// GetTranslationSettings reads the runtime translation settings.
func (s *Service) GetTranslationSettings(ctx context.Context) (SettingsView, error) {
	can, canErr := policyv1.TranslationSettings.View()
	if err := s.requireTranslationPlatformCan(ctx, can, canErr); err != nil {
		return SettingsView{}, err
	}
	var settings translation.RuntimeSettings
	// Reading the settings inserts the default singleton when it is missing.
	err := s.units.ReadCurrent(ctx, func(store Store) error {
		var err error
		settings, err = store.RuntimeSettings(ctx)
		return err
	})
	if err != nil {
		return SettingsView{}, err
	}
	return SettingsView{Settings: settings}, nil
}

// UpdateTranslationSettingsCommand patches the runtime translation settings.
// UpdateMask names the fields to change.
type UpdateTranslationSettingsCommand struct {
	Settings   *translation.RuntimeSettings
	UpdateMask *fieldmaskpb.FieldMask
}

type settingsUpdate struct {
	settings translationRuntimeSettings
	fields   []string
}

// UpdateTranslationSettings applies the masked settings fields.
func (s *Service) UpdateTranslationSettings(ctx context.Context, command UpdateTranslationSettingsCommand) (translation.RuntimeSettings, error) {
	var stored translation.RuntimeSettings
	updated, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[settingsUpdate, Store]{
		Lock: func(ctx context.Context, store Store) error {
			var err error
			stored, err = store.LockRuntimeSettings(ctx)
			return err
		},
		Access: mutation.FreshAdminCan(policyv1.TranslationSettings.Update()),
		Change: func(ctx context.Context, store Store) (settingsUpdate, error) {
			return updateTranslationRuntimeSettings(ctx, store, stored, command)
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
		return translation.RuntimeSettings{}, err
	}
	return updated.settings, nil
}

func updateTranslationRuntimeSettings(
	ctx context.Context,
	store Store,
	stored translation.RuntimeSettings,
	command UpdateTranslationSettingsCommand,
) (settingsUpdate, error) {
	current, err := normalizeTranslationRuntimeSettings(stored)
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
	var change SettingsChange
	if current.DefaultLocale != requested.DefaultLocale {
		change.DefaultLocale = &requested.DefaultLocale
	}
	updatedAt, err := store.UpdateRuntimeSettings(ctx, change)
	if err != nil {
		return settingsUpdate{}, err
	}
	requested.UpdatedAt = &updatedAt
	return settingsUpdate{settings: requested, fields: fields}, nil
}

func requireTranslationAdmin(ctx context.Context, spiceDB mutation.Authorizer) error {
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
	if current.DefaultLocale != requested.DefaultLocale {
		return []string{"default_locale"}
	}
	return nil
}

// Pagination is where a page sits among every match.
type Pagination struct {
	Total   int32
	Limit   int32
	Offset  int32
	HasMore bool
}
