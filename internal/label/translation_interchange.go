package label

import (
	"context"
	"errors"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// TranslationInterchangeTarget is Label's raw sparse target projection.
type TranslationInterchangeTarget struct {
	Exists   bool
	Revision string
	Document *contentv1.LocalizedRichTextDocument
}

// LoadTypedTranslationInterchangeTarget reads the Label's sparse target
// projection in the translation domain's transaction, which store is bound
// to.
func LoadTypedTranslationInterchangeTarget(
	ctx context.Context,
	store Store,
	labelID string,
	targetLocale string,
) (TranslationInterchangeTarget, error) {
	if store == nil {
		return TranslationInterchangeTarget{}, errors.New("label translation interchange dependencies are not configured")
	}
	if _, err := canonicalLabelUUID(labelID); err != nil {
		return TranslationInterchangeTarget{}, domainerr.InvalidField("label_id", "must be a canonical UUID")
	}
	var err error
	targetLocale, err = normalizeLabelDocumentLocale(targetLocale)
	if err != nil {
		return TranslationInterchangeTarget{}, domainerr.InvalidField("target_locale", err.Error())
	}
	documentID, err := loadLabelContentDocumentID(ctx, store, labelID)
	if err != nil {
		return TranslationInterchangeTarget{}, err
	}
	state, err := loadLabelTargetLocaleState(ctx, store, labelID, documentID, targetLocale, false)
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
