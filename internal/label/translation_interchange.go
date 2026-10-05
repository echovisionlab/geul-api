package label

import (
	"context"
	"errors"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"gorm.io/gorm"
)

// TranslationInterchangeTarget is Label's raw sparse target projection.
type TranslationInterchangeTarget struct {
	Exists   bool
	Revision string
	Document *contentv1.LocalizedRichTextDocument
}

func LoadTypedTranslationInterchangeTarget(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	labelID string,
	targetLocale string,
) (TranslationInterchangeTarget, error) {
	if tx == nil || store == nil {
		return TranslationInterchangeTarget{}, errs.Internal(errors.New("label translation interchange dependencies are not configured"))
	}
	if _, err := canonicalLabelUUID(labelID); err != nil {
		return TranslationInterchangeTarget{}, errs.InvalidArgument("label_id", "must be a canonical UUID")
	}
	var err error
	targetLocale, err = normalizeLabelDocumentLocale(targetLocale)
	if err != nil {
		return TranslationInterchangeTarget{}, errs.InvalidArgument("target_locale", err.Error())
	}
	documentID, err := loadLabelContentDocumentID(ctx, tx, labelID)
	if err != nil {
		return TranslationInterchangeTarget{}, err
	}
	state, err := loadLabelTargetLocaleState(ctx, tx, store, labelID, documentID, targetLocale, false)
	if err != nil {
		return TranslationInterchangeTarget{}, err
	}
	document, err := contentblock.SnapshotToLocalizedRichTextDocument(state.Snapshot, targetLocale)
	if err != nil {
		return TranslationInterchangeTarget{}, normalizeLabelContentBlockError(err)
	}
	revision := ""
	if state.TargetMetadata != nil {
		revision = state.TargetRevision
	}
	return TranslationInterchangeTarget{
		Exists: state.TargetMetadata != nil, Revision: revision, Document: document,
	}, nil
}
