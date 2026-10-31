package release

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"gorm.io/gorm"
)

// TranslationInterchangeTarget is Release's raw sparse target projection.
// CreditNotes retain Release aggregate ownership and stable Credit identity.
type TranslationInterchangeTarget struct {
	Exists      bool
	Revision    string
	Document    *contentv1.LocalizedRichTextDocument
	CreditNotes map[string]string
}

func LoadTypedTranslationInterchangeTarget(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	releaseID string,
	targetLocale string,
) (TranslationInterchangeTarget, error) {
	if _, err := uuidFromCanonicalString(releaseID); err != nil {
		return TranslationInterchangeTarget{}, domainerr.InvalidField("release_id", "must be a canonical UUID")
	}
	targetLocale, err := normalizeReleaseDocumentLocale(targetLocale)
	if err != nil {
		return TranslationInterchangeTarget{}, err
	}
	documentID, err := loadReleaseContentDocumentID(ctx, tx, releaseID)
	if err != nil {
		return TranslationInterchangeTarget{}, err
	}
	target, err := loadReleaseTargetLocaleState(ctx, tx, store, releaseID, documentID, targetLocale, false)
	if err != nil {
		return TranslationInterchangeTarget{}, err
	}
	if target.SourceLocale == targetLocale {
		return TranslationInterchangeTarget{}, domainerr.InvalidField("target_locale", "must differ from the Release source locale")
	}
	document, err := contentblock.SnapshotToLocalizedRichTextDocument(target.Snapshot, targetLocale)
	if err != nil {
		return TranslationInterchangeTarget{}, normalizeReleaseContentBlockError(err)
	}
	return TranslationInterchangeTarget{
		Exists: target.TargetMetadata != nil, Revision: target.TargetRevision,
		Document: document, CreditNotes: target.TargetNotes,
	}, nil
}
