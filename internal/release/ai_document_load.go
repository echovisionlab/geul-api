package release

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
)

// AIDocumentStateQuery names the Release locale an AI document reads.
type AIDocumentStateQuery struct {
	ReleaseID string
	Locale    string
}

// LoadAIDocumentState reads the Release locale an AI document edits, for an
// onboarded Member that can view the Release.
func (s *Service) LoadAIDocumentState(ctx context.Context, query AIDocumentStateQuery) (AIDocumentState, error) {
	id, err := uuidFromCanonicalString(query.ReleaseID)
	if err != nil {
		return AIDocumentState{}, domainerr.InvalidField("release_id", "must be a canonical UUID")
	}
	locale, err := normalizeReleaseDocumentLocale(query.Locale)
	if err != nil {
		return AIDocumentState{}, err
	}
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated || !principal.Onboarded || principal.Banned || principal.IdentityID == "" || principal.MemberID == "" {
		return AIDocumentState{}, authenticationRequired()
	}
	var state AIDocumentState
	err = s.units.ReadLocked(ctx, func(store Store) error {
		root, err := loadReleaseAIDocumentRoot(ctx, store, id.String(), KeyShareLock)
		if err != nil {
			return err
		}
		if err := requireReleaseAction(ctx, s.spiceDB, id.String(), releaseActionView); err != nil {
			return err
		}
		state, err = loadAIDocumentStateAfterAuthorization(
			ctx, store, root, locale, principal.MemberID.String(),
		)
		return err
	})
	return state, err
}

func loadReleaseAIDocumentRoot(
	ctx context.Context,
	store Store,
	releaseID string,
	lock RowLock,
) (DocumentRoot, error) {
	root, err := store.DocumentRoot(ctx, releaseID, lock)
	if err != nil {
		return DocumentRoot{}, releaseMissingAs(err, releaseNotFound(releaseID))
	}
	if root.DocumentID == nil || *root.DocumentID == uuid.Nil {
		return DocumentRoot{}, domainerr.Precondition("Release content document is not initialized")
	}
	if !knownReleaseLifecycle(root.Status) {
		return DocumentRoot{}, errors.New("Release has an unsupported lifecycle status")
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
	target, err := loadReleaseTargetLocaleState(
		ctx, store, root.ID, *root.DocumentID, locale, false,
	)
	if err != nil {
		return AIDocumentState{}, err
	}
	if target.Snapshot.Document.Profile != releaseContentProfile {
		return AIDocumentState{}, errors.New("Release AI document requires the compact content profile")
	}
	exists := target.TargetMetadata != nil
	document, err := contentblock.SnapshotToLocalizedRichTextDocument(target.Snapshot, locale)
	if err != nil {
		return AIDocumentState{}, normalizeReleaseContentBlockError(err)
	}
	if !exists && targetlocale.SnapshotHasLocale(target.Snapshot, locale) {
		return AIDocumentState{}, errors.New("Release locale overlay exists without translation metadata")
	}
	creditIDs, err := store.CreditIDs(ctx, root.ID)
	if err != nil {
		return AIDocumentState{}, err
	}
	state := AIDocumentState{
		ReleaseID: root.ID, Status: string(root.Status), DocumentID: *root.DocumentID,
		DocumentRevision: target.Snapshot.Document.Revision.String(), SourceLocale: target.SourceLocale,
		Locale: locale, LocaleExists: exists, Document: document,
		CreditIDs: creditIDs,
		SourceMetadata: AIDocumentLocaleMetadata{
			Title: target.SourceMetadata.Title, CreditNotes: target.SourceNotes,
		},
		ViewerMemberID: viewerMemberID,
	}
	if exists {
		state.RequestedMetadata = &AIDocumentLocaleMetadata{
			Title: target.TargetMetadata.Title, CreditNotes: target.TargetNotes,
		}
		if locale != target.SourceLocale {
			revision := target.TargetRevision
			state.TargetRevision = &revision
		}
	}
	return state, nil
}

// knownReleaseLifecycle reports whether status is a lifecycle state an AI
// document may read and edit; any other stored value is corrupt.
func knownReleaseLifecycle(status Status) bool {
	return status == StatusDraft || status == StatusPublished
}
