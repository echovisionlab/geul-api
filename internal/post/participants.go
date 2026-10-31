package post

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/identityread"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// PostParticipantsQuery lists a Post's durable authors and collaborators.
type PostParticipantsQuery struct{ PostID string }

// PostParticipantCommand adds or removes one participant of a Post.
type PostParticipantCommand struct{ PostID, MemberID string }

type postParticipantRow struct {
	MemberID              string    `gorm:"column:member_id"`
	IdentityID            string    `gorm:"column:identity_id"`
	Role                  string    `gorm:"column:role"`
	CreatedAt             time.Time `gorm:"column:created_at"`
	HasEffectiveAuthority bool      `gorm:"column:has_effective_authority"`
}

// ListPostParticipants returns durable authors and collaborators separately.
func (s *Service) ListPostParticipants(ctx context.Context, query PostParticipantsQuery) (_ *managev1.ListPostParticipantsResponse, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", query.PostID))

	var post model.Post
	if err := s.readLocked(ctx, mutation.Checked(func(ctx context.Context, tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "KEY SHARE"}).First(&post, "id = ?", query.PostID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domainerr.ResourceNotFound("post", query.PostID)
			}
			return err
		}
		_, err := requirePostViewForStatus(ctx, s.spiceDB, post.ID, post.Status)
		return err
	}), func(context.Context, *gorm.DB) error { return nil }); err != nil {
		return nil, err
	}

	var rows []postParticipantRow
	if err := s.db.WithContext(ctx).Raw(`
		SELECT author.member_id::text AS member_id,
		       identity.id::text AS identity_id,
		       'author' AS role,
		       author.created_at,
		       CASE
		         WHEN identity.id IS NULL THEN FALSE
		         WHEN member.deleted_at IS NOT NULL OR member.onboarded = FALSE THEN FALSE
		         WHEN identity.state <> 'active' THEN FALSE
		         WHEN COALESCE((identity.metadata_admin ->> 'banned')::boolean, false) THEN FALSE
		         ELSE TRUE
		       END AS has_effective_authority
		FROM post_author AS author
		JOIN member ON member.id = author.member_id
		`+identityread.LeftJoinLinked("member", "identity")+`
		WHERE author.post_id = ?::uuid
		UNION ALL
		SELECT collaborator.member_id::text AS member_id,
		       identity.id::text AS identity_id,
		       'collaborator' AS role,
		       collaborator.created_at,
		       CASE
		         WHEN identity.id IS NULL THEN FALSE
		         WHEN member.deleted_at IS NOT NULL OR member.onboarded = FALSE THEN FALSE
		         WHEN identity.state <> 'active' THEN FALSE
		         WHEN COALESCE((identity.metadata_admin ->> 'banned')::boolean, false) THEN FALSE
		         ELSE TRUE
		       END AS has_effective_authority
		FROM post_collaborator AS collaborator
		JOIN member ON member.id = collaborator.member_id
		`+identityread.LeftJoinLinked("member", "identity")+`
		WHERE collaborator.post_id = ?::uuid
		ORDER BY role ASC, created_at ASC, member_id ASC
	`, query.PostID, query.PostID).Scan(&rows).Error; err != nil {
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
	participants := make([]*managev1.PostParticipant, 0, len(rows))
	action := postActionForStatus(post.Status, policyv1.Post.Edit, policyv1.Post.EditArchived)
	can, err := action(query.PostID)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		summary := summaries[row.MemberID]
		if summary == nil {
			return nil, errors.New("post participant member was not found")
		}
		role := managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_COLLABORATOR
		if row.Role == "author" {
			role = managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_AUTHOR
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
		participants = append(participants, &managev1.PostParticipant{
			Member:                summary,
			Role:                  role,
			CreatedAt:             timestamppb.New(row.CreatedAt),
			HasEffectiveAuthority: effective,
		})
	}
	return &managev1.ListPostParticipantsResponse{Participants: participants}, nil
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
	change func(context.Context, *gorm.DB, *postEffects) (postParticipantChange, error),
) (postParticipantChange, error) {
	if auth.GetUser(ctx) == nil {
		return postParticipantChange{}, domainerr.AuthenticationRequired()
	}
	result, _, err := runPostMutation(ctx, s, mutation.Spec[postParticipantChange]{
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
func roleChange(effects *postEffects, postID, memberID string, actor policyv1.Actor, desired, previous managev1.PostParticipantRole, createdAt time.Time) (postParticipantChange, error) {
	apply, compensate, err := postParticipantAuthorizationMutations(postID, actor, desired, previous)
	if err != nil {
		return postParticipantChange{}, err
	}
	if desired != previous {
		appendPostParticipantAudit(effects, postID, memberID, postParticipantAuditRelationship(previous), postParticipantAuditRelationship(desired))
	}
	return postParticipantChange{apply: apply, compensate: compensate, createdAt: createdAt}, nil
}

func (s *Service) AddPostAuthor(ctx context.Context, command PostParticipantCommand) (_ *managev1.PostParticipant, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", command.PostID))

	change, err := s.runPostParticipantChange(ctx, lockedPost(command.PostID, policyv1.Post.ManageParticipants, nil),
		func(ctx context.Context, tx *gorm.DB, effects *postEffects) (postParticipantChange, error) {
			return s.addPostAuthor(ctx, tx, effects, command)
		})
	if err != nil {
		return nil, err
	}
	return s.postParticipantResponse(ctx, command.MemberID, managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_AUTHOR, change.createdAt, true)
}

func (s *Service) addPostAuthor(ctx context.Context, tx *gorm.DB, effects *postEffects, command PostParticipantCommand) (postParticipantChange, error) {
	target, err := authorizationtarget.RequireLocked(ctx, tx, command.MemberID)
	if err != nil {
		return postParticipantChange{}, err
	}
	targetActor, err := policyv1.NewAccountIdentityActor(target.IdentityID)
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
	existing, err := postRelationCreatedAt(tx, "post_author", command.PostID, command.MemberID)
	if err != nil {
		return postParticipantChange{}, err
	}
	author := managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_AUTHOR
	if !existing.IsZero() {
		return roleChange(effects, command.PostID, command.MemberID, targetActor, author, author, existing)
	}
	collaboratorExists, err := postRelationExists(ctx, tx, "post_collaborator", command.PostID, command.MemberID)
	if err != nil {
		return postParticipantChange{}, err
	}
	// Author and Collaborator are mutually exclusive peer roles. Reassigning
	// Collaborator -> Author changes both product attribution rows before the
	// exact SpiceDB state transition is applied.
	if err := tx.Exec(
		"DELETE FROM post_collaborator WHERE post_id = ?::uuid AND member_id = ?::uuid",
		command.PostID, command.MemberID,
	).Error; err != nil {
		return postParticipantChange{}, err
	}
	createdAt := dbtime.Now()
	if err := tx.Exec(`
		INSERT INTO post_author (post_id, member_id, created_at)
		VALUES (?::uuid, ?::uuid, ?)
	`, command.PostID, command.MemberID, createdAt).Error; err != nil {
		return postParticipantChange{}, err
	}
	previous := managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_UNSPECIFIED
	if collaboratorExists {
		previous = managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_COLLABORATOR
	}
	return roleChange(effects, command.PostID, command.MemberID, targetActor, author, previous, createdAt)
}

// postRelationCreatedAt returns when memberID joined table's relation to
// postID, or the zero time.
func postRelationCreatedAt(tx *gorm.DB, table, postID, memberID string) (time.Time, error) {
	var existing struct {
		CreatedAt time.Time `gorm:"column:created_at"`
	}
	err := tx.Table(table).
		Select("created_at").
		Where("post_id = ?::uuid AND member_id = ?::uuid", postID, memberID).
		Scan(&existing).Error
	return existing.CreatedAt, err
}

func (s *Service) RemovePostAuthor(ctx context.Context, command PostParticipantCommand) (returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", command.PostID))

	_, err := s.runPostParticipantChange(ctx, lockedPost(command.PostID, policyv1.Post.RemoveAuthor, nil),
		func(ctx context.Context, tx *gorm.DB, effects *postEffects) (postParticipantChange, error) {
			var authorExists int64
			if err := tx.Table("post_author").
				Where("post_id = ?::uuid AND member_id = ?::uuid", command.PostID, command.MemberID).
				Count(&authorExists).Error; err != nil {
				return postParticipantChange{}, err
			}
			if authorExists == 0 {
				return postParticipantChange{}, domainerr.New(domainerr.NotFound, "", "post author relation not found")
			}
			if err := requireAnotherPostAuthor(tx, command.PostID); err != nil {
				return postParticipantChange{}, err
			}
			targetActor, err := lockedLinkedParticipant(ctx, tx, command.MemberID)
			if err != nil {
				return postParticipantChange{}, err
			}
			if err := tx.Exec(
				"DELETE FROM post_author WHERE post_id = ?::uuid AND member_id = ?::uuid",
				command.PostID, command.MemberID,
			).Error; err != nil {
				return postParticipantChange{}, err
			}
			return roleChange(effects, command.PostID, command.MemberID, targetActor,
				managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_UNSPECIFIED,
				managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_AUTHOR, time.Time{})
		})
	return err
}

// requireAnotherPostAuthor keeps a Post from losing its last durable author.
func requireAnotherPostAuthor(tx *gorm.DB, postID string) error {
	var authorCount int64
	if err := tx.Table("post_author").Where("post_id = ?::uuid", postID).Count(&authorCount).Error; err != nil {
		return err
	}
	if authorCount <= 1 {
		return domainerr.Precondition("a post must retain at least one durable author")
	}
	return nil
}

func lockedLinkedParticipant(ctx context.Context, tx *gorm.DB, memberID string) (policyv1.Actor, error) {
	target, err := authorizationtarget.RequireLockedLinked(ctx, tx, memberID)
	if err != nil {
		return policyv1.Actor{}, err
	}
	return policyv1.NewAccountIdentityActor(target.IdentityID)
}

// collaboratorAccess is the access of adding a collaborator: reassigning an
// author needs the author removal action, a new participant the
// participant management action.
func collaboratorAccess(command PostParticipantCommand, authorExists *bool) mutation.Access {
	return mutation.Locked(func(ctx context.Context, tx *gorm.DB, require mutation.Require) error {
		status, err := lockPostParticipantRoot(ctx, tx, command.PostID)
		if err != nil {
			return err
		}
		if *authorExists, err = postRelationExists(ctx, tx, "post_author", command.PostID, command.MemberID); err != nil {
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

func (s *Service) AddPostCollaborator(ctx context.Context, command PostParticipantCommand) (_ *managev1.PostParticipant, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", command.PostID))

	var authorExists bool
	change, err := s.runPostParticipantChange(ctx, collaboratorAccess(command, &authorExists),
		func(ctx context.Context, tx *gorm.DB, effects *postEffects) (postParticipantChange, error) {
			return addPostCollaborator(ctx, tx, effects, command, authorExists)
		})
	if err != nil {
		return nil, err
	}
	return s.postParticipantResponse(ctx, command.MemberID, managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_COLLABORATOR, change.createdAt, true)
}

func addPostCollaborator(ctx context.Context, tx *gorm.DB, effects *postEffects, command PostParticipantCommand, authorExists bool) (postParticipantChange, error) {
	existing, err := postRelationCreatedAt(tx, "post_collaborator", command.PostID, command.MemberID)
	if err != nil {
		return postParticipantChange{}, err
	}
	target, err := authorizationtarget.RequireLocked(ctx, tx, command.MemberID)
	if err != nil {
		return postParticipantChange{}, err
	}
	targetActor, err := policyv1.NewAccountIdentityActor(target.IdentityID)
	if err != nil {
		return postParticipantChange{}, err
	}
	collaborator := managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_COLLABORATOR
	if !existing.IsZero() {
		return roleChange(effects, command.PostID, command.MemberID, targetActor, collaborator, collaborator, existing)
	}
	previous := managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_UNSPECIFIED
	if authorExists {
		if err := requireAnotherPostAuthor(tx, command.PostID); err != nil {
			return postParticipantChange{}, err
		}
		// Author and Collaborator are mutually exclusive peer roles. Reassign
		// the Author to Collaborator in this locked transaction.
		if err := tx.Exec(
			"DELETE FROM post_author WHERE post_id = ?::uuid AND member_id = ?::uuid",
			command.PostID, command.MemberID,
		).Error; err != nil {
			return postParticipantChange{}, err
		}
		previous = managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_AUTHOR
	}
	createdAt := dbtime.Now()
	if err := tx.Exec(`
		INSERT INTO post_collaborator (post_id, member_id, created_at)
		VALUES (?::uuid, ?::uuid, ?)
	`, command.PostID, command.MemberID, createdAt).Error; err != nil {
		return postParticipantChange{}, err
	}
	return roleChange(effects, command.PostID, command.MemberID, targetActor, collaborator, previous, createdAt)
}

func (s *Service) RemovePostCollaborator(ctx context.Context, command PostParticipantCommand) (returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", command.PostID))

	_, err := s.runPostParticipantChange(ctx, lockedPost(command.PostID, policyv1.Post.ManageParticipants, nil),
		func(ctx context.Context, tx *gorm.DB, effects *postEffects) (postParticipantChange, error) {
			targetActor, err := lockedLinkedParticipant(ctx, tx, command.MemberID)
			if err != nil {
				return postParticipantChange{}, err
			}
			result := tx.Exec(
				"DELETE FROM post_collaborator WHERE post_id = ?::uuid AND member_id = ?::uuid",
				command.PostID, command.MemberID,
			)
			if result.Error != nil {
				return postParticipantChange{}, result.Error
			}
			if result.RowsAffected == 0 {
				return postParticipantChange{}, domainerr.New(domainerr.NotFound, "", "post collaborator relation not found")
			}
			return roleChange(effects, command.PostID, command.MemberID, targetActor,
				managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_UNSPECIFIED,
				managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_COLLABORATOR, time.Time{})
		})
	return err
}

func postParticipantAuthorizationMutations(
	postID string,
	actor policyv1.Actor,
	desired managev1.PostParticipantRole,
	previous managev1.PostParticipantRole,
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
	role managev1.PostParticipantRole,
) ([]policyv1.RelationshipMutation, error) {
	if role != managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_UNSPECIFIED &&
		role != managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_AUTHOR &&
		role != managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_COLLABORATOR {
		return nil, domainerr.InvalidField("participant_role", "is not supported")
	}
	var authorMutation policyv1.RelationshipMutation
	var collaboratorMutation policyv1.RelationshipMutation
	var err error
	if role == managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_AUTHOR {
		authorMutation, err = policyv1.Post.TouchAuthor(postID, actor)
	} else {
		authorMutation, err = policyv1.Post.DeleteAuthor(postID, actor)
	}
	if err != nil {
		return nil, err
	}
	if role == managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_COLLABORATOR {
		collaboratorMutation, err = policyv1.Post.TouchCollaborator(postID, actor)
	} else {
		collaboratorMutation, err = policyv1.Post.DeleteCollaborator(postID, actor)
	}
	if err != nil {
		return nil, err
	}
	return []policyv1.RelationshipMutation{authorMutation, collaboratorMutation}, nil
}

func postParticipantAuditRelationship(role managev1.PostParticipantRole) sharedtelemetry.AuditRelationship {
	switch role {
	case managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_AUTHOR:
		return sharedtelemetry.AuditRelationshipAuthor
	case managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_COLLABORATOR:
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

func lockPostParticipantRoot(ctx context.Context, tx *gorm.DB, postID string) (model.PostStatus, error) {
	var row struct {
		ID     string           `gorm:"column:id"`
		Status model.PostStatus `gorm:"column:status"`
	}
	if err := tx.WithContext(ctx).
		Table("post").
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id", "status").
		Where("id = ?::uuid", postID).
		Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", domainerr.ResourceNotFound("post", postID)
		}
		return "", err
	}
	return row.Status, nil
}

func (s *Service) postParticipantResponse(
	ctx context.Context,
	memberID string,
	role managev1.PostParticipantRole,
	createdAt time.Time,
	effective bool,
) (*managev1.PostParticipant, error) {
	summaries, err := s.members.LoadMemberSummaries(ctx, []string{memberID})
	if err != nil {
		return nil, err
	}
	if summaries[memberID] == nil {
		return nil, domainerr.ResourceNotFound("member", memberID)
	}
	return &managev1.PostParticipant{
		Member:                summaries[memberID],
		Role:                  role,
		CreatedAt:             timestamppb.New(createdAt),
		HasEffectiveAuthority: effective,
	}, nil
}

// CheckSlugAvailable checks if a slug is available for posts
// If excludePostId is provided, requires edit permission on that post
