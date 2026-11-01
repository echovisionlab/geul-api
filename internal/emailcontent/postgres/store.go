// Package emailcontentpostgres is the PostgreSQL emailcontent.Store of one
// owner: its root row, its locale rows in the owner's _translation table and
// its content document in the shared content store, bound to one gorm
// transaction. An owner's Store adapter embeds it.
package emailcontentpostgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// Store is the email content of the owner whose table is entity, over tx.
type Store struct {
	tx       *gorm.DB
	contents *contentblock.Store
	entity   string
}

var _ emailcontent.Store = Store{}

// New binds the email content of the owner table entity to tx; contents is
// the shared content store.
func New(tx *gorm.DB, contents *contentblock.Store, entity string) Store {
	return Store{tx: tx, contents: contents, entity: entity}
}

func (s Store) translationTable() string { return s.entity + "_translation" }

// Lock is the row lock clause of lock, empty for an unlocked read.
func Lock(lock emailcontent.RowLock) string {
	switch lock {
	case emailcontent.ShareLock:
		return clause.LockingStrengthShare
	case emailcontent.UpdateLock:
		return clause.LockingStrengthUpdate
	default:
		return ""
	}
}

// DomainFence runs fence over the transaction the content store hands it,
// which is the Store's own.
func DomainFence(fence emailcontent.Fence) contentblock.DomainFence {
	if fence == nil {
		return nil
	}
	return func(ctx context.Context, _ *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		return fence(ctx, documentID)
	}
}

// MetadataMutation runs effect in the content store's transaction.
func MetadataMutation(effect emailcontent.MetadataEffect) contentblock.MetadataMutation {
	if effect == nil {
		return nil
	}
	return func(ctx context.Context, _ *gorm.DB) (contentblock.MetadataEffect, error) {
		return effect(ctx)
	}
}

func (s Store) CreateContentDocument(ctx context.Context, input contentblock.CreateInput) (contentblock.Snapshot, error) {
	return s.contents.CreateDocument(ctx, s.tx, input)
}

func (s Store) ContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error) {
	return s.contents.LoadSnapshot(ctx, s.tx, documentID, sourceLocale)
}

func (s Store) LockedContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error) {
	return s.contents.LoadSnapshotInTransaction(ctx, s.tx, documentID, sourceLocale)
}

func (s Store) ApplyContentBatch(ctx context.Context, batch contentblock.Batch, fence emailcontent.Fence) (contentblock.Result, error) {
	return s.contents.ApplyBatch(ctx, s.tx, batch, DomainFence(fence))
}

func (s Store) ApplyContentBatchWithMetadata(ctx context.Context, batch contentblock.Batch, fence emailcontent.Fence, effect emailcontent.MetadataEffect) (contentblock.Result, error) {
	return s.contents.ApplyBatchWithMetadata(ctx, s.tx, batch, DomainFence(fence), MetadataMutation(effect))
}

func (s Store) ApplyTargetLocaleContentBatch(ctx context.Context, batch contentblock.Batch, locale string, fence emailcontent.Fence, effect emailcontent.TargetMetadataEffect) (contentblock.Result, error) {
	var mutation contentblock.TargetLocaleMetadataMutation
	if effect != nil {
		mutation = func(ctx context.Context, _ *gorm.DB, contentChanged bool) (contentblock.MetadataEffect, error) {
			return effect(ctx, contentChanged)
		}
	}
	return s.contents.ApplyTargetLocaleBatchWithMetadata(ctx, s.tx, batch, locale, DomainFence(fence), mutation)
}

func (s Store) AdvanceContentRevision(ctx context.Context, input contentblock.AdvanceInput, fence emailcontent.Fence, effect emailcontent.MetadataEffect) (contentblock.AdvanceResult, error) {
	return s.contents.AdvanceRevision(ctx, s.tx, input, DomainFence(fence), MetadataMutation(effect))
}

func (s Store) DeleteContentDocument(ctx context.Context, documentID uuid.UUID, fence emailcontent.Fence) error {
	return s.contents.DeleteDocument(ctx, s.tx, documentID, DomainFence(fence))
}

type localeRow struct {
	Locale    string         `gorm:"column:locale"`
	Subject   sql.NullString `gorm:"column:subject"`
	UpdatedAt time.Time      `gorm:"column:updated_at"`
}

func (s Store) LocaleRow(ctx context.Context, id, locale string, lock emailcontent.RowLock) (emailcontent.LocaleMetadata, bool, error) {
	query := s.tx.WithContext(ctx).Table(s.translationTable()).
		Select("locale", "subject", "updated_at").
		Where("entity_id = ? AND locale = ?", id, locale)
	if strength := Lock(lock); strength != "" {
		query = query.Clauses(clause.Locking{Strength: strength})
	}
	var row localeRow
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return emailcontent.LocaleMetadata{}, false, nil
		}
		return emailcontent.LocaleMetadata{}, false, err
	}
	metadata := emailcontent.LocaleMetadata{Locale: row.Locale, UpdatedAt: row.UpdatedAt}
	if row.Subject.Valid {
		subject := row.Subject.String
		metadata.Subject = &subject
	}
	return metadata, true, nil
}

func (s Store) LocaleNames(ctx context.Context, id string) ([]string, error) {
	var locales []string
	err := s.tx.WithContext(ctx).Table(s.translationTable()).
		Where("entity_id = ?", id).
		Order("locale ASC").Pluck("locale", &locales).Error
	return locales, err
}

// subjectValue is the stored subject: NULL for nil.
func subjectValue(subject *string) any {
	if subject == nil {
		return nil
	}
	return *subject
}

func (s Store) InsertLocale(ctx context.Context, id, locale string, subject *string, now time.Time) (bool, error) {
	created := s.tx.WithContext(ctx).Exec(
		`INSERT INTO `+s.translationTable()+` (
		 entity_id, locale, subject, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?)`,
		id, locale, subjectValue(subject), now, now,
	)
	return created.RowsAffected == 1, created.Error
}

func (s Store) UpdateLocale(ctx context.Context, id, locale string, subject *string, updatedAt time.Time) (bool, error) {
	updated := s.tx.WithContext(ctx).Table(s.translationTable()).
		Where("entity_id = ? AND locale = ?", id, locale).
		Updates(map[string]any{"subject": subjectValue(subject), "updated_at": updatedAt})
	return updated.RowsAffected == 1, updated.Error
}

func (s Store) UpsertLocaleSubject(ctx context.Context, id, locale string, subject *string, now time.Time) error {
	return s.tx.WithContext(ctx).Exec(`
		INSERT INTO `+s.translationTable()+` (
			entity_id, locale, subject, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (entity_id, locale) DO UPDATE SET
			subject = EXCLUDED.subject, updated_at = EXCLUDED.updated_at`,
		id, locale, subject, now, now,
	).Error
}

func (s Store) DeleteLocale(ctx context.Context, id, locale string) (bool, error) {
	deleted := s.tx.WithContext(ctx).Exec(
		"DELETE FROM "+s.translationTable()+" WHERE entity_id = ? AND locale = ?", id, locale,
	)
	return deleted.RowsAffected == 1, deleted.Error
}

func (s Store) ProjectLocale(ctx context.Context, id, locale string, projection emailcontent.LocaleProjection) (bool, error) {
	updates := map[string]any{"content_html": projection.HTML, "content_text": projection.Text}
	if projection.UpdatedAt != nil {
		updates["updated_at"] = *projection.UpdatedAt
	}
	updated := s.tx.WithContext(ctx).Table(s.translationTable()).
		Where("entity_id = ? AND locale = ?", id, locale).
		Updates(updates)
	return updated.RowsAffected == 1, updated.Error
}

func (s Store) TouchOwner(ctx context.Context, id string, now time.Time) error {
	return s.tx.WithContext(ctx).Table(s.entity).Where("id = ?", id).Update("updated_at", now).Error
}

// contentEntityTypes name the email content owners in content.updated
// signals.
var contentEntityTypes = map[string]managev1.ContentEntityType{
	"campaign":       managev1.ContentEntityType_CONTENT_ENTITY_TYPE_CAMPAIGN,
	"email_template": managev1.ContentEntityType_CONTENT_ENTITY_TYPE_EMAIL_TEMPLATE,
	"email_layout":   managev1.ContentEntityType_CONTENT_ENTITY_TYPE_EMAIL_LAYOUT,
}

func (s Store) PublishContentUpdate(ctx context.Context, update emailcontent.ContentUpdate) error {
	return contentsignal.PublishInteractive(
		ctx, s.tx, contentEntityTypes[update.Entity], update.ID, update.Locale, update.DocumentRevision,
		update.TargetRevision, update.Changed, update.Deleted, update.ContributorMemberID,
	)
}
