package programevent

import (
	"context"
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

type programEventSeriesUpdate struct {
	fields        structured.Fields
	slug          *string
	posterFileID  *string
	posterChanged bool
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
		if err == gorm.ErrRecordNotFound {
			return series, errs.NotFound("program event series", seriesID)
		}
		return series, errs.Internal(err)
	}
	return series, nil
}

func (s *ProgramEventSeriesService) prepareProgramEventSeriesUpdate(
	ctx context.Context,
	request *managev1.UpdateProgramEventSeriesRequest,
	now time.Time,
) (programEventSeriesUpdate, error) {
	update := programEventSeriesUpdate{fields: structured.Fields{}}
	if request.Title != nil {
		title := strings.TrimSpace(request.GetTitle())
		if title == "" {
			return update, errs.Required("title")
		}
		update.fields["title"] = title
	}
	if request.Summary != nil {
		update.fields["summary"] = request.Summary
	}
	if request.Description != nil {
		update.fields["description"] = request.Description
	}
	if request.PosterFileId != nil {
		update.posterChanged = true
		update.posterFileID = nullableString(request.PosterFileId)
		update.fields["poster_file_id"] = update.posterFileID
	}
	if request.Status != nil && request.GetStatus() != managev1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_UNSPECIFIED {
		status, err := programEventSeriesStatusStorageValue(request.GetStatus())
		if err != nil {
			return update, err
		}
		update.fields["status"] = status
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
		update.fields["slug"] = slug
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
		changedFields := programEventSeriesMetadataChangedFields(current, update.fields)
		statusChanged := programEventSeriesStatusChanged(current, update.fields)
		posterChanged := update.posterChanged && !sameNullableString(current.PosterFileID, update.posterFileID)
		if posterChanged && update.posterFileID != nil {
			if err := s.runtime.LockAttachableFilesForUpdate(ctx, tx, []string{*update.posterFileID}); err != nil {
				return err
			}
		}
		if len(changedFields) == 0 && !statusChanged && !posterChanged {
			return nil
		}
		previousStatus := current.Status
		previousPosterFileID := current.PosterFileID
		update.fields["updated_at"] = time.Now().UTC()
		if err := tx.Model(&current).Updates(update.fields).Error; err != nil {
			if dberrors.IsUniqueViolation(err) && update.slug != nil {
				return errs.SlugAlreadyExists("program event series", request.GetSlug())
			}
			return errs.Internal(err)
		}
		if posterChanged {
			if err := s.syncProgramEventSeriesPoster(ctx, tx, series.ID, update); err != nil {
				return err
			}
			if update.posterFileID != nil {
				if err := appendOptionalProgramEventAudit(ctx, tx, s.auditWriter, sharedtelemetry.AuditProgramEventSeriesUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
					return sharedtelemetry.NewProgramEventSeriesPosterAuditRecord(metadata, current.ID, *update.posterFileID, sharedtelemetry.AuditCollectionOperationAdded)
				}); err != nil {
					return err
				}
			} else if previousPosterFileID != nil {
				if err := appendOptionalProgramEventAudit(ctx, tx, s.auditWriter, sharedtelemetry.AuditProgramEventSeriesUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
					return sharedtelemetry.NewProgramEventSeriesPosterAuditRecord(metadata, current.ID, *previousPosterFileID, sharedtelemetry.AuditCollectionOperationRemoved)
				}); err != nil {
					return err
				}
			}
		}
		if len(changedFields) > 0 {
			if err := appendOptionalProgramEventAudit(ctx, tx, s.auditWriter, sharedtelemetry.AuditProgramEventSeriesUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewProgramEventSeriesMetadataAuditRecord(metadata, current.ID, changedFields)
			}); err != nil {
				return err
			}
		}
		if statusChanged {
			return appendOptionalProgramEventAudit(ctx, tx, s.auditWriter, sharedtelemetry.AuditProgramEventSeriesUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewProgramEventSeriesLifecycleAuditRecord(metadata, current.ID, programEventSeriesAuditState(previousStatus), programEventSeriesAuditState(update.fields["status"].(string)))
			})
		}
		return nil
	})
}

func lockProgramEventSeriesForUpdate(tx *gorm.DB, seriesID string) (model.ProgramEventSeries, error) {
	var series model.ProgramEventSeries
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&series, "id = ?", seriesID).Error
	if err == gorm.ErrRecordNotFound {
		return series, errs.NotFound("program event series", seriesID)
	}
	if err != nil {
		return series, errs.Internal(err)
	}
	return series, nil
}

func programEventSeriesMetadataChangedFields(series model.ProgramEventSeries, fields structured.Fields) []string {
	changed := make([]string, 0, 4)
	if value, ok := fields["slug"].(string); ok && value != series.Slug {
		changed = append(changed, "slug")
	}
	if value, ok := fields["title"].(string); ok && value != series.Title {
		changed = append(changed, "title")
	}
	if value, ok := fields["summary"].(*string); ok && !sameNullableString(series.Summary, value) {
		changed = append(changed, "summary")
	}
	if value, ok := fields["description"].(*string); ok && !sameNullableString(series.Description, value) {
		changed = append(changed, "description")
	}
	return changed
}

func programEventSeriesStatusChanged(series model.ProgramEventSeries, fields structured.Fields) bool {
	value, ok := fields["status"].(string)
	return ok && value != series.Status
}

func sameNullableString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func programEventSeriesAuditState(status string) sharedtelemetry.AuditState {
	if status == managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED.String() {
		return sharedtelemetry.AuditStatePublished
	}
	return sharedtelemetry.AuditStateDraft
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
