package emailauthoring

import (
	"context"
	"database/sql"
	"errors"
	"maps"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/email"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/translation"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// EmailLayoutTranslationInterchangeTarget is Email Layout's sparse target
// values keyed by stable source-unit handle.
type EmailLayoutTranslationInterchangeTarget struct {
	Exists   bool
	Revision string
	Values   map[string]string
}

// EmailLayoutTranslationInterchangeMutation replaces the complete sparse
// target value map already compiled by the shared XLIFF adapter.
type EmailLayoutTranslationInterchangeMutation struct {
	TargetLocale     string
	ExpectedRevision string
	ExpectedPresence bool
	Values           map[string]string
	Now              time.Time
}

// LoadEmailLayoutTranslationInterchangeTarget loads target-owned values
// without resolving omitted units from the canonical source wrapper.
func LoadEmailLayoutTranslationInterchangeTarget(
	ctx context.Context,
	tx *gorm.DB,
	layoutID string,
	targetLocale string,
) (EmailLayoutTranslationInterchangeTarget, error) {
	if tx == nil {
		return EmailLayoutTranslationInterchangeTarget{}, errs.Internal(errors.New("email layout translation interchange transaction is not configured"))
	}
	layoutID, err := canonicalEmailAIDocumentID("email_layout_id", layoutID)
	if err != nil {
		return EmailLayoutTranslationInterchangeTarget{}, err
	}
	targetLocale, err = canonicalEmailLayoutRoomLocale(targetLocale)
	if err != nil {
		return EmailLayoutTranslationInterchangeTarget{}, err
	}
	authority, err := loadEmailLayoutDocumentAuthority(ctx, tx, layoutID, "SHARE")
	if err != nil {
		return EmailLayoutTranslationInterchangeTarget{}, err
	}
	if targetLocale == authority.SourceLocale {
		return EmailLayoutTranslationInterchangeTarget{}, errs.InvalidArgument("target_locale", "must differ from the Email Layout source locale")
	}
	entry, err := loadEmailLayoutInterchangeEntry(ctx, tx, layoutID, targetLocale, "SHARE")
	if err != nil {
		return EmailLayoutTranslationInterchangeTarget{}, err
	}
	return projectEmailLayoutInterchangeTarget(authority.DocumentRevision.String(), entry)
}

// ApplyEmailLayoutTranslationInterchange applies one complete sparse value map
// under the Layout lifecycle fence and target-row CAS.
func ApplyEmailLayoutTranslationInterchange(
	ctx context.Context,
	tx *gorm.DB,
	references CampaignDeliveryReferences,
	layoutID string,
	sourceLocale string,
	input EmailLayoutTranslationInterchangeMutation,
) (EmailLayoutTranslationInterchangeTarget, bool, error) {
	if tx == nil || references == nil {
		return EmailLayoutTranslationInterchangeTarget{}, false, errs.Internal(errors.New("email layout translation interchange dependencies are not configured"))
	}
	layoutID, err := canonicalEmailAIDocumentID("email_layout_id", layoutID)
	if err != nil {
		return EmailLayoutTranslationInterchangeTarget{}, false, err
	}
	input.TargetLocale, err = canonicalEmailLayoutRoomLocale(input.TargetLocale)
	if err != nil {
		return EmailLayoutTranslationInterchangeTarget{}, false, err
	}
	sourceLocale, err = canonicalEmailLayoutRoomLocale(sourceLocale)
	if err != nil {
		return EmailLayoutTranslationInterchangeTarget{}, false, err
	}
	if input.TargetLocale == sourceLocale {
		return EmailLayoutTranslationInterchangeTarget{}, false, errs.InvalidArgument("target_locale", "must be a non-source Email Layout locale")
	}
	if err := lockEmailLayoutAIDocumentRoot(ctx, tx, layoutID, "UPDATE"); err != nil {
		return EmailLayoutTranslationInterchangeTarget{}, false, err
	}
	if err := ensureEmailLayoutMutableForActiveDelivery(ctx, tx, references, layoutID); err != nil {
		return EmailLayoutTranslationInterchangeTarget{}, false, err
	}
	authority, err := loadEmailLayoutDocumentAuthority(ctx, tx, layoutID, "UPDATE")
	if err != nil {
		return EmailLayoutTranslationInterchangeTarget{}, false, err
	}
	if authority.SourceLocale != sourceLocale {
		return EmailLayoutTranslationInterchangeTarget{}, false, errs.FailedPrecondition("Email Layout source locale changed; reload before importing")
	}
	source, err := email.LoadCanonicalLayoutTranslationDocument(ctx, tx, layoutID, sourceLocale)
	if err != nil {
		return EmailLayoutTranslationInterchangeTarget{}, false, err
	}
	entry, err := loadEmailLayoutInterchangeEntry(ctx, tx, layoutID, input.TargetLocale, "UPDATE")
	if err != nil {
		return EmailLayoutTranslationInterchangeTarget{}, false, err
	}
	current, err := projectEmailLayoutInterchangeTarget(authority.DocumentRevision.String(), entry)
	if err != nil {
		return EmailLayoutTranslationInterchangeTarget{}, false, err
	}
	if current.Exists != input.ExpectedPresence ||
		(current.Exists && current.Revision != strings.TrimSpace(input.ExpectedRevision)) ||
		(!current.Exists && strings.TrimSpace(input.ExpectedRevision) != "") {
		var currentTargetRevision *string
		if current.Exists {
			targetRevision := current.Revision
			currentTargetRevision = &targetRevision
		}
		return EmailLayoutTranslationInterchangeTarget{}, false, &EmailLayoutAIDocumentRevisionConflictError{
			Kind:                    EmailLayoutAIDocumentTargetRevisionConflict,
			CurrentDocumentRevision: authority.DocumentRevision.String(),
			CurrentTargetRevision:   currentTargetRevision,
		}
	}
	nextValues := maps.Clone(input.Values)
	if nextValues == nil {
		nextValues = make(map[string]string)
	}
	changed := !current.Exists || !maps.Equal(current.Values, nextValues)
	if !changed {
		return current, false, nil
	}
	if input.Now.IsZero() {
		input.Now = time.Now().UTC()
	} else {
		input.Now = input.Now.UTC()
	}
	contentHTML, contentText, err := email.ApplyLayoutLocaleValues(derefString(source.ContentHTML), nextValues)
	if err != nil {
		return EmailLayoutTranslationInterchangeTarget{}, false, errs.InvalidArgument("content", err.Error())
	}
	updatedAt := translation.NextTargetUpdatedAt(input.Now, emailLayoutInterchangeUpdatedAt(entry))
	if err := email.UpsertLayoutTranslationEntry(
		ctx,
		tx,
		layoutID,
		input.TargetLocale,
		translation.EntryWrite{ContentHTML: contentHTML, ContentText: contentText, Now: updatedAt},
	); err != nil {
		return EmailLayoutTranslationInterchangeTarget{}, false, err
	}
	revision, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
		LocaleExists: true, DocumentRevision: authority.DocumentRevision.String(), LocaleUpdatedAt: &updatedAt,
	})
	if err != nil {
		return EmailLayoutTranslationInterchangeTarget{}, false, errs.Internal(err)
	}
	return EmailLayoutTranslationInterchangeTarget{
		Exists: true, Revision: revision, Values: nextValues,
	}, true, nil
}

func loadEmailLayoutInterchangeEntry(
	ctx context.Context,
	tx *gorm.DB,
	layoutID string,
	locale string,
	lock string,
) (*email.LayoutTranslationEntry, error) {
	var row struct {
		ContentHTML sql.NullString `gorm:"column:html_content"`
		ContentText sql.NullString `gorm:"column:content_text"`
		UpdatedAt   time.Time      `gorm:"column:updated_at"`
	}
	query := tx.WithContext(ctx).Table("email_layout_translation").
		Select("html_content, content_text, updated_at").
		Where("entity_id = ? AND locale = ?", layoutID, locale)
	if lock != "" {
		query = query.Clauses(clause.Locking{Strength: lock})
	}
	result := query.Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if result.Error != nil {
		return nil, errs.Internal(result.Error)
	}
	entry := &email.LayoutTranslationEntry{LayoutTranslationDocument: email.LayoutTranslationDocument{
		UpdatedAt: row.UpdatedAt,
	}}
	if row.ContentHTML.Valid {
		value := email.NormalizeTemplatePlaceholders(row.ContentHTML.String)
		entry.ContentHTML = &value
	}
	if row.ContentText.Valid {
		value := row.ContentText.String
		entry.ContentText = &value
	}
	return entry, nil
}

func projectEmailLayoutInterchangeTarget(
	documentRevision string,
	entry *email.LayoutTranslationEntry,
) (EmailLayoutTranslationInterchangeTarget, error) {
	if entry == nil {
		return EmailLayoutTranslationInterchangeTarget{Values: make(map[string]string)}, nil
	}
	values := make(map[string]string)
	var err error
	if entry.ContentHTML != nil {
		values, err = email.ExtractLayoutStoredLocaleValues(*entry.ContentHTML)
		if err != nil {
			return EmailLayoutTranslationInterchangeTarget{}, errs.FailedPrecondition("Email Layout target unit markers require backfill before interchange")
		}
	}
	updatedAt := entry.UpdatedAt
	revision, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
		LocaleExists: true, DocumentRevision: documentRevision, LocaleUpdatedAt: &updatedAt,
	})
	if err != nil {
		return EmailLayoutTranslationInterchangeTarget{}, errs.Internal(err)
	}
	return EmailLayoutTranslationInterchangeTarget{
		Exists: true, Revision: revision, Values: values,
	}, nil
}

func emailLayoutInterchangeUpdatedAt(entry *email.LayoutTranslationEntry) time.Time {
	if entry == nil {
		return time.Time{}
	}
	return entry.UpdatedAt
}
