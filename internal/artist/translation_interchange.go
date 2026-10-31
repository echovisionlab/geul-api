package artist

import (
	"context"
	"errors"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"gorm.io/gorm"
)

// TranslationInterchangeTarget is Artist's raw sparse target projection.
// Exists is owned by artist_translation row presence; Document deliberately
// retains missing locale Blocks and fields instead of applying source fallback.
type TranslationInterchangeTarget struct {
	Exists   bool
	Revision string
	Document *contentv1.LocalizedRichTextDocument
}

func LoadTypedTranslationInterchangeTarget(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	artistID string,
	targetLocale string,
) (TranslationInterchangeTarget, error) {
	if tx == nil || store == nil {
		return TranslationInterchangeTarget{}, errors.New("artist translation interchange dependencies are not configured")
	}
	if _, err := canonicalArtistUUID(artistID); err != nil {
		return TranslationInterchangeTarget{}, domainerr.InvalidField("artist_id", "must be a canonical UUID")
	}
	normalizedLocale, err := normalizeArtistDocumentLocale(targetLocale)
	if err != nil {
		return TranslationInterchangeTarget{}, err
	}
	snapshot, source, err := loadCreativeContentSnapshotInTransaction(
		ctx, tx, store, artistContentEntity, artistID,
	)
	if err != nil {
		return TranslationInterchangeTarget{}, err
	}
	if source.SourceLocale == normalizedLocale {
		return TranslationInterchangeTarget{}, domainerr.InvalidField("target_locale", "must differ from the Artist source locale")
	}
	state, err := loadArtistTargetLocaleState(
		ctx, tx, store, artistID, snapshot.Document.ID, normalizedLocale, false,
	)
	if err != nil {
		return TranslationInterchangeTarget{}, err
	}
	document, err := artistSparseLocalizedDocument(state, normalizedLocale)
	if err != nil {
		return TranslationInterchangeTarget{}, err
	}
	exists := state.TargetMetadata != nil
	return TranslationInterchangeTarget{
		Exists: exists, Revision: state.TargetRevision, Document: document,
	}, nil
}
