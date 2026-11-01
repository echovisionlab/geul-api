// Package postgres is the PostgreSQL entityparticipant.Store over one
// participant-governed entity's tables. Artist and Label embed Store in
// their own PostgreSQL stores.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
)

// Tables names one entity's tables and columns.
type Tables struct {
	Table           string
	IDColumn        string
	OwnerTable      string
	ManagerTable    string
	ParentColumn    string
	ParentGraphLock string
}

// Store implements entityparticipant.Store over tx.
type Store struct {
	tx     *gorm.DB
	tables Tables
}

// New binds the entity's tables to tx.
func New(tx *gorm.DB, tables Tables) Store {
	return Store{tx: tx, tables: tables}
}

var _ entityparticipant.Store = Store{}

// notFoundError keeps the driver's not-found error readable and in the
// chain while it answers entityparticipant.ErrEntityNotFound.
type notFoundError struct{ error }

func (err notFoundError) Is(target error) bool { return target == entityparticipant.ErrEntityNotFound }
func (err notFoundError) Unwrap() error        { return err.error }

// LockEntity implements entityparticipant.Store.
func (s Store) LockEntity(ctx context.Context, entityID string) error {
	var row struct {
		ID string `gorm:"column:id"`
	}
	err := s.tx.WithContext(ctx).Table(s.tables.Table).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Select("id").Where("id = ?::uuid", entityID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return notFoundError{err}
	}
	return err
}

// ParticipantRole implements entityparticipant.Store.
func (s Store) ParticipantRole(ctx context.Context, entityID, memberID string) (string, error) {
	for _, role := range []string{entityparticipant.RoleOwner, entityparticipant.RoleManager} {
		var row struct {
			CreatedAt time.Time `gorm:"column:created_at"`
		}
		err := s.tx.Table(s.relationTable(role)).
			Select("created_at").
			Where(s.tables.IDColumn+" = ?::uuid AND member_id = ?::uuid", entityID, memberID).
			Scan(&row).Error
		if err != nil {
			return entityparticipant.RoleNone, err
		}
		if !row.CreatedAt.IsZero() {
			return role, nil
		}
	}
	return entityparticipant.RoleNone, nil
}

// CountOwners implements entityparticipant.Store.
func (s Store) CountOwners(_ context.Context, entityID, memberID string) (int64, error) {
	query := s.tx.Table(s.tables.OwnerTable).Where(s.tables.IDColumn+" = ?::uuid", entityID)
	if memberID != "" {
		query = query.Where("member_id = ?::uuid", memberID)
	}
	var count int64
	return count, query.Count(&count).Error
}

// InsertParticipant implements entityparticipant.Store.
func (s Store) InsertParticipant(_ context.Context, entityID, memberID, role string, createdAt time.Time) error {
	return s.tx.Exec(
		fmt.Sprintf("INSERT INTO %s (%s, member_id, created_at) VALUES (?::uuid, ?::uuid, ?)", s.relationTable(role), s.tables.IDColumn),
		entityID, memberID, createdAt,
	).Error
}

// DeleteParticipant implements entityparticipant.Store.
func (s Store) DeleteParticipant(_ context.Context, entityID, memberID, role string) (bool, error) {
	result := s.tx.Exec(
		fmt.Sprintf("DELETE FROM %s WHERE %s = ?::uuid AND member_id = ?::uuid", s.relationTable(role), s.tables.IDColumn),
		entityID, memberID,
	)
	return result.RowsAffected != 0, result.Error
}

// LockMemberIdentity implements entityparticipant.Store.
func (s Store) LockMemberIdentity(ctx context.Context, memberID string) (string, error) {
	target, err := authorizationtarget.RequireLocked(ctx, s.tx, memberID)
	if err != nil {
		return "", err
	}
	return target.IdentityID, nil
}

// ListParticipants implements entityparticipant.Store.
func (s Store) ListParticipants(ctx context.Context, entityID string) ([]entityparticipant.Participant, error) {
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
	`, s.tables.IDColumn, s.tables.OwnerTable, s.tables.IDColumn, s.tables.IDColumn, s.tables.ManagerTable, s.tables.IDColumn)
	var rows []struct {
		MemberID  string    `gorm:"column:member_id"`
		Role      string    `gorm:"column:role"`
		CreatedAt time.Time `gorm:"column:created_at"`
	}
	if err := s.tx.WithContext(ctx).Raw(query, entityID, entityID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	participants := make([]entityparticipant.Participant, 0, len(rows))
	for _, row := range rows {
		participants = append(participants, entityparticipant.Participant{MemberID: row.MemberID, Role: row.Role, CreatedAt: row.CreatedAt})
	}
	return participants, nil
}

// LockParentGraph implements entityparticipant.Store.
func (s Store) LockParentGraph(ctx context.Context) error {
	return s.tx.WithContext(ctx).
		Exec("SELECT pg_advisory_xact_lock(hashtext(?))", s.tables.ParentGraphLock).
		Error
}

// EntityExists implements entityparticipant.Store.
func (s Store) EntityExists(ctx context.Context, entityID string) (bool, error) {
	var exists bool
	err := s.tx.WithContext(ctx).Raw(fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM %s WHERE id = ?::uuid)`, s.tables.Table), entityID).Scan(&exists).Error
	return exists, err
}

// IsDescendant implements entityparticipant.Store.
func (s Store) IsDescendant(ctx context.Context, entityID, candidateID string) (bool, error) {
	var cycle bool
	err := s.tx.WithContext(ctx).Raw(fmt.Sprintf(`
		WITH RECURSIVE descendants(id) AS (
			SELECT id FROM %[1]s WHERE %[2]s = ?::uuid
			UNION
			SELECT child.id
			FROM %[1]s child
			JOIN descendants parent ON child.%[2]s = parent.id
		)
		SELECT EXISTS(SELECT 1 FROM descendants WHERE id = ?::uuid)
	`, s.tables.Table, s.tables.ParentColumn), entityID, candidateID).Scan(&cycle).Error
	return cycle, err
}

// EntityParent implements entityparticipant.Store.
func (s Store) EntityParent(ctx context.Context, entityID string) (*string, error) {
	var row struct {
		ParentID *string `gorm:"column:parent_id"`
	}
	err := s.tx.WithContext(ctx).
		Table(s.tables.Table).
		Select(s.tables.ParentColumn+" AS parent_id").
		Where("id = ?", entityID).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, notFoundError{err}
	}
	if err != nil {
		return nil, err
	}
	return row.ParentID, nil
}

func (s Store) relationTable(role string) string {
	if role == entityparticipant.RoleOwner {
		return s.tables.OwnerTable
	}
	return s.tables.ManagerTable
}
