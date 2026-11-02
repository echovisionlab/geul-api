package programevent

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// programEventSeriesUpdate is a validated series update and the slug as
// requested, which a duplicate names.
type programEventSeriesUpdate struct {
	SeriesChange
	requestedSlug string
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
	Status       *Status
}

// UpdateProgramEventSeries changes the locked series and returns it.
func (s *Service) UpdateProgramEventSeries(ctx context.Context, command UpdateProgramEventSeriesCommand) (Series, error) {
	can, err := programEventSeriesUpdateCan(command)
	if err != nil {
		return Series{}, err
	}
	series, err := s.loadProgramEventSeriesForUpdate(ctx, command.ID)
	if err != nil {
		return Series{}, err
	}
	update, err := s.prepareProgramEventSeriesUpdate(ctx, command)
	if err != nil {
		return Series{}, err
	}
	var current Series
	if _, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[[]mutation.Audit, Store]{
		Lock: func(ctx context.Context, store Store) error {
			current, err = lockProgramEventSeriesForUpdate(ctx, store, series.ID)
			return err
		},
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, store Store) ([]mutation.Audit, error) {
			return applyProgramEventSeriesUpdate(ctx, store, current, update)
		},
		Audit: func(records []mutation.Audit) mutation.Audit {
			return mutation.Audits(records...)
		},
	}); err != nil {
		return Series{}, err
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

func (s *Service) loadProgramEventSeriesForUpdate(ctx context.Context, seriesID string) (Series, error) {
	var series Series
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		series, err = store.Series(ctx, seriesID, NoLock)
		return err
	})
	if errors.Is(err, ErrSeriesNotFound) {
		return series, domainerr.ResourceNotFound("program event series", seriesID)
	}
	return series, err
}

func (s *Service) prepareProgramEventSeriesUpdate(
	ctx context.Context,
	command UpdateProgramEventSeriesCommand,
) (programEventSeriesUpdate, error) {
	update := programEventSeriesUpdate{SeriesChange: SeriesChange{Summary: command.Summary, Description: command.Description}}
	if command.Title != nil {
		title := strings.TrimSpace(*command.Title)
		if title == "" {
			return update, domainerr.Required("title")
		}
		update.Title = &title
	}
	if command.PosterFileID != nil {
		update.PosterChanged = true
		update.PosterFileID = nullableString(command.PosterFileID)
	}
	if command.Status != nil {
		if *command.Status != StatusDraft && *command.Status != StatusPublished {
			return update, domainerr.InvalidField("status", "must be draft or published")
		}
		update.Status = command.Status
	}
	if command.Slug != nil {
		slug, err := validateProgramEventSlug(*command.Slug)
		if err != nil {
			return update, err
		}
		if err := s.checkRoute(ctx, SeriesRoute, slug); err != nil {
			return update, err
		}
		update.Slug, update.requestedSlug = &slug, *command.Slug
	}
	return update, nil
}

// applyProgramEventSeriesUpdate writes update to the locked series and
// returns the records of what changed: the poster, then the metadata
// fields, then the lifecycle.
func applyProgramEventSeriesUpdate(
	ctx context.Context,
	store Store,
	current Series,
	update programEventSeriesUpdate,
) ([]mutation.Audit, error) {
	if update.Slug != nil {
		if err := store.LockRoute(ctx, SeriesRoute, *update.Slug); err != nil {
			return nil, err
		}
	}
	changedFields := programEventSeriesMetadataChangedFields(current, update)
	statusChanged := update.Status != nil && *update.Status != current.Status
	posterChanged, err := lockChangedProgramEventSeriesPoster(ctx, store, current, update)
	if err != nil {
		return nil, err
	}
	if len(changedFields) == 0 && !statusChanged && !posterChanged {
		return nil, nil
	}
	previousStatus, previousPosterFileID := current.Status, current.PosterFileID
	if err := store.UpdateSeries(ctx, current.ID, update.SeriesChange, time.Now().UTC()); err != nil {
		if errors.Is(err, ErrSlugTaken) && update.Slug != nil {
			return nil, domainerr.Duplicate("program event series", "slug", update.requestedSlug).WithCause(err)
		}
		return nil, err
	}
	var records []mutation.Audit
	if posterChanged {
		record, err := applyProgramEventSeriesPosterChange(ctx, store, current.ID, previousPosterFileID, update)
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
			return sharedtelemetry.NewProgramEventSeriesLifecycleAuditRecord(metadata, current.ID, programEventSeriesAuditState(previousStatus), programEventSeriesAuditState(*update.Status))
		}))
	}
	return records, nil
}

func seriesAudit(build func(sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error)) mutation.Audit {
	return mutation.RequestAudit(sharedtelemetry.AuditProgramEventSeriesUpdated, build)
}

// lockChangedProgramEventSeriesPoster reports whether update changes the
// series poster and locks a new poster file for attachment.
func lockChangedProgramEventSeriesPoster(
	ctx context.Context,
	store Store,
	current Series,
	update programEventSeriesUpdate,
) (bool, error) {
	if !update.PosterChanged || sameNullableString(current.PosterFileID, update.PosterFileID) {
		return false, nil
	}
	if update.PosterFileID == nil {
		return true, nil
	}
	return true, store.LockAttachableFiles(ctx, []string{*update.PosterFileID})
}

// applyProgramEventSeriesPosterChange binds the poster a series update set,
// or releases the removed one, and returns the record of the change.
func applyProgramEventSeriesPosterChange(
	ctx context.Context,
	store Store,
	seriesID string,
	previousPosterFileID *string,
	update programEventSeriesUpdate,
) (*mutation.Audit, error) {
	if err := syncProgramEventSeriesPoster(ctx, store, seriesID, update); err != nil {
		return nil, err
	}
	fileID, operation := update.PosterFileID, sharedtelemetry.AuditCollectionOperationAdded
	if update.PosterFileID == nil {
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

func lockProgramEventSeriesForUpdate(ctx context.Context, store Store, seriesID string) (Series, error) {
	series, err := store.Series(ctx, seriesID, UpdateLock)
	if errors.Is(err, ErrSeriesNotFound) {
		return series, domainerr.ResourceNotFound("program event series", seriesID)
	}
	return series, err
}

func programEventSeriesMetadataChangedFields(series Series, update programEventSeriesUpdate) []string {
	changed := make([]string, 0, 4)
	if update.Slug != nil && *update.Slug != series.Slug {
		changed = append(changed, "slug")
	}
	if update.Title != nil && *update.Title != series.Title {
		changed = append(changed, "title")
	}
	if update.Summary != nil && !sameNullableString(series.Summary, update.Summary) {
		changed = append(changed, "summary")
	}
	if update.Description != nil && !sameNullableString(series.Description, update.Description) {
		changed = append(changed, "description")
	}
	return changed
}

func syncProgramEventSeriesPoster(
	ctx context.Context,
	store Store,
	seriesID string,
	update programEventSeriesUpdate,
) error {
	if !update.PosterChanged {
		return nil
	}
	if update.PosterFileID == nil {
		return store.ReleaseSeriesPoster(ctx, seriesID)
	}
	return store.BindSeriesPoster(ctx, seriesID, *update.PosterFileID)
}
