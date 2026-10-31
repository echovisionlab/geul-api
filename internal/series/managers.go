package series

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func (s *Service) GetSeriesWithManagers(ctx context.Context, query GetSeriesWithManagersQuery) (*managev1.GetSeriesWithManagersResponse, error) {
	if err := s.requireSeriesPermissionOrNotFound(ctx, query.ID, policyv1.PostSeries.View); err != nil {
		return nil, err
	}

	// Get series (admin/manager can see any status)
	var series model.Series
	if err := s.db.WithContext(ctx).First(&series, "id = ?", query.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.ResourceNotFound("series", query.ID)
		}
		return nil, err
	}
	if err := s.overlaySeriesSourceLocaleDocument(ctx, &series); err != nil {
		return nil, err
	}

	protoMembers, err := s.loadSeriesManagerProjections(ctx, query.ID)
	if err != nil {
		return nil, err
	}

	ogAsset, err := s.media.ReadyAsset(ctx, series.OgAssetID)
	if err != nil {
		return nil, err
	}
	protoSeries := s.toProtoSeries(&series, ogAsset)
	s.setSeriesFeaturedImageAsset(ctx, protoSeries)

	return &managev1.GetSeriesWithManagersResponse{
		Series:   protoSeries,
		Managers: protoMembers,
	}, nil
}

// ListSeriesManagers returns members of a series
func (s *Service) ListSeriesManagers(ctx context.Context, query ListSeriesManagersQuery) (*managev1.ListSeriesManagersResponse, error) {
	if err := s.requireSeriesPermissionOrNotFound(ctx, query.SeriesID, policyv1.PostSeries.View); err != nil {
		return nil, err
	}

	// Verify series exists
	var series model.Series
	if err := s.db.WithContext(ctx).First(&series, "id = ?", query.SeriesID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.ResourceNotFound("series", query.SeriesID)
		}
		return nil, err
	}

	protoMembers, err := s.loadSeriesManagerProjections(ctx, query.SeriesID)
	if err != nil {
		return nil, err
	}

	return &managev1.ListSeriesManagersResponse{
		Managers: protoMembers,
	}, nil
}

// seriesManagerChange is the policy relationship batch of a manager change
// and its exact inverse.
type seriesManagerChange struct {
	apply, compensate []policyv1.RelationshipMutation
}

func (s *Service) AddSeriesManager(ctx context.Context, command AddSeriesManagerCommand) error {
	var audits seriesAudits
	_, err := mutation.Run(ctx, s.mutations, mutation.Spec[seriesManagerChange]{
		Access: lockedSeries(command.SeriesID, policyv1.PostSeries.ManageParticipants),
		Change: func(ctx context.Context, tx *gorm.DB) (seriesManagerChange, error) {
			present, err := seriesManagerAttributionExists(ctx, tx, command.SeriesID, command.MemberID)
			if err != nil {
				return seriesManagerChange{}, err
			}
			target, err := authorizationtarget.RequireLocked(ctx, tx, command.MemberID)
			if err != nil {
				return seriesManagerChange{}, err
			}
			if !present {
				if err := upsertSeriesManagerAttribution(ctx, tx, command.SeriesID, command.MemberID, time.Now().UTC()); err != nil {
					return seriesManagerChange{}, err
				}
				audits.manager(command.SeriesID, command.MemberID, sharedtelemetry.AuditRelationshipNone, sharedtelemetry.AuditRelationshipManager)
			}
			var change seriesManagerChange
			change.apply, change.compensate, err = seriesManagerAuthorizationMutations(command.SeriesID, target.IdentityID, true, present)
			return change, err
		},
		Audit: func(seriesManagerChange) mutation.Audit { return audits.audit() },
		Relationships: func(change seriesManagerChange) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return change.apply, change.compensate, nil
		},
	})
	return err
}

func (s *Service) RemoveSeriesManager(ctx context.Context, command RemoveSeriesManagerCommand) error {
	// A retired target has no remaining SpiceDB identity relationship. Keep
	// that cleanup on a transaction which performs the same one exact Series
	// permission check but deliberately performs no authorization write.
	if _, linkedErr := authorizationtarget.RequireLinkedMember(ctx, s.db, command.MemberID, false); linkedErr != nil {
		if !mutation.IsNotFound(linkedErr) {
			return linkedErr
		}
		return s.removeRetiredSeriesManagerAttribution(ctx, command.SeriesID, command.MemberID)
	}
	var audits seriesAudits
	_, err := mutation.Run(ctx, s.mutations, mutation.Spec[seriesManagerChange]{
		Access: lockedSeries(command.SeriesID, policyv1.PostSeries.ManageParticipants),
		Change: func(ctx context.Context, tx *gorm.DB) (seriesManagerChange, error) {
			present, err := seriesManagerAttributionExists(ctx, tx, command.SeriesID, command.MemberID)
			if err != nil {
				return seriesManagerChange{}, err
			}
			if !present {
				return seriesManagerChange{}, domainerr.New(domainerr.NotFound, "", "series manager relation not found")
			}
			target, err := authorizationtarget.RequireLockedLinked(ctx, tx, command.MemberID)
			if err != nil {
				if mutation.IsNotFound(err) {
					return seriesManagerChange{}, domainerr.Precondition("series manager target changed; retry").WithCause(err)
				}
				return seriesManagerChange{}, err
			}
			if err := deleteSeriesManagerAttribution(ctx, tx, command.SeriesID, command.MemberID); err != nil {
				return seriesManagerChange{}, err
			}
			audits.manager(command.SeriesID, command.MemberID, sharedtelemetry.AuditRelationshipManager, sharedtelemetry.AuditRelationshipNone)
			var change seriesManagerChange
			change.apply, change.compensate, err = seriesManagerAuthorizationMutations(command.SeriesID, target.IdentityID, false, true)
			return change, err
		},
		Audit: func(seriesManagerChange) mutation.Audit { return audits.audit() },
		Relationships: func(change seriesManagerChange) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return change.apply, change.compensate, nil
		},
	})
	return err
}

// removeRetiredSeriesManagerAttribution clears the product-owned record after
// account deletion has already removed every identity relationship from
// SpiceDB. There is deliberately no authorization write to compensate.
func (s *Service) removeRetiredSeriesManagerAttribution(ctx context.Context, seriesID, memberID string) error {
	var audits seriesAudits
	_, err := mutation.Run(ctx, s.mutations, mutation.Spec[struct{}]{
		Access: lockedSeries(seriesID, policyv1.PostSeries.ManageParticipants),
		Change: func(ctx context.Context, tx *gorm.DB) (struct{}, error) {
			present, err := seriesManagerAttributionExists(ctx, tx, seriesID, memberID)
			if err != nil {
				return struct{}{}, err
			}
			if !present {
				return struct{}{}, domainerr.New(domainerr.NotFound, "", "series manager relation not found")
			}
			if _, err := authorizationtarget.RequireLockedLinked(ctx, tx, memberID); err == nil {
				return struct{}{}, domainerr.Precondition("series manager target is still linked; retry")
			} else if !mutation.IsNotFound(err) {
				return struct{}{}, err
			}
			if err := deleteSeriesManagerAttribution(ctx, tx, seriesID, memberID); err != nil {
				return struct{}{}, err
			}
			audits.manager(seriesID, memberID, sharedtelemetry.AuditRelationshipManager, sharedtelemetry.AuditRelationshipNone)
			return struct{}{}, nil
		},
		Audit: func(struct{}) mutation.Audit { return audits.audit() },
	})
	return err
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

type GetSeriesWithManagersQuery struct{ ID string }
type ListSeriesManagersQuery struct{ SeriesID string }
type AddSeriesManagerCommand struct{ SeriesID, MemberID string }
type RemoveSeriesManagerCommand struct{ SeriesID, MemberID string }
