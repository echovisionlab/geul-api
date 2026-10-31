package programevent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// InterchangeLocale is one locale of an event as XLIFF interchange reads
// it: the localized document projected from the source snapshot, and the
// locale's summary when the locale exists.
type InterchangeLocale struct {
	Localized        *contentv1.LocalizedRichTextDocument
	DocumentRevision string
	Exists           bool
	Summary          *string
}

// LoadInterchangeLocale reads locale of the event in db, the caller's
// transaction, against the source locale the extraction plan names.
func (s *Service) LoadInterchangeLocale(ctx context.Context, db *gorm.DB, eventID, sourceLocale, locale string) (InterchangeLocale, error) {
	var root struct {
		ContentDocumentID *string `gorm:"column:content_document_id"`
	}
	if err := db.WithContext(ctx).Table("program_event").Select("content_document_id").
		Where("id = ?", eventID).Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return InterchangeLocale{}, domainerr.ResourceNotFound("program event", eventID)
		}
		return InterchangeLocale{}, err
	}
	documentID, err := canonicalContentDocumentID(root.ContentDocumentID)
	if err != nil {
		return InterchangeLocale{}, domainerr.Precondition("Program Event content document identity is invalid").WithCause(failurecause.ServerFault(err))
	}
	snapshot, err := s.contentBlocks.LoadSnapshotInTransaction(ctx, db, documentID, sourceLocale)
	if err != nil {
		return InterchangeLocale{}, err
	}
	localized, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, locale)
	if err != nil {
		return InterchangeLocale{}, err
	}
	if localized.GetProfile() != contentv1.RichTextProfile_RICH_TEXT_PROFILE_PROGRAM_EVENT {
		return InterchangeLocale{}, domainerr.Precondition("Program Event translation interchange requires the Program Event content profile")
	}
	var row struct {
		Summary *string `gorm:"column:summary"`
	}
	query := db.WithContext(ctx).Table("program_event_translation").Select("summary").
		Where("entity_id = ? AND locale = ?", eventID, locale).Take(&row)
	if query.Error != nil && !errors.Is(query.Error, gorm.ErrRecordNotFound) {
		return InterchangeLocale{}, query.Error
	}
	exists := query.Error == nil
	if !exists && len(localized.GetLocaleOverlay().GetBlocks()) != 0 {
		return InterchangeLocale{}, errors.New("program Event target Blocks exist without owning locale metadata")
	}
	return InterchangeLocale{
		Localized: localized, DocumentRevision: snapshot.Document.Revision.String(),
		Exists: exists, Summary: row.Summary,
	}, nil
}

func canonicalContentDocumentID(value *string) (uuid.UUID, error) {
	if value == nil {
		return uuid.Nil, errors.New("content document identity is missing")
	}
	normalized := strings.TrimSpace(*value)
	parsed, err := uuid.Parse(normalized)
	if err != nil {
		return uuid.Nil, fmt.Errorf("content document identity is invalid: %w", err)
	}
	if parsed == uuid.Nil || parsed.String() != normalized {
		return uuid.Nil, errors.New("content document identity is invalid")
	}
	return parsed, nil
}
