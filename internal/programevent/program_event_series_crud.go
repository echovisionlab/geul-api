package programevent

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
)

// CreateProgramEventSeriesCommand creates a draft series.
type CreateProgramEventSeriesCommand struct {
	Title        string
	Slug         string
	Summary      *string
	Description  *string
	PosterFileID *string
}

// CreateProgramEventSeries creates the series and its authorization policy.
func (s *Service) CreateProgramEventSeries(ctx context.Context, command CreateProgramEventSeriesCommand) (Series, error) {
	can, err := policyv1.ProgramEventSeries.Create()
	if err != nil {
		return Series{}, err
	}
	series, err := s.newProgramEventSeries(ctx, command)
	if err != nil {
		return Series{}, err
	}
	if _, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[string, Store]{
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, store Store) (string, error) {
			return series.ID, createProgramEventSeries(ctx, store, series)
		},
		Audit: func(seriesID string) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditProgramEventSeriesCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewProgramEventSeriesCreatedAuditRecord(metadata, seriesID)
			})
		},
		Relationships: func(seriesID string) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return policyRelationships(policyv1.ProgramEventSeries.TouchPolicy, policyv1.ProgramEventSeries.DeletePolicy, seriesID)
		},
	}); err != nil {
		return Series{}, err
	}
	return s.loadProgramEventSeries(ctx, series.ID)
}

func (s *Service) newProgramEventSeries(ctx context.Context, command CreateProgramEventSeriesCommand) (*Series, error) {
	title := strings.TrimSpace(command.Title)
	if title == "" {
		return nil, domainerr.Required("title")
	}
	slug, err := validateProgramEventSlug(command.Slug)
	if err != nil {
		return nil, err
	}
	if err := s.checkRoute(ctx, SeriesRoute, slug); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	return &Series{
		Slug:         slug,
		Status:       StatusDraft,
		Title:        title,
		Summary:      command.Summary,
		Description:  command.Description,
		PosterFileID: nullableString(command.PosterFileID),
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

func createProgramEventSeries(ctx context.Context, store Store, series *Series) error {
	if err := store.LockRoute(ctx, SeriesRoute, series.Slug); err != nil {
		return err
	}
	if series.PosterFileID != nil {
		if err := store.LockAttachableFiles(ctx, []string{*series.PosterFileID}); err != nil {
			return err
		}
	}
	if err := store.InsertSeries(ctx, series); err != nil {
		if errors.Is(err, ErrSlugTaken) {
			return domainerr.Duplicate("program event series", "slug", series.Slug).WithCause(err)
		}
		return err
	}
	if series.PosterFileID == nil {
		return nil
	}
	return store.BindSeriesPoster(ctx, series.ID, *series.PosterFileID)
}

// GetProgramEventSeriesQuery names the series by ID or slug.
type GetProgramEventSeriesQuery struct {
	IDOrSlug string
}

// GetProgramEventSeries returns one series to an administrator.
func (s *Service) GetProgramEventSeries(ctx context.Context, query GetProgramEventSeriesQuery) (Series, error) {
	series, err := s.loadProgramEventSeries(ctx, query.IDOrSlug)
	if err != nil {
		return Series{}, err
	}
	can, err := policyv1.ProgramEventSeries.View(series.ID)
	if err != nil {
		return Series{}, err
	}
	if err := authz.RequireAdminCan(ctx, s.spiceDB, can); err != nil {
		return Series{}, err
	}
	return series, nil
}

// ListProgramEventSeriesQuery is one page of the administrator series list.
type ListProgramEventSeriesQuery struct {
	Limit   int32
	Offset  int32
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
}

// SeriesList is one page of series.
type SeriesList struct {
	Series     []Series
	Pagination Pagination
}

// ListProgramEventSeriesAdmin returns one page of series to an
// administrator. A status filter names series API statuses.
func (s *Service) ListProgramEventSeriesAdmin(ctx context.Context, query ListProgramEventSeriesQuery) (SeriesList, error) {
	can, err := policyv1.ProgramEventSeries.List()
	if err != nil {
		return SeriesList{}, err
	}
	if err := authz.RequireAdminCan(ctx, s.spiceDB, can); err != nil {
		return SeriesList{}, err
	}
	mappedFilters, err := mapProgramEventSeriesFilters(query.Filters)
	if err != nil {
		return SeriesList{}, err
	}
	page := queryutil.ExtractPaginationRaw(query.Limit, query.Offset, queryutil.DefaultPaginationConfig)
	var series []Series
	var total int64
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		series, total, err = store.ListSeries(ctx, ListPage{Filters: mappedFilters, Sorts: query.Sorts, Limit: page.Limit, Offset: page.Offset})
		return err
	}); err != nil {
		return SeriesList{}, err
	}
	return SeriesList{Series: series, Pagination: Pagination{
		Total: int32(total), Limit: page.Limit, Offset: page.Offset, HasMore: page.HasMore(total),
	}}, nil
}

// DeleteProgramEventSeriesCommand names the series to delete.
type DeleteProgramEventSeriesCommand struct {
	ID string
}

// DeleteProgramEventSeries deletes the series and its policy; its events
// leave the series.
func (s *Service) DeleteProgramEventSeries(ctx context.Context, command DeleteProgramEventSeriesCommand) error {
	can, err := policyv1.ProgramEventSeries.Delete(command.ID)
	if err != nil {
		return domainerr.InvalidField("id", "must be a canonical resource UUID")
	}
	_, err = mutation.Run(ctx, s.mutations, mutation.StoreSpec[[]string, Store]{
		Lock: func(ctx context.Context, store Store) error {
			_, err := lockProgramEventSeriesForUpdate(ctx, store, command.ID)
			return err
		},
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, store Store) ([]string, error) {
			return deleteProgramEventSeries(ctx, store, command.ID)
		},
		Audit: func(detachedEventIDs []string) mutation.Audit {
			records := make([]mutation.Audit, 0, len(detachedEventIDs)+1)
			for _, eventID := range detachedEventIDs {
				records = append(records, eventAudit(func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
					return sharedtelemetry.NewProgramEventMetadataAuditRecord(metadata, eventID, []string{"series"})
				}))
			}
			return mutation.Audits(append(records, mutation.RequestAudit(sharedtelemetry.AuditProgramEventSeriesDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewProgramEventSeriesDeletedAuditRecord(metadata, command.ID)
			}))...)
		},
		Relationships: func([]string) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return policyRelationships(policyv1.ProgramEventSeries.DeletePolicy, policyv1.ProgramEventSeries.TouchPolicy, command.ID)
		},
	})
	if errors.Is(err, ErrSeriesNotFound) {
		return domainerr.ResourceNotFound("program event series", command.ID)
	}
	return err
}

// deleteProgramEventSeries detaches the series' events, deletes it and
// releases its poster. It returns the detached events.
func deleteProgramEventSeries(ctx context.Context, store Store, seriesID string) ([]string, error) {
	affected, err := store.LockSeriesEvents(ctx, seriesID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	detached := make([]string, 0, len(affected))
	for _, eventID := range affected {
		if err := store.DetachFromSeries(ctx, eventID, now); err != nil {
			return nil, err
		}
		detached = append(detached, eventID)
	}
	deleted, err := store.DeleteSeries(ctx, seriesID)
	if err != nil {
		return nil, err
	}
	if !deleted {
		return nil, ErrSeriesNotFound
	}
	return detached, store.ReleaseSeriesPoster(ctx, seriesID)
}

// loadProgramEventSeries reads the series an ID or slug names.
func (s *Service) loadProgramEventSeries(ctx context.Context, idOrSlug string) (Series, error) {
	var series Series
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		if _, parseErr := uuid.Parse(idOrSlug); parseErr == nil {
			series, err = store.Series(ctx, idOrSlug, NoLock)
		} else {
			series, err = store.SeriesBySlug(ctx, idOrSlug)
		}
		return err
	})
	if errors.Is(err, ErrSeriesNotFound) {
		return Series{}, domainerr.ResourceNotFound("program event series", idOrSlug)
	}
	return series, err
}
