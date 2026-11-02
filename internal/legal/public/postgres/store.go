// Package legalpublicpostgres is the PostgreSQL store of the public Terms
// and Privacy reads, and their localization and OG image adapters.
package legalpublicpostgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/legal/public"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
)

// NewUnits returns the read snapshots of the public Terms and Privacy reads
// over db, with the content store they read documents through.
func NewUnits(db *gorm.DB, contentBlocks *contentblock.Store) (mutation.UnitOfWork[public.Store], error) {
	if db == nil {
		return nil, errors.New("public legal store database is required")
	}
	if contentBlocks == nil {
		return nil, errors.New("public legal store content blocks are required")
	}
	return mutation.Postgres(db, func(tx *gorm.DB) public.Store {
		return &store{tx: tx, contentBlocks: contentBlocks}
	}), nil
}

type store struct {
	tx            *gorm.DB
	contentBlocks *contentblock.Store
}

var _ public.Store = (*store)(nil)

func historyTable(kind legaldomain.PolicyKind) string { return string(kind) + "_history" }

func status(kind legaldomain.PolicyKind, state string) legaldomain.Status {
	stored, _ := legaldomain.PolicyStatus(kind, state)
	return stored
}

// publicPolicyRow is a published row of terms_history or privacy_history.
type publicPolicyRow struct {
	ID             string     `gorm:"column:id;primaryKey"`
	Version        int        `gorm:"column:version"`
	Title          string     `gorm:"column:title"`
	Status         string     `gorm:"column:status"`
	EffectiveFrom  *time.Time `gorm:"column:effective_from"`
	EffectiveUntil *time.Time `gorm:"column:effective_until"`
}

func (row publicPolicyRow) version() public.Version {
	return public.Version{
		ID: row.ID, Version: row.Version, Title: row.Title, Status: legaldomain.Status(row.Status),
		EffectiveFrom: row.EffectiveFrom, EffectiveUntil: row.EffectiveUntil,
	}
}

// first reads the first row query finds and reports whether one exists.
func first(query *gorm.DB) (public.Version, bool, error) {
	var row publicPolicyRow
	err := query.First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return public.Version{}, false, nil
	}
	if err != nil {
		return public.Version{}, false, err
	}
	return row.version(), true, nil
}

func (s *store) PublishedVersion(ctx context.Context, kind legaldomain.PolicyKind, id string) (public.Version, bool, error) {
	return first(s.tx.WithContext(ctx).Table(historyTable(kind)).
		Where("id = ?", id).
		Where("status IN ?", []legaldomain.Status{status(kind, "active"), status(kind, "archived")}))
}

func (s *store) VersionInStatus(ctx context.Context, kind legaldomain.PolicyKind, id string, status legaldomain.Status) (public.Version, bool, error) {
	return first(s.tx.WithContext(ctx).Table(historyTable(kind)).
		Where("id = ? AND status = ?", id, status))
}

func (s *store) EffectiveVersion(ctx context.Context, kind legaldomain.PolicyKind, now time.Time) (public.Version, bool, error) {
	return first(s.tx.WithContext(ctx).Table(historyTable(kind)).
		Where("status = ?", status(kind, "active")).
		Where("effective_from <= ?", now).
		Where("effective_until IS NULL OR effective_until > ?", now).
		Order("version DESC"))
}

func (s *store) NextScheduledVersion(ctx context.Context, kind legaldomain.PolicyKind, now time.Time) (public.Version, bool, error) {
	return first(s.tx.WithContext(ctx).Table(historyTable(kind)).
		Where("status = ?", status(kind, "scheduled")).
		Where("effective_from > ?", now).
		Order("effective_from ASC"))
}

func (s *store) CountArchived(ctx context.Context, kind legaldomain.PolicyKind) (int64, error) {
	var total int64
	err := s.tx.WithContext(ctx).Table(historyTable(kind)).Where("status = ?", status(kind, "archived")).Count(&total).Error
	return total, err
}

func (s *store) ListArchived(ctx context.Context, kind legaldomain.PolicyKind, limit, offset int32) ([]public.Version, error) {
	var rows []publicPolicyRow
	if err := s.tx.WithContext(ctx).Table(historyTable(kind)).
		Where("status = ?", status(kind, "archived")).
		Order("effective_from DESC").
		Limit(int(limit)).Offset(int(offset)).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	versions := make([]public.Version, len(rows))
	for i, row := range rows {
		versions[i] = row.version()
	}
	return versions, nil
}

func (s *store) ShareLink(ctx context.Context, token, entityType, entityID string, now time.Time) (public.ShareLink, bool, error) {
	var link model.ShareLink
	if err := s.tx.WithContext(ctx).
		Where("token = ? AND entity_type = ? AND entity_id = ? AND expires_at > ?", token, entityType, entityID, now).
		Take(&link).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return public.ShareLink{}, false, nil
		}
		return public.ShareLink{}, false, err
	}
	return public.ShareLink{ExpiresAt: link.ExpiresAt, PasswordHash: link.PasswordHash}, true, nil
}

func (s *store) Lifecycle(ctx context.Context, kind legaldomain.PolicyKind, id string) (legaldomain.Lifecycle, bool, error) {
	var target struct {
		Status        string     `gorm:"column:status"`
		EffectiveFrom *time.Time `gorm:"column:effective_from"`
	}
	if err := s.tx.WithContext(ctx).
		Table(historyTable(kind)).
		Select("status", "effective_from").
		Where("id = ?", id).
		Take(&target).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return legaldomain.Lifecycle{}, false, nil
		}
		return legaldomain.Lifecycle{}, false, err
	}
	return legaldomain.Lifecycle{Status: legaldomain.Status(target.Status), EffectiveFrom: target.EffectiveFrom}, true, nil
}

func (s *store) DocumentRoot(ctx context.Context, kind legaldomain.PolicyKind, id string) (public.DocumentRoot, error) {
	var root struct {
		ContentDocumentID *uuid.UUID `gorm:"column:content_document_id;type:uuid"`
		SourceLocale      string     `gorm:"column:source_locale"`
	}
	if err := s.tx.WithContext(ctx).Table(historyTable(kind)).
		Select("content_document_id", "source_locale").
		Where("id = ?", id).
		Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return public.DocumentRoot{}, notFoundError{err}
		}
		return public.DocumentRoot{}, err
	}
	return public.DocumentRoot{ContentDocumentID: root.ContentDocumentID, SourceLocale: root.SourceLocale}, nil
}

// notFoundError keeps the driver's not-found error readable and in the
// chain while it answers legaldomain.ErrPolicyNotFound.
type notFoundError struct{ error }

func (err notFoundError) Is(target error) bool { return target == legaldomain.ErrPolicyNotFound }
func (err notFoundError) Unwrap() error        { return err.error }

func (s *store) ContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error) {
	return s.contentBlocks.LoadSnapshot(ctx, s.tx, documentID, sourceLocale)
}
