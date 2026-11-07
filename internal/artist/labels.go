package artist

import (
	"context"
	"log/slog"
	"slices"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// SetArtistLabelsCommand replaces the Labels the Artist belongs to.
type SetArtistLabelsCommand struct {
	ArtistID string
	LabelIDs []string
}

// SetArtistLabels replaces the Artist's Labels. A Label's people may edit and
// publish an Artist that belongs to it, so this is a participant change: the
// caller must manage the participants of the Artist and of every Label joined
// or left.
func (s *Service) SetArtistLabels(ctx context.Context, command SetArtistLabelsCommand) (_ []string, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("artist_id", command.ArtistID))

	next, err := normalizeArtistLabelIDs(command.LabelIDs)
	if err != nil {
		return nil, err
	}
	var previous []string
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		previous, err = store.LabelIDs(ctx, command.ArtistID)
		return err
	}); err != nil {
		return nil, err
	}
	if slices.Equal(previous, next) {
		return next, artistEntity.RequirePermission(ctx, s.spiceDB, command.ArtistID, policyv1.Artist.ManageParticipants)
	}
	joined, left := labelChanges(previous, next)
	_, err = mutation.Run(ctx, s.mutations, mutation.StoreSpec[struct{}, Store]{
		Lock: func(ctx context.Context, store Store) error {
			return artistEntity.LockRoot(ctx, store, command.ArtistID)
		},
		Access: mutation.Fenced(func(ctx context.Context, store Store, lockActive func(*auth.UserInfo) (bool, error)) error {
			current, err := store.LabelIDs(ctx, command.ArtistID)
			if err != nil {
				return err
			}
			if !slices.Equal(current, previous) {
				return domainerr.Precondition("Artist Labels changed concurrently; reload before saving")
			}
			return s.requireLabelChangeAuthority(ctx, lockActive, command.ArtistID, append(slices.Clone(joined), left...))
		}),
		Change: func(ctx context.Context, store Store) (struct{}, error) {
			if err := validateArtistLabelIDsExist(ctx, store, next); err != nil {
				return struct{}{}, err
			}
			return struct{}{}, store.ReplaceLabelIDs(ctx, command.ArtistID, next)
		},
		// Telemetry has no Artist Label record yet.
		Audit: func(struct{}) mutation.Audit { return mutation.Unchanged() },
		Outbox: func(ctx context.Context, store Store, _ struct{}) error {
			return store.PublishContentUpdate(ctx, ContentUpdate{ArtistID: command.ArtistID, Kind: StateUpdate, StatePaths: []string{"label_ids"}})
		},
		Relationships: func(struct{}) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return artistLabelRelationships(command.ArtistID, joined, left)
		},
	})
	if err != nil {
		return nil, artistEntity.MapMutationError(err)
	}
	return next, nil
}

func (s *Service) requireLabelChangeAuthority(ctx context.Context, lockActive func(*auth.UserInfo) (bool, error), artistID string, labelIDs []string) error {
	principal, err := artistEntity.LockActivePrincipal(ctx, lockActive)
	if err != nil {
		return err
	}
	if err := artistEntity.RequirePrincipalPermission(ctx, s.spiceDB, artistID, policyv1.Artist.ManageParticipants, principal); err != nil {
		return err
	}
	for _, labelID := range labelIDs {
		allowed, err := entityparticipant.CheckPermissionForPrincipal(ctx, s.spiceDB, labelID, policyv1.Label.ManageParticipants, principal)
		if err != nil {
			return domainerr.DependencyUnavailable("SpiceDB").WithCause(failurecause.WithAttrs(err, slog.String("label_id", labelID)))
		}
		if !allowed {
			return domainerr.ResourceNotFound("label", labelID)
		}
	}
	return nil
}

func normalizeArtistLabelIDs(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		parsed, err := uuid.Parse(strings.TrimSpace(raw))
		if err != nil {
			return nil, domainerr.InvalidField("label_ids", "must contain valid Label IDs")
		}
		id := parsed.String()
		if _, exists := seen[id]; exists {
			return nil, domainerr.InvalidField("label_ids", "must contain unique Label IDs")
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	sort.Strings(result)
	return result, nil
}

// validateArtistLabelIDsExist requires every Label of labelIDs to exist.
func validateArtistLabelIDsExist(ctx context.Context, store Store, labelIDs []string) error {
	if len(labelIDs) == 0 {
		return nil
	}
	existing, err := store.LabelTitles(ctx, labelIDs)
	if err != nil {
		return err
	}
	if len(existing) != len(labelIDs) {
		return domainerr.InvalidField("label_ids", "contains a Label that does not exist")
	}
	return nil
}

// labelChanges compares two sorted Label ID lists.
func labelChanges(previous, next []string) (joined, left []string) {
	for _, id := range next {
		if !slices.Contains(previous, id) {
			joined = append(joined, id)
		}
	}
	for _, id := range previous {
		if !slices.Contains(next, id) {
			left = append(left, id)
		}
	}
	return joined, left
}

// artistLabelRelationships returns the SpiceDB writes of an Artist joining and
// leaving Labels, with their inverse.
func artistLabelRelationships(artistID string, joined, left []string) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
	apply := make([]policyv1.RelationshipMutation, 0, len(joined)+len(left))
	compensate := make([]policyv1.RelationshipMutation, 0, len(joined)+len(left))
	for _, labelID := range joined {
		touch, remove, err := artistLabelMutationPair(artistID, labelID)
		if err != nil {
			return nil, nil, err
		}
		apply = append(apply, touch)
		compensate = append(compensate, remove)
	}
	for _, labelID := range left {
		touch, remove, err := artistLabelMutationPair(artistID, labelID)
		if err != nil {
			return nil, nil, err
		}
		apply = append(apply, remove)
		compensate = append(compensate, touch)
	}
	return apply, compensate, nil
}

func artistLabelMutationPair(artistID, labelID string) (touch, remove policyv1.RelationshipMutation, err error) {
	if touch, err = policyv1.Artist.TouchLabel(artistID, labelID); err != nil {
		return touch, remove, err
	}
	remove, err = policyv1.Artist.DeleteLabel(artistID, labelID)
	return touch, remove, err
}

// artistDeletionRelationships maps the Artist's relationship snapshot to its
// deletes and their inverse, its Labels among them.
func artistDeletionRelationships(artistID string, snapshots []auth.RelationshipSnapshot) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
	var labelIDs []string
	entity := make([]auth.RelationshipSnapshot, 0, len(snapshots))
	for _, snapshot := range snapshots {
		if snapshot.ResourceID() == artistID && snapshot.SubjectType() == "label" {
			labelIDs = append(labelIDs, snapshot.SubjectID())
			continue
		}
		entity = append(entity, snapshot)
	}
	deleted, restored, err := artistEntity.DeletionRelationshipMutations(artistID, entity)
	if err != nil {
		return nil, nil, err
	}
	removed, touched, err := artistLabelRelationships(artistID, nil, labelIDs)
	if err != nil {
		return nil, nil, err
	}
	return append(deleted, removed...), append(restored, touched...), nil
}
