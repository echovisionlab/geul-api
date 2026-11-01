package emailauthoring

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
	emailpostgres "github.com/echovisionlab/geul-api/internal/email/postgres"
	"github.com/echovisionlab/geul-api/internal/translation"
)

type emailLayoutDocumentAuthority struct {
	LayoutID         string    `gorm:"column:layout_id"`
	SourceLocale     string    `gorm:"column:source_locale"`
	DocumentID       uuid.UUID `gorm:"column:content_document_id"`
	DocumentRevision uuid.UUID `gorm:"column:document_revision"`
}

func loadEmailLayoutDocumentAuthority(
	ctx context.Context,
	db *gorm.DB,
	layoutID string,
	lock string,
) (emailLayoutDocumentAuthority, error) {
	if strings.TrimSpace(layoutID) == "" {
		return emailLayoutDocumentAuthority{}, domainerr.InvalidField("email_layout_id", "is required")
	}
	query := db.WithContext(ctx).
		Table("email_layout AS root").
		Select("root.id AS layout_id, root.source_locale, root.content_document_id, document.revision AS document_revision").
		Joins("JOIN content_document AS document ON document.id = root.content_document_id").
		Where("root.id = ?", layoutID)
	if lock != "" {
		query = query.Clauses(clause.Locking{Strength: lock, Table: clause.Table{Name: "root"}})
	}
	var authority emailLayoutDocumentAuthority
	if err := query.Take(&authority).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return emailLayoutDocumentAuthority{}, domainerr.ResourceNotFound("email layout", layoutID)
		}
		return emailLayoutDocumentAuthority{}, err
	}
	if authority.DocumentID == uuid.Nil || authority.DocumentRevision == uuid.Nil || strings.TrimSpace(authority.SourceLocale) == "" {
		return emailLayoutDocumentAuthority{}, domainerr.Precondition("Email Layout Content Document authority is not initialized")
	}
	return authority, nil
}

func parseEmailLayoutDocumentRevision(value string) (uuid.UUID, error) {
	value = strings.TrimSpace(value)
	revision, err := uuid.Parse(value)
	if err != nil || revision == uuid.Nil || revision.String() != value {
		return uuid.Nil, domainerr.InvalidField("expected_document_revision", "must be a canonical UUID")
	}
	return revision, nil
}

func emailLayoutContentFence(
	references CampaignDeliveryReferences,
	layoutID string,
) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		authority, err := loadEmailLayoutDocumentAuthority(ctx, tx, layoutID, clause.LockingStrengthUpdate)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if authority.DocumentID != documentID {
			return contentblock.DomainContext{}, domainerr.Precondition("Email Layout Content Document changed")
		}
		if err := references.RequireLayoutMutable(ctx, tx, layoutID); err != nil {
			return contentblock.DomainContext{}, err
		}
		return contentblock.DomainContext{SourceLocale: authority.SourceLocale}, nil
	}
}

func deriveEmailLayoutTargetRevision(
	documentRevision string,
	entry *emailutil.LayoutTranslationEntry,
) (*string, error) {
	if entry == nil {
		return nil, nil
	}
	updatedAt := entry.UpdatedAt
	revision, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
		LocaleExists: true, DocumentRevision: documentRevision, LocaleUpdatedAt: &updatedAt,
	})
	if err != nil {
		return nil, err
	}
	return &revision, nil
}

// saveEmailLayoutSource stores document as the source locale's HTML and text
// in the next Content Document revision after expected. A nil document
// changes nothing but still proves the revision and the content fence.
func saveEmailLayoutSource(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	references CampaignDeliveryReferences,
	layoutID string,
	authority emailLayoutDocumentAuthority,
	expected uuid.UUID,
	document *emailutil.LayoutTranslationDocument,
	now time.Time,
) (contentblock.AdvanceResult, error) {
	return store.AdvanceRevision(
		ctx, tx,
		contentblock.AdvanceInput{DocumentID: authority.DocumentID, ExpectedRevision: expected},
		emailLayoutContentFence(references, layoutID),
		func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
			if document == nil {
				return contentblock.MetadataEffect{}, nil
			}
			if err := emailutil.SaveLayoutSourceLocaleDocument(ctx, emailpostgres.Bind(tx), layoutID, authority.SourceLocale, *document, now); err != nil {
				return contentblock.MetadataEffect{}, err
			}
			return contentblock.MetadataEffect{
				Changed: true, AffectsTranslationSource: true, SourceLocale: authority.SourceLocale,
				ChangedLocales: []string{authority.SourceLocale},
			}, nil
		},
	)
}

// saveEmailLayoutTarget stores values over the source HTML as the target
// locale's row, after the row's previous updated_at, and returns the stored
// updated_at.
func saveEmailLayoutTarget(
	ctx context.Context,
	tx *gorm.DB,
	layoutID string,
	locale string,
	sourceHTML string,
	values map[string]string,
	now time.Time,
	previous time.Time,
) (time.Time, error) {
	contentHTML, contentText, err := emailutil.ApplyLayoutLocaleValues(sourceHTML, values)
	if err != nil {
		return time.Time{}, domainerr.InvalidField("content", err.Error())
	}
	updatedAt := translation.NextTargetUpdatedAt(now, previous)
	return updatedAt, emailutil.UpsertLayoutTranslationEntry(
		ctx, emailpostgres.Bind(tx), layoutID, locale,
		translation.EntryWrite{ContentHTML: contentHTML, ContentText: contentText, Now: updatedAt},
	)
}
