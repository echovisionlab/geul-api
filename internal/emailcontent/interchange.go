package emailcontent

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// InterchangeTarget is an owner's raw sparse target projection. Locale row
// presence owns Exists; Document keeps absent Block values absent so readers
// resolve source fallback without materializing it into the editable target.
type InterchangeTarget struct {
	Exists   bool
	Revision string
	Subject  *string
	Document *contentv1.LocalizedRichTextDocument
}

// InterchangeMutation is one already-authorized XLIFF target mutation. The
// Translation application owns the outer transaction and the adapter
// appends the typed Member Audit before commit.
type InterchangeMutation struct {
	TargetLocale             string
	ExpectedDocumentRevision string
	ExpectedTargetRevision   *string
	ExpectedPresence         bool
	Subject                  *string
	LocaleMutations          []*contentv1.RichTextBlockLocaleMutation
	ContributorMemberID      string
	Now                      time.Time
}

// InterchangeMutationResult reports the owner's aggregate CAS token after an
// XLIFF target mutation.
type InterchangeMutationResult struct {
	Revision string
	Changed  bool
}

// LoadInterchangeTarget loads one target without applying source fallback.
// The Translation application has already locked the root and authorized
// source-locale editing in the same transaction.
func (o Owner[S]) LoadInterchangeTarget(
	ctx context.Context,
	store S,
	id string,
	targetLocale string,
) (InterchangeTarget, error) {
	id, err := CanonicalID(o.IDField, id)
	if err != nil {
		return InterchangeTarget{}, err
	}
	if targetLocale, err = NormalizeLocale(targetLocale); err != nil {
		return InterchangeTarget{}, err
	}
	documentID, err := o.Rows.DocumentID(ctx, store, id)
	if err != nil {
		return InterchangeTarget{}, err
	}
	domain, err := o.Rows.SourceContext(ctx, store, id, false)
	if err != nil {
		return InterchangeTarget{}, err
	}
	if targetLocale == domain.SourceLocale {
		return InterchangeTarget{}, errs.InvalidArgument("target_locale", "must differ from the "+o.Name+" source locale")
	}
	snapshot, err := store.LockedContentSnapshot(ctx, documentID, domain.SourceLocale)
	if err != nil {
		return InterchangeTarget{}, o.NormalizeContentBlockError(err)
	}
	if snapshot.Document.Profile != Profile {
		return InterchangeTarget{}, errs.FailedPrecondition(o.Name + " translation interchange requires the Email content profile")
	}
	subject, updatedAt, exists, err := o.loadInterchangeMetadata(ctx, store, id, targetLocale)
	if err != nil {
		return InterchangeTarget{}, err
	}
	document, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, targetLocale)
	if err != nil {
		return InterchangeTarget{}, o.NormalizeContentBlockError(err)
	}
	if !exists && len(document.GetLocaleOverlay().GetBlocks()) != 0 {
		return InterchangeTarget{}, errs.InternalMsg(o.Name + " target Blocks exist without owning locale metadata")
	}
	result := InterchangeTarget{Exists: exists, Subject: subject, Document: document}
	if exists {
		result.Revision, err = translation.DeriveTargetRevision(translation.TargetRevisionFacts{
			LocaleExists: true, DocumentRevision: snapshot.Document.Revision.String(), LocaleUpdatedAt: &updatedAt,
		})
		if err != nil {
			return InterchangeTarget{}, errs.Internal(err)
		}
	}
	return result, nil
}

// loadInterchangeMetadata share-locks the target row and reads its subject
// and update time.
func (o Owner[S]) loadInterchangeMetadata(ctx context.Context, store S, id, locale string) (*string, time.Time, bool, error) {
	row, exists, err := store.LocaleRow(ctx, id, locale, ShareLock)
	if err != nil {
		return nil, time.Time{}, false, errs.Internal(err)
	}
	if !exists {
		return nil, time.Time{}, false, nil
	}
	return row.Subject, row.UpdatedAt, true, nil
}

// ApplyInterchange applies one sparse target overlay and subject behind the
// owner's lifecycle fence and the Content Document CAS.
func (o Owner[S]) ApplyInterchange(
	ctx context.Context,
	store S,
	id string,
	sourceLocale string,
	input InterchangeMutation,
	fence Fence,
) (InterchangeMutationResult, error) {
	id, expectedRevision, contributor, err := o.validateInterchange(id, &sourceLocale, &input)
	if err != nil {
		return InterchangeMutationResult{}, err
	}
	documentID, err := o.Rows.DocumentID(ctx, store, id)
	if err != nil {
		return InterchangeMutationResult{}, err
	}
	batch, err := contentblock.BatchFromRichTextProto(documentID, &contentv1.RichTextBlockMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_EMAIL,
		ExpectedRevision:        expectedRevision.String(),
		LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{
			Locale: input.TargetLocale, Mutations: input.LocaleMutations,
		}},
		ContributorMemberIds: []string{contributor.String()},
	})
	if err != nil {
		return InterchangeMutationResult{}, o.NormalizeContentBlockError(err)
	}
	result, err := o.ApplyTargetLocaleMutation(ctx, store, TargetLocaleMutation{
		ID: id, DocumentID: documentID, Locale: input.TargetLocale,
		Batch: batch, ExpectedDocumentRevision: expectedRevision,
		ExpectedLocaleExists:   &input.ExpectedPresence,
		ExpectedTargetRevision: input.ExpectedTargetRevision,
		Policy:                 targetlocale.Policy{AllowCreate: true, AllowLocaleDeletes: true},
		SetSubject:             true, Subject: cloneString(input.Subject), Now: input.Now,
		Fence: fence,
	})
	if err != nil {
		var targetConflict *translation.TargetRevisionConflict
		if errors.As(err, &targetConflict) {
			return InterchangeMutationResult{}, err
		}
		return InterchangeMutationResult{}, o.NormalizeContentBlockError(err)
	}
	return InterchangeMutationResult{Revision: result.TargetRevision, Changed: result.Result.Changed}, nil
}

// validateInterchange canonicalizes the owner, the locales and the time of
// an interchange mutation and returns its expected revision and contributor.
func (o Owner[S]) validateInterchange(id string, sourceLocale *string, input *InterchangeMutation) (string, uuid.UUID, uuid.UUID, error) {
	id, err := CanonicalID(o.IDField, id)
	if err != nil {
		return "", uuid.Nil, uuid.Nil, err
	}
	if input.TargetLocale, err = NormalizeLocale(input.TargetLocale); err != nil {
		return "", uuid.Nil, uuid.Nil, err
	}
	if *sourceLocale, err = NormalizeLocale(*sourceLocale); err != nil {
		return "", uuid.Nil, uuid.Nil, err
	}
	if input.TargetLocale == *sourceLocale {
		return "", uuid.Nil, uuid.Nil, errs.InvalidArgument("target_locale", "must be a non-source "+o.Name+" locale")
	}
	expected, err := canonicalUUID(input.ExpectedDocumentRevision, "expected_revision")
	if err != nil {
		return "", uuid.Nil, uuid.Nil, err
	}
	contributor, err := canonicalUUID(input.ContributorMemberID, "contributor_member_id")
	if err != nil {
		return "", uuid.Nil, uuid.Nil, err
	}
	if input.Now.IsZero() {
		input.Now = dbtime.Now()
	} else {
		input.Now = input.Now.UTC()
	}
	return id, expected, contributor, nil
}

func canonicalUUID(value, field string) (uuid.UUID, error) {
	value = strings.TrimSpace(value)
	parsed, err := uuid.Parse(value)
	if err != nil || parsed == uuid.Nil || parsed.String() != value {
		return uuid.Nil, errs.InvalidArgument(field, "must be a canonical UUID")
	}
	return parsed, nil
}
