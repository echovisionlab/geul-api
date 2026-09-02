// Package memberpat adapts the Member PAT domain to PostgreSQL persistence.
package memberpat

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/echovisionlab/geul-api/internal/dberrors"
	memberpat "github.com/echovisionlab/geul-api/internal/member/pat"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const personalAccessTokenTable = "member_personal_access_token"

// PersonalAccessTokenRepository persists the current Member-owned PAT
// credential. It stores only the selector and SHA-256 verifier, never the
// bearer secret.
type PersonalAccessTokenRepository struct {
	db *gorm.DB
}

// NewPersonalAccessTokenRepository constructs the Member PAT persistence
// adapter. Schema creation remains owned by the schema repository.
func NewPersonalAccessTokenRepository(db *gorm.DB) (*PersonalAccessTokenRepository, error) {
	if db == nil {
		return nil, memberpat.ErrInvalidDependencies
	}
	return &PersonalAccessTokenRepository{db: db}, nil
}

func (repository *PersonalAccessTokenRepository) Create(
	ctx context.Context,
	record memberpat.StoredToken,
) error {
	row := personalAccessTokenRowFromDomain(record)
	if err := repository.db.WithContext(ctx).Table(personalAccessTokenTable).Create(&row).Error; err != nil {
		if dberrors.IsUniqueViolation(err) {
			return memberpat.ErrTokenAlreadyExists
		}
		return fmt.Errorf("insert Member personal access token: %w", err)
	}
	return nil
}

func (repository *PersonalAccessTokenRepository) ListByMember(
	ctx context.Context,
	memberID memberpat.MemberID,
) ([]memberpat.StoredToken, error) {
	var rows []personalAccessTokenRow
	if err := repository.db.WithContext(ctx).
		Table(personalAccessTokenTable).
		Where("member_id = ?", string(memberID)).
		Order("created_at ASC, selector ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list Member personal access tokens: %w", err)
	}
	return personalAccessTokenRowsToDomain(rows)
}

func (repository *PersonalAccessTokenRepository) FindByID(
	ctx context.Context,
	tokenID memberpat.TokenID,
) (memberpat.StoredToken, error) {
	var row personalAccessTokenRow
	err := repository.db.WithContext(ctx).
		Table(personalAccessTokenTable).
		Where("selector = ?", string(tokenID)).
		Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return memberpat.StoredToken{}, memberpat.ErrTokenNotFound
		}
		return memberpat.StoredToken{}, fmt.Errorf("find Member personal access token: %w", err)
	}
	return personalAccessTokenRowToDomain(row)
}

func (repository *PersonalAccessTokenRepository) ReplaceVerifier(
	ctx context.Context,
	memberID memberpat.MemberID,
	tokenID memberpat.TokenID,
	verifier memberpat.Verifier,
	updatedAt time.Time,
) (memberpat.StoredToken, error) {
	var row personalAccessTokenRow
	result := repository.db.WithContext(ctx).
		Table(personalAccessTokenTable).
		Model(&row).
		Clauses(clause.Returning{}).
		Where("selector = ? AND member_id = ?", string(tokenID), string(memberID)).
		Updates(map[string]any{
			"secret_hash": verifier.Bytes(),
			"updated_at":  updatedAt.UTC(),
		})
	if result.Error != nil {
		return memberpat.StoredToken{}, fmt.Errorf("replace Member personal access token verifier: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return memberpat.StoredToken{}, memberpat.ErrTokenNotFound
	}
	return personalAccessTokenRowToDomain(row)
}

func (repository *PersonalAccessTokenRepository) TouchLastUsedAt(
	ctx context.Context,
	memberID memberpat.MemberID,
	tokenID memberpat.TokenID,
	verifier memberpat.Verifier,
	usedAt time.Time,
) error {
	usedAt = usedAt.UTC()
	result := repository.db.WithContext(ctx).
		Table(personalAccessTokenTable).
		Where(
			"selector = ? AND member_id = ? AND secret_hash = ?",
			string(tokenID),
			string(memberID),
			verifier.Bytes(),
		).
		UpdateColumn(
			"last_used_at",
			gorm.Expr(
				"CASE WHEN last_used_at IS NULL OR last_used_at < ? THEN ? ELSE last_used_at END",
				usedAt,
				usedAt,
			),
		)
	if result.Error != nil {
		return fmt.Errorf("touch Member personal access token last use: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return memberpat.ErrTokenNotFound
	}
	return nil
}

func (repository *PersonalAccessTokenRepository) Delete(
	ctx context.Context,
	memberID memberpat.MemberID,
	tokenID memberpat.TokenID,
) error {
	result := repository.db.WithContext(ctx).
		Table(personalAccessTokenTable).
		Where("selector = ? AND member_id = ?", string(tokenID), string(memberID)).
		Delete(&personalAccessTokenRow{})
	if result.Error != nil {
		return fmt.Errorf("delete Member personal access token: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return memberpat.ErrTokenNotFound
	}
	return nil
}

type personalAccessTokenRow struct {
	Selector   string     `gorm:"column:selector;primaryKey"`
	MemberID   string     `gorm:"column:member_id"`
	SecretHash []byte     `gorm:"column:secret_hash"`
	CreatedAt  time.Time  `gorm:"column:created_at;autoCreateTime:false"`
	UpdatedAt  time.Time  `gorm:"column:updated_at;autoUpdateTime:false"`
	LastUsedAt *time.Time `gorm:"column:last_used_at"`
}

func personalAccessTokenRowFromDomain(record memberpat.StoredToken) personalAccessTokenRow {
	return personalAccessTokenRow{
		Selector:   string(record.ID),
		MemberID:   string(record.MemberID),
		SecretHash: record.Verifier.Bytes(),
		CreatedAt:  record.CreatedAt.UTC(),
		UpdatedAt:  record.UpdatedAt.UTC(),
	}
}

func personalAccessTokenRowsToDomain(rows []personalAccessTokenRow) ([]memberpat.StoredToken, error) {
	records := make([]memberpat.StoredToken, len(rows))
	for index, row := range rows {
		record, err := personalAccessTokenRowToDomain(row)
		if err != nil {
			return nil, err
		}
		records[index] = record
	}
	return records, nil
}

func personalAccessTokenRowToDomain(row personalAccessTokenRow) (memberpat.StoredToken, error) {
	verifier, err := memberpat.VerifierFromBytes(row.SecretHash)
	if err != nil {
		return memberpat.StoredToken{}, memberpat.ErrInvalidStoredToken
	}
	return memberpat.StoredToken{
		ID:        memberpat.TokenID(row.Selector),
		MemberID:  memberpat.MemberID(row.MemberID),
		Verifier:  verifier,
		CreatedAt: row.CreatedAt.UTC(),
		UpdatedAt: row.UpdatedAt.UTC(),
	}, nil
}

var _ memberpat.Repository = (*PersonalAccessTokenRepository)(nil)
