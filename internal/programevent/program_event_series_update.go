package programevent

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// programEventSeriesUpdate is a validated series update; nil fields are left
// alone. posterChanged with a nil posterFileID removes the poster.
type programEventSeriesUpdate struct {
	title         *string
	summary       *string
	description   *string
	status        *model.ProgramEventStatus
	slug          *string
	posterFileID  *string
	posterChanged bool
	// requestedSlug is the slug as requested, which a duplicate names.
	requestedSlug string
}

// columns is the program_event_series row update for the fields the request
// sets.
func (u programEventSeriesUpdate) columns(updatedAt time.Time) structured.Fields {
	columns := structured.Fields{"updated_at": updatedAt}
	for column, value := range map[string]*string{
		"title":       u.title,
		"summary":     u.summary,
		"description": u.description,
		"slug":        u.slug,
	} {
		if value != nil {
			columns[column] = *value
		}
	}
	if u.status != nil {
		columns["status"] = *u.status
	}
	if u.posterChanged {
		columns["poster_file_id"] = nil
		if u.posterFileID != nil {
			columns["poster_file_id"] = *u.posterFileID
		}
	}
	return columns
}

// UpdateProgramEventSeriesCommand changes a series; nil fields are left
// alone and an empty PosterFileID removes the poster. Status changes the
// lifecycle and requires Publish; an empty Status is rejected.
type UpdateProgramEventSeriesCommand struct {
	ID           string
	Title        *string
	Slug         *string
	Summary      *string
	Description  *string
	PosterFileID *string
	Status       *model.ProgramEventStatus
}

// UpdateProgramEventSeries changes the locked series and returns it.
func (s *Service) UpdateProgramEventSeries(ctx context.Context, command UpdateProgramEventSeriesCommand) (*managev1.ProgramEventSeries, error) {
	can, err := programEventSeriesUpdateCan(command)
	if err != nil {
		return nil, err
	}
	series, err := s.loadProgramEventSeriesForUpdate(ctx, command.ID)
	if err != nil {
		return nil, err
	}
	update, err := s.prepareProgramEventSeriesUpdate(ctx, command)
	if err != nil {
		return nil, err
	}
	var current model.ProgramEventSeries
	if _, err := mutation.Run(ctx, s.mutations, mutation.Spec[[]mutation.Audit]{
		Lock: func(_ context.Context, tx *gorm.DB) error {
			current, err = lockProgramEventSeriesForUpdate(tx, series.ID)
			return err
		},
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, tx *gorm.DB) ([]mutation.Audit, error) {
			return s.applyProgramEventSeriesUpdate(ctx, tx, current, update)
		},
		Audit: func(records []mutation.Audit) mutation.Audit {
			if len(records) == 0 {
				return mutation.Unchanged()
			}
			return mutation.All(records...)
		},
	}); err != nil {
		return nil, err
	}
	return s.loadProgramEventSeries(ctx, series.ID)
}

func programEventSeriesUpdateCan(command UpdateProgramEventSeriesCommand) (policyv1.Can, error) {
	if command.Status != nil {
		can, err := policyv1.ProgramEventSeries.Publish(command.ID)
		if err != nil {
			return policyv1.Can{}, domainerr.InvalidField("id", "must be a canonical resource UUID")
		}
		return can, nil
	}
	can, err := policyv1.ProgramEventSeries.Edit(command.ID)
	if err != nil {
		return policyv1.Can{}, domainerr.InvalidField("id", "must be a canonical resource UUID")
	}
	return can, nil
}

func (s *Service) loadProgramEventSeriesForUpdate(
	ctx context.Context,
	seriesID string,
) (model.ProgramEventSeries, error) {
	var series model.ProgramEventSeries
	if err := s.db.WithContext(ctx).First(&series, "id = ?", seriesID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return series, domainerr.ResourceNotFound("program event series", seriesID)
		}
		return series, err
	}
	return series, nil
}

func (s *Service) prepareProgramEventSeriesUpdate(
	ctx context.Context,
	command UpdateProgramEventSeriesCommand,
) (programEventSeriesUpdate, error) {
	update := programEventSeriesUpdate{summary: command.Summary, description: command.Description}
	if command.Title != nil {
		title := strings.TrimSpace(*command.Title)
		if title == "" {
			return update, domainerr.Required("title")
		}
		update.title = &title
	}
	if command.PosterFileID != nil {
		update.posterChanged = true
		update.posterFileID = nullableString(command.PosterFileID)
	}
	if command.Status != nil {
		if *command.Status != model.ProgramEventStatusDraft && *command.Status != model.ProgramEventStatusPublished {
			return update, domainerr.InvalidField("status", "must be draft or published")
		}
		update.status = command.Status
	}
	if command.Slug != nil {
		slug, err := validateProgramEventSlug(*command.Slug)
		if err != nil {
			return update, err
		}
		if err := routeregistry.EnsureResourceRouteAvailable(ctx, s.db, "program event series", "event-series", slug); err != nil {
			return update, err
		}
		update.slug, update.requestedSlug = &slug, *command.Slug
	}
	return update, nil
}

// applyProgramEventSeriesUpdate writes update to the locked series and
// returns the records of what changed: the poster, then the metadata
// fields, then the lifecycle.
func (s *Service) applyProgramEventSeriesUpdate(
	ctx context.Context,
	tx *gorm.DB,
	current model.ProgramEventSeries,
	update programEventSeriesUpdate,
) ([]mutation.Audit, error) {
	if update.slug != nil {
		if err := routeregistry.EnsureResourceRouteAvailableInTx(ctx, tx, "program event series", "event-series", *update.slug); err != nil {
			return nil, err
		}
	}
	changedFields := programEventSeriesMetadataChangedFields(current, update)
	statusChanged := update.status != nil && *update.status != current.Status
	posterChanged, err := s.lockChangedProgramEventSeriesPoster(ctx, tx, current, update)
	if err != nil {
		return nil, err
	}
	if len(changedFields) == 0 && !statusChanged && !posterChanged {
		return nil, nil
	}
	previousStatus, previousPosterFileID := current.Status, current.PosterFileID
	if err := tx.Model(&current).Updates(update.columns(time.Now().UTC())).Error; err != nil {
		if dberrors.IsUniqueViolation(err) && update.slug != nil {
			return nil, domainerr.Duplicate("program event series", "slug", update.requestedSlug).WithCause(err)
		}
		return nil, err
	}
	var records []mutation.Audit
	if posterChanged {
		record, err := s.applyProgramEventSeriesPosterChange(ctx, tx, current.ID, previousPosterFileID, update)
		if err != nil {
			return nil, err
		}
		if record != nil {
			records = append(records, *record)
		}
	}
	if len(changedFields) > 0 {
		records = append(records, seriesAudit(func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewProgramEventSeriesMetadataAuditRecord(metadata, current.ID, changedFields)
		}))
	}
	if statusChanged {
		records = append(records, seriesAudit(func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewProgramEventSeriesLifecycleAuditRecord(metadata, current.ID, programEventSeriesAuditState(previousStatus), programEventSeriesAuditState(*update.status))
		}))
	}
	return records, nil
}

func seriesAudit(build func(sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error)) mutation.Audit {
	return mutation.RequestAudit(sharedtelemetry.AuditProgramEventSeriesUpdated, build)
}

// lockChangedProgramEventSeriesPoster reports whether update changes the
// series poster and locks a new poster file for attachment.
func (s *Service) lockChangedProgramEventSeriesPoster(
	ctx context.Context,
	tx *gorm.DB,
	current model.ProgramEventSeries,
	update programEventSeriesUpdate,
) (bool, error) {
	if !update.posterChanged || sameNullableString(current.PosterFileID, update.posterFileID) {
		return false, nil
	}
	if update.posterFileID == nil {
		return true, nil
	}
	return true, s.media.LockAttachableFilesForUpdate(ctx, tx, []string{*update.posterFileID})
}

// applyProgramEventSeriesPosterChange binds the poster a series update set,
// or releases the removed one, and returns the record of the change.
func (s *Service) applyProgramEventSeriesPosterChange(
	ctx context.Context,
	tx *gorm.DB,
	seriesID string,
	previousPosterFileID *string,
	update programEventSeriesUpdate,
) (*mutation.Audit, error) {
	if err := s.syncProgramEventSeriesPoster(ctx, tx, seriesID, update); err != nil {
		return nil, err
	}
	fileID, operation := update.posterFileID, sharedtelemetry.AuditCollectionOperationAdded
	if update.posterFileID == nil {
		fileID, operation = previousPosterFileID, sharedtelemetry.AuditCollectionOperationRemoved
	}
	if fileID == nil {
		return nil, nil
	}
	record := seriesAudit(func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewProgramEventSeriesPosterAuditRecord(metadata, seriesID, *fileID, operation)
	})
	return &record, nil
}

func lockProgramEventSeriesForUpdate(tx *gorm.DB, seriesID string) (model.ProgramEventSeries, error) {
	var series model.ProgramEventSeries
	err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&series, "id = ?", seriesID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return series, domainerr.ResourceNotFound("program event series", seriesID)
	}
	if err != nil {
		return series, err
	}
	return series, nil
}

func programEventSeriesMetadataChangedFields(series model.ProgramEventSeries, update programEventSeriesUpdate) []string {
	changed := make([]string, 0, 4)
	if update.slug != nil && *update.slug != series.Slug {
		changed = append(changed, "slug")
	}
	if update.title != nil && *update.title != series.Title {
		changed = append(changed, "title")
	}
	if update.summary != nil && !sameNullableString(series.Summary, update.summary) {
		changed = append(changed, "summary")
	}
	if update.description != nil && !sameNullableString(series.Description, update.description) {
		changed = append(changed, "description")
	}
	return changed
}

func sameNullableString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (s *Service) syncProgramEventSeriesPoster(
	ctx context.Context,
	tx *gorm.DB,
	seriesID string,
	update programEventSeriesUpdate,
) error {
	if !update.posterChanged {
		return nil
	}
	if update.posterFileID == nil {
		return s.media.ReleasePublicAssetBindings(ctx, tx, "program_event_series", seriesID, "poster")
	}
	_, err := s.media.BindReadyAssetForSourceFile(ctx, tx, *update.posterFileID, "program_event_series", seriesID, "poster", "poster")
	return err
}
