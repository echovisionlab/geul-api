package release

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

type releaseManageContentProjection struct {
	Document    *contentv1.RichTextDocument
	Revision    string
	SourceTitle string
}

// loadReleaseManageContentProjection reads the Release's content document
// and source title in one snapshot that share-locks the root, after
// loadRoot, when set, read the root in it.
func (s *Service) loadReleaseManageContentProjection(
	ctx context.Context,
	releaseID string,
	loadRoot func(context.Context, Store) error,
) (releaseManageContentProjection, error) {
	var projection releaseManageContentProjection
	err := s.units.ReadLocked(ctx, func(store Store) error {
		if loadRoot != nil {
			if err := loadRoot(ctx, store); err != nil {
				return err
			}
		}
		snapshot, sourceLocale, err := loadReleaseContentSnapshotInTransaction(ctx, store, releaseID)
		if err != nil {
			return err
		}
		document, err := contentblock.SnapshotToRichTextDocument(snapshot)
		if err != nil {
			return normalizeReleaseContentBlockError(err)
		}
		title, err := loadReleaseSourceTitle(ctx, store, releaseID, sourceLocale)
		if err != nil {
			return err
		}
		projection = releaseManageContentProjection{
			Document: document, Revision: snapshot.Document.Revision.String(), SourceTitle: title,
		}
		return nil
	})
	return projection, err
}

// loadReleaseSourceTitle reads the title of the Release's source locale; a
// missing locale answers not found.
func loadReleaseSourceTitle(ctx context.Context, store Store, releaseID, locale string) (string, error) {
	metadata, exists, err := store.LocaleMetadata(ctx, releaseID, locale, false)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", domainerr.ResourceNotFound("release_translation", releaseID)
	}
	if metadata.Title == nil {
		return "", nil
	}
	return *metadata.Title, nil
}

func saveReleaseSourceLocaleMetadata(
	ctx context.Context,
	store Store,
	releaseID string,
	locale string,
	input translationLocaleDocumentSaveInput,
) error {
	if input.Title == nil || strings.TrimSpace(*input.Title) == "" {
		return domainerr.InvalidField("title", "must not be empty")
	}
	sourceLocale, err := loadReleaseSourceLocale(ctx, store, releaseID, Unlocked)
	if err != nil {
		return err
	}
	if sourceLocale != locale {
		return domainerr.Precondition("source locale metadata must match translation source authority")
	}
	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return store.SaveSourceLocaleTitle(ctx, releaseID, locale, strings.TrimSpace(*input.Title), now)
}

// loadReleaseSourceLocale reads the Release's source locale with lock; a
// missing Release answers not found.
func loadReleaseSourceLocale(ctx context.Context, store Store, releaseID string, lock RowLock) (string, error) {
	sourceLocale, err := store.SourceLocale(ctx, releaseID, lock)
	if err != nil {
		return "", releaseMissingAs(err, releaseNotFound(releaseID))
	}
	return sourceLocale, nil
}

// loadReleaseContentSnapshotInTransaction share-locks the Release root and
// reads its content document in the source locale.
func loadReleaseContentSnapshotInTransaction(ctx context.Context, store Store, releaseID string) (contentblock.Snapshot, string, error) {
	documentID, err := loadReleaseContentDocumentID(ctx, store, releaseID, ShareLock)
	if err != nil {
		return contentblock.Snapshot{}, "", err
	}
	sourceLocale, err := loadReleaseSourceLocale(ctx, store, releaseID, ShareLock)
	if err != nil {
		return contentblock.Snapshot{}, "", err
	}
	snapshot, err := store.LockedContentSnapshot(ctx, documentID, sourceLocale)
	if err != nil {
		return contentblock.Snapshot{}, "", normalizeReleaseContentBlockError(err)
	}
	if strings.TrimSpace(snapshot.SourceLocale) != sourceLocale {
		return contentblock.Snapshot{}, "", errors.New("content document source locale does not match release root")
	}
	return snapshot, sourceLocale, nil
}

func initializeReleaseContentDocument(
	ctx context.Context,
	store Store,
	releaseID string,
	sourceLocale string,
	created contentblock.Snapshot,
	input *contentv1.RichTextDocument,
) (contentblock.Result, error) {
	if input == nil {
		return contentblock.Result{}, nil
	}
	if input.GetProfile() != contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT {
		return contentblock.Result{}, domainerr.InvalidField("document.profile", "must be compact")
	}
	if input.GetSourceLocale() != sourceLocale {
		return contentblock.Result{}, domainerr.InvalidField("document.source_locale", "must match the server-selected source locale")
	}
	replace, err := contentblock.ReplaceFromRichTextProto(
		created.Document.ID,
		created.Document.Revision,
		input,
	)
	if err != nil {
		return contentblock.Result{}, normalizeReleaseContentBlockError(err)
	}
	result, err := store.ReplaceContentSnapshot(ctx, replace, releaseContentCreationFence(releaseID, sourceLocale))
	if err != nil {
		return contentblock.Result{}, normalizeReleaseContentBlockError(err)
	}
	return result, nil
}

type releaseContentBootstrapSnapshot struct {
	Snapshot     contentblock.Snapshot
	SourceLocale string
}

// loadReleaseContentBlockBootstrap reads the Release content snapshot a
// collaboration room opens, after resolving the room's principal from its
// session and checking its exact Release edit permission. loadMetadata
// reads the room's metadata in the same snapshot.
func (s *Service) loadReleaseContentBlockBootstrap(
	ctx context.Context,
	releaseID string,
	principal *CollaborationPrincipal,
	loadMetadata func(context.Context, Store) error,
) (releaseContentBootstrapSnapshot, error) {
	if principal == nil {
		return releaseContentBootstrapSnapshot{}, authenticationRequired()
	}
	var output releaseContentBootstrapSnapshot
	err := s.units.ReadLocked(ctx, func(store Store) error {
		documentID, err := loadReleaseContentDocumentID(ctx, store, releaseID, ShareLock)
		if err != nil {
			return err
		}
		if err := requireCollaborationSession(ctx, store, s.spiceDB, releaseID, principal.SessionID); err != nil {
			return err
		}
		sourceLocale, err := loadReleaseSourceLocale(ctx, store, releaseID, ShareLock)
		if err != nil {
			return err
		}
		snapshot, err := store.LockedContentSnapshot(ctx, documentID, sourceLocale)
		if err != nil {
			return normalizeReleaseContentBlockError(err)
		}
		if snapshot.SourceLocale != sourceLocale {
			return errors.New("content document source locale does not match translation authority")
		}
		if err := loadMetadata(ctx, store); err != nil {
			return err
		}
		output = releaseContentBootstrapSnapshot{Snapshot: snapshot, SourceLocale: sourceLocale}
		return nil
	})
	return output, err
}

// requireCollaborationSession resolves the principal of a collaboration
// room's session and requires its exact Release edit permission.
func requireCollaborationSession(
	ctx context.Context,
	store Store,
	spiceDB CollaborationPermissionChecker,
	releaseID string,
	sessionID string,
) error {
	principal, err := store.ResolveSessionPrincipal(ctx, sessionID)
	if errors.Is(err, auth.ErrSessionPrincipalInvalid) {
		return authenticationRequired().WithCause(err)
	}
	if err != nil {
		return fmt.Errorf("resolve Release collaboration principal: %w", err)
	}
	switch {
	case principal == nil || !principal.Authenticated:
		return authenticationRequired()
	case principal.Banned:
		return accountBanned()
	case !principal.Onboarded:
		return noPermission("edit", releaseContentEntity)
	}
	return requireCollaborationEdit(ctx, spiceDB, releaseID, principal)
}

const (
	releaseContentEntity  = "release"
	releaseContentProfile = "compact"
)

// lockedContentFence admits only documentID and answers with the domain
// context the caller already authorized under its lock
// (contentblock.LockedFence).
func lockedContentFence(documentID uuid.UUID, domain contentblock.DomainContext) ContentFence {
	return func(_ context.Context, _ Store, requested uuid.UUID) (contentblock.DomainContext, error) {
		if requested != documentID {
			return contentblock.DomainContext{}, domainerr.Precondition(releaseEntityName + " content document changed; reload before saving")
		}
		return domain, nil
	}
}

func internalReleaseContentFence(releaseID string) ContentFence {
	return func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if err := lockReleaseContentDocumentRoot(ctx, store, releaseID, documentID); err != nil {
			return contentblock.DomainContext{}, err
		}
		return lockReleaseTranslationSourceContext(ctx, store, releaseID)
	}
}

// releaseContentDocumentFence locks the Release row of the document the
// mutation, already authorized, removes.
func releaseContentDocumentFence(releaseID string) ContentFence {
	return func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error) {
		return contentblock.DomainContext{}, lockReleaseContentDocumentRoot(ctx, store, releaseID, documentID)
	}
}

// releaseContentCreationFence locks the new Release row and names its
// source locale while the creation, already authorized, fills its document.
func releaseContentCreationFence(releaseID, sourceLocale string) ContentFence {
	return func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if err := lockReleaseContentDocumentRoot(ctx, store, releaseID, documentID); err != nil {
			return contentblock.DomainContext{}, err
		}
		return contentblock.DomainContext{SourceLocale: sourceLocale}, nil
	}
}

func lockReleaseTranslationSourceContext(ctx context.Context, store Store, releaseID string) (contentblock.DomainContext, error) {
	sourceLocale, err := loadReleaseSourceLocale(ctx, store, releaseID, UpdateLock)
	if err != nil {
		return contentblock.DomainContext{}, err
	}
	return contentblock.DomainContext{SourceLocale: sourceLocale}, nil
}

// normalizeReleaseContentBlockError answers the Content Block store's
// sentinel failures. A revision conflict keeps its cause, so a conflict
// found while storing takes the same errors.Is/As branches (AI document
// conflict result, translation source no longer current) as one found by
// the pre-check. Any other error passes through unchanged.
func normalizeReleaseContentBlockError(err error) error {
	if err == nil {
		return nil
	}
	var targetConflict *translation.TargetRevisionConflict
	if errors.As(err, &targetConflict) {
		return releaseConflict(intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED, targetConflict.Error(), err)
	}
	switch {
	case errors.Is(err, contentblock.ErrDocumentNotFound):
		return notFound("release content document not found").WithCause(err)
	case errors.Is(err, contentblock.ErrStaleRevision):
		return releaseConflict(intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED, "release content revision changed; reload before saving", err)
	case errors.Is(err, contentblock.ErrCrossDocument):
		return domainerr.InvalidField("blocks", "a Block belongs to another document").WithCause(err)
	case errors.Is(err, contentblock.ErrFileReference):
		return domainerr.InvalidField("blocks", "compact documents cannot contain File Blocks").WithCause(err)
	case errors.Is(err, contentblock.ErrInvalidMutation):
		return domainerr.InvalidField("blocks", err.Error())
	default:
		return err
	}
}

// releaseConflict is the collaboration conflict answer for cause: it reports
// the current revisions cause carries and keeps cause in the chain.
func releaseConflict(reason intrav1.CollaborationConflictReason, message string, cause error) error {
	return fmt.Errorf("%w: %w", domainerr.CollaborationConflict(reason, message, domainerr.CollaborationRevisionsOf(cause)), cause)
}

// readReleaseContentDocumentID is loadReleaseContentDocumentID unlocked in
// its own read snapshot, for a use case that names the document before its
// mutation.
func (s *Service) readReleaseContentDocumentID(ctx context.Context, releaseID string) (uuid.UUID, error) {
	var documentID uuid.UUID
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		documentID, err = loadReleaseContentDocumentID(ctx, store, releaseID, Unlocked)
		return err
	})
	return documentID, err
}

func lockReleaseContentDocumentRoot(ctx context.Context, store Store, releaseID string, expectedDocumentID uuid.UUID) error {
	documentID, err := loadReleaseContentDocumentID(ctx, store, releaseID, UpdateLock)
	if err != nil {
		return err
	}
	if expectedDocumentID == uuid.Nil || documentID != expectedDocumentID {
		return domainerr.Precondition("content document ownership changed; reload before saving")
	}
	return nil
}

// loadReleaseContentDocumentID reads the Release's content document with
// lock; a missing Release answers not found.
func loadReleaseContentDocumentID(ctx context.Context, store Store, releaseID string, lock RowLock) (uuid.UUID, error) {
	documentID, err := store.ContentDocumentID(ctx, releaseID, lock)
	if err != nil {
		return uuid.Nil, releaseMissingAs(err, releaseNotFound(releaseID))
	}
	if documentID == nil {
		return uuid.Nil, domainerr.Precondition("content document is not initialized")
	}
	return *documentID, nil
}

// touchReleaseSourceLocaleState moves the Release's updated_at after a
// source locale change; a missing Release answers not found.
func touchReleaseSourceLocaleState(ctx context.Context, store Store, releaseID string, now time.Time) error {
	return releaseMissingAs(store.TouchRelease(ctx, releaseID, now), releaseNotFound(releaseID))
}

type translationLocaleDocumentSaveInput struct {
	Title *string
	Now   time.Time
}
