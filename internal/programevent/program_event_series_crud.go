package programevent

import (
	"context"
	"strings"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func (s *ProgramEventSeriesService) CreateProgramEventSeries(
	ctx context.Context,
	req *connect.Request[managev1.CreateProgramEventSeriesRequest],
) (*connect.Response[managev1.ProgramEventSeries], error) {
	can, err := policyv1.ProgramEventSeries.Create()
	if err != nil {
		return nil, errs.Internal(err)
	}
	title := strings.TrimSpace(req.Msg.Title)
	if title == "" {
		return nil, errs.Required("title")
	}
	slug, err := validateProgramEventSlug(req.Msg.Slug)
	if err != nil {
		return nil, err
	}
	if err := routeregistry.EnsureResourceRouteAvailable(ctx, s.db, "program event series", "event-series", slug); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	posterFileID := nullableString(req.Msg.PosterFileId)
	series := &model.ProgramEventSeries{
		Slug:         slug,
		Status:       managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_DRAFT.String(),
		Title:        title,
		Summary:      req.Msg.Summary,
		Description:  req.Msg.Description,
		PosterFileID: posterFileID,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		if err := routeregistry.EnsureResourceRouteAvailableInTx(ctx, tx, "program event series", "event-series", slug); err != nil {
			return err
		}
		if posterFileID != nil {
			if err := s.runtime.LockAttachableFilesForUpdate(ctx, tx, []string{*posterFileID}); err != nil {
				return err
			}
		}
		if err := tx.Omit("ID").Clauses(clause.Returning{}).Create(series).Error; err != nil {
			if dberrors.IsUniqueViolation(err) {
				return errs.SlugAlreadyExists("program event series", slug)
			}
			return errs.Internal(err)
		}
		if posterFileID != nil {
			if _, err := s.runtime.BindReadyAssetForSourceFile(ctx, tx, *posterFileID, "program_event_series", series.ID, "poster", "poster"); err != nil {
				return err
			}
		}
		if err := appendOptionalProgramEventAudit(ctx, tx, s.auditWriter, sharedtelemetry.AuditProgramEventSeriesCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewProgramEventSeriesCreatedAuditRecord(metadata, series.ID)
		}); err != nil {
			return err
		}
		apply, err := policyv1.ProgramEventSeries.TouchPolicy(series.ID)
		if err != nil {
			return err
		}
		compensate, err := policyv1.ProgramEventSeries.DeletePolicy(series.ID)
		if err != nil {
			return err
		}
		return write(
			[]policyv1.RelationshipMutation{apply},
			[]policyv1.RelationshipMutation{compensate},
		)
	})
	if err != nil {
		return nil, err
	}
	proto, err := s.loadProgramEventSeries(ctx, series.ID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(proto), nil
}

func (s *ProgramEventSeriesService) GetProgramEventSeries(
	ctx context.Context,
	req *connect.Request[managev1.GetProgramEventSeriesRequest],
) (*connect.Response[managev1.ProgramEventSeries], error) {
	series, err := s.loadProgramEventSeriesRow(ctx, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	can, err := policyv1.ProgramEventSeries.View(series.ID)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAdminCan(ctx, s.spiceDB, can); err != nil {
		return nil, err
	}
	proto, err := s.toProtoProgramEventSeries(ctx, series)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(proto), nil
}

func (s *ProgramEventSeriesService) ListProgramEventSeriesAdmin(
	ctx context.Context,
	req *connect.Request[managev1.ListProgramEventSeriesAdminRequest],
) (*connect.Response[managev1.ListProgramEventSeriesAdminResponse], error) {
	can, err := policyv1.ProgramEventSeries.List()
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAdminCan(ctx, s.spiceDB, can); err != nil {
		return nil, err
	}
	query := s.db.WithContext(ctx).Model(&model.ProgramEventSeries{})
	mappedFilters, err := mapProgramEventSeriesFilters(req.Msg.Filters)
	if err != nil {
		return nil, err
	}
	query, err = ProgramEventSeriesFilterConfig.ApplyFilters(query, mappedFilters)
	if err != nil {
		return nil, err
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}
	query, err = programEventSeriesSortConfig.ApplySort(query, req.Msg.Sorts)
	if err != nil {
		return nil, err
	}
	pagination := queryutil.ExtractPagination(req.Msg.Pagination, queryutil.DefaultPaginationConfig)
	var rows []model.ProgramEventSeries
	if err := pagination.Apply(query).Find(&rows).Error; err != nil {
		return nil, errs.Internal(err)
	}
	result := make([]*managev1.ProgramEventSeries, 0, len(rows))
	for i := range rows {
		item, err := s.toProtoProgramEventSeries(ctx, &rows[i])
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return connect.NewResponse(&managev1.ListProgramEventSeriesAdminResponse{
		Series:     result,
		Pagination: pagination.BuildResponse(total),
	}), nil
}

func (s *ProgramEventSeriesService) UpdateProgramEventSeries(
	ctx context.Context,
	req *connect.Request[managev1.UpdateProgramEventSeriesRequest],
) (*connect.Response[managev1.ProgramEventSeries], error) {
	can, err := programEventSeriesUpdateCan(req.Msg)
	if err != nil {
		return nil, err
	}
	series, err := s.loadProgramEventSeriesForUpdate(ctx, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	update, err := s.prepareProgramEventSeriesUpdate(ctx, req.Msg, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if err := s.applyProgramEventSeriesUpdate(ctx, &series, req.Msg, update, can); err != nil {
		return nil, err
	}
	proto, err := s.loadProgramEventSeries(ctx, series.ID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(proto), nil
}

func (s *ProgramEventSeriesService) DeleteProgramEventSeries(
	ctx context.Context,
	req *connect.Request[managev1.DeleteProgramEventSeriesRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	can, err := policyv1.ProgramEventSeries.Delete(req.Msg.Id)
	if err != nil {
		return nil, errs.InvalidArgument("id", "must be a canonical resource UUID")
	}
	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		if _, err := lockProgramEventSeriesForUpdate(tx, req.Msg.Id); err != nil {
			return err
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		var affectedEvents []model.ProgramEvent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("series_id = ?", req.Msg.Id).
			Find(&affectedEvents).Error; err != nil {
			return errs.Internal(err)
		}
		now := time.Now().UTC()
		for _, event := range affectedEvents {
			if err := tx.Model(&model.ProgramEvent{}).Where("id = ?", event.ID).
				Updates(structured.Fields{"series_id": nil, "series_order": nil, "updated_at": now}).Error; err != nil {
				return errs.Internal(err)
			}
			if err := appendOptionalProgramEventAudit(ctx, tx, s.auditWriter, sharedtelemetry.AuditProgramEventUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewProgramEventMetadataAuditRecord(metadata, event.ID, []string{"series"})
			}); err != nil {
				return err
			}
		}
		result := tx.Delete(&model.ProgramEventSeries{}, "id = ?", req.Msg.Id)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		if err := s.runtime.ReleasePublicAssetBindings(ctx, tx, "program_event_series", req.Msg.Id, "poster"); err != nil {
			return err
		}
		if err := appendOptionalProgramEventAudit(ctx, tx, s.auditWriter, sharedtelemetry.AuditProgramEventSeriesDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewProgramEventSeriesDeletedAuditRecord(metadata, req.Msg.Id)
		}); err != nil {
			return err
		}
		apply, err := policyv1.ProgramEventSeries.DeletePolicy(req.Msg.Id)
		if err != nil {
			return err
		}
		compensate, err := policyv1.ProgramEventSeries.TouchPolicy(req.Msg.Id)
		if err != nil {
			return err
		}
		return write(
			[]policyv1.RelationshipMutation{apply},
			[]policyv1.RelationshipMutation{compensate},
		)
	})
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("program event series", req.Msg.Id)
		}
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&managev1.DeleteResponse{Success: true}), nil
}
