package translationpostgres

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"time"

	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/sliceutil"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
)

func (s *store) DocumentAuthority(ctx context.Context, entityType, entityID string) (application.DocumentAuthority, error) {
	definition, ok := translation.DefinitionForKind(entityType)
	if !ok {
		return application.DocumentAuthority{}, fmt.Errorf("unsupported translation entity type %q", entityType)
	}
	var authority struct {
		SourceLocale string    `gorm:"column:source_locale"`
		DocumentID   uuid.UUID `gorm:"column:content_document_id"`
	}
	result := s.tx.WithContext(ctx).Raw(
		"SELECT source_locale, content_document_id FROM "+definition.RootTable+" WHERE id = ?",
		entityID,
	).Scan(&authority)
	if result.Error != nil {
		return application.DocumentAuthority{}, result.Error
	}
	if result.RowsAffected != 1 || authority.DocumentID == uuid.Nil {
		return application.DocumentAuthority{}, application.ErrEntityNotFound
	}
	revision, err := contentblock.CurrentRevision(ctx, s.tx, authority.DocumentID)
	if err != nil {
		return application.DocumentAuthority{}, notFound(err, application.ErrEntityNotFound)
	}
	return application.DocumentAuthority{
		SourceLocale: authority.SourceLocale, DocumentID: authority.DocumentID, DocumentRevision: revision,
	}, nil
}

func (s *store) Entries(ctx context.Context, entityType, entityID string) ([]application.Entry, error) {
	selectSQL, err := s.entrySelectSQL(entityType)
	if err != nil {
		return nil, err
	}
	return s.scanEntries(ctx, entityType, entityID,
		fmt.Sprintf(`%s WHERE entity_id = ? ORDER BY locale ASC`, selectSQL), entityID)
}

func (s *store) Entry(ctx context.Context, entityType, entityID, locale string) (application.Entry, error) {
	selectSQL, err := s.entrySelectSQL(entityType)
	if err != nil {
		return application.Entry{}, err
	}
	entries, err := s.scanEntries(ctx, entityType, entityID,
		fmt.Sprintf(`%s WHERE entity_id = ? AND locale = ? LIMIT 1`, selectSQL), entityID, locale)
	if err != nil {
		return application.Entry{}, err
	}
	if len(entries) == 0 {
		return application.Entry{}, application.ErrEntryNotFound
	}
	return entries[0], nil
}

func (s *store) entrySelectSQL(entityType string) (string, error) {
	definition, ok := translation.DefinitionForKind(entityType)
	if !ok {
		return "", fmt.Errorf("unsupported translation entity type %q", entityType)
	}
	return s.ports.Domains.TranslationEntrySelectSQL(entityType, definition.EntryTable)
}

type entryRow struct {
	locale      string
	title       sql.NullString
	summary     sql.NullString
	contentHTML sql.NullString
	contentText sql.NullString
	contentJSON []byte
	updatedAt   time.Time
	ogAssetID   sql.NullString
}

// scanEntries reads every row before it resolves an OG asset, since the
// transaction's connection serves one statement at a time.
func (s *store) scanEntries(ctx context.Context, entityType, entityID, query string, args ...any) ([]application.Entry, error) {
	scanned, err := s.entryRows(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	entries := make([]application.Entry, 0, len(scanned))
	for _, row := range scanned {
		entry := application.Entry{
			EntityType: entityType, EntityID: entityID, Locale: row.locale, UpdatedAt: row.updatedAt,
			Title: nullableString(row.title), Summary: nullableString(row.summary),
			ContentHTML: nullableString(row.contentHTML), ContentText: nullableString(row.contentText),
			ContentJSON: row.contentJSON,
		}
		if row.ogAssetID.Valid {
			asset, err := mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(s.tx), s.ports.CDNDomain).ReadyAssetRef(ctx, row.ogAssetID.String)
			if err != nil {
				return nil, err
			}
			entry.OgAsset = asset
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func (s *store) entryRows(ctx context.Context, query string, args ...any) ([]entryRow, error) {
	rows, err := s.tx.WithContext(ctx).Raw(query, args...).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var scanned []entryRow
	for rows.Next() {
		var row entryRow
		if err := rows.Scan(
			&row.locale, &row.title, &row.summary, &row.contentHTML, &row.contentText,
			&row.contentJSON, &row.updatedAt, &row.ogAssetID,
		); err != nil {
			return nil, err
		}
		scanned = append(scanned, row)
	}
	return scanned, rows.Err()
}

func nullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

func (s *store) SourceSwitchLocales(ctx context.Context, entityType, entityID string, documentID uuid.UUID, previous, next string) ([]string, error) {
	definition, ok := translation.DefinitionForKind(entityType)
	if !ok {
		return nil, fmt.Errorf("unsupported translation entity type %q", entityType)
	}
	locales := []string{previous, next}
	var entryLocales []string
	if err := s.tx.WithContext(ctx).Table(definition.EntryTable).
		Where("entity_id = ?", entityID).Pluck("locale", &entryLocales).Error; err != nil {
		return nil, err
	}
	locales = append(locales, entryLocales...)
	blockLocales, err := contentblock.DocumentLocales(ctx, s.tx, documentID)
	if err != nil {
		return nil, err
	}
	locales = sliceutil.UniqueNonZero(append(locales, blockLocales...))
	slices.Sort(locales)
	return locales, nil
}

func (s *store) SwitchSourceLocale(ctx context.Context, change application.SourceLocaleSwitch) (application.SwitchedSourceLocale, error) {
	definition, ok := translation.DefinitionForKind(change.EntityType)
	if !ok {
		return application.SwitchedSourceLocale{}, fmt.Errorf("unsupported translation entity type %q", change.EntityType)
	}
	authority := change.Authority
	switched, err := s.ports.ContentBlocks.SwitchSourceLocale(
		ctx,
		s.tx,
		contentblock.SourceLocaleSwitchInput{
			DocumentID:           authority.DocumentID,
			ExpectedRevision:     change.ExpectedRevision,
			RequestedLocale:      change.Requested,
			ContributorMemberIDs: []uuid.UUID{change.Contributor},
		},
		func(context.Context, *gorm.DB, uuid.UUID) (contentblock.DomainContext, error) {
			return contentblock.DomainContext{
				SourceLocale: authority.SourceLocale,
			}, nil
		},
		func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
			if authority.SourceLocale == change.Requested {
				return contentblock.MetadataEffect{}, nil
			}
			if err := s.ports.Domains.PrepareSourceLocale(
				ctx, tx, change.EntityType, change.EntityID, authority.SourceLocale, change.Requested, change.Now,
			); err != nil {
				return contentblock.MetadataEffect{}, err
			}
			updated := tx.WithContext(ctx).Exec(
				"UPDATE "+definition.RootTable+" SET source_locale = ?, updated_at = ? WHERE id = ? AND source_locale = ? AND content_document_id = ?",
				change.Requested,
				change.Now,
				change.EntityID,
				authority.SourceLocale,
				authority.DocumentID,
			)
			if updated.Error != nil {
				return contentblock.MetadataEffect{}, updated.Error
			}
			if updated.RowsAffected != 1 {
				return contentblock.MetadataEffect{}, translation.ErrSourceNoLongerCurrent
			}
			return contentblock.MetadataEffect{
				Changed: true, AffectsTranslationSource: true,
				SourceLocale: change.Requested,
			}, nil
		},
	)
	if err != nil {
		return application.SwitchedSourceLocale{}, err
	}
	return application.SwitchedSourceLocale{DocumentRevision: switched.DocumentRevision, Changed: switched.Changed}, nil
}
