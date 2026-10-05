package release

import (
	"context"
	"errors"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
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
	if tx == nil || store == nil {
		return TranslationInterchangeTarget{}, errs.Internal(errors.New("release translation interchange dependencies are not configured"))
	}
	if _, err := uuidFromCanonicalString(releaseID); err != nil {
		return TranslationInterchangeTarget{}, errs.InvalidArgument("release_id", "must be a canonical UUID")
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
		return TranslationInterchangeTarget{}, errs.InvalidArgument("target_locale", "must differ from the Release source locale")
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
