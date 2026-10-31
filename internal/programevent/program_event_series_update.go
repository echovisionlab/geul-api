package programevent

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/dberrors"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
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

func programEventSeriesUpdateCan(request *managev1.UpdateProgramEventSeriesRequest) (policyv1.Can, error) {
	if request == nil {
		return policyv1.Can{}, errs.InvalidArgument("request", "must be provided")
	}
	if request.Status != nil && request.GetStatus() != managev1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_UNSPECIFIED {
		can, err := policyv1.ProgramEventSeries.Publish(request.Id)
		if err != nil {
			return policyv1.Can{}, errs.InvalidArgument("id", "must be a canonical resource UUID")
		}
		return can, nil
	}
	can, err := policyv1.ProgramEventSeries.Edit(request.Id)
	if err != nil {
		return policyv1.Can{}, errs.InvalidArgument("id", "must be a canonical resource UUID")
	}
	return can, nil
}

func (s *ProgramEventSeriesService) loadProgramEventSeriesForUpdate(
	ctx context.Context,
	seriesID string,
) (model.ProgramEventSeries, error) {
	var series model.ProgramEventSeries
	if err := s.db.WithContext(ctx).First(&series, "id = ?", seriesID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return series, errs.NotFound("program event series", seriesID)
		}
		return series, errs.Internal(err)
	}
	return series, nil
}

func (s *ProgramEventSeriesService) prepareProgramEventSeriesUpdate(
	ctx context.Context,
	request *managev1.UpdateProgramEventSeriesRequest,
) (programEventSeriesUpdate, error) {
	update := programEventSeriesUpdate{summary: request.Summary, description: request.Description}
	if request.Title != nil {
		title := strings.TrimSpace(request.GetTitle())
		if title == "" {
			return update, errs.Required("title")
		}
		update.title = &title
	}
	if request.PosterFileId != nil {
		update.posterChanged = true
		update.posterFileID = nullableString(request.PosterFileId)
	}
	if request.Status != nil && request.GetStatus() != managev1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_UNSPECIFIED {
		status, err := programEventSeriesStatusStorageValue(request.GetStatus())
		if err != nil {
			return update, err
		}
		update.status = &status
	}
	if request.Slug != nil {
		slug, err := validateProgramEventSlug(request.GetSlug())
		if err != nil {
			return update, err
		}
		if err := routeregistry.EnsureResourceRouteAvailable(ctx, s.db, "program event series", "event-series", slug); err != nil {
			return update, err
		}
		update.slug = &slug
	}
	return update, nil
}

func (s *ProgramEventSeriesService) applyProgramEventSeriesUpdate(
	ctx context.Context,
	series *model.ProgramEventSeries,
	request *managev1.UpdateProgramEventSeriesRequest,
	update programEventSeriesUpdate,
	can policyv1.Can,
) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if update.slug != nil {
			if err := routeregistry.EnsureResourceRouteAvailableInTx(ctx, tx, "program event series", "event-series", *update.slug); err != nil {
				return err
			}
		}
		current, err := lockProgramEventSeriesForUpdate(tx, series.ID)
		if err != nil {
			return err
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		changedFields := programEventSeriesMetadataChangedFields(current, update)
		statusChanged := update.status != nil && *update.status != current.Status
		posterChanged, err := s.lockChangedProgramEventSeriesPoster(ctx, tx, current, update)
		if err != nil {
			return err
		}
		if len(changedFields) == 0 && !statusChanged && !posterChanged {
			return nil
		}
		previousStatus := current.Status
		previousPosterFileID := current.PosterFileID
		if err := tx.Model(&current).Updates(update.columns(time.Now().UTC())).Error; err != nil {
			if dberrors.IsUniqueViolation(err) && update.slug != nil {
				return errs.WithCause(errs.SlugAlreadyExists("program event series", request.GetSlug()), err)
			}
			return errs.Internal(err)
		}
		if posterChanged {
			if err := s.applyProgramEventSeriesPosterChange(ctx, tx, current.ID, previousPosterFileID, update); err != nil {
				return err
			}
		}
		var nextStatus *model.ProgramEventStatus
		if statusChanged {
			nextStatus = update.status
		}
		return s.appendProgramEventSeriesUpdateAudit(ctx, tx, current.ID, changedFields, previousStatus, nextStatus)
	})
}

// appendProgramEventSeriesUpdateAudit audits the changed metadata fields and,
// when nextStatus is set, the lifecycle transition from previousStatus.
func (s *ProgramEventSeriesService) appendProgramEventSeriesUpdateAudit(
	ctx context.Context,
	tx *gorm.DB,
	seriesID string,
	changedFields []string,
	previousStatus model.ProgramEventStatus,
	nextStatus *model.ProgramEventStatus,
) error {
	if len(changedFields) > 0 {
		if err := appendOptionalProgramEventAudit(ctx, tx, s.auditWriter, sharedtelemetry.AuditProgramEventSeriesUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewProgramEventSeriesMetadataAuditRecord(metadata, seriesID, changedFields)
		}); err != nil {
			return err
		}
	}
	if nextStatus == nil {
		return nil
	}
	return appendOptionalProgramEventAudit(ctx, tx, s.auditWriter, sharedtelemetry.AuditProgramEventSeriesUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewProgramEventSeriesLifecycleAuditRecord(metadata, seriesID, programEventSeriesAuditState(previousStatus), programEventSeriesAuditState(*nextStatus))
	})
}

// lockChangedProgramEventSeriesPoster reports whether update changes the
// series poster and locks a new poster file for attachment.
func (s *ProgramEventSeriesService) lockChangedProgramEventSeriesPoster(
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
	return true, s.runtime.LockAttachableFilesForUpdate(ctx, tx, []string{*update.posterFileID})
}

// applyProgramEventSeriesPosterChange binds the poster a series update set,
// or releases the removed one, and audits the change.
func (s *ProgramEventSeriesService) applyProgramEventSeriesPosterChange(
	ctx context.Context,
	tx *gorm.DB,
	seriesID string,
	previousPosterFileID *string,
	update programEventSeriesUpdate,
) error {
	if err := s.syncProgramEventSeriesPoster(ctx, tx, seriesID, update); err != nil {
		return err
	}
	posterFileID := update.posterFileID
	fileID, operation := posterFileID, sharedtelemetry.AuditCollectionOperationAdded
	if posterFileID == nil {
		fileID, operation = previousPosterFileID, sharedtelemetry.AuditCollectionOperationRemoved
	}
	if fileID == nil {
		return nil
	}
	return appendOptionalProgramEventAudit(ctx, tx, s.auditWriter, sharedtelemetry.AuditProgramEventSeriesUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewProgramEventSeriesPosterAuditRecord(metadata, seriesID, *fileID, operation)
	})
}

func lockProgramEventSeriesForUpdate(tx *gorm.DB, seriesID string) (model.ProgramEventSeries, error) {
	var series model.ProgramEventSeries
	err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&series, "id = ?", seriesID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return series, errs.NotFound("program event series", seriesID)
	}
	if err != nil {
		return series, errs.Internal(err)
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

func (s *ProgramEventSeriesService) syncProgramEventSeriesPoster(
	ctx context.Context,
	tx *gorm.DB,
	seriesID string,
	update programEventSeriesUpdate,
) error {
	if !update.posterChanged {
		return nil
	}
	if update.posterFileID == nil {
		return s.runtime.ReleasePublicAssetBindings(ctx, tx, "program_event_series", seriesID, "poster")
	}
	_, err := s.runtime.BindReadyAssetForSourceFile(ctx, tx, *update.posterFileID, "program_event_series", seriesID, "poster", "poster")
	return err
}
