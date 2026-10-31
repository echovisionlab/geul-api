package programevent

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
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
func (s *Service) CreateProgramEventSeries(ctx context.Context, command CreateProgramEventSeriesCommand) (*managev1.ProgramEventSeries, error) {
	can, err := policyv1.ProgramEventSeries.Create()
	if err != nil {
		return nil, err
	}
	series, err := s.newProgramEventSeries(ctx, command)
	if err != nil {
		return nil, err
	}
	if _, err := mutation.Run(ctx, s.mutations, mutation.Spec[string]{
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, tx *gorm.DB) (string, error) {
			return series.ID, s.createProgramEventSeriesWithDB(ctx, tx, series)
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
		return nil, err
	}
	return s.loadProgramEventSeries(ctx, series.ID)
}

func (s *Service) newProgramEventSeries(ctx context.Context, command CreateProgramEventSeriesCommand) (*model.ProgramEventSeries, error) {
	title := strings.TrimSpace(command.Title)
	if title == "" {
		return nil, domainerr.Required("title")
	}
	slug, err := validateProgramEventSlug(command.Slug)
	if err != nil {
		return nil, err
	}
	if err := routeregistry.EnsureResourceRouteAvailable(ctx, s.db, "program event series", "event-series", slug); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	return &model.ProgramEventSeries{
		Slug:         slug,
		Status:       model.ProgramEventStatusDraft,
		Title:        title,
		Summary:      command.Summary,
		Description:  command.Description,
		PosterFileID: nullableString(command.PosterFileID),
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

func (s *Service) createProgramEventSeriesWithDB(ctx context.Context, tx *gorm.DB, series *model.ProgramEventSeries) error {
	if err := routeregistry.EnsureResourceRouteAvailableInTx(ctx, tx, "program event series", "event-series", series.Slug); err != nil {
		return err
	}
	if series.PosterFileID != nil {
		if err := s.media.LockAttachableFilesForUpdate(ctx, tx, []string{*series.PosterFileID}); err != nil {
			return err
		}
	}
	if err := tx.Omit("ID").Clauses(clause.Returning{}).Create(series).Error; err != nil {
		if dberrors.IsUniqueViolation(err) {
			return domainerr.Duplicate("program event series", "slug", series.Slug).WithCause(err)
		}
		return err
	}
	if series.PosterFileID == nil {
		return nil
	}
	_, err := s.media.BindReadyAssetForSourceFile(ctx, tx, *series.PosterFileID, "program_event_series", series.ID, "poster", "poster")
	return err
}

// GetProgramEventSeriesQuery names the series by ID or slug.
type GetProgramEventSeriesQuery struct {
	IDOrSlug string
}

// GetProgramEventSeries returns one series to an administrator.
func (s *Service) GetProgramEventSeries(ctx context.Context, query GetProgramEventSeriesQuery) (*managev1.ProgramEventSeries, error) {
	series, err := s.loadProgramEventSeriesRow(ctx, query.IDOrSlug)
	if err != nil {
		return nil, err
	}
	can, err := policyv1.ProgramEventSeries.View(series.ID)
	if err != nil {
		return nil, err
	}
	if err := authz.RequireAdminCan(ctx, s.spiceDB, can); err != nil {
		return nil, err
	}
	return s.toProtoProgramEventSeries(series)
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
	Series     []*managev1.ProgramEventSeries
	Pagination *commonv1.PaginationResponse
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
	rows := s.db.WithContext(ctx).Model(&model.ProgramEventSeries{})
	mappedFilters, err := mapProgramEventSeriesFilters(query.Filters)
	if err != nil {
		return SeriesList{}, err
	}
	rows, err = ProgramEventSeriesFilterConfig.ApplyFilters(rows, mappedFilters)
	if err != nil {
		return SeriesList{}, err
	}
	var total int64
	if err := rows.Count(&total).Error; err != nil {
		return SeriesList{}, err
	}
	rows, err = programEventSeriesSortConfig.ApplySort(rows, query.Sorts)
	if err != nil {
		return SeriesList{}, err
	}
	pagination := queryutil.ExtractPaginationRaw(query.Limit, query.Offset, queryutil.DefaultPaginationConfig)
	var series []model.ProgramEventSeries
	if err := pagination.Apply(rows).Find(&series).Error; err != nil {
		return SeriesList{}, err
	}
	result := make([]*managev1.ProgramEventSeries, 0, len(series))
	for i := range series {
		item, err := s.toProtoProgramEventSeries(&series[i])
		if err != nil {
			return SeriesList{}, err
		}
		result = append(result, item)
	}
	return SeriesList{Series: result, Pagination: pagination.BuildResponse(total)}, nil
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
	_, err = mutation.Run(ctx, s.mutations, mutation.Spec[[]string]{
		Lock: func(_ context.Context, tx *gorm.DB) error {
			_, err := lockProgramEventSeriesForUpdate(tx, command.ID)
			return err
		},
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, tx *gorm.DB) ([]string, error) {
			return s.deleteProgramEventSeries(ctx, tx, command.ID)
		},
		Audit: func(detachedEventIDs []string) mutation.Audit {
			records := make([]mutation.Audit, 0, len(detachedEventIDs)+1)
			for _, eventID := range detachedEventIDs {
				records = append(records, eventAudit(func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
					return sharedtelemetry.NewProgramEventMetadataAuditRecord(metadata, eventID, []string{"series"})
				}))
			}
			return mutation.All(append(records, mutation.RequestAudit(sharedtelemetry.AuditProgramEventSeriesDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewProgramEventSeriesDeletedAuditRecord(metadata, command.ID)
			}))...)
		},
		Relationships: func([]string) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return policyRelationships(policyv1.ProgramEventSeries.DeletePolicy, policyv1.ProgramEventSeries.TouchPolicy, command.ID)
		},
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domainerr.ResourceNotFound("program event series", command.ID)
	}
	return err
}

// deleteProgramEventSeries detaches the series' events, deletes it and
// releases its poster. It returns the detached events.
func (s *Service) deleteProgramEventSeries(ctx context.Context, tx *gorm.DB, seriesID string) ([]string, error) {
	var affectedEvents []model.ProgramEvent
	if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("series_id = ?", seriesID).
		Find(&affectedEvents).Error; err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	detached := make([]string, 0, len(affectedEvents))
	for _, event := range affectedEvents {
		if err := tx.Model(&model.ProgramEvent{}).Where("id = ?", event.ID).
			Updates(structured.Fields{"series_id": nil, "series_order": nil, "updated_at": now}).Error; err != nil {
			return nil, err
		}
		detached = append(detached, event.ID)
	}
	result := tx.Delete(&model.ProgramEventSeries{}, "id = ?", seriesID)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return detached, s.media.ReleasePublicAssetBindings(ctx, tx, "program_event_series", seriesID, "poster")
}
