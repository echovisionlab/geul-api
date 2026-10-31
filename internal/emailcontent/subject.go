package emailcontent

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
)

// loadSubject reads the subject of the owner's locale row, reporting
// whether the row exists; a row may hold no subject.
func (o Owner) loadSubject(ctx context.Context, db *gorm.DB, id, locale string, forUpdate bool) (*string, bool, error) {
	var row struct{ Subject sql.NullString }
	query := db.WithContext(ctx).Table(o.translationTable()).
		Select("subject").Where("entity_id = ? AND locale = ?", id, locale)
	if forUpdate {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	result := query.Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if result.Error != nil {
		return nil, false, errs.Internal(result.Error)
	}
	if !row.Subject.Valid {
		return nil, true, nil
	}
	value := row.Subject.String
	return &value, true, nil
}

// ApplySubject stores the mutation's subject in its locale row with the
// content batch. The locale's presence must be what the caller observed,
// and the source subject cannot be empty.
func (o Owner) ApplySubject(ctx context.Context, tx *gorm.DB, mutation AIDocumentMutation, now time.Time) (contentblock.MetadataEffect, error) {
	current, exists, err := o.loadSubject(ctx, tx, mutation.Reference, mutation.Locale, true)
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	if exists != mutation.ExpectedPresence {
		return contentblock.MetadataEffect{}, errs.FailedPrecondition(o.Name + " locale presence changed; reload before saving")
	}
	source := mutation.Locale == mutation.ExpectedSource
	if !exists && source {
		return contentblock.MetadataEffect{}, errs.FailedPrecondition(o.Name + " source locale is missing")
	}
	next := cloneString(current)
	if mutation.SetSubject {
		next = cloneString(&mutation.Subject)
	}
	if source && (next == nil || strings.TrimSpace(*next) == "") {
		return contentblock.MetadataEffect{}, errs.InvalidArgument("subject", o.Name+" source subject cannot be empty")
	}
	if exists && stringsEqual(current, next) {
		return contentblock.MetadataEffect{}, nil
	}
	if err := tx.WithContext(ctx).Exec(`
		INSERT INTO `+o.translationTable()+` (
			entity_id, locale, subject, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (entity_id, locale) DO UPDATE SET
			subject = EXCLUDED.subject, updated_at = EXCLUDED.updated_at`,
		mutation.Reference, mutation.Locale, next, now, now,
	).Error; err != nil {
		return contentblock.MetadataEffect{}, errs.Internal(err)
	}
	return contentblock.MetadataEffect{
		Changed: true, AffectsTranslationSource: source,
		SourceLocale: mutation.ExpectedSource, ChangedLocales: []string{mutation.Locale},
	}, nil
}
