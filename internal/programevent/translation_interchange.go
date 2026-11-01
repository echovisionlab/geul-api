package programevent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

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

// LoadInterchangeLocale reads locale of the event through store, bound to
// the interchange's transaction, against the source locale the extraction
// plan names.
func LoadInterchangeLocale(ctx context.Context, store Store, eventID, sourceLocale, locale string) (InterchangeLocale, error) {
	reference, err := store.ContentDocumentID(ctx, eventID, NoLock)
	if err != nil {
		return InterchangeLocale{}, notFoundAs(err, domainerr.ResourceNotFound("program event", eventID))
	}
	documentID, err := canonicalContentDocumentID(reference)
	if err != nil {
		return InterchangeLocale{}, domainerr.Precondition("Program Event content document identity is invalid").WithCause(failurecause.ServerFault(err))
	}
	snapshot, err := store.ContentSnapshot(ctx, documentID, sourceLocale)
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
	summary, exists, err := store.LocaleSummary(ctx, eventID, locale)
	if err != nil {
		return InterchangeLocale{}, err
	}
	if !exists && len(localized.GetLocaleOverlay().GetBlocks()) != 0 {
		return InterchangeLocale{}, errors.New("program Event target Blocks exist without owning locale metadata")
	}
	return InterchangeLocale{
		Localized: localized, DocumentRevision: snapshot.Document.Revision.String(),
		Exists: exists, Summary: summary,
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
