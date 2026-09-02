package emailauthoring

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
	errs "github.com/echovisionlab/geul-api/internal/errors"
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
	if db == nil || strings.TrimSpace(layoutID) == "" {
		return emailLayoutDocumentAuthority{}, errs.InvalidArgument("email_layout_id", "is required")
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
			return emailLayoutDocumentAuthority{}, errs.NotFound("email layout", layoutID)
		}
		return emailLayoutDocumentAuthority{}, errs.Internal(err)
	}
	if authority.DocumentID == uuid.Nil || authority.DocumentRevision == uuid.Nil || strings.TrimSpace(authority.SourceLocale) == "" {
		return emailLayoutDocumentAuthority{}, errs.FailedPrecondition("Email Layout Content Document authority is not initialized")
	}
	return authority, nil
}

func parseEmailLayoutDocumentRevision(value string) (uuid.UUID, error) {
	value = strings.TrimSpace(value)
	revision, err := uuid.Parse(value)
	if err != nil || revision == uuid.Nil || revision.String() != value {
		return uuid.Nil, errs.InvalidArgument("expected_document_revision", "must be a canonical UUID")
	}
	return revision, nil
}

func emailLayoutContentFence(
	references CampaignDeliveryReferences,
	layoutID string,
) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		authority, err := loadEmailLayoutDocumentAuthority(ctx, tx, layoutID, "UPDATE")
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if authority.DocumentID != documentID {
			return contentblock.DomainContext{}, errs.FailedPrecondition("Email Layout Content Document changed")
		}
		if err := ensureEmailLayoutMutableForActiveDelivery(ctx, tx, references, layoutID); err != nil {
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
