package series

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// SeriesService implements the SeriesService Connect handler
func (s *SeriesService) GetSeriesWithManagers(
	ctx context.Context,
	req *connect.Request[managev1.GetSeriesWithManagersRequest],
) (*connect.Response[managev1.GetSeriesWithManagersResponse], error) {
	if err := s.requireSeriesPermissionOrNotFound(ctx, req.Msg.Id, policyv1.PostSeries.View); err != nil {
		return nil, err
	}

	// Get series (admin/manager can see any status)
	var series model.Series
	if err := s.db.WithContext(ctx).First(&series, "id = ?", req.Msg.Id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("series", req.Msg.Id)
		}
		return nil, errs.Internal(err)
	}
	if err := s.overlaySeriesSourceLocaleDocument(ctx, &series); err != nil {
		return nil, err
	}

	memberIDs, err := loadSeriesManagerMemberIDs(ctx, s.db, req.Msg.Id)
	if err != nil {
		return nil, errs.Internal(err)
	}

	var protoMembers []*managev1.SeriesManager
	if len(memberIDs) > 0 {
		summaries, err := s.members.LoadSeriesManagers(ctx, memberIDs)
		if err != nil {
			return nil, errs.Internal(err)
		}
		protoMembers = make([]*managev1.SeriesManager, 0, len(memberIDs))
		for _, memberID := range memberIDs {
			summary := summaries[memberID]
			if summary == nil {
				return nil, errs.InternalMsg("series manager member was not found")
			}
			protoMembers = append(protoMembers, summary)
		}
	}

	ogAsset, err := s.media.ReadyAsset(ctx, series.OgAssetID)
	if err != nil {
		return nil, err
	}
	protoSeries := s.toProtoSeries(&series, ogAsset)
	s.setSeriesFeaturedImageAsset(ctx, protoSeries)

	return connect.NewResponse(&managev1.GetSeriesWithManagersResponse{
		Series:   protoSeries,
		Managers: protoMembers,
	}), nil
}

// ListSeriesManagers returns members of a series
func (s *SeriesService) ListSeriesManagers(
	ctx context.Context,
	req *connect.Request[managev1.ListSeriesManagersRequest],
) (*connect.Response[managev1.ListSeriesManagersResponse], error) {
	if err := s.requireSeriesPermissionOrNotFound(ctx, req.Msg.SeriesId, policyv1.PostSeries.View); err != nil {
		return nil, err
	}

	// Verify series exists
	var series model.Series
	if err := s.db.WithContext(ctx).First(&series, "id = ?", req.Msg.SeriesId).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("series", req.Msg.SeriesId)
		}
		return nil, errs.Internal(err)
	}

	memberIDs, err := loadSeriesManagerMemberIDs(ctx, s.db, req.Msg.SeriesId)
	if err != nil {
		return nil, errs.Internal(err)
	}

	if len(memberIDs) == 0 {
		return connect.NewResponse(&managev1.ListSeriesManagersResponse{
			Managers: []*managev1.SeriesManager{},
		}), nil
	}

	summaries, err := s.members.LoadSeriesManagers(ctx, memberIDs)
	if err != nil {
		return nil, errs.Internal(err)
	}
	protoMembers := make([]*managev1.SeriesManager, 0, len(memberIDs))
	for _, memberID := range memberIDs {
		summary := summaries[memberID]
		if summary == nil {
			return nil, errs.InternalMsg("series manager member was not found")
		}
		protoMembers = append(protoMembers, summary)
	}

	return connect.NewResponse(&managev1.ListSeriesManagersResponse{
		Managers: protoMembers,
	}), nil
}

// AddSeriesManager adds a member to a series
func (s *SeriesService) AddSeriesManager(
	ctx context.Context,
	req *connect.Request[managev1.AddSeriesManagerRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	_, err := authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		if err := s.requireSeriesPermissionAndLock(ctx, tx, req.Msg.SeriesId, policyv1.PostSeries.ManageParticipants); err != nil {
			return err
		}
		present, err := seriesManagerAttributionExists(ctx, tx, req.Msg.SeriesId, req.Msg.MemberId)
		if err != nil {
			return err
		}
		target, err := authorizationtarget.RequireLocked(ctx, tx, req.Msg.MemberId)
		if err != nil {
			return err
		}
		if !present {
			if err := upsertSeriesManagerAttribution(ctx, tx, req.Msg.SeriesId, req.Msg.MemberId, time.Now().UTC()); err != nil {
				return err
			}
			if err := s.appendPostSeriesManagerAudit(ctx, tx, req.Msg.SeriesId, req.Msg.MemberId, sharedtelemetry.AuditRelationshipNone, sharedtelemetry.AuditRelationshipManager); err != nil {
				return err
			}
		}
		apply, compensate, err := seriesManagerAuthorizationMutations(req.Msg.SeriesId, target.IdentityID, true, present)
		if err != nil {
			return err
		}
		return write(apply, compensate)
	})
	if err != nil {
		if _, ok := err.(*connect.Error); ok {
			return nil, err
		}
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&managev1.DeleteResponse{Success: true}), nil
}

// RemoveSeriesManager removes a member from a series
func (s *SeriesService) RemoveSeriesManager(
	ctx context.Context,
	req *connect.Request[managev1.RemoveSeriesManagerRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	// A retired target has no remaining SpiceDB identity relationship. Keep
	// that cleanup on a transaction which performs the same one exact Series
	// permission check but deliberately performs no authorization write.
	if _, linkedErr := authorizationtarget.RequireLinkedMember(ctx, s.db, req.Msg.MemberId, false); linkedErr != nil {
		if connect.CodeOf(linkedErr) != connect.CodeNotFound {
			return nil, linkedErr
		}
		if err := s.removeRetiredSeriesManagerAttribution(ctx, req.Msg.SeriesId, req.Msg.MemberId); err != nil {
			return nil, err
		}
		return connect.NewResponse(&managev1.DeleteResponse{Success: true}), nil
	}

	_, err := authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		if err := s.requireSeriesPermissionAndLock(ctx, tx, req.Msg.SeriesId, policyv1.PostSeries.ManageParticipants); err != nil {
			return err
		}
		present, err := seriesManagerAttributionExists(ctx, tx, req.Msg.SeriesId, req.Msg.MemberId)
		if err != nil {
			return err
		}
		if !present {
			return errs.NotFoundMsg("series manager relation not found")
		}
		target, targetErr := authorizationtarget.RequireLockedLinked(ctx, tx, req.Msg.MemberId)
		if targetErr != nil {
			if connect.CodeOf(targetErr) == connect.CodeNotFound {
				return errs.FailedPrecondition("series manager target changed; retry")
			}
			return targetErr
		}
		if err := deleteSeriesManagerAttribution(ctx, tx, req.Msg.SeriesId, req.Msg.MemberId); err != nil {
			return err
		}
		if err := s.appendPostSeriesManagerAudit(ctx, tx, req.Msg.SeriesId, req.Msg.MemberId, sharedtelemetry.AuditRelationshipManager, sharedtelemetry.AuditRelationshipNone); err != nil {
			return err
		}
		apply, compensate, err := seriesManagerAuthorizationMutations(req.Msg.SeriesId, target.IdentityID, false, true)
		if err != nil {
			return err
		}
		return write(apply, compensate)
	})
	if err != nil {
		if _, ok := err.(*connect.Error); ok {
			return nil, err
		}
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&managev1.DeleteResponse{Success: true}), nil
}

// removeRetiredSeriesManagerAttribution clears the product-owned record after
// account deletion has already removed every identity relationship from
// SpiceDB. There is deliberately no authorization write to compensate.
func (s *SeriesService) removeRetiredSeriesManagerAttribution(ctx context.Context, seriesID, memberID string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.requireSeriesPermissionAndLock(ctx, tx, seriesID, policyv1.PostSeries.ManageParticipants); err != nil {
			return err
		}
		present, err := seriesManagerAttributionExists(ctx, tx, seriesID, memberID)
		if err != nil {
			return err
		}
		if !present {
			return errs.NotFoundMsg("series manager relation not found")
		}
		if _, err := authorizationtarget.RequireLockedLinked(ctx, tx, memberID); err != nil {
			if connect.CodeOf(err) != connect.CodeNotFound {
				return err
			}
		} else {
			return errs.FailedPrecondition("series manager target is still linked; retry")
		}
		if err := deleteSeriesManagerAttribution(ctx, tx, seriesID, memberID); err != nil {
			return err
		}
		return s.appendPostSeriesManagerAudit(ctx, tx, seriesID, memberID, sharedtelemetry.AuditRelationshipManager, sharedtelemetry.AuditRelationshipNone)
	})
}

func seriesManagerAuthorizationMutations(
	seriesID string,
	identityID string,
	desired bool,
	previous bool,
) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
	actor, err := policyv1.NewAccountIdentityActor(identityID)
	if err != nil {
		return nil, nil, err
	}
	apply, err := seriesManagerAuthorizationState(seriesID, actor, desired)
	if err != nil {
		return nil, nil, err
	}
	compensate, err := seriesManagerAuthorizationState(seriesID, actor, previous)
	if err != nil {
		return nil, nil, err
	}
	return []policyv1.RelationshipMutation{apply}, []policyv1.RelationshipMutation{compensate}, nil
}

func seriesManagerAuthorizationState(seriesID string, actor policyv1.Actor, present bool) (policyv1.RelationshipMutation, error) {
	if present {
		return policyv1.PostSeries.TouchManager(seriesID, actor)
	}
	return policyv1.PostSeries.DeleteManager(seriesID, actor)
}

// AssignPostToSeries assigns a post to a series
