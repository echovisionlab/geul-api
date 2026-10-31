package entityparticipant

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// Members supplies the Member-owned summaries of participants.
type Members interface {
	LoadMemberSummaries(context.Context, []string) (map[string]*commonv1.MemberSummary, error)
	LoadAuthorizationEligibleMemberSummary(context.Context, string) (*commonv1.MemberSummary, error)
}

// View is one participant as the domain RPCs return it.
type View struct {
	Member                *commonv1.MemberSummary
	Role                  string
	CreatedAt             time.Time
	HasEffectiveAuthority bool
}

// Audit records a participant role change inside the mutation transaction.
type Audit func(ctx context.Context, tx *gorm.DB, previous, next sharedtelemetry.AuditRelationship) error

type participantRow struct {
	MemberID              string    `gorm:"column:member_id"`
	IdentityID            string    `gorm:"column:identity_id"`
	Role                  string    `gorm:"column:role"`
	CreatedAt             time.Time `gorm:"column:created_at"`
	HasEffectiveAuthority bool      `gorm:"column:has_effective_authority"`
}

// MapViews projects participant views into the domain's response type.
func MapViews[T any](views []View, project func(View) T) []T {
	result := make([]T, len(views))
	for i := range views {
		result[i] = project(views[i])
	}
	return result
}

// ListParticipants returns the entity's Owners, then Managers, oldest first.
// HasEffectiveAuthority is true only for an active Member that SpiceDB still
// lets manage the entity.
func (s Spec) ListParticipants(ctx context.Context, db *gorm.DB, spiceDB *auth.SpiceDBClient, members Members, entityID string) ([]View, error) {
	rows, err := s.listParticipantRows(ctx, db, entityID)
	if err != nil {
		return nil, errs.Internal(err)
	}
	memberIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		memberIDs = append(memberIDs, row.MemberID)
	}
	summaries, err := members.LoadMemberSummaries(ctx, memberIDs)
	if err != nil {
		return nil, errs.Internal(err)
	}
	manageCan, err := s.Policy.Manage(entityID)
	if err != nil {
		return nil, errs.Internal(err)
	}
	views := make([]View, 0, len(rows))
	for _, row := range rows {
		summary := summaries[row.MemberID]
		if summary == nil {
			return nil, errs.InternalMsg(s.Title + " participant Member was not found")
		}
		hasEffectiveAuthority, err := hasEffectiveAuthority(ctx, spiceDB, row, manageCan)
		if err != nil {
			return nil, err
		}
		views = append(views, View{
			Member: summary, Role: row.Role, CreatedAt: row.CreatedAt,
			HasEffectiveAuthority: hasEffectiveAuthority,
		})
	}
	return views, nil
}

func hasEffectiveAuthority(ctx context.Context, spiceDB *auth.SpiceDBClient, row participantRow, manageCan policyv1.Can) (bool, error) {
	if !row.HasEffectiveAuthority || row.IdentityID == "" {
		return false, nil
	}
	actor, err := policyv1.NewAccountIdentityActor(row.IdentityID)
	if err != nil {
		return false, errs.Internal(err)
	}
	allowed, err := spiceDB.CheckActorCan(ctx, actor, manageCan)
	if err != nil {
		return false, errs.WithCause(errs.DependencyUnavailable("SpiceDB"), err)
	}
	return allowed, nil
}

func (s Spec) listParticipantRows(ctx context.Context, db *gorm.DB, entityID string) ([]participantRow, error) {
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
	`, s.IDColumn, s.OwnerTable, s.IDColumn, s.IDColumn, s.ManagerTable, s.IDColumn)
	var rows []participantRow
	return rows, db.WithContext(ctx).Raw(query, entityID, entityID).Scan(&rows).Error
}

// SetParticipant gives the Member role on the entity, moving an existing
// participant between Owner and Manager. Demoting an Owner requires
// RemoveOwner, and the entity always keeps one Owner.
func (s Spec) SetParticipant(ctx context.Context, db *gorm.DB, spiceDB *auth.SpiceDBClient, members Members, entityID, memberID, role string, audit Audit) (View, error) {
	createdAt, err := s.setParticipant(ctx, db, spiceDB, entityID, memberID, role, audit)
	if err != nil {
		return View{}, err
	}
	summary, err := members.LoadAuthorizationEligibleMemberSummary(ctx, memberID)
	if err != nil {
		return View{}, err
	}
	return View{Member: summary, Role: role, CreatedAt: createdAt, HasEffectiveAuthority: true}, nil
}

func (s Spec) setParticipant(ctx context.Context, db *gorm.DB, spiceDB *auth.SpiceDBClient, entityID, memberID, role string, audit Audit) (time.Time, error) {
	var createdAt time.Time
	_, err := authzmutation.Execute(ctx, db, spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		mutation := participantMutation{spec: s, spiceDB: spiceDB, tx: tx, entityID: entityID, memberID: memberID}
		principal, err := mutation.lock(ctx)
		if err != nil {
			return err
		}
		previous, err := mutation.currentRole()
		if err != nil {
			return err
		}
		if err := mutation.requireRoleChange(ctx, principal, previous, role); err != nil {
			return err
		}
		target, err := authorizationtarget.RequireLocked(ctx, tx, memberID)
		if err != nil {
			return err
		}
		if previous == RoleOwner {
			if err := mutation.requireAnotherOwner(); err != nil {
				return err
			}
		}
		if previous != RoleNone {
			if err := mutation.deleteRelation(previous); err != nil {
				return err
			}
		}
		if createdAt, err = mutation.insertRelation(role); err != nil {
			return err
		}
		return mutation.commit(ctx, audit, write, target.IdentityID, previous, role)
	})
	if err != nil {
		return time.Time{}, s.MapMutationError(err)
	}
	return createdAt, nil
}

// RemoveParticipant removes the Member from the entity. Removing an Owner
// requires RemoveOwner and leaves at least one Owner.
func (s Spec) RemoveParticipant(ctx context.Context, db *gorm.DB, spiceDB *auth.SpiceDBClient, entityID, memberID string, audit Audit) error {
	_, err := authzmutation.Execute(ctx, db, spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		mutation := participantMutation{spec: s, spiceDB: spiceDB, tx: tx, entityID: entityID, memberID: memberID}
		principal, err := mutation.lock(ctx)
		if err != nil {
			return err
		}
		previous, err := mutation.removeRelation(ctx, principal)
		if err != nil {
			return err
		}
		target, err := authorizationtarget.RequireLocked(ctx, tx, memberID)
		if err != nil {
			return err
		}
		return mutation.commit(ctx, audit, write, target.IdentityID, previous, RoleNone)
	})
	if err != nil {
		return s.MapMutationError(err)
	}
	return nil
}

// participantMutation is one participant change inside its authorization
// mutation transaction.
type participantMutation struct {
	spec     Spec
	spiceDB  *auth.SpiceDBClient
	tx       *gorm.DB
	entityID string
	memberID string
}

// lock locks the entity row, then the principal, and answers not found once
// the principal is no longer active.
func (m participantMutation) lock(ctx context.Context) (*auth.UserInfo, error) {
	if err := m.spec.LockRoot(ctx, m.tx, m.entityID); err != nil {
		return nil, err
	}
	principal, err := m.spec.LockActivePrincipal(ctx, m.tx)
	if err != nil {
		return nil, err
	}
	if principal == nil {
		return nil, errs.NotFound(m.spec.Name, m.entityID)
	}
	return principal, nil
}

func (m participantMutation) currentRole() (string, error) {
	for _, role := range []string{RoleOwner, RoleManager} {
		var row struct {
			CreatedAt time.Time `gorm:"column:created_at"`
		}
		err := m.tx.Table(m.spec.relationTable(role)).
			Select("created_at").
			Where(m.spec.IDColumn+" = ?::uuid AND member_id = ?::uuid", m.entityID, m.memberID).
			Scan(&row).Error
		if err != nil {
			return RoleNone, err
		}
		if !row.CreatedAt.IsZero() {
			return role, nil
		}
	}
	return RoleNone, nil
}

func (m participantMutation) requireRoleChange(ctx context.Context, principal *auth.UserInfo, previous, next string) error {
	action := auth.ResourceAction(m.spec.Policy.ManageParticipants)
	if previous == RoleOwner && next != RoleOwner {
		action = m.spec.Policy.RemoveOwner
	}
	if err := m.requirePermission(ctx, action, principal); err != nil {
		return err
	}
	if previous != RoleNone && previous == next {
		return errs.FailedPrecondition(m.spec.Title + " participant already has this role")
	}
	return nil
}

// removeRelation deletes the Member's relation and returns the role it had.
func (m participantMutation) removeRelation(ctx context.Context, principal *auth.UserInfo) (string, error) {
	ownerCount, err := m.countOwners(m.memberID)
	if err != nil {
		return RoleNone, err
	}
	if ownerCount > 0 {
		if err := m.requirePermission(ctx, m.spec.Policy.RemoveOwner, principal); err != nil {
			return RoleNone, err
		}
		if err := m.requireAnotherOwner(); err != nil {
			return RoleNone, err
		}
		return RoleOwner, m.deleteRelation(RoleOwner)
	}
	if err := m.requirePermission(ctx, m.spec.Policy.ManageParticipants, principal); err != nil {
		return RoleNone, err
	}
	result := m.tx.Exec(m.spec.deleteRelationSQL(RoleManager), m.entityID, m.memberID)
	if result.Error != nil {
		return RoleNone, result.Error
	}
	if result.RowsAffected == 0 {
		return RoleNone, errs.NotFoundMsg(m.spec.Title + " participant not found")
	}
	return RoleManager, nil
}

func (m participantMutation) requireAnotherOwner() error {
	totalOwners, err := m.countOwners("")
	if err != nil {
		return err
	}
	if totalOwners <= 1 {
		return errs.FailedPrecondition(m.spec.Article + " " + lastOwnerConstraint)
	}
	return nil
}

func (m participantMutation) countOwners(memberID string) (int64, error) {
	query := m.tx.Table(m.spec.OwnerTable).Where(m.spec.IDColumn+" = ?::uuid", m.entityID)
	if memberID != "" {
		query = query.Where("member_id = ?::uuid", memberID)
	}
	var count int64
	return count, query.Count(&count).Error
}

func (m participantMutation) deleteRelation(role string) error {
	return m.tx.Exec(m.spec.deleteRelationSQL(role), m.entityID, m.memberID).Error
}

func (m participantMutation) insertRelation(role string) (time.Time, error) {
	createdAt := dbtime.Now()
	return createdAt, m.tx.Exec(
		fmt.Sprintf("INSERT INTO %s (%s, member_id, created_at) VALUES (?::uuid, ?::uuid, ?)", m.spec.relationTable(role), m.spec.IDColumn),
		m.entityID, m.memberID, createdAt,
	).Error
}

// commit audits the role change and writes its SpiceDB relationships.
func (m participantMutation) commit(ctx context.Context, audit Audit, write authzmutation.WriteRelationships, identityID, previous, next string) error {
	if audit != nil {
		if err := audit(ctx, m.tx, auditRelationship(previous), auditRelationship(next)); err != nil {
			return err
		}
	}
	apply, compensate, err := m.spec.participantRelationshipMutations(m.entityID, identityID, previous, next)
	if err != nil {
		return err
	}
	return write(apply, compensate)
}

func (m participantMutation) requirePermission(ctx context.Context, action auth.ResourceAction, principal *auth.UserInfo) error {
	can, err := action(m.entityID)
	if err != nil {
		return errs.Internal(err)
	}
	decision, err := auth.AuthorizationDecision(auth.WithUser(ctx, principal), can)
	if err != nil {
		return errs.WithCause(errs.NotFound(m.spec.Name, m.entityID), err)
	}
	allowed, err := m.spiceDB.Can(ctx, decision)
	if err != nil {
		return errs.WithCause(errs.DependencyUnavailable("SpiceDB"), err)
	}
	if !allowed {
		return errs.NotFound(m.spec.Name, m.entityID)
	}
	return nil
}

func (s Spec) relationTable(role string) string {
	if role == RoleOwner {
		return s.OwnerTable
	}
	return s.ManagerTable
}

func (s Spec) deleteRelationSQL(role string) string {
	return fmt.Sprintf("DELETE FROM %s WHERE %s = ?::uuid AND member_id = ?::uuid", s.relationTable(role), s.IDColumn)
}

func auditRelationship(role string) sharedtelemetry.AuditRelationship {
	switch role {
	case RoleOwner:
		return sharedtelemetry.AuditRelationshipOwner
	case RoleManager:
		return sharedtelemetry.AuditRelationshipManager
	default:
		return sharedtelemetry.AuditRelationshipNone
	}
}
