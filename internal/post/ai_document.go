package post

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// AIDocumentLocaleMetadata preserves missing and explicit-empty locale values.
// A nil Metadata on AIDocumentState means the target translation resource does
// not exist; nil fields inside a present Metadata remain explicit missing.
type AIDocumentLocaleMetadata struct {
	Title   *string
	Summary *string
}

// AIDocumentState is the Post-owned authorized read model consumed by the
// schema adapter. Editor JSON and transport messages do not cross this domain
// boundary.
type AIDocumentState struct {
	PostID            string
	ContentDocumentID string
	DocumentRevision  string
	TargetRevision    *string
	SourceLocale      string
	RequestedLocale   string
	LocaleExists      bool
	SourceMetadata    AIDocumentLocaleMetadata
	RequestedMetadata *AIDocumentLocaleMetadata
	CategoryIDs       []string
	TagIDs            []string
	LocalizedDocument *contentv1.LocalizedRichTextDocument
	ViewerMemberID    string
}

// LoadAIDocumentState applies current Member/Identity authority and one
// database snapshot before projecting the exact requested locale. Archived
// Posts remain readable to their Author and Site Admin, but not to a former
// collaborator-only editor.
func (s *Service) LoadAIDocumentState(
	ctx context.Context,
	postID string,
	locale string,
) (AIDocumentState, error) {
	var err error
	locale, err = validatePostAIDocumentIdentity(postID, locale)
	if err != nil {
		return AIDocumentState{}, err
	}
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated || !principal.Onboarded || principal.Banned || principal.MemberID == "" {
		return AIDocumentState{}, domainerr.AuthenticationRequired()
	}

	var root AIDocumentRoot
	access := mutation.Checked(func(ctx context.Context, store Store) error {
		var err error
		if root, err = loadPostAIDocumentRoot(ctx, store, postID, KeyShareLock); err != nil {
			return err
		}
		_, err = requirePostViewForStatus(ctx, s.spiceDB, postID, root.Status)
		return err
	})
	var state AIDocumentState
	err = s.readLocked(ctx, access, func(ctx context.Context, store Store) error {
		var err error
		state, err = loadAIDocumentStateAfterAuthorization(
			ctx,
			store,
			root,
			locale,
			principal.MemberID.String(),
		)
		return err
	})
	if err != nil {
		return AIDocumentState{}, err
	}
	return state, nil
}

func validatePostAIDocumentIdentity(postID, locale string) (string, error) {
	if _, err := uuidutil.ParseCanonical(postID, "post_id"); err != nil {
		return "", domainerr.InvalidField("post_id", "must be a canonical UUID").WithCause(err)
	}
	normalizedLocale := localization.NormalizeExactSupportedLocale(locale)
	if normalizedLocale == nil {
		return "", domainerr.InvalidField("locale", "must be an exact canonical locale")
	}
	return *normalizedLocale, nil
}

func loadPostAIDocumentRoot(
	ctx context.Context,
	store Store,
	postID string,
	lock RowLock,
) (AIDocumentRoot, error) {
	root, err := store.AIDocumentRoot(ctx, postID, lock)
	if err != nil {
		if errors.Is(err, ErrPostNotFound) {
			return AIDocumentRoot{}, domainerr.ResourceNotFound("post", postID)
		}
		return AIDocumentRoot{}, fmt.Errorf("load Post AI document root: %w", err)
	}
	if root.ContentDocumentID == nil || strings.TrimSpace(*root.ContentDocumentID) == "" {
		return AIDocumentRoot{}, domainerr.Precondition("Post content document has not been populated")
	}
	return root, nil
}

func loadAIDocumentStateAfterAuthorization(
	ctx context.Context,
	store Store,
	root AIDocumentRoot,
	locale string,
	viewerMemberID string,
) (AIDocumentState, error) {
	documentID, err := uuidutil.ParseCanonical(*root.ContentDocumentID, "content_document_id")
	if err != nil {
		return AIDocumentState{}, domainerr.Precondition("Post content document identity is invalid").WithCause(failurecause.ServerFault(err))
	}
	domain, err := loadPostContentDomainContext(ctx, store, root.ID)
	if err != nil {
		return AIDocumentState{}, err
	}
	snapshot, err := store.LockedContentSnapshot(ctx, documentID, domain.SourceLocale)
	if err != nil {
		return AIDocumentState{}, normalizePostContentBlockError(err)
	}
	if snapshot.Document.Profile != postContentDocumentProfile {
		return AIDocumentState{}, domainerr.Precondition("Post AI document requires the Post content profile")
	}

	state := AIDocumentState{
		PostID:            root.ID,
		ContentDocumentID: documentID.String(),
		DocumentRevision:  snapshot.Document.Revision.String(),
		SourceLocale:      domain.SourceLocale,
		RequestedLocale:   locale,
		ViewerMemberID:    viewerMemberID,
	}
	sourceMetadata, found, err := loadAIDocumentMetadata(ctx, store, root.ID, domain.SourceLocale)
	if err != nil {
		return AIDocumentState{}, err
	}
	if !found {
		return AIDocumentState{}, domainerr.Precondition("Post source locale metadata is missing")
	}
	state.SourceMetadata = sourceMetadata
	if locale == domain.SourceLocale {
		state.LocaleExists = true
		copy := sourceMetadata
		state.RequestedMetadata = &copy
	} else {
		requested, exists, err := store.OptionalLocaleMetadata(ctx, root.ID, locale, false)
		if err != nil {
			return AIDocumentState{}, err
		}
		if !exists && targetlocale.SnapshotHasLocale(snapshot, locale) {
			return AIDocumentState{}, domainerr.Precondition("Post locale Blocks exist without owning translation metadata")
		}
		state.LocaleExists = exists
		if exists {
			state.RequestedMetadata = &AIDocumentLocaleMetadata{Title: requested.Title, Summary: requested.Summary}
			targetRevision, revisionErr := derivePostTargetRevision(snapshot.Document.Revision.String(), requested)
			if revisionErr != nil {
				return AIDocumentState{}, revisionErr
			}
			state.TargetRevision = &targetRevision
		}
	}
	state.CategoryIDs, state.TagIDs, err = store.TaxonomyIDs(ctx, root.ID)
	if err != nil {
		return AIDocumentState{}, fmt.Errorf("load Post AI document taxonomy: %w", err)
	}
	state.LocalizedDocument, err = contentblock.SnapshotToLocalizedRichTextDocument(snapshot, locale)
	if err != nil {
		return AIDocumentState{}, normalizePostContentBlockError(err)
	}
	return state, nil
}

func loadAIDocumentMetadata(
	ctx context.Context,
	store Store,
	postID string,
	locale string,
) (AIDocumentLocaleMetadata, bool, error) {
	metadata, found, err := store.LocaleText(ctx, postID, locale, false)
	if err != nil {
		return AIDocumentLocaleMetadata{}, false, fmt.Errorf("load Post AI document locale metadata: %w", err)
	}
	return metadata, found, nil
}
