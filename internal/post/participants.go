package post

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// PostService implements the PostService Connect handler
type postParticipantRow struct {
	MemberID              string    `gorm:"column:member_id"`
	IdentityID            string    `gorm:"column:identity_id"`
	Role                  string    `gorm:"column:role"`
	CreatedAt             time.Time `gorm:"column:created_at"`
	HasEffectiveAuthority bool      `gorm:"column:has_effective_authority"`
}

// ListPostParticipants returns durable authors and collaborators separately.
func (s *PostService) ListPostParticipants(
	ctx context.Context,
	req *connect.Request[managev1.ListPostParticipantsRequest],
) (*connect.Response[managev1.ListPostParticipantsResponse], error) {
	var post model.Post
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "KEY SHARE"}).First(&post, "id = ?", req.Msg.PostId).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("post", req.Msg.PostId)
			}
			return errs.Internal(err)
		}
		_, err := requirePostViewForStatus(ctx, s.spiceDB, post.ID, post.Status)
		return err
	}); err != nil {
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
		LEFT JOIN kratos.identities AS identity
		  ON identity.id = member.account_identity_id
		 AND identity.external_id = member.id::text
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
		LEFT JOIN kratos.identities AS identity
		  ON identity.id = member.account_identity_id
		 AND identity.external_id = member.id::text
		WHERE collaborator.post_id = ?::uuid
		ORDER BY role ASC, created_at ASC, member_id ASC
	`, req.Msg.PostId, req.Msg.PostId).Scan(&rows).Error; err != nil {
		return nil, errs.Internal(err)
	}
	memberIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		memberIDs = append(memberIDs, row.MemberID)
	}
	summaries, err := s.members.LoadMemberSummaries(ctx, memberIDs)
	if err != nil {
		return nil, errs.Internal(err)
	}
	participants := make([]*managev1.PostParticipant, 0, len(rows))
	action := postActionForStatus(post.Status, policyv1.Post.Edit, policyv1.Post.EditArchived)
	can, err := action(req.Msg.PostId)
	if err != nil {
		return nil, errs.Internal(err)
	}
	for _, row := range rows {
		summary := summaries[row.MemberID]
		if summary == nil {
			return nil, errs.InternalMsg("post participant member was not found")
		}
		role := managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_COLLABORATOR
		if row.Role == "author" {
			role = managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_AUTHOR
		}
		effective := row.HasEffectiveAuthority
		if effective {
			actor, actorErr := policyv1.NewAccountIdentityActor(row.IdentityID)
			if actorErr != nil {
				return nil, errs.Internal(actorErr)
			}
			ok, checkErr := s.spiceDB.CheckActorCan(ctx, actor, can)
			if checkErr != nil {
				return nil, errs.DependencyUnavailable("SpiceDB")
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
	return connect.NewResponse(&managev1.ListPostParticipantsResponse{
		Participants: participants,
	}), nil
}

func (s *PostService) AddPostAuthor(
	ctx context.Context,
	req *connect.Request[managev1.AddPostAuthorRequest],
) (*connect.Response[managev1.PostParticipant], error) {
	principal := auth.GetUser(ctx)
	if principal == nil {
		return nil, errs.AuthenticationRequired()
	}
	var createdAt time.Time
	_, err := authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		status, err := lockPostParticipantRoot(ctx, tx, req.Msg.PostId)
		if err != nil {
			return err
		}
		if _, err := requireLockedPostActionForStatus(ctx, tx, s.spiceDB, req.Msg.PostId, status, policyv1.Post.ManageParticipants); err != nil {
			return err
		}
		target, err := authorizationtarget.RequireLocked(ctx, tx, req.Msg.MemberId)
		if err != nil {
			return err
		}
		targetActor, actorErr := policyv1.NewAccountIdentityActor(target.IdentityID)
		if actorErr != nil {
			return errs.Internal(actorErr)
		}
		can, canErr := policyv1.Platform.IsAuthor()
		if canErr != nil {
			return errs.Internal(canErr)
		}
		canAuthor, checkErr := s.spiceDB.CheckActorCan(ctx, targetActor, can)
		if checkErr != nil {
			return errs.DependencyUnavailable("SpiceDB")
		}
		if !canAuthor {
			return errs.InvalidArgument("member_id", "post authors must have the site Author or Admin role")
		}
		var existing struct {
			CreatedAt time.Time `gorm:"column:created_at"`
		}
		if err := tx.Table("post_author").
			Select("created_at").
			Where("post_id = ?::uuid AND member_id = ?::uuid", req.Msg.PostId, req.Msg.MemberId).
			Scan(&existing).Error; err != nil {
			return err
		}
		if !existing.CreatedAt.IsZero() {
			createdAt = existing.CreatedAt
			apply, compensate, mutationErr := postParticipantAuthorizationMutations(
				req.Msg.PostId,
				targetActor,
				managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_AUTHOR,
				managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_AUTHOR,
			)
			if mutationErr != nil {
				return mutationErr
			}
			return write(apply, compensate)
		}
		collaboratorExists, err := postRelationExists(ctx, tx, "post_collaborator", req.Msg.PostId, req.Msg.MemberId)
		if err != nil {
			return err
		}
		// Author and Collaborator are mutually exclusive peer roles. Reassigning
		// Collaborator -> Author changes both product attribution rows before the
		// exact SpiceDB state transition is applied.
		if err := tx.Exec(
			"DELETE FROM post_collaborator WHERE post_id = ?::uuid AND member_id = ?::uuid",
			req.Msg.PostId,
			req.Msg.MemberId,
		).Error; err != nil {
			return err
		}
		createdAt = time.Now().UTC()
		if err := tx.Exec(`
			INSERT INTO post_author (post_id, member_id, created_at)
			VALUES (?::uuid, ?::uuid, ?)
		`, req.Msg.PostId, req.Msg.MemberId, createdAt).Error; err != nil {
			return err
		}
		previous := managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_UNSPECIFIED
		if collaboratorExists {
			previous = managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_COLLABORATOR
		}
		if err := s.appendPostParticipantAudit(ctx, tx, req.Msg.PostId, req.Msg.MemberId, postParticipantAuditRelationship(previous), sharedtelemetry.AuditRelationshipAuthor); err != nil {
			return err
		}
		apply, compensate, err := postParticipantAuthorizationMutations(
			req.Msg.PostId,
			targetActor,
			managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_AUTHOR,
			previous,
		)
		if err != nil {
			return err
		}
		return write(apply, compensate)
	})
	if err != nil {
		return nil, mapPostParticipantMutationError(err)
	}
	response, err := s.postParticipantResponse(ctx, req.Msg.MemberId, managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_AUTHOR, createdAt, true)
	if err != nil {
		return nil, err
	}
	return response, nil
}

func (s *PostService) RemovePostAuthor(
	ctx context.Context,
	req *connect.Request[managev1.RemovePostAuthorRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	principal := auth.GetUser(ctx)
	if principal == nil {
		return nil, errs.AuthenticationRequired()
	}
	_, err := authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		status, err := lockPostParticipantRoot(ctx, tx, req.Msg.PostId)
		if err != nil {
			return err
		}
		if _, err := requireLockedPostRemoveAuthor(ctx, tx, s.spiceDB, req.Msg.PostId, status); err != nil {
			return err
		}
		var authorExists int64
		if err := tx.Table("post_author").
			Where("post_id = ?::uuid AND member_id = ?::uuid", req.Msg.PostId, req.Msg.MemberId).
			Count(&authorExists).Error; err != nil {
			return err
		}
		if authorExists == 0 {
			return errs.NotFoundMsg("post author relation not found")
		}
		var authorCount int64
		if err := tx.Table("post_author").Where("post_id = ?::uuid", req.Msg.PostId).Count(&authorCount).Error; err != nil {
			return err
		}
		if authorCount <= 1 {
			return errs.FailedPrecondition("a post must retain at least one durable author")
		}
		target, err := authorizationtarget.RequireLockedLinked(ctx, tx, req.Msg.MemberId)
		if err != nil {
			return err
		}
		targetActor, err := policyv1.NewAccountIdentityActor(target.IdentityID)
		if err != nil {
			return err
		}
		result := tx.Exec(
			"DELETE FROM post_author WHERE post_id = ?::uuid AND member_id = ?::uuid",
			req.Msg.PostId,
			req.Msg.MemberId,
		)
		if result.Error != nil {
			return result.Error
		}
		if err := s.appendPostParticipantAudit(ctx, tx, req.Msg.PostId, req.Msg.MemberId, sharedtelemetry.AuditRelationshipAuthor, sharedtelemetry.AuditRelationshipNone); err != nil {
			return err
		}
		apply, compensate, err := postParticipantAuthorizationMutations(
			req.Msg.PostId,
			targetActor,
			managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_UNSPECIFIED,
			managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_AUTHOR,
		)
		if err != nil {
			return err
		}
		return write(apply, compensate)
	})
	if err != nil {
		return nil, mapPostParticipantMutationError(err)
	}
	return connect.NewResponse(&managev1.DeleteResponse{Success: true}), nil
}

func (s *PostService) AddPostCollaborator(
	ctx context.Context,
	req *connect.Request[managev1.AddPostCollaboratorRequest],
) (*connect.Response[managev1.PostParticipant], error) {
	principal := auth.GetUser(ctx)
	if principal == nil {
		return nil, errs.AuthenticationRequired()
	}
	var createdAt time.Time
	_, err := authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		status, err := lockPostParticipantRoot(ctx, tx, req.Msg.PostId)
		if err != nil {
			return err
		}
		var existing struct {
			CreatedAt time.Time `gorm:"column:created_at"`
		}
		if err := tx.Table("post_collaborator").
			Select("created_at").
			Where("post_id = ?::uuid AND member_id = ?::uuid", req.Msg.PostId, req.Msg.MemberId).
			Scan(&existing).Error; err != nil {
			return err
		}
		authorExists, err := postRelationExists(ctx, tx, "post_author", req.Msg.PostId, req.Msg.MemberId)
		if err != nil {
			return err
		}
		if authorExists {
			if _, err := requireLockedPostRemoveAuthor(ctx, tx, s.spiceDB, req.Msg.PostId, status); err != nil {
				return err
			}
		} else if _, err := requireLockedPostActionForStatus(ctx, tx, s.spiceDB, req.Msg.PostId, status, policyv1.Post.ManageParticipants); err != nil {
			return err
		}
		target, err := authorizationtarget.RequireLocked(ctx, tx, req.Msg.MemberId)
		if err != nil {
			return err
		}
		targetActor, err := policyv1.NewAccountIdentityActor(target.IdentityID)
		if err != nil {
			return err
		}
		if !existing.CreatedAt.IsZero() {
			createdAt = existing.CreatedAt
			apply, compensate, mutationErr := postParticipantAuthorizationMutations(
				req.Msg.PostId,
				targetActor,
				managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_COLLABORATOR,
				managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_COLLABORATOR,
			)
			if mutationErr != nil {
				return mutationErr
			}
			return write(apply, compensate)
		}
		if authorExists {
			var authorCount int64
			if err := tx.Table("post_author").Where("post_id = ?::uuid", req.Msg.PostId).Count(&authorCount).Error; err != nil {
				return err
			}
			if authorCount <= 1 {
				return errs.FailedPrecondition("a post must retain at least one durable author")
			}
			// Author and Collaborator are mutually exclusive peer roles. Reassign
			// the Author to Collaborator in this locked transaction.
			if err := tx.Exec(
				"DELETE FROM post_author WHERE post_id = ?::uuid AND member_id = ?::uuid",
				req.Msg.PostId,
				req.Msg.MemberId,
			).Error; err != nil {
				return err
			}
		}
		createdAt = time.Now().UTC()
		if err := tx.Exec(`
			INSERT INTO post_collaborator (post_id, member_id, created_at)
			VALUES (?::uuid, ?::uuid, ?)
		`, req.Msg.PostId, req.Msg.MemberId, createdAt).Error; err != nil {
			return err
		}
		previous := managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_UNSPECIFIED
		if authorExists {
			previous = managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_AUTHOR
		}
		if err := s.appendPostParticipantAudit(ctx, tx, req.Msg.PostId, req.Msg.MemberId, postParticipantAuditRelationship(previous), sharedtelemetry.AuditRelationshipCollaborator); err != nil {
			return err
		}
		apply, compensate, err := postParticipantAuthorizationMutations(
			req.Msg.PostId,
			targetActor,
			managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_COLLABORATOR,
			previous,
		)
		if err != nil {
			return err
		}
		return write(apply, compensate)
	})
	if err != nil {
		return nil, mapPostParticipantMutationError(err)
	}
	response, err := s.postParticipantResponse(ctx, req.Msg.MemberId, managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_COLLABORATOR, createdAt, true)
	if err != nil {
		return nil, err
	}
	return response, nil
}

func (s *PostService) RemovePostCollaborator(
	ctx context.Context,
	req *connect.Request[managev1.RemovePostCollaboratorRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	principal := auth.GetUser(ctx)
	if principal == nil {
		return nil, errs.AuthenticationRequired()
	}
	_, err := authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		status, err := lockPostParticipantRoot(ctx, tx, req.Msg.PostId)
		if err != nil {
			return err
		}
		if _, err := requireLockedPostActionForStatus(ctx, tx, s.spiceDB, req.Msg.PostId, status, policyv1.Post.ManageParticipants); err != nil {
			return err
		}
		target, err := authorizationtarget.RequireLockedLinked(ctx, tx, req.Msg.MemberId)
		if err != nil {
			return err
		}
		targetActor, err := policyv1.NewAccountIdentityActor(target.IdentityID)
		if err != nil {
			return err
		}
		result := tx.Exec(
			"DELETE FROM post_collaborator WHERE post_id = ?::uuid AND member_id = ?::uuid",
			req.Msg.PostId,
			req.Msg.MemberId,
		)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errs.NotFoundMsg("post collaborator relation not found")
		}
		if err := s.appendPostParticipantAudit(ctx, tx, req.Msg.PostId, req.Msg.MemberId, sharedtelemetry.AuditRelationshipCollaborator, sharedtelemetry.AuditRelationshipNone); err != nil {
			return err
		}
		apply, compensate, err := postParticipantAuthorizationMutations(
			req.Msg.PostId,
			targetActor,
			managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_UNSPECIFIED,
			managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_COLLABORATOR,
		)
		if err != nil {
			return err
		}
		return write(apply, compensate)
	})
	if err != nil {
		return nil, mapPostParticipantMutationError(err)
	}
	return connect.NewResponse(&managev1.DeleteResponse{Success: true}), nil
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
		return nil, errs.InvalidArgument("participant_role", "is not supported")
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

func (s *PostService) appendPostParticipantAudit(ctx context.Context, tx *gorm.DB, postID, memberID string, previous, next sharedtelemetry.AuditRelationship) error {
	if s.auditWriter == nil {
		return nil
	}
	return domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditPostUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
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
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "status").
		Where("id = ?::uuid", postID).
		Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", errs.NotFound("post", postID)
		}
		return "", errs.Internal(err)
	}
	return row.Status, nil
}

func (s *PostService) postParticipantResponse(
	ctx context.Context,
	memberID string,
	role managev1.PostParticipantRole,
	createdAt time.Time,
	effective bool,
) (*connect.Response[managev1.PostParticipant], error) {
	summaries, err := s.members.LoadMemberSummaries(ctx, []string{memberID})
	if err != nil {
		return nil, errs.Internal(err)
	}
	if summaries[memberID] == nil {
		return nil, errs.NotFound("member", memberID)
	}
	return connect.NewResponse(&managev1.PostParticipant{
		Member:                summaries[memberID],
		Role:                  role,
		CreatedAt:             timestamppb.New(createdAt),
		HasEffectiveAuthority: effective,
	}), nil
}

// CheckSlugAvailable checks if a slug is available for posts
// If excludePostId is provided, requires edit permission on that post
