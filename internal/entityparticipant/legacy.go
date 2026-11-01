package entityparticipant

// The gorm-bound mechanism Artist still runs on until it moves to its Store.

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// LegacyRequireLockedPermission locks the request principal's identity state and
// then requires action on the entity, so a concurrent ban or deletion of the
// principal cannot race the mutation.
func (s Spec) LegacyRequireLockedPermission(ctx context.Context, tx *gorm.DB, checker PermissionChecker, entityID string, action auth.ResourceAction) error {
	principal, err := s.LegacyLockActivePrincipal(ctx, tx)
	if err != nil {
		return err
	}
	return s.RequirePrincipalPermission(ctx, checker, entityID, action, principal)
}

// LegacyLockedAccess is the mutation.Access of LegacyRequireLockedPermission: the
// principal is locked inside the mutation transaction and a denial answers
// not found, as the entity's read paths do.
func (s Spec) LegacyLockedAccess(checker PermissionChecker, entityID string, action auth.ResourceAction) mutation.Access {
	return mutation.Checked(func(ctx context.Context, tx *gorm.DB) error {
		return s.LegacyRequireLockedPermission(ctx, tx, checker, entityID, action)
	})
}

// LegacyLockRoot locks the entity row for update.
func (s Spec) LegacyLockRoot(ctx context.Context, tx *gorm.DB, entityID string) error {
	var row struct {
		ID string `gorm:"column:id"`
	}
	if err := tx.WithContext(ctx).Table(s.Table).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Select("id").Where("id = ?::uuid", entityID).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domainerr.ResourceNotFound(s.Name, entityID)
		}
		return err
	}
	return nil
}

// LegacyLockActivePrincipal locks the request principal's identity state. It
// returns nil without an error when the principal is no longer active.
func (s Spec) LegacyLockActivePrincipal(ctx context.Context, tx *gorm.DB) (*auth.UserInfo, error) {
	principal := auth.GetUser(ctx)
	active, err := identitystate.LockActivePrincipal(ctx, tx, principal)
	if err != nil {
		return nil, fmt.Errorf("lock %s mutation principal: %w", s.Title, err)
	}
	if !active {
		return nil, nil
	}
	return principal, nil
}


// legacyParticipantRow is one Owner or Manager relation; IdentityID is the
// Identity the Member may act through, empty when it may not.
type legacyParticipantRow struct {
	MemberID   string    `gorm:"column:member_id"`
	Role       string    `gorm:"column:role"`
	CreatedAt  time.Time `gorm:"column:created_at"`
	IdentityID string    `gorm:"-"`
}


// LegacyListParticipants returns the entity's Owners, then Managers, oldest first.
// HasEffectiveAuthority is true only for an active Member that SpiceDB still
// lets manage the entity.
func (s Spec) LegacyListParticipants(ctx context.Context, db *gorm.DB, spiceDB *auth.SpiceDBClient, members Members, entityID string) ([]View, error) {
	rows, err := s.legacyListParticipantRows(ctx, db, entityID)
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
		legacyHasEffectiveAuthority, err := legacyHasEffectiveAuthority(ctx, spiceDB, row, manageCan)
		if err != nil {
			return nil, err
		}
		views = append(views, View{
			Member: summary, Role: row.Role, CreatedAt: row.CreatedAt,
			HasEffectiveAuthority: legacyHasEffectiveAuthority,
		})
	}
	return views, nil
}

func legacyHasEffectiveAuthority(ctx context.Context, spiceDB *auth.SpiceDBClient, row legacyParticipantRow, manageCan policyv1.Can) (bool, error) {
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

// legacyListParticipantRows lists the entity's Owners, then Managers, oldest
// first.
func (s Spec) legacyListParticipantRows(ctx context.Context, db *gorm.DB, entityID string) ([]legacyParticipantRow, error) {
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
	var rows []legacyParticipantRow
	return rows, db.WithContext(ctx).Raw(query, entityID, entityID).Scan(&rows).Error
}

// legacyParticipantMutation is one participant change inside its authorization
// mutation transaction.
type legacyParticipantMutation struct {
	spec     Spec
	spiceDB  *auth.SpiceDBClient
	tx       *gorm.DB
	entityID string
	memberID string
}

// lockPrincipal locks the principal and answers not found once the principal
// is no longer active.
func (m legacyParticipantMutation) lockPrincipal(ctx context.Context) (*auth.UserInfo, error) {
	principal, err := m.spec.LegacyLockActivePrincipal(ctx, m.tx)
	if err != nil {
		return nil, err
	}
	if principal == nil {
		return nil, domainerr.ResourceNotFound(m.spec.Name, m.entityID)
	}
	return principal, nil
}

func (m legacyParticipantMutation) currentRole() (string, error) {
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
func (m legacyParticipantMutation) requireManage(ctx context.Context, principal *auth.UserInfo, previous, next string) error {
	action := auth.ResourceAction(m.spec.Policy.ManageParticipants)
	if previous == RoleOwner && next != RoleOwner {
		action = m.spec.Policy.RemoveOwner
	}
	return m.requirePermission(ctx, action, principal)
}

func (m legacyParticipantMutation) alreadyHasRole() error {
	return domainerr.Precondition(m.spec.Title + " participant already has this role")
}

// requireRemove requires RemoveOwner to remove an Owner and
// ManageParticipants otherwise, and returns the role the Member holds as far
// as the owner table tells.
func (m legacyParticipantMutation) requireRemove(ctx context.Context, principal *auth.UserInfo) (string, error) {
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
func (m legacyParticipantMutation) deleteForRemoval(role string) error {
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

func (m legacyParticipantMutation) requireAnotherOwner() error {
	totalOwners, err := m.countOwners("")
	if err != nil {
		return err
	}
	if totalOwners <= 1 {
		return domainerr.Precondition(m.spec.Article + " " + lastOwnerConstraint)
	}
	return nil
}

func (m legacyParticipantMutation) countOwners(memberID string) (int64, error) {
	query := m.tx.Table(m.spec.OwnerTable).Where(m.spec.IDColumn+" = ?::uuid", m.entityID)
	if memberID != "" {
		query = query.Where("member_id = ?::uuid", memberID)
	}
	var count int64
	return count, query.Count(&count).Error
}

func (m legacyParticipantMutation) deleteRelation(role string) error {
	return m.tx.Exec(m.spec.deleteRelationSQL(role), m.entityID, m.memberID).Error
}

func (m legacyParticipantMutation) insertRelation(role string) (time.Time, error) {
	createdAt := dbtime.Now()
	return createdAt, m.tx.Exec(
		fmt.Sprintf("INSERT INTO %s (%s, member_id, created_at) VALUES (?::uuid, ?::uuid, ?)", m.spec.relationTable(role), m.spec.IDColumn),
		m.entityID, m.memberID, createdAt,
	).Error
}

func (m legacyParticipantMutation) requirePermission(ctx context.Context, action auth.ResourceAction, principal *auth.UserInfo) error {
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


// LegacyMutations runs the entity's participant and metadata mutations through the
// mutation pipeline.
type LegacyMutations struct {
	spec    Spec
	runner  *mutation.Runner
	spiceDB *auth.SpiceDBClient
}

// LegacyMutations returns the entity's pipeline mutations.
func (s Spec) LegacyMutations(runner *mutation.Runner, spiceDB *auth.SpiceDBClient) LegacyMutations {
	return LegacyMutations{spec: s, runner: runner, spiceDB: spiceDB}
}

// legacyParticipantResult is a committed participant change.
type legacyParticipantResult struct {
	previous   string
	identityID string
	createdAt  time.Time
}

// SetParticipant gives the Member role on the entity, moving an existing
// participant between Owner and Manager. Demoting an Owner requires
// RemoveOwner, and the entity always keeps one Owner.
func (m LegacyMutations) SetParticipant(ctx context.Context, members Members, change ParticipantChange) (View, error) {
	var participant legacyParticipantMutation
	var previous string
	result, err := m.run(ctx, change, &participant, func(principal *auth.UserInfo) error {
		var err error
		if previous, err = participant.currentRole(); err != nil {
			return err
		}
		return participant.requireManage(ctx, principal, previous, change.Role)
	}, func(ctx context.Context, tx *gorm.DB) (legacyParticipantResult, error) {
		if previous != RoleNone && previous == change.Role {
			return legacyParticipantResult{}, participant.alreadyHasRole()
		}
		target, err := authorizationtarget.RequireLocked(ctx, tx, change.MemberID)
		if err != nil {
			return legacyParticipantResult{}, err
		}
		if previous == RoleOwner {
			if err := participant.requireAnotherOwner(); err != nil {
				return legacyParticipantResult{}, err
			}
		}
		if previous != RoleNone {
			if err := participant.deleteRelation(previous); err != nil {
				return legacyParticipantResult{}, err
			}
		}
		createdAt, err := participant.insertRelation(change.Role)
		return legacyParticipantResult{previous: previous, identityID: target.IdentityID, createdAt: createdAt}, err
	})
	if err != nil {
		return View{}, err
	}
	summary, err := members.LoadAuthorizationEligibleMemberSummary(ctx, change.MemberID)
	if err != nil {
		return View{}, err
	}
	return View{Member: summary, Role: change.Role, CreatedAt: result.createdAt, HasEffectiveAuthority: true}, nil
}

// RemoveParticipant removes the Member from the entity. Removing an Owner
// requires RemoveOwner and leaves at least one Owner.
func (m LegacyMutations) RemoveParticipant(ctx context.Context, change ParticipantChange) error {
	change.Role = RoleNone
	var participant legacyParticipantMutation
	var previous string
	_, err := m.run(ctx, change, &participant, func(principal *auth.UserInfo) error {
		var err error
		previous, err = participant.requireRemove(ctx, principal)
		return err
	}, func(ctx context.Context, tx *gorm.DB) (legacyParticipantResult, error) {
		if err := participant.deleteForRemoval(previous); err != nil {
			return legacyParticipantResult{}, err
		}
		target, err := authorizationtarget.RequireLocked(ctx, tx, change.MemberID)
		if err != nil {
			return legacyParticipantResult{}, err
		}
		return legacyParticipantResult{previous: previous, identityID: target.IdentityID}, nil
	})
	return err
}

// run locks the entity row in Lock, then authorizes the active principal
// with authorize, which may read the Member's current role, in Access.
func (m LegacyMutations) run(
	ctx context.Context,
	change ParticipantChange,
	participant *legacyParticipantMutation,
	authorize func(*auth.UserInfo) error,
	apply func(context.Context, *gorm.DB) (legacyParticipantResult, error),
) (legacyParticipantResult, error) {
	result, err := mutation.Run(ctx, m.runner, mutation.Spec[legacyParticipantResult]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			*participant = legacyParticipantMutation{spec: m.spec, spiceDB: m.spiceDB, tx: tx, entityID: change.EntityID, memberID: change.MemberID}
			return m.spec.LegacyLockRoot(ctx, tx, change.EntityID)
		},
		Access: mutation.Checked(func(ctx context.Context, tx *gorm.DB) error {
			principal, err := participant.lockPrincipal(ctx)
			if err != nil {
				return err
			}
			return authorize(principal)
		}),
		Change: apply,
		Audit: func(result legacyParticipantResult) mutation.Audit {
			return change.Audit(auditRelationship(result.previous), auditRelationship(change.Role))
		},
		Relationships: func(result legacyParticipantResult) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return m.spec.participantRelationshipMutations(change.EntityID, result.identityID, result.previous, change.Role)
		},
	})
	return result, m.spec.legacyMapMutationError(err)
}

// LegacyLockParentGraph serializes every change to the entity hierarchy for the
// rest of the transaction.
func (s Spec) LegacyLockParentGraph(ctx context.Context, tx *gorm.DB) error {
	return tx.WithContext(ctx).
		Exec("SELECT pg_advisory_xact_lock(hashtext(?))", s.ParentGraphLock).
		Error
}

// LegacyValidateParentTransition locks the hierarchy and checks that parentID, when
// set, names another existing entity that is not a descendant of entityID.
func (s Spec) LegacyValidateParentTransition(ctx context.Context, tx *gorm.DB, entityID, parentID string) error {
	if err := s.LegacyLockParentGraph(ctx, tx); err != nil {
		return err
	}
	if parentID == "" {
		return nil
	}
	if parentID == entityID {
		return domainerr.InvalidField(s.ParentColumn, "cannot reference the same "+s.Name)
	}
	if _, err := uuid.Parse(parentID); err != nil {
		return domainerr.InvalidField(s.ParentColumn, "must be a valid "+s.Title+" ID")
	}
	var parentExists bool
	if err := tx.WithContext(ctx).Raw(fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM %s WHERE id = ?::uuid)`, s.Table), parentID).Scan(&parentExists).Error; err != nil {
		return err
	}
	if !parentExists {
		return domainerr.InvalidField(s.ParentColumn, s.Name+" does not exist")
	}
	var cycle bool
	if err := tx.WithContext(ctx).Raw(fmt.Sprintf(`
		WITH RECURSIVE descendants(id) AS (
			SELECT id FROM %[1]s WHERE %[2]s = ?::uuid
			UNION
			SELECT child.id
			FROM %[1]s child
			JOIN descendants parent ON child.%[2]s = parent.id
		)
		SELECT EXISTS(SELECT 1 FROM descendants WHERE id = ?::uuid)
	`, s.Table, s.ParentColumn), entityID, parentID).Scan(&cycle).Error; err != nil {
		return err
	}
	if cycle {
		return domainerr.InvalidField(s.ParentColumn, "would create "+strings.ToLower(s.Article)+" hierarchy cycle")
	}
	return nil
}

// LegacyLoadParent returns the entity's current parent ID.
func (s Spec) LegacyLoadParent(ctx context.Context, db *gorm.DB, entityID string) (*string, error) {
	var row struct {
		ParentID *string `gorm:"column:parent_id"`
	}
	result := db.WithContext(ctx).
		Table(s.Table).
		Select(s.ParentColumn+" AS parent_id").
		Where("id = ?", entityID).
		Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, domainerr.ResourceNotFound(s.Name, entityID)
	}
	if result.Error != nil {
		return nil, result.Error
	}
	return row.ParentID, nil
}

// LegacyParseNullableStringUpdate trims a set value and rejects an empty set, a
// false clear and an empty mutation.
func LegacyParseNullableStringUpdate(field string, input *intrav1.NullableStringMutation) (NullableStringUpdate, error) {
	if input == nil {
		return NullableStringUpdate{}, nil
	}
	switch operation := input.Operation.(type) {
	case *intrav1.NullableStringMutation_Set:
		value := strings.TrimSpace(operation.Set)
		if value == "" {
			return NullableStringUpdate{}, domainerr.InvalidField(field, "set value must not be empty; use clear instead")
		}
		return NullableStringUpdate{Present: true, Value: &value}, nil
	case *intrav1.NullableStringMutation_Clear:
		if !operation.Clear {
			return NullableStringUpdate{}, domainerr.InvalidField(field, "clear must be true")
		}
		return NullableStringUpdate{Present: true}, nil
	default:
		return NullableStringUpdate{}, domainerr.InvalidField(field, "set or clear is required")
	}
}


// LegacyAssignColumns checks the planned parent against the row, adds every
// changed shared column to fields and has slugAvailable check a changed slug.
// It reports whether a shared column changed.
func (s Spec) LegacyAssignColumns(
	ctx context.Context,
	tx *gorm.DB,
	entityID string,
	columns MetadataColumns,
	parent ParentUpdate,
	writesParent bool,
	current MetadataRow,
	fields structured.Fields,
	slugAvailable func(context.Context, *gorm.DB, string) error,
) (bool, error) {
	if _, err := s.LegacyCheckParent(ctx, tx, entityID, parent, current.Parent, writesParent); err != nil {
		return false, err
	}
	slugChanged := columns.Slug.LegacyAssign(fields, "slug", current.Slug)
	changed := slugChanged
	changed = columns.CountryCode.LegacyAssign(fields, "country_code", current.CountryCode) || changed
	changed = columns.Website.LegacyAssign(fields, "website", current.Website) || changed
	changed = columns.Parent.LegacyAssign(fields, s.ParentColumn, current.Parent) || changed
	if columns.SetSocialLinks && !maps.Equal(current.SocialLinks, columns.SocialLinks) {
		fields["social_links"] = columns.SocialLinks
		changed = true
	}
	if slugChanged && columns.Slug.Value != nil {
		if err := slugAvailable(ctx, tx, *columns.Slug.Value); err != nil {
			return false, err
		}
	}
	return changed, nil
}


// LegacyPlanParentUpdate reads the entity's parent outside the transaction, which
// decides whether the save needs an authorization mutation.
func (s Spec) LegacyPlanParentUpdate(ctx context.Context, db *gorm.DB, entityID string, update NullableStringUpdate) (ParentUpdate, error) {
	previous, err := s.LegacyLoadParent(ctx, db, entityID)
	if err != nil {
		return ParentUpdate{}, err
	}
	return ParentUpdate{Update: update, previous: previous}, nil
}


// LegacyCheckParent validates the planned parent change against current, the
// parent read under the transaction, and reports whether it changes. writes
// is false when the save runs without an authorization mutation.
func (s Spec) LegacyCheckParent(ctx context.Context, tx *gorm.DB, entityID string, plan ParentUpdate, current *string, writes bool) (bool, error) {
	changed := plan.Update.Changes(current)
	if plan.Update.Present && (!sameOptionalString(current, plan.previous) || changed != plan.RequiresWrite()) {
		return false, s.parentChangedConcurrently()
	}
	if !changed {
		return false, nil
	}
	if !writes {
		return false, s.parentChangedConcurrently()
	}
	parentID := ""
	if plan.Update.Value != nil {
		parentID = *plan.Update.Value
	}
	return true, s.LegacyValidateParentTransition(ctx, tx, entityID, parentID)
}


// LegacyMetadataSave is one document-metadata save of the entity: it advances the
// entity's content document under Fence and moves the SpiceDB parent when
// the planned parent changes.
type LegacyMetadataSave struct {
	EntityID         string
	Documents        *contentblock.Store
	DocumentID       uuid.UUID
	ExpectedRevision uuid.UUID
	Parent           ParentUpdate
	// Access authorizes the save's contributors.
	Access mutation.Access
	Fence  contentblock.DomainFence
	// Apply writes the entity's metadata row under the fence. writesParent
	// tells LegacyCheckParent whether the save moves the SpiceDB parent.
	Apply func(ctx context.Context, tx *gorm.DB, writesParent bool) (LegacyMetadataApplied, error)
	// NormalizeError gives a content document failure the domain's answer.
	NormalizeError func(error) error
}

// LegacyMetadataApplied is what Apply changed and the parent it read under the
// lock, which the SpiceDB parent move starts from.
type LegacyMetadataApplied struct {
	Changed       bool
	CurrentParent *string
}

// LegacySaveMetadata runs save through the mutation pipeline.
func (m LegacyMutations) LegacySaveMetadata(ctx context.Context, save LegacyMetadataSave) (contentblock.AdvanceResult, error) {
	writesParent := save.Parent.RequiresWrite()
	var currentParent *string
	spec := mutation.Spec[contentblock.AdvanceResult]{
		Access: save.Access,
		Change: func(ctx context.Context, tx *gorm.DB) (contentblock.AdvanceResult, error) {
			advanced, err := save.Documents.AdvanceRevision(
				ctx, tx, contentblock.AdvanceInput{DocumentID: save.DocumentID, ExpectedRevision: save.ExpectedRevision}, save.Fence,
				func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
					applied, err := save.Apply(ctx, tx, writesParent)
					currentParent = applied.CurrentParent
					return contentblock.MetadataEffect{Changed: applied.Changed}, err
				},
			)
			if err != nil {
				return contentblock.AdvanceResult{}, save.NormalizeError(err)
			}
			return advanced, nil
		},
		// Telemetry has no document-metadata record; these saves have never
		// been audited.
		Audit: func(contentblock.AdvanceResult) mutation.Audit { return mutation.Unchanged() },
	}
	if writesParent {
		spec.Relationships = func(contentblock.AdvanceResult) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return m.spec.ReplaceParentRelationships(save.EntityID, currentParent, save.Parent.Update.Value)
		}
	}
	return mutation.Run(ctx, m.runner, spec)
}


func (s Spec) legacyMapMutationError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) && !mutation.IsClassified(err) {
		return domainerr.New(domainerr.NotFound, "", s.Title+" participant not found")
	}
	return s.MapMutationError(err)
}

// LegacyAssign stores the update under column when it changes current.
func (u NullableStringUpdate) LegacyAssign(fields structured.Fields, column string, current *string) bool {
	if !u.Changes(current) {
		return false
	}
	if u.Value == nil {
		fields[column] = nil
	} else {
		fields[column] = *u.Value
	}
	return true
}
