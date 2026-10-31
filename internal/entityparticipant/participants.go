package entityparticipant

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// Members supplies the Member-owned summaries of participants and the
// Identities they act through.
type Members interface {
	LoadMemberSummaries(context.Context, []string) (map[string]*commonv1.MemberSummary, error)
	LoadAuthorizationEligibleMemberSummary(context.Context, string) (*commonv1.MemberSummary, error)
	// LoadAuthorizationEligibleIdentities maps each active, onboarded Member
	// whose linked Identity is active and not banned to that Identity.
	LoadAuthorizationEligibleIdentities(context.Context, []string) (map[string]string, error)
}

// View is one participant as the domain RPCs return it.
type View struct {
	Member                *commonv1.MemberSummary
	Role                  string
	CreatedAt             time.Time
	HasEffectiveAuthority bool
}

// participantRow is one Owner or Manager relation; IdentityID is the
// Identity the Member may act through, empty when it may not.
type participantRow struct {
	MemberID   string    `gorm:"column:member_id"`
	Role       string    `gorm:"column:role"`
	CreatedAt  time.Time `gorm:"column:created_at"`
	IdentityID string    `gorm:"-"`
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
		return nil, err
	}
	memberIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		memberIDs = append(memberIDs, row.MemberID)
	}
	identities, err := members.LoadAuthorizationEligibleIdentities(ctx, memberIDs)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		rows[i].IdentityID = identities[rows[i].MemberID]
	}
	summaries, err := members.LoadMemberSummaries(ctx, memberIDs)
	if err != nil {
		return nil, err
	}
	manageCan, err := s.Policy.Manage(entityID)
	if err != nil {
		return nil, err
	}
	views := make([]View, 0, len(rows))
	for _, row := range rows {
		summary := summaries[row.MemberID]
		if summary == nil {
			return nil, errors.New(s.Title + " participant Member was not found")
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
	if row.IdentityID == "" {
		return false, nil
	}
	actor, err := policyv1.NewAccountIdentityActor(row.IdentityID)
	if err != nil {
		return false, err
	}
	allowed, err := spiceDB.CheckActorCan(ctx, actor, manageCan)
	if err != nil {
		return false, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	return allowed, nil
}

// listParticipantRows lists the entity's Owners, then Managers, oldest
// first.
func (s Spec) listParticipantRows(ctx context.Context, db *gorm.DB, entityID string) ([]participantRow, error) {
	query := fmt.Sprintf(`
		SELECT relation.member_id::text, relation.role, relation.created_at
		FROM (
			SELECT %s, member_id, created_at, 'owner'::text AS role FROM %s WHERE %s = ?::uuid
			UNION ALL
			SELECT %s, member_id, created_at, 'manager'::text AS role FROM %s WHERE %s = ?::uuid
		) AS relation
		ORDER BY CASE relation.role WHEN 'owner' THEN 0 ELSE 1 END,
		         relation.created_at ASC,
		         relation.member_id ASC
	`, s.IDColumn, s.OwnerTable, s.IDColumn, s.IDColumn, s.ManagerTable, s.IDColumn)
	var rows []participantRow
	return rows, db.WithContext(ctx).Raw(query, entityID, entityID).Scan(&rows).Error
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

// lockPrincipal locks the principal and answers not found once the principal
// is no longer active.
func (m participantMutation) lockPrincipal(ctx context.Context) (*auth.UserInfo, error) {
	principal, err := m.spec.LockActivePrincipal(ctx, m.tx)
	if err != nil {
		return nil, err
	}
	if principal == nil {
		return nil, domainerr.ResourceNotFound(m.spec.Name, m.entityID)
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

// requireManage requires ManageParticipants, or RemoveOwner to demote an
// Owner.
func (m participantMutation) requireManage(ctx context.Context, principal *auth.UserInfo, previous, next string) error {
	action := auth.ResourceAction(m.spec.Policy.ManageParticipants)
	if previous == RoleOwner && next != RoleOwner {
		action = m.spec.Policy.RemoveOwner
	}
	return m.requirePermission(ctx, action, principal)
}

func (m participantMutation) alreadyHasRole() error {
	return domainerr.Precondition(m.spec.Title + " participant already has this role")
}

// requireRemove requires RemoveOwner to remove an Owner and
// ManageParticipants otherwise, and returns the role the Member holds as far
// as the owner table tells.
func (m participantMutation) requireRemove(ctx context.Context, principal *auth.UserInfo) (string, error) {
	ownerCount, err := m.countOwners(m.memberID)
	if err != nil {
		return RoleNone, err
	}
	if ownerCount > 0 {
		return RoleOwner, m.requirePermission(ctx, m.spec.Policy.RemoveOwner, principal)
	}
	return RoleManager, m.requirePermission(ctx, m.spec.Policy.ManageParticipants, principal)
}

// deleteForRemoval deletes the relation requireRemove found. An Owner must
// leave another Owner; a missing Manager relation answers not found.
func (m participantMutation) deleteForRemoval(role string) error {
	if role == RoleOwner {
		if err := m.requireAnotherOwner(); err != nil {
			return err
		}
		return m.deleteRelation(RoleOwner)
	}
	result := m.tx.Exec(m.spec.deleteRelationSQL(RoleManager), m.entityID, m.memberID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return domainerr.New(domainerr.NotFound, "", m.spec.Title+" participant not found")
	}
	return nil
}

func (m participantMutation) requireAnotherOwner() error {
	totalOwners, err := m.countOwners("")
	if err != nil {
		return err
	}
	if totalOwners <= 1 {
		return domainerr.Precondition(m.spec.Article + " " + lastOwnerConstraint)
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

func (m participantMutation) requirePermission(ctx context.Context, action auth.ResourceAction, principal *auth.UserInfo) error {
	can, err := action(m.entityID)
	if err != nil {
		return err
	}
	decision, err := auth.AuthorizationDecision(auth.WithUser(ctx, principal), can)
	if err != nil {
		return domainerr.ResourceNotFound(m.spec.Name, m.entityID).WithCause(err)
	}
	allowed, err := m.spiceDB.Can(ctx, decision)
	if err != nil {
		return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if !allowed {
		return domainerr.ResourceNotFound(m.spec.Name, m.entityID)
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
