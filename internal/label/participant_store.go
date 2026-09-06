package label

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

const (
	participantRoleOwner   = "owner"
	participantRoleManager = "manager"
	participantRoleNone    = ""
)

type entityParticipantRow struct {
	MemberID              string    `gorm:"column:member_id"`
	IdentityID            string    `gorm:"column:identity_id"`
	Role                  string    `gorm:"column:role"`
	CreatedAt             time.Time `gorm:"column:created_at"`
	HasEffectiveAuthority bool      `gorm:"column:has_effective_authority"`
}

type entityParticipantView struct {
	Member                *commonv1.MemberSummary
	Role                  string
	CreatedAt             time.Time
	HasEffectiveAuthority bool
}

func mapEntityParticipantViews[T interface{}](views []entityParticipantView, project func(entityParticipantView) T) []T {
	result := make([]T, len(views))
	for i := range views {
		result[i] = project(views[i])
	}
	return result
}

type entityParticipantSpec struct {
	entityName       string
	entityTitle      string
	entityArticle    string
	entityIDColumn   string
	ownerTable       string
	managerTable     string
	lockRoot         func(context.Context, *gorm.DB, string) error
	lockPrincipal    func(context.Context, *gorm.DB) (*auth.UserInfo, error)
	mapMutationError func(error) error
}

type entityParticipantAudit func(context.Context, *gorm.DB, string, string) error

func labelParticipantSpec() entityParticipantSpec {
	return entityParticipantSpec{
		entityName: "label", entityTitle: "Label", entityArticle: "a Label", entityIDColumn: "label_id",
		ownerTable: "label_owner", managerTable: "label_manager", lockRoot: lockLabelParticipantRoot,
		lockPrincipal:    lockActiveLabelPrincipal,
		mapMutationError: mapLabelParticipantMutationError,
	}
}

func listEntityParticipants(ctx context.Context, db *gorm.DB, spec entityParticipantSpec, entityID string) ([]entityParticipantRow, error) {
	query := fmt.Sprintf(`
		SELECT relation.member_id::text,
		       COALESCE(target.identity_id, '') AS identity_id,
		       relation.role,
		       relation.created_at,
		       target.identity_id IS NOT NULL AS has_effective_authority
		FROM (
			SELECT %s, member_id, created_at, 'owner'::text AS role FROM %s WHERE %s = ?::uuid
			UNION ALL
			SELECT %s, member_id, created_at, 'manager'::text AS role FROM %s WHERE %s = ?::uuid
		) AS relation
		LEFT JOIN LATERAL (
				SELECT identity.id::text AS identity_id
				FROM member
				JOIN kratos.identities AS identity
				  ON identity.id = member.account_identity_id
				 AND identity.external_id = member.id::text
				WHERE member.id = relation.member_id
				  AND member.deleted_at IS NULL
				  AND member.onboarded = TRUE
				  AND identity.state = 'active'
				  AND LOWER(COALESCE(identity.metadata_admin ->> 'banned', 'false')) NOT IN ('true', '1')
				LIMIT 1
		) AS target ON TRUE
		ORDER BY CASE relation.role WHEN 'owner' THEN 0 ELSE 1 END,
		         relation.created_at ASC,
		         relation.member_id ASC
	`, spec.entityIDColumn, spec.ownerTable, spec.entityIDColumn, spec.entityIDColumn, spec.managerTable, spec.entityIDColumn)
	var rows []entityParticipantRow
	return rows, db.WithContext(ctx).Raw(query, entityID, entityID).Scan(&rows).Error
}

func loadParticipantMemberSummaries(
	ctx context.Context,
	members MemberProjection,
	rows []entityParticipantRow,
) (map[string]*commonv1.MemberSummary, error) {
	memberIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		memberIDs = append(memberIDs, row.MemberID)
	}
	return members.LoadMemberSummaries(ctx, memberIDs)
}

func loadEntityParticipantViews(
	ctx context.Context,
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	members MemberProjection,
	spec entityParticipantSpec,
	entityID string,
) ([]entityParticipantView, error) {
	rows, err := listEntityParticipants(ctx, db, spec, entityID)
	if err != nil {
		return nil, errs.Internal(err)
	}
	summaries, err := loadParticipantMemberSummaries(ctx, members, rows)
	if err != nil {
		return nil, errs.Internal(err)
	}
	canManage, err := policyv1.Label.Manage(entityID)
	if err != nil {
		return nil, errs.Internal(err)
	}
	views := make([]entityParticipantView, 0, len(rows))
	for _, row := range rows {
		summary := summaries[row.MemberID]
		if summary == nil {
			return nil, errs.InternalMsg(spec.entityTitle + " participant Member was not found")
		}
		hasEffectiveAuthority := false
		if row.HasEffectiveAuthority && row.IdentityID != "" {
			actor, actorErr := policyv1.NewAccountIdentityActor(row.IdentityID)
			if actorErr != nil {
				return nil, errs.Internal(actorErr)
			}
			hasEffectiveAuthority, err = spiceDB.CheckActorCan(ctx, actor, canManage)
			if err != nil {
				return nil, errs.DependencyUnavailable("SpiceDB")
			}
		}
		views = append(views, entityParticipantView{
			Member: summary, Role: row.Role, CreatedAt: row.CreatedAt,
			HasEffectiveAuthority: hasEffectiveAuthority,
		})
	}
	return views, nil
}

func setEntityParticipantViewWithAudit(ctx context.Context, db *gorm.DB, spiceDB *auth.SpiceDBClient, members MemberProjection, spec entityParticipantSpec, entityID, memberID, role string, audit entityParticipantAudit) (entityParticipantView, error) {
	createdAt, err := setEntityParticipantWithAudit(ctx, db, spiceDB, spec, entityID, memberID, role, audit)
	if err != nil {
		return entityParticipantView{}, err
	}
	summary, err := members.LoadAuthorizationEligibleMemberSummary(ctx, memberID)
	if err != nil {
		return entityParticipantView{}, err
	}
	return entityParticipantView{
		Member: summary, Role: role, CreatedAt: createdAt, HasEffectiveAuthority: true,
	}, nil
}

func setEntityParticipantWithAudit(ctx context.Context, db *gorm.DB, spiceDB *auth.SpiceDBClient, spec entityParticipantSpec, entityID, memberID, role string, audit entityParticipantAudit) (time.Time, error) {
	var createdAt time.Time
	_, err := authzmutation.Execute(ctx, db, spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		principal, err := lockParticipantMutationContext(ctx, tx, spec, entityID)
		if err != nil {
			return err
		}
		ownerCreatedAt, err := loadParticipantRelationCreatedAt(tx, spec.ownerTable, spec.entityIDColumn, entityID, memberID)
		if err != nil {
			return err
		}
		if !ownerCreatedAt.IsZero() {
			action := labelAction(policyv1.Label.ManageParticipants)
			if role != participantRoleOwner {
				action = policyv1.Label.RemoveOwner
			}
			if err := requireParticipantPermission(ctx, spiceDB, spec, entityID, action, principal); err != nil {
				return err
			}
			if role == participantRoleOwner {
				return errs.FailedPrecondition(spec.entityTitle + " participant already has this role")
			}
			target, err := authorizationtarget.RequireLocked(ctx, tx, memberID)
			if err != nil {
				return err
			}
			totalOwners, err := countParticipantRelations(tx, spec.ownerTable, spec.entityIDColumn, entityID, "")
			if err != nil {
				return err
			}
			if totalOwners <= 1 {
				return errs.FailedPrecondition(spec.entityArticle + " must retain at least one durable Owner")
			}
			if err := deleteParticipantRelation(tx, spec.ownerTable, spec.entityIDColumn, entityID, memberID); err != nil {
				return err
			}
			if err := insertParticipantRelation(tx, spec, entityID, memberID, role, &createdAt); err != nil {
				return err
			}
			if audit != nil {
				if err := audit(ctx, tx, participantRoleOwner, role); err != nil {
					return err
				}
			}
			apply, compensate, err := participantRelationshipMutations(entityID, target.IdentityID, participantRoleOwner, role)
			if err != nil {
				return err
			}
			return write(apply, compensate)
		}
		managerCreatedAt, err := loadParticipantRelationCreatedAt(tx, spec.managerTable, spec.entityIDColumn, entityID, memberID)
		if err != nil {
			return err
		}
		if !managerCreatedAt.IsZero() {
			if err := requireParticipantPermission(ctx, spiceDB, spec, entityID, policyv1.Label.ManageParticipants, principal); err != nil {
				return err
			}
			if role == participantRoleManager {
				return errs.FailedPrecondition(spec.entityTitle + " participant already has this role")
			}
			target, err := authorizationtarget.RequireLocked(ctx, tx, memberID)
			if err != nil {
				return err
			}
			if err := deleteParticipantRelation(tx, spec.managerTable, spec.entityIDColumn, entityID, memberID); err != nil {
				return err
			}
			if err := insertParticipantRelation(tx, spec, entityID, memberID, role, &createdAt); err != nil {
				return err
			}
			if audit != nil {
				if err := audit(ctx, tx, participantRoleManager, role); err != nil {
					return err
				}
			}
			apply, compensate, err := participantRelationshipMutations(entityID, target.IdentityID, participantRoleManager, role)
			if err != nil {
				return err
			}
			return write(apply, compensate)
		}
		if err := requireParticipantPermission(ctx, spiceDB, spec, entityID, policyv1.Label.ManageParticipants, principal); err != nil {
			return err
		}
		target, err := authorizationtarget.RequireLocked(ctx, tx, memberID)
		if err != nil {
			return err
		}
		if err := insertParticipantRelation(tx, spec, entityID, memberID, role, &createdAt); err != nil {
			return err
		}
		if audit != nil {
			if err := audit(ctx, tx, participantRoleNone, role); err != nil {
				return err
			}
		}
		apply, compensate, err := participantRelationshipMutations(entityID, target.IdentityID, participantRoleNone, role)
		if err != nil {
			return err
		}
		return write(apply, compensate)
	})
	if err != nil {
		return time.Time{}, spec.mapMutationError(err)
	}
	return createdAt, nil
}

func participantRelationshipMutations(entityID, identityID, previousRole, nextRole string) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
	actor, err := policyv1.NewAccountIdentityActor(identityID)
	if err != nil {
		return nil, nil, err
	}
	touchRole := func(role string) (policyv1.RelationshipMutation, error) {
		switch role {
		case participantRoleOwner:
			return policyv1.Label.TouchOwner(entityID, actor)
		case participantRoleManager:
			return policyv1.Label.TouchManager(entityID, actor)
		default:
			return policyv1.RelationshipMutation{}, fmt.Errorf("unsupported participant role %q", role)
		}
	}
	deleteRole := func(role string) (policyv1.RelationshipMutation, error) {
		switch role {
		case participantRoleOwner:
			return policyv1.Label.DeleteOwner(entityID, actor)
		case participantRoleManager:
			return policyv1.Label.DeleteManager(entityID, actor)
		default:
			return policyv1.RelationshipMutation{}, fmt.Errorf("unsupported participant role %q", role)
		}
	}

	apply := make([]policyv1.RelationshipMutation, 0, 2)
	compensate := make([]policyv1.RelationshipMutation, 0, 2)
	if previousRole != participantRoleNone {
		mutation, mutationErr := deleteRole(previousRole)
		if mutationErr != nil {
			return nil, nil, mutationErr
		}
		restore, restoreErr := touchRole(previousRole)
		if restoreErr != nil {
			return nil, nil, restoreErr
		}
		apply = append(apply, mutation)
		compensate = append(compensate, restore)
	}
	if nextRole != participantRoleNone {
		mutation, mutationErr := touchRole(nextRole)
		if mutationErr != nil {
			return nil, nil, mutationErr
		}
		remove, removeErr := deleteRole(nextRole)
		if removeErr != nil {
			return nil, nil, removeErr
		}
		apply = append(apply, mutation)
		compensate = append([]policyv1.RelationshipMutation{remove}, compensate...)
	}
	return apply, compensate, nil
}

func insertParticipantRelation(tx *gorm.DB, spec entityParticipantSpec, entityID, memberID, role string, createdAt *time.Time) error {
	*createdAt = time.Now().UTC()
	table := spec.managerTable
	if role == participantRoleOwner {
		table = spec.ownerTable
	}
	return tx.Exec(fmt.Sprintf("INSERT INTO %s (%s, member_id, created_at) VALUES (?::uuid, ?::uuid, ?)", table, spec.entityIDColumn), entityID, memberID, *createdAt).Error
}

func removeEntityParticipantWithAudit(ctx context.Context, db *gorm.DB, spiceDB *auth.SpiceDBClient, spec entityParticipantSpec, entityID, memberID string, audit entityParticipantAudit) error {
	_, err := authzmutation.Execute(ctx, db, spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		principal, err := lockParticipantMutationContext(ctx, tx, spec, entityID)
		if err != nil {
			return err
		}
		ownerCount, err := countParticipantRelations(tx, spec.ownerTable, spec.entityIDColumn, entityID, memberID)
		if err != nil {
			return err
		}
		if ownerCount > 0 {
			if err := requireParticipantPermission(ctx, spiceDB, spec, entityID, policyv1.Label.RemoveOwner, principal); err != nil {
				return err
			}
			totalOwners, err := countParticipantRelations(tx, spec.ownerTable, spec.entityIDColumn, entityID, "")
			if err != nil {
				return err
			}
			if totalOwners <= 1 {
				return errs.FailedPrecondition(spec.entityArticle + " must retain at least one durable Owner")
			}
			if err := deleteParticipantRelation(tx, spec.ownerTable, spec.entityIDColumn, entityID, memberID); err != nil {
				return err
			}
			target, targetErr := authorizationtarget.RequireLocked(ctx, tx, memberID)
			if targetErr != nil {
				return targetErr
			}
			if audit != nil {
				if err := audit(ctx, tx, participantRoleOwner, participantRoleNone); err != nil {
					return err
				}
			}
			apply, compensate, err := participantRelationshipMutations(entityID, target.IdentityID, participantRoleOwner, participantRoleNone)
			if err != nil {
				return err
			}
			return write(apply, compensate)
		}
		if err := requireParticipantPermission(ctx, spiceDB, spec, entityID, policyv1.Label.ManageParticipants, principal); err != nil {
			return err
		}
		result := tx.Exec(
			fmt.Sprintf("DELETE FROM %s WHERE %s = ?::uuid AND member_id = ?::uuid", spec.managerTable, spec.entityIDColumn),
			entityID, memberID,
		)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errs.NotFoundMsg(spec.entityTitle + " participant not found")
		}
		target, targetErr := authorizationtarget.RequireLocked(ctx, tx, memberID)
		if targetErr != nil {
			return targetErr
		}
		if audit != nil {
			if err := audit(ctx, tx, participantRoleManager, participantRoleNone); err != nil {
				return err
			}
		}
		apply, compensate, err := participantRelationshipMutations(entityID, target.IdentityID, participantRoleManager, participantRoleNone)
		if err != nil {
			return err
		}
		return write(apply, compensate)
	})
	if err != nil {
		return spec.mapMutationError(err)
	}
	return nil
}

func removeEntityParticipantResponseWithAudit(ctx context.Context, db *gorm.DB, spiceDB *auth.SpiceDBClient, spec entityParticipantSpec, entityID, memberID string, audit entityParticipantAudit) (*connect.Response[managev1.DeleteResponse], error) {
	if err := removeEntityParticipantWithAudit(ctx, db, spiceDB, spec, entityID, memberID, audit); err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.DeleteResponse{Success: true}), nil
}

func lockParticipantMutationContext(ctx context.Context, tx *gorm.DB, spec entityParticipantSpec, entityID string) (*auth.UserInfo, error) {
	if err := spec.lockRoot(ctx, tx, entityID); err != nil {
		return nil, err
	}
	principal, err := spec.lockPrincipal(ctx, tx)
	if err != nil {
		return nil, err
	}
	if principal == nil {
		return nil, errs.NotFound(spec.entityName, entityID)
	}
	return principal, nil
}

func requireParticipantPermission(ctx context.Context, spiceDB *auth.SpiceDBClient, spec entityParticipantSpec, entityID string, action labelAction, principal *auth.UserInfo) error {
	can, err := action(entityID)
	if err != nil {
		return errs.Internal(err)
	}
	decision, err := auth.AuthorizationDecision(auth.WithUser(ctx, principal), can)
	if err != nil {
		return errs.NotFound(spec.entityName, entityID)
	}
	allowed, err := spiceDB.Can(ctx, decision)
	if err != nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	if !allowed {
		return errs.NotFound(spec.entityName, entityID)
	}
	return nil
}

func loadParticipantRelationCreatedAt(tx *gorm.DB, table, entityIDColumn, entityID, memberID string) (time.Time, error) {
	var row struct {
		CreatedAt time.Time `gorm:"column:created_at"`
	}
	err := tx.Table(table).
		Select("created_at").
		Where(entityIDColumn+" = ?::uuid AND member_id = ?::uuid", entityID, memberID).
		Scan(&row).Error
	return row.CreatedAt, err
}

func countParticipantRelations(tx *gorm.DB, table, entityIDColumn, entityID, memberID string) (int64, error) {
	query := tx.Table(table).Where(entityIDColumn+" = ?::uuid", entityID)
	if memberID != "" {
		query = query.Where("member_id = ?::uuid", memberID)
	}
	var count int64
	return count, query.Count(&count).Error
}

func deleteParticipantRelation(tx *gorm.DB, table, entityIDColumn, entityID, memberID string) error {
	return tx.Exec(
		fmt.Sprintf("DELETE FROM %s WHERE %s = ?::uuid AND member_id = ?::uuid", table, entityIDColumn),
		entityID, memberID,
	).Error
}
