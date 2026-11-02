package post

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// PostParticipantsQuery lists a Post's durable authors and collaborators.
type PostParticipantsQuery struct{ PostID string }

// PostParticipantCommand adds or removes one participant of a Post.
type PostParticipantCommand struct{ PostID, MemberID string }

// ListPostParticipants returns durable authors and collaborators separately.
func (s *Service) ListPostParticipants(ctx context.Context, query PostParticipantsQuery) (_ []Participant, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", query.PostID))

	var status Status
	if err := s.readLocked(ctx, mutation.Checked(func(ctx context.Context, store Store) error {
		stored, err := store.KeyShareLockPost(ctx, query.PostID)
		if err != nil {
			if errors.Is(err, ErrPostNotFound) {
				return domainerr.ResourceNotFound("post", query.PostID)
			}
			return err
		}
		status = stored.Status
		_, err = requirePostViewForStatus(ctx, s.spiceDB, stored.ID, stored.Status)
		return err
	}), func(context.Context, Store) error { return nil }); err != nil {
		return nil, err
	}

	rows, err := readStore(ctx, s, func(store Store) ([]ParticipantRow, error) { return store.ListParticipants(ctx, query.PostID) })
	if err != nil {
		return nil, err
	}
	memberIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		memberIDs = append(memberIDs, row.MemberID)
	}
	summaries, err := s.members.LoadMemberSummaries(ctx, memberIDs)
	if err != nil {
		return nil, err
	}
	participants := make([]Participant, 0, len(rows))
	action := postActionForStatus(status, policyv1.Post.Edit, policyv1.Post.EditArchived)
	can, err := action(query.PostID)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		summary := summaries[row.MemberID]
		if summary == nil {
			return nil, errors.New("post participant member was not found")
		}
		effective := row.HasEffectiveAuthority
		if effective {
			actor, actorErr := policyv1.NewAccountIdentityActor(row.IdentityID)
			if actorErr != nil {
				return nil, actorErr
			}
			ok, checkErr := s.spiceDB.CheckActorCan(ctx, actor, can)
			if checkErr != nil {
				return nil, domainerr.DependencyUnavailable("SpiceDB").WithCause(checkErr)
			}
			effective = ok
		}
		participants = append(participants, Participant{
			Member:                summary,
			Role:                  row.Role,
			CreatedAt:             row.CreatedAt,
			HasEffectiveAuthority: effective,
		})
	}
	return participants, nil
}

// postParticipantChange is the outcome of a participant mutation: the
// relationship batch the role change writes and the role's creation time.
type postParticipantChange struct {
	apply      []policyv1.RelationshipMutation
	compensate []policyv1.RelationshipMutation
	createdAt  time.Time
}

func (change postParticipantChange) relationships() ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
	return change.apply, change.compensate, nil
}

// runPostParticipantChange runs change on postID's participants with the
// access its locked state requires, writing the role's relationships.
func (s *Service) runPostParticipantChange(
	ctx context.Context,
	access mutation.Access,
	change func(context.Context, Store, *postEffects) (postParticipantChange, error),
) (postParticipantChange, error) {
	if auth.GetUser(ctx) == nil {
		return postParticipantChange{}, domainerr.AuthenticationRequired()
	}
	result, _, err := runPostMutation(ctx, s, mutation.StoreSpec[postParticipantChange, Store]{
		Access:        access,
		Relationships: postParticipantChange.relationships,
	}, change)
	if err != nil {
		return postParticipantChange{}, mapPostParticipantMutationError(err)
	}
	return result, nil
}

// roleChange records the move of memberID from previous to desired and
// returns its relationship batch.
func roleChange(effects *postEffects, postID, memberID string, actor policyv1.Actor, desired, previous ParticipantRole, createdAt time.Time) (postParticipantChange, error) {
	apply, compensate, err := postParticipantAuthorizationMutations(postID, actor, desired, previous)
	if err != nil {
		return postParticipantChange{}, err
	}
	if desired != previous {
		appendPostParticipantAudit(effects, postID, memberID, postParticipantAuditRelationship(previous), postParticipantAuditRelationship(desired))
	}
	return postParticipantChange{apply: apply, compensate: compensate, createdAt: createdAt}, nil
}

func (s *Service) AddPostAuthor(ctx context.Context, command PostParticipantCommand) (_ Participant, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", command.PostID))

	change, err := s.runPostParticipantChange(ctx, lockedPost(command.PostID, policyv1.Post.ManageParticipants, nil),
		func(ctx context.Context, store Store, effects *postEffects) (postParticipantChange, error) {
			return s.addPostAuthor(ctx, store, effects, command)
		})
	if err != nil {
		return Participant{}, err
	}
	return s.postParticipant(ctx, command.MemberID, RoleAuthor, change.createdAt, true)
}

func (s *Service) addPostAuthor(ctx context.Context, store Store, effects *postEffects, command PostParticipantCommand) (postParticipantChange, error) {
	targetIdentityID, err := store.LockMemberIdentity(ctx, command.MemberID)
	if err != nil {
		return postParticipantChange{}, err
	}
	targetActor, err := policyv1.NewAccountIdentityActor(targetIdentityID)
	if err != nil {
		return postParticipantChange{}, err
	}
	can, err := policyv1.Platform.IsAuthor()
	if err != nil {
		return postParticipantChange{}, err
	}
	canAuthor, err := s.spiceDB.CheckActorCan(ctx, targetActor, can)
	if err != nil {
		return postParticipantChange{}, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if !canAuthor {
		return postParticipantChange{}, domainerr.InvalidField("member_id", "post authors must have the site Author or Admin role")
	}
	existing, err := store.ParticipantCreatedAt(ctx, command.PostID, command.MemberID, RoleAuthor)
	if err != nil {
		return postParticipantChange{}, err
	}
	if !existing.IsZero() {
		return roleChange(effects, command.PostID, command.MemberID, targetActor, RoleAuthor, RoleAuthor, existing)
	}
	collaboratorExists, err := store.ParticipantExists(ctx, command.PostID, command.MemberID, RoleCollaborator)
	if err != nil {
		return postParticipantChange{}, err
	}
	// Author and Collaborator are mutually exclusive peer roles. Reassigning
	// Collaborator -> Author changes both product attribution rows before the
	// exact SpiceDB state transition is applied.
	if _, err := store.DeleteParticipant(ctx, command.PostID, command.MemberID, RoleCollaborator); err != nil {
		return postParticipantChange{}, err
	}
	createdAt := dbtime.Now()
	if err := store.InsertParticipant(ctx, command.PostID, command.MemberID, RoleAuthor, createdAt); err != nil {
		return postParticipantChange{}, err
	}
	previous := RoleNone
	if collaboratorExists {
		previous = RoleCollaborator
	}
	return roleChange(effects, command.PostID, command.MemberID, targetActor, RoleAuthor, previous, createdAt)
}

func (s *Service) RemovePostAuthor(ctx context.Context, command PostParticipantCommand) (returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", command.PostID))

	_, err := s.runPostParticipantChange(ctx, lockedPost(command.PostID, policyv1.Post.RemoveAuthor, nil),
		func(ctx context.Context, store Store, effects *postEffects) (postParticipantChange, error) {
			authorExists, err := store.CountAuthors(ctx, command.PostID, command.MemberID)
			if err != nil {
				return postParticipantChange{}, err
			}
			if authorExists == 0 {
				return postParticipantChange{}, domainerr.New(domainerr.NotFound, "", "post author relation not found")
			}
			if err := requireAnotherPostAuthor(ctx, store, command.PostID); err != nil {
				return postParticipantChange{}, err
			}
			targetActor, err := lockedLinkedParticipant(ctx, store, command.MemberID)
			if err != nil {
				return postParticipantChange{}, err
			}
			if _, err := store.DeleteParticipant(ctx, command.PostID, command.MemberID, RoleAuthor); err != nil {
				return postParticipantChange{}, err
			}
			return roleChange(effects, command.PostID, command.MemberID, targetActor, RoleNone, RoleAuthor, time.Time{})
		})
	return err
}

// requireAnotherPostAuthor keeps a Post from losing its last durable author.
func requireAnotherPostAuthor(ctx context.Context, store Store, postID string) error {
	authorCount, err := store.CountAuthors(ctx, postID, "")
	if err != nil {
		return err
	}
	if authorCount <= 1 {
		return domainerr.Precondition("a post must retain at least one durable author")
	}
	return nil
}

func lockedLinkedParticipant(ctx context.Context, store Store, memberID string) (policyv1.Actor, error) {
	identityID, err := store.LockLinkedMemberIdentity(ctx, memberID)
	if err != nil {
		return policyv1.Actor{}, err
	}
	return policyv1.NewAccountIdentityActor(identityID)
}

// collaboratorAccess is the access of adding a collaborator: reassigning an
// author needs the author removal action, a new participant the
// participant management action.
func collaboratorAccess(command PostParticipantCommand, authorExists *bool) mutation.Access {
	return mutation.Locked(func(ctx context.Context, store Store, require mutation.Require) error {
		status, err := lockPostParticipantRoot(ctx, store, command.PostID)
		if err != nil {
			return err
		}
		if *authorExists, err = store.ParticipantExists(ctx, command.PostID, command.MemberID, RoleAuthor); err != nil {
			return err
		}
		action := policyv1.Post.ManageParticipants
		if *authorExists {
			action = policyv1.Post.RemoveAuthor
		}
		_, err = requireLockedPostActionForStatus(require, command.PostID, status, action)
		return err
	})
}

func (s *Service) AddPostCollaborator(ctx context.Context, command PostParticipantCommand) (_ Participant, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", command.PostID))

	var authorExists bool
	change, err := s.runPostParticipantChange(ctx, collaboratorAccess(command, &authorExists),
		func(ctx context.Context, store Store, effects *postEffects) (postParticipantChange, error) {
			return addPostCollaborator(ctx, store, effects, command, authorExists)
		})
	if err != nil {
		return Participant{}, err
	}
	return s.postParticipant(ctx, command.MemberID, RoleCollaborator, change.createdAt, true)
}

func addPostCollaborator(ctx context.Context, store Store, effects *postEffects, command PostParticipantCommand, authorExists bool) (postParticipantChange, error) {
	existing, err := store.ParticipantCreatedAt(ctx, command.PostID, command.MemberID, RoleCollaborator)
	if err != nil {
		return postParticipantChange{}, err
	}
	targetIdentityID, err := store.LockMemberIdentity(ctx, command.MemberID)
	if err != nil {
		return postParticipantChange{}, err
	}
	targetActor, err := policyv1.NewAccountIdentityActor(targetIdentityID)
	if err != nil {
		return postParticipantChange{}, err
	}
	if !existing.IsZero() {
		return roleChange(effects, command.PostID, command.MemberID, targetActor, RoleCollaborator, RoleCollaborator, existing)
	}
	previous := RoleNone
	if authorExists {
		if err := requireAnotherPostAuthor(ctx, store, command.PostID); err != nil {
			return postParticipantChange{}, err
		}
		// Author and Collaborator are mutually exclusive peer roles. Reassign
		// the Author to Collaborator in this locked transaction.
		if _, err := store.DeleteParticipant(ctx, command.PostID, command.MemberID, RoleAuthor); err != nil {
			return postParticipantChange{}, err
		}
		previous = RoleAuthor
	}
	createdAt := dbtime.Now()
	if err := store.InsertParticipant(ctx, command.PostID, command.MemberID, RoleCollaborator, createdAt); err != nil {
		return postParticipantChange{}, err
	}
	return roleChange(effects, command.PostID, command.MemberID, targetActor, RoleCollaborator, previous, createdAt)
}

func (s *Service) RemovePostCollaborator(ctx context.Context, command PostParticipantCommand) (returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", command.PostID))

	_, err := s.runPostParticipantChange(ctx, lockedPost(command.PostID, policyv1.Post.ManageParticipants, nil),
		func(ctx context.Context, store Store, effects *postEffects) (postParticipantChange, error) {
			targetActor, err := lockedLinkedParticipant(ctx, store, command.MemberID)
			if err != nil {
				return postParticipantChange{}, err
			}
			removed, err := store.DeleteParticipant(ctx, command.PostID, command.MemberID, RoleCollaborator)
			if err != nil {
				return postParticipantChange{}, err
			}
			if removed == 0 {
				return postParticipantChange{}, domainerr.New(domainerr.NotFound, "", "post collaborator relation not found")
			}
			return roleChange(effects, command.PostID, command.MemberID, targetActor, RoleNone, RoleCollaborator, time.Time{})
		})
	return err
}

func postParticipantAuthorizationMutations(
	postID string,
	actor policyv1.Actor,
	desired ParticipantRole,
	previous ParticipantRole,
) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
	apply, err := postParticipantAuthorizationState(postID, actor, desired)
	if err != nil {
		return nil, nil, err
	}
	compensate, err := postParticipantAuthorizationState(postID, actor, previous)
	if err != nil {
		return nil, nil, err
	}
	return apply, compensate, nil
}

func postParticipantAuthorizationState(
	postID string,
	actor policyv1.Actor,
	role ParticipantRole,
) ([]policyv1.RelationshipMutation, error) {
	if role != RoleNone && role != RoleAuthor && role != RoleCollaborator {
		return nil, domainerr.InvalidField("participant_role", "is not supported")
	}
	var authorMutation policyv1.RelationshipMutation
	var collaboratorMutation policyv1.RelationshipMutation
	var err error
	if role == RoleAuthor {
		authorMutation, err = policyv1.Post.TouchAuthor(postID, actor)
	} else {
		authorMutation, err = policyv1.Post.DeleteAuthor(postID, actor)
	}
	if err != nil {
		return nil, err
	}
	if role == RoleCollaborator {
		collaboratorMutation, err = policyv1.Post.TouchCollaborator(postID, actor)
	} else {
		collaboratorMutation, err = policyv1.Post.DeleteCollaborator(postID, actor)
	}
	if err != nil {
		return nil, err
	}
	return []policyv1.RelationshipMutation{authorMutation, collaboratorMutation}, nil
}

func postParticipantAuditRelationship(role ParticipantRole) sharedtelemetry.AuditRelationship {
	switch role {
	case RoleAuthor:
		return sharedtelemetry.AuditRelationshipAuthor
	case RoleCollaborator:
		return sharedtelemetry.AuditRelationshipCollaborator
	default:
		return sharedtelemetry.AuditRelationshipNone
	}
}

func appendPostParticipantAudit(effects *postEffects, postID, memberID string, previous, next sharedtelemetry.AuditRelationship) {
	effects.updated(func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewPostParticipantAuditRecord(metadata, postID, memberID, previous, next)
	})
}

func (s *Service) postParticipant(
	ctx context.Context,
	memberID string,
	role ParticipantRole,
	createdAt time.Time,
	effective bool,
) (Participant, error) {
	summaries, err := s.members.LoadMemberSummaries(ctx, []string{memberID})
	if err != nil {
		return Participant{}, err
	}
	if summaries[memberID] == nil {
		return Participant{}, domainerr.ResourceNotFound("member", memberID)
	}
	return Participant{
		Member:                summaries[memberID],
		Role:                  role,
		CreatedAt:             createdAt,
		HasEffectiveAuthority: effective,
	}, nil
}
