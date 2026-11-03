// Package oauthapppostgres is the PostgreSQL Store of the OAuth app use
// cases: the oauth_sign_in_app rows and their Member criteria links, bound to
// one gorm transaction.
package oauthapppostgres

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/oauthapp"
)

// NewUnits returns the unit of work of the OAuth app use cases.
func NewUnits(db *gorm.DB) (mutation.UnitOfWork[oauthapp.Store], error) {
	if db == nil {
		return nil, errors.New("OAuth app store database is required")
	}
	return mutation.Postgres(db, func(tx *gorm.DB) oauthapp.Store { return store{tx: tx} }), nil
}

type store struct {
	tx *gorm.DB
}

type appRow struct {
	ClientID  string    `gorm:"column:client_id"`
	Access    string    `gorm:"column:access"`
	Revision  string    `gorm:"column:revision"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

type linkRow struct {
	ClientID         string `gorm:"column:client_id"`
	MemberCriteriaID string `gorm:"column:member_criteria_id"`
}

const appColumns = `client_id, access, revision::text AS revision, created_at, updated_at`

func (s store) Apps(ctx context.Context) ([]oauthapp.Record, error) {
	var rows []appRow
	if err := s.tx.WithContext(ctx).Raw(`SELECT ` + appColumns + ` FROM oauth_sign_in_app ORDER BY client_id`).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return s.records(ctx, rows)
}

func (s store) App(ctx context.Context, clientID string) (oauthapp.Record, error) {
	return s.app(ctx, `SELECT `+appColumns+` FROM oauth_sign_in_app WHERE client_id = ?`, clientID)
}

func (s store) LockApp(ctx context.Context, clientID string) (oauthapp.Record, error) {
	return s.app(ctx, `SELECT `+appColumns+` FROM oauth_sign_in_app WHERE client_id = ? FOR UPDATE`, clientID)
}

func (s store) app(ctx context.Context, query, clientID string) (oauthapp.Record, error) {
	var rows []appRow
	if err := s.tx.WithContext(ctx).Raw(query, clientID).Scan(&rows).Error; err != nil {
		return oauthapp.Record{}, err
	}
	if len(rows) == 0 {
		return oauthapp.Record{}, oauthapp.ErrAppNotFound
	}
	records, err := s.records(ctx, rows)
	if err != nil {
		return oauthapp.Record{}, err
	}
	return records[0], nil
}

func (s store) InsertApp(ctx context.Context, record oauthapp.Record) (oauthapp.Record, error) {
	var rows []appRow
	if err := s.tx.WithContext(ctx).Raw(`
		INSERT INTO oauth_sign_in_app (client_id, access) VALUES (?, ?)
		ON CONFLICT (client_id) DO NOTHING
		RETURNING `+appColumns, record.ClientID, string(record.Access)).Scan(&rows).Error; err != nil {
		return oauthapp.Record{}, err
	}
	if len(rows) == 0 {
		return oauthapp.Record{}, oauthapp.ErrAppExists
	}
	if err := s.insertLinks(ctx, record.ClientID, record.MemberCriteriaIDs); err != nil {
		return oauthapp.Record{}, err
	}
	return recordOf(rows[0], record.MemberCriteriaIDs), nil
}

func (s store) UpdateApp(ctx context.Context, record oauthapp.Record) (oauthapp.Record, error) {
	var rows []appRow
	if err := s.tx.WithContext(ctx).Raw(`
		UPDATE oauth_sign_in_app
		SET access = ?, revision = gen_random_uuid(), updated_at = now()
		WHERE client_id = ?
		RETURNING `+appColumns, string(record.Access), record.ClientID).Scan(&rows).Error; err != nil {
		return oauthapp.Record{}, err
	}
	if len(rows) == 0 {
		return oauthapp.Record{}, oauthapp.ErrAppNotFound
	}
	if err := s.tx.WithContext(ctx).Exec(`DELETE FROM oauth_sign_in_app_member_criteria WHERE client_id = ?`, record.ClientID).Error; err != nil {
		return oauthapp.Record{}, err
	}
	if err := s.insertLinks(ctx, record.ClientID, record.MemberCriteriaIDs); err != nil {
		return oauthapp.Record{}, err
	}
	return recordOf(rows[0], record.MemberCriteriaIDs), nil
}

func (s store) DeleteApp(ctx context.Context, clientID string) error {
	result := s.tx.WithContext(ctx).Exec(`DELETE FROM oauth_sign_in_app WHERE client_id = ?`, clientID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return oauthapp.ErrAppNotFound
	}
	return nil
}

func (s store) insertLinks(ctx context.Context, clientID string, criteriaIDs []string) error {
	if len(criteriaIDs) == 0 {
		return nil
	}
	links := make([]linkRow, len(criteriaIDs))
	for i, criteriaID := range criteriaIDs {
		links[i] = linkRow{ClientID: clientID, MemberCriteriaID: criteriaID}
	}
	return s.tx.WithContext(ctx).Table("oauth_sign_in_app_member_criteria").Create(&links).Error
}

// records attaches each row's Member criteria links.
func (s store) records(ctx context.Context, rows []appRow) ([]oauthapp.Record, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	clientIDs := make([]string, len(rows))
	for i, row := range rows {
		clientIDs[i] = row.ClientID
	}
	var links []linkRow
	if err := s.tx.WithContext(ctx).Raw(`
		SELECT client_id, member_criteria_id::text AS member_criteria_id
		FROM oauth_sign_in_app_member_criteria
		WHERE client_id IN ?
		ORDER BY client_id, member_criteria_id`, clientIDs).Scan(&links).Error; err != nil {
		return nil, err
	}
	criteriaByApp := make(map[string][]string, len(rows))
	for _, link := range links {
		criteriaByApp[link.ClientID] = append(criteriaByApp[link.ClientID], link.MemberCriteriaID)
	}
	records := make([]oauthapp.Record, len(rows))
	for i, row := range rows {
		records[i] = recordOf(row, criteriaByApp[row.ClientID])
	}
	return records, nil
}

func recordOf(row appRow, criteriaIDs []string) oauthapp.Record {
	return oauthapp.Record{
		ClientID: row.ClientID, Access: oauthapp.Access(row.Access), MemberCriteriaIDs: criteriaIDs,
		Revision: row.Revision, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}
