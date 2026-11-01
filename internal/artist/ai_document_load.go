package artist

import (
	"context"
	"errors"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
)

// LoadAIDocumentState preserves published visibility while keeping non-public
// reads behind Artist's current SpiceDB authority fence.
func (s *Service) LoadAIDocumentState(ctx context.Context, artistID, locale string) (AIDocumentState, error) {
	id, err := canonicalArtistUUID(artistID)
	if err != nil {
		return AIDocumentState{}, domainerr.InvalidField("artist_id", "must be a canonical UUID")
	}
	locale, err = normalizeArtistDocumentLocale(locale)
	if err != nil {
		return AIDocumentState{}, err
	}
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated || !principal.Onboarded || principal.Banned || principal.IdentityID == "" || principal.MemberID == "" {
		return AIDocumentState{}, errAuthenticationRequired
	}

	var state AIDocumentState
	err = s.units.ReadLocked(ctx, func(store Store) error {
		root, err := loadArtistAIDocumentRoot(ctx, store, id.String(), KeyShareLock)
		if err != nil {
			return err
		}
		published, valid := artistAIDocumentLifecycle(root.Status)
		if !valid {
			return errors.New("Artist has an unsupported lifecycle status")
		}
		if !published {
			if err := artistEntity.RequirePermission(ctx, s.spiceDB, id.String(), policyv1.Artist.View); err != nil {
				return err
			}
		}
		state, err = loadAIDocumentStateAfterAuthorization(
			ctx, store, root, locale, principal.MemberID.String(),
		)
		return err
	})
	return state, err
}

// loadArtistAIDocumentRoot reads the Artist root with lock; a missing
// Artist answers not found and one without a document fails its
// precondition.
func loadArtistAIDocumentRoot(ctx context.Context, store Store, artistID string, lock RowLock) (DocumentRoot, error) {
	root, err := store.DocumentRoot(ctx, artistID, lock)
	if err != nil {
		if errors.Is(err, ErrArtistNotFound) {
			return DocumentRoot{}, domainerr.ResourceNotFound("artist", artistID)
		}
		return DocumentRoot{}, err
	}
	if root.DocumentID == nil || *root.DocumentID == uuid.Nil {
		return DocumentRoot{}, domainerr.Precondition("Artist content document is not initialized")
	}
	return root, nil
}

func loadAIDocumentStateAfterAuthorization(
	ctx context.Context,
	store Store,
	root DocumentRoot,
	locale string,
	viewerMemberID string,
) (AIDocumentState, error) {
	locale, err := normalizeArtistDocumentLocale(locale)
	if err != nil {
		return AIDocumentState{}, err
	}
	localeState, err := loadArtistTargetLocaleState(ctx, store, root.ID, *root.DocumentID, locale, false)
	if err != nil {
		return AIDocumentState{}, err
	}
	document, err := artistSparseLocalizedDocument(localeState, locale)
	if err != nil {
		return AIDocumentState{}, err
	}
	var targetRevision *string
	if locale != localeState.SourceLocale && localeState.TargetMetadata != nil {
		targetRevision = &localeState.TargetRevision
	}
	return AIDocumentState{
		ArtistID: root.ID, Status: string(root.Status), DocumentID: *root.DocumentID,
		Revision: localeState.Snapshot.Document.Revision.String(), TargetRevision: targetRevision,
		SourceLocale: localeState.SourceLocale,
		Locale:       locale, LocaleExists: localeState.TargetMetadata != nil, Document: document,
		ViewerMemberID: viewerMemberID,
	}, nil
}

func artistAIDocumentLifecycle(status Status) (published, valid bool) {
	switch status {
	case StatusDraft:
		return false, true
	case StatusPublished:
		return true, true
	default:
		return false, false
	}
}
