package series

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// SeriesWithManagers is a Series and its managers.
type SeriesWithManagers struct {
	Detail
	Managers []Manager
}

func (s *Service) GetSeriesWithManagers(ctx context.Context, query GetSeriesWithManagersQuery) (SeriesWithManagers, error) {
	if err := s.requireSeriesPermissionOrNotFound(ctx, query.ID, policyv1.PostSeries.View); err != nil {
		return SeriesWithManagers{}, err
	}

	// Get series (admin/manager can see any status)
	var series Series
	var attributions []ManagerAttribution
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		if series, err = loadSeries(ctx, store, query.ID); err != nil {
			return err
		}
		if err := overlaySourceCopy(ctx, store, &series); err != nil {
			return err
		}
		attributions, err = store.ManagerAttributions(ctx, query.ID)
		return err
	}); err != nil {
		return SeriesWithManagers{}, err
	}
	managers, err := s.seriesManagers(ctx, attributions)
	if err != nil {
		return SeriesWithManagers{}, err
	}
	detail, err := s.detail(ctx, series)
	if err != nil {
		return SeriesWithManagers{}, err
	}
	return SeriesWithManagers{Detail: detail, Managers: managers}, nil
}

// ListSeriesManagers returns members of a series
func (s *Service) ListSeriesManagers(ctx context.Context, query ListSeriesManagersQuery) ([]Manager, error) {
	if err := s.requireSeriesPermissionOrNotFound(ctx, query.SeriesID, policyv1.PostSeries.View); err != nil {
		return nil, err
	}
	var attributions []ManagerAttribution
	if err := s.units.Read(ctx, func(store Store) error {
		// Verify series exists
		if _, err := loadSeries(ctx, store, query.SeriesID); err != nil {
			return err
		}
		var err error
		attributions, err = store.ManagerAttributions(ctx, query.SeriesID)
		return err
	}); err != nil {
		return nil, err
	}
	return s.seriesManagers(ctx, attributions)
}

// seriesManagerChange is the policy relationship batch of a manager change
// and its exact inverse.
type seriesManagerChange struct {
	apply, compensate []policyv1.RelationshipMutation
}

func (s *Service) AddSeriesManager(ctx context.Context, command AddSeriesManagerCommand) error {
	var audits seriesAudits
	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[seriesManagerChange, Store]{
		Access: lockedSeries(command.SeriesID, policyv1.PostSeries.ManageParticipants),
		Change: func(ctx context.Context, store Store) (seriesManagerChange, error) {
			present, err := store.ManagerAttributed(ctx, command.SeriesID, command.MemberID)
			if err != nil {
				return seriesManagerChange{}, err
			}
			identityID, err := store.LockManagerTarget(ctx, command.MemberID)
			if err != nil {
				return seriesManagerChange{}, err
			}
			if !present {
				if err := store.UpsertManagerAttribution(ctx, command.SeriesID, command.MemberID, time.Now().UTC()); err != nil {
					return seriesManagerChange{}, err
				}
				audits.manager(command.SeriesID, command.MemberID, sharedtelemetry.AuditRelationshipNone, sharedtelemetry.AuditRelationshipManager)
			}
			var change seriesManagerChange
			change.apply, change.compensate, err = seriesManagerAuthorizationMutations(command.SeriesID, identityID, true, present)
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
	if linkedErr := s.units.Read(ctx, func(store Store) error { return store.LinkedMember(ctx, command.MemberID) }); linkedErr != nil {
		if !mutation.IsNotFound(linkedErr) {
			return linkedErr
		}
		return s.removeRetiredSeriesManagerAttribution(ctx, command.SeriesID, command.MemberID)
	}
	var audits seriesAudits
	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[seriesManagerChange, Store]{
		Access: lockedSeries(command.SeriesID, policyv1.PostSeries.ManageParticipants),
		Change: func(ctx context.Context, store Store) (seriesManagerChange, error) {
			present, err := store.ManagerAttributed(ctx, command.SeriesID, command.MemberID)
			if err != nil {
				return seriesManagerChange{}, err
			}
			if !present {
				return seriesManagerChange{}, domainerr.New(domainerr.NotFound, "", "series manager relation not found")
			}
			identityID, err := store.LockLinkedManagerTarget(ctx, command.MemberID)
			if err != nil {
				if mutation.IsNotFound(err) {
					return seriesManagerChange{}, domainerr.Precondition("series manager target changed; retry").WithCause(err)
				}
				return seriesManagerChange{}, err
			}
			if err := store.DeleteManagerAttribution(ctx, command.SeriesID, command.MemberID); err != nil {
				return seriesManagerChange{}, err
			}
			audits.manager(command.SeriesID, command.MemberID, sharedtelemetry.AuditRelationshipManager, sharedtelemetry.AuditRelationshipNone)
			var change seriesManagerChange
			change.apply, change.compensate, err = seriesManagerAuthorizationMutations(command.SeriesID, identityID, false, true)
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
	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[struct{}, Store]{
		Access: lockedSeries(seriesID, policyv1.PostSeries.ManageParticipants),
		Change: func(ctx context.Context, store Store) (struct{}, error) {
			present, err := store.ManagerAttributed(ctx, seriesID, memberID)
			if err != nil {
				return struct{}{}, err
			}
			if !present {
				return struct{}{}, domainerr.New(domainerr.NotFound, "", "series manager relation not found")
			}
			if _, err := store.LockLinkedManagerTarget(ctx, memberID); err == nil {
				return struct{}{}, domainerr.Precondition("series manager target is still linked; retry")
			} else if !mutation.IsNotFound(err) {
				return struct{}{}, err
			}
			if err := store.DeleteManagerAttribution(ctx, seriesID, memberID); err != nil {
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

type GetSeriesWithManagersQuery struct{ ID string }
type ListSeriesManagersQuery struct{ SeriesID string }
type AddSeriesManagerCommand struct{ SeriesID, MemberID string }
type RemoveSeriesManagerCommand struct{ SeriesID, MemberID string }
