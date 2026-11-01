package artist

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/google/uuid"
)

// artistEntityName names the owner in shared content document messages.
const artistEntityName = "Artist"

type artistTargetLocaleState struct {
	Snapshot       contentblock.Snapshot
	SourceLocale   string
	SourceMetadata LocaleTitle
	TargetMetadata *LocaleTitle
	TargetRevision string
}

func (state artistTargetLocaleState) locked() targetlocale.State {
	return targetlocale.State{
		Entity: artistEntityName, Snapshot: state.Snapshot, SourceLocale: state.SourceLocale,
		TargetExists: state.TargetMetadata != nil, TargetRevision: state.TargetRevision,
	}
}

func normalizeArtistDocumentLocale(locale string) (string, error) {
	normalized := localization.NormalizeExactSupportedLocale(locale)
	if normalized == nil {
		return "", domainerr.InvalidField("locale", "must be an exact canonical locale")
	}
	return *normalized, nil
}

func requireArtistSourceRoomLocale(
	ctx context.Context,
	store Store,
	artistID string,
	locale string,
) (string, error) {
	normalized, err := normalizeArtistDocumentLocale(locale)
	if err != nil {
		return "", err
	}
	sourceLocale, err := loadCreativeDocumentAuthority(ctx, store, artistID, Unlocked)
	if err != nil {
		return "", err
	}
	if normalized != sourceLocale {
		return "", domainerr.CollaborationRejection(
			intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_DOCUMENT_METADATA_FORBIDDEN,
			"Artist document metadata and audit checkpoints are source-locale only",
		)
	}
	return normalized, nil
}

func artistSourceRoomFence(artistID string, locale string) ContentFence {
	base := internalCreativeContentFence(artistID)
	return func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error) {
		domain, err := base(ctx, store, documentID)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if domain.SourceLocale != locale {
			return contentblock.DomainContext{}, store.LocaleOwnershipConflict(ctx, documentID,
				"Artist source locale changed since the room was loaded; reload before saving",
			)
		}
		return domain, nil
	}
}

func deriveArtistTargetRevision(documentRevision string, metadata LocaleTitle) (string, error) {
	updatedAt := metadata.UpdatedAt
	revision, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
		LocaleExists: true, DocumentRevision: documentRevision, LocaleUpdatedAt: &updatedAt,
	})
	if err != nil {
		return "", err
	}
	return revision, nil
}

func loadArtistTargetLocaleState(
	ctx context.Context,
	store Store,
	artistID string,
	documentID uuid.UUID,
	locale string,
	forUpdate bool,
) (artistTargetLocaleState, error) {
	locale, err := normalizeArtistDocumentLocale(locale)
	if err != nil {
		return artistTargetLocaleState{}, err
	}
	lock := ShareLock
	if forUpdate {
		lock = UpdateLock
	}
	lockedDocumentID, err := loadCreativeContentDocumentIDWithStrength(ctx, store, artistID, lock)
	if err != nil {
		return artistTargetLocaleState{}, err
	}
	if lockedDocumentID != documentID {
		return artistTargetLocaleState{}, domainerr.Precondition("Artist content document changed; reload before saving")
	}
	sourceLocale, err := loadCreativeDocumentAuthority(ctx, store, artistID, lock)
	if err != nil {
		return artistTargetLocaleState{}, err
	}
	snapshot, err := store.LockedContentSnapshot(ctx, documentID, sourceLocale)
	if err != nil {
		return artistTargetLocaleState{}, normalizeCreativeContentBlockError(err)
	}
	if snapshot.SourceLocale != sourceLocale || snapshot.Document.Profile != creativeContentProfile {
		return artistTargetLocaleState{}, domainerr.Precondition("Artist content document source or profile is inconsistent")
	}
	sourceMetadata, sourceExists, err := store.LocaleTitle(ctx, artistID, sourceLocale, forUpdate)
	if err != nil {
		return artistTargetLocaleState{}, err
	}
	if !sourceExists {
		return artistTargetLocaleState{}, domainerr.Precondition("Artist source locale metadata is missing")
	}
	state := artistTargetLocaleState{
		Snapshot: snapshot, SourceLocale: sourceLocale, SourceMetadata: sourceMetadata,
	}
	if locale == sourceLocale {
		state.TargetMetadata = &sourceMetadata
		return state, nil
	}
	target, exists, err := store.LocaleTitle(ctx, artistID, locale, forUpdate)
	if err != nil {
		return artistTargetLocaleState{}, err
	}
	if !exists {
		if targetlocale.SnapshotHasLocale(snapshot, locale) {
			return artistTargetLocaleState{}, domainerr.Precondition("Artist target locale Blocks exist without owning metadata")
		}
		return state, nil
	}
	state.TargetMetadata = &target
	state.TargetRevision, err = deriveArtistTargetRevision(snapshot.Document.Revision.String(), target)
	if err != nil {
		return artistTargetLocaleState{}, err
	}
	return state, nil
}

func validateArtistSourceStorage(
	storage contentv1.ContentStorageMutationBatch,
	sourceLocale string,
) error {
	for _, group := range storage.LocaleGroups {
		if group.Locale != sourceLocale {
			return domainerr.CollaborationRejection(
				intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_ROOM_LOCALE_MISMATCH,
				"Artist source room mutation must match the current source locale",
			)
		}
	}
	return nil
}

// artistTargetLocaleMutationInput is one write to an Artist target locale.
type artistTargetLocaleMutationInput struct {
	ArtistID               string
	DocumentID             uuid.UUID
	Locale                 string
	Batch                  contentblock.Batch
	ExpectedTargetRevision *string
	Policy                 targetlocale.Policy
	Now                    time.Time
	Fence                  ContentFence
}

func applyArtistTargetLocaleMutation(
	ctx context.Context,
	store Store,
	input artistTargetLocaleMutationInput,
) (contentblock.Result, string, error) {
	artistID, documentID, locale := input.ArtistID, input.DocumentID, input.Locale
	domain, err := input.Fence(ctx, store, documentID)
	if err != nil {
		return contentblock.Result{}, "", err
	}
	state, err := loadArtistTargetLocaleState(ctx, store, artistID, documentID, locale, true)
	if err != nil {
		return contentblock.Result{}, "", err
	}
	if locale == state.SourceLocale {
		return contentblock.Result{}, "", domainerr.InvalidField("locale", "Artist target locale must differ from source locale")
	}
	if state.SourceLocale != domain.SourceLocale {
		return contentblock.Result{}, "", domainerr.Precondition("Artist source locale changed; reload before saving")
	}
	batch, err := input.Policy.Admit(state.locked(), locale, input.Batch, input.ExpectedTargetRevision)
	if err != nil {
		return contentblock.Result{}, "", err
	}
	now := targetlocale.WriteTime(input.Now)
	var final LocaleTitle
	result, err := store.ApplyTargetLocaleContentBatch(
		ctx, batch, locale, lockedContentFence(documentID, domain),
		func(ctx context.Context, store Store, contentChanged bool) (contentblock.MetadataEffect, error) {
			if state.TargetMetadata == nil {
				created, err := store.CreateTargetLocale(ctx, artistID, locale, now)
				if err != nil {
					return contentblock.MetadataEffect{}, err
				}
				if !created {
					return contentblock.MetadataEffect{}, errors.New("Artist target locale could not be created")
				}
				final = LocaleTitle{Locale: locale, UpdatedAt: now}
				return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{locale}}, nil
			}
			final = *state.TargetMetadata
			if !contentChanged {
				return contentblock.MetadataEffect{}, nil
			}
			final.UpdatedAt = translation.NextTargetUpdatedAt(now, final.UpdatedAt)
			updated, err := store.TouchTargetLocale(ctx, artistID, locale, final.UpdatedAt)
			if err != nil {
				return contentblock.MetadataEffect{}, err
			}
			if !updated {
				return contentblock.MetadataEffect{}, errors.New("Artist target locale disappeared while saving")
			}
			return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{locale}}, nil
		},
	)
	if err != nil {
		return contentblock.Result{}, "", normalizeCreativeContentBlockError(err)
	}
	if result.Changed {
		if err := store.StampArtist(ctx, artistID, now); err != nil {
			return contentblock.Result{}, "", err
		}
	}
	targetRevision, err := deriveArtistTargetRevision(result.DocumentRevision.String(), final)
	if err != nil {
		return contentblock.Result{}, "", err
	}
	return result, targetRevision, nil
}

func updateArtistTargetLocaleTitle(
	ctx context.Context,
	store Store,
	artistID string,
	documentID uuid.UUID,
	locale string,
	expectedRevision uuid.UUID,
	expectedTargetRevision *string,
	title *string,
	now time.Time,
	fence ContentFence,
) (contentblock.Result, string, error) {
	if title == nil {
		return contentblock.Result{}, "", domainerr.InvalidField("title", "is required")
	}
	domain, err := fence(ctx, store, documentID)
	if err != nil {
		return contentblock.Result{}, "", err
	}
	state, err := loadArtistTargetLocaleState(ctx, store, artistID, documentID, locale, true)
	if err != nil {
		return contentblock.Result{}, "", err
	}
	if state.TargetMetadata == nil || locale == state.SourceLocale {
		return contentblock.Result{}, "", domainerr.Precondition("Artist target locale must exist before metadata is edited")
	}
	if state.SourceLocale != domain.SourceLocale {
		return contentblock.Result{}, "", domainerr.Precondition("Artist source locale changed; reload before saving")
	}
	if state.Snapshot.Document.Revision != expectedRevision {
		return contentblock.Result{}, "", &contentblock.StaleRevisionError{CurrentRevision: state.Snapshot.Document.Revision}
	}
	if err := state.locked().CheckRevision(expectedTargetRevision, targetlocale.RevisionObserved); err != nil {
		return contentblock.Result{}, "", err
	}
	normalizedTitle := strings.TrimSpace(*title)
	batch := contentblock.Batch{DocumentID: documentID, ExpectedRevision: expectedRevision}
	now = targetlocale.WriteTime(now)
	final := *state.TargetMetadata
	result, err := store.ApplyTargetLocaleContentBatch(
		ctx, batch, locale, lockedContentFence(documentID, domain),
		func(ctx context.Context, store Store, _ bool) (contentblock.MetadataEffect, error) {
			if final.Title != nil && *final.Title == normalizedTitle {
				return contentblock.MetadataEffect{}, nil
			}
			final.Title = &normalizedTitle
			final.UpdatedAt = translation.NextTargetUpdatedAt(now, final.UpdatedAt)
			updated, err := store.SetTargetLocaleTitle(ctx, artistID, locale, normalizedTitle, final.UpdatedAt)
			if err != nil {
				return contentblock.MetadataEffect{}, err
			}
			if !updated {
				return contentblock.MetadataEffect{}, errors.New("Artist target locale disappeared while saving")
			}
			return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{locale}}, nil
		},
	)
	if err != nil {
		return contentblock.Result{}, "", normalizeCreativeContentBlockError(err)
	}
	if result.Changed {
		if err := store.StampArtist(ctx, artistID, now); err != nil {
			return contentblock.Result{}, "", err
		}
	}
	targetRevision, err := deriveArtistTargetRevision(result.DocumentRevision.String(), final)
	if err != nil {
		return contentblock.Result{}, "", err
	}
	return result, targetRevision, nil
}

func deleteArtistTargetLocale(
	ctx context.Context,
	store Store,
	artistID string,
	documentID uuid.UUID,
	locale string,
	expectedRevision uuid.UUID,
	expectedTargetRevision *string,
	contributors []uuid.UUID,
	fence ContentFence,
) (contentblock.Result, error) {
	domain, err := fence(ctx, store, documentID)
	if err != nil {
		return contentblock.Result{}, err
	}
	state, err := loadArtistTargetLocaleState(ctx, store, artistID, documentID, locale, true)
	if err != nil {
		return contentblock.Result{}, err
	}
	if state.TargetMetadata == nil || locale == state.SourceLocale {
		return contentblock.Result{}, domainerr.Precondition("Artist target locale must exist before deletion")
	}
	if state.SourceLocale != domain.SourceLocale {
		return contentblock.Result{}, domainerr.Precondition("Artist source locale changed; reload before saving")
	}
	if state.Snapshot.Document.Revision != expectedRevision {
		return contentblock.Result{}, &contentblock.StaleRevisionError{CurrentRevision: state.Snapshot.Document.Revision}
	}
	if err := state.locked().CheckRevision(expectedTargetRevision, targetlocale.RevisionObserved); err != nil {
		return contentblock.Result{}, err
	}
	batch := contentblock.Batch{
		DocumentID: documentID, ExpectedRevision: expectedRevision,
		ContributorMemberIDs: append([]uuid.UUID(nil), contributors...),
		LocaleGroups:         targetlocale.DeleteGroups(state.Snapshot, locale),
	}
	result, err := store.ApplyTargetLocaleContentBatch(
		ctx, batch, locale, lockedContentFence(documentID, domain),
		func(ctx context.Context, store Store, _ bool) (contentblock.MetadataEffect, error) {
			deleted, err := store.DeleteTargetLocale(ctx, artistID, locale)
			if err != nil {
				return contentblock.MetadataEffect{}, err
			}
			if !deleted {
				return contentblock.MetadataEffect{}, errors.New("Artist target locale disappeared while deleting")
			}
			return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{locale}}, nil
		},
	)
	if err != nil {
		return contentblock.Result{}, normalizeCreativeContentBlockError(err)
	}
	if result.Changed {
		if err := store.StampArtist(ctx, artistID, time.Now().UTC()); err != nil {
			return contentblock.Result{}, err
		}
	}
	return result, nil
}

func artistLocalizedDocument(
	state artistTargetLocaleState,
	locale string,
) (*contentv1.LocalizedRichTextDocument, error) {
	document, err := contentblock.MaterializeSnapshotRichTextLocale(state.Snapshot, locale)
	if err != nil {
		return nil, normalizeCreativeContentBlockError(err)
	}
	return document, nil
}

// artistSparseLocalizedDocument is the exact durable locale projection used by
// AI/XLIFF callers. Unlike the collaboration/public projection, it must retain
// absent target leaves so the caller can distinguish an absent unit from an
// explicitly authored empty value.
func artistSparseLocalizedDocument(
	state artistTargetLocaleState,
	locale string,
) (*contentv1.LocalizedRichTextDocument, error) {
	document, err := contentblock.SnapshotToLocalizedRichTextDocument(state.Snapshot, locale)
	if err != nil {
		return nil, normalizeCreativeContentBlockError(err)
	}
	return document, nil
}
