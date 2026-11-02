package artist

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
)

const (
	artistContentEntity    = "artist"
	creativeContentProfile = "compact"
)

type translationLocaleDocumentSaveInput struct {
	SourceLocale string
	Title        *string
	Now          time.Time
}

type creativeManageContentProjection struct {
	Document        *contentv1.RichTextDocument
	Revision        string
	SourceTitle     string
	SourceOgAssetID *string
}

// loadCreativeManageContentProjection reads the Artist's source-locale
// document, title and OG image in one snapshot, after loadRoot reads the
// root row.
func (s *Service) loadCreativeManageContentProjection(ctx context.Context, artistID string, loadRoot func(context.Context, Store) error) (creativeManageContentProjection, error) {
	var projection creativeManageContentProjection
	err := s.units.ReadLocked(ctx, func(store Store) error {
		if err := loadRoot(ctx, store); err != nil {
			return err
		}
		snapshot, sourceLocale, err := loadCreativeContentSnapshotInTransaction(ctx, store, artistID)
		if err != nil {
			return err
		}
		document, err := contentblock.SnapshotToRichTextDocument(snapshot)
		if err != nil {
			return normalizeCreativeContentBlockError(err)
		}
		title, err := loadCreativeSourceTitle(ctx, store, artistID, sourceLocale)
		if err != nil {
			return err
		}
		ogAssetID, err := store.LocaleOgAssetID(ctx, artistID, sourceLocale)
		if err != nil {
			return err
		}
		projection = creativeManageContentProjection{
			Document:        document,
			Revision:        snapshot.Document.Revision.String(),
			SourceTitle:     title,
			SourceOgAssetID: ogAssetID,
		}
		return nil
	})
	return projection, err
}

func loadCreativeSourceTitle(ctx context.Context, store Store, artistID string, locale string) (string, error) {
	title, err := store.LocaleTitleText(ctx, artistID, locale)
	if errors.Is(err, ErrLocaleNotFound) {
		return "", domainerr.ResourceNotFound("artist_translation", artistID)
	}
	return title, err
}

func saveCreativeSourceLocaleMetadata(ctx context.Context, store Store, artistID string, locale string, input translationLocaleDocumentSaveInput) error {
	if input.Title == nil || strings.TrimSpace(*input.Title) == "" {
		return domainerr.InvalidField("title", "must not be empty")
	}
	if strings.TrimSpace(input.SourceLocale) == "" {
		return domainerr.Precondition("Artist source locale is not initialized")
	}
	if input.SourceLocale != locale {
		return domainerr.Precondition("source locale metadata must match translation source authority")
	}
	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return store.SaveLocaleTitle(ctx, artistID, locale, strings.TrimSpace(*input.Title), now)
}

// touchArtist moves the Artist's updated_at; a missing Artist answers not
// found.
func touchArtist(ctx context.Context, store Store, artistID string, now time.Time) error {
	if err := store.TouchArtist(ctx, artistID, now); err != nil {
		if errors.Is(err, ErrArtistNotFound) {
			return domainerr.ResourceNotFound("artist", artistID)
		}
		return err
	}
	return nil
}

// loadCreativeDocumentAuthority reads the Artist's source locale with lock;
// a missing Artist answers not found and a blank one fails its
// precondition.
func loadCreativeDocumentAuthority(ctx context.Context, store Store, artistID string, lock RowLock) (string, error) {
	sourceLocale, err := store.SourceLocale(ctx, artistID, lock)
	if errors.Is(err, ErrArtistNotFound) {
		return "", domainerr.ResourceNotFound(artistContentEntity, artistID)
	}
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(sourceLocale) == "" {
		return "", domainerr.Precondition(artistContentEntity + " source locale is not initialized")
	}
	return sourceLocale, nil
}

// readCreativeDocumentAuthority is loadCreativeDocumentAuthority in its own
// read snapshot.
func (s *Service) readCreativeDocumentAuthority(ctx context.Context, artistID string) (string, error) {
	var sourceLocale string
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		sourceLocale, err = loadCreativeDocumentAuthority(ctx, store, artistID, Unlocked)
		return err
	})
	return sourceLocale, err
}

func loadCreativeContentSnapshotInTransaction(ctx context.Context, store Store, artistID string) (contentblock.Snapshot, string, error) {
	documentID, err := loadCreativeContentDocumentIDWithStrength(ctx, store, artistID, ShareLock)
	if err != nil {
		return contentblock.Snapshot{}, "", err
	}
	sourceLocale, err := loadCreativeDocumentAuthority(ctx, store, artistID, ShareLock)
	if err != nil {
		return contentblock.Snapshot{}, "", err
	}
	snapshot, err := store.LockedContentSnapshot(ctx, documentID, sourceLocale)
	if err != nil {
		return contentblock.Snapshot{}, "", normalizeCreativeContentBlockError(err)
	}
	if strings.TrimSpace(snapshot.SourceLocale) != sourceLocale {
		return contentblock.Snapshot{}, "", errors.New("content document source locale does not match translation authority")
	}
	return snapshot, sourceLocale, nil
}

func initializeCreativeContentDocument(
	ctx context.Context,
	store Store,
	artistID string,
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
	replace, err := contentblock.ReplaceFromRichTextProto(created.Document.ID, created.Document.Revision, input)
	if err != nil {
		return contentblock.Result{}, normalizeCreativeContentBlockError(err)
	}
	result, err := store.ReplaceContentSnapshot(ctx, replace, creativeContentCreationFence(artistID, sourceLocale))
	if err != nil {
		return contentblock.Result{}, normalizeCreativeContentBlockError(err)
	}
	return result, nil
}

type creativeContentBootstrapSnapshot struct {
	Snapshot     contentblock.Snapshot
	SourceLocale string
}

// loadCreativeContentBlockBootstrap resolves the collaboration session's
// principal, requires Edit, and reads the Artist's document and
// loadMetadata in one snapshot.
func (s *Service) loadCreativeContentBlockBootstrap(ctx context.Context, artistID string, sessionID string, loadMetadata func(context.Context, Store) error) (creativeContentBootstrapSnapshot, error) {
	if sessionID == "" {
		return creativeContentBootstrapSnapshot{}, errAuthenticationRequired
	}
	var output creativeContentBootstrapSnapshot
	err := s.units.ReadLocked(ctx, func(store Store) error {
		documentID, err := loadCreativeContentDocumentIDWithStrength(ctx, store, artistID, ShareLock)
		if err != nil {
			return err
		}
		principal, err := store.ResolveSessionPrincipal(ctx, sessionID)
		if errors.Is(err, auth.ErrSessionPrincipalInvalid) {
			return errAuthenticationRequired.WithCause(err)
		}
		if err != nil {
			return fmt.Errorf("resolve %s collaboration principal: %w", artistContentEntity, err)
		}
		if principal == nil || !principal.Authenticated {
			return errAuthenticationRequired
		}
		if principal.Banned {
			return domainerr.Denied("account is banned")
		}
		if !principal.Onboarded {
			return domainerr.Denied(fmt.Sprintf("no permission to %s this %s", "edit", artistContentEntity))
		}
		can, err := policyv1.Artist.Edit(artistID)
		if err != nil {
			return domainerr.InvalidField("artist_id", "must be a canonical Artist UUID")
		}
		decision, err := auth.AuthorizationDecision(auth.WithUser(ctx, principal), can)
		if err != nil {
			return errAuthenticationRequired.WithCause(err)
		}
		allowed, err := s.spiceDB.Can(ctx, decision)
		if err != nil {
			return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
		}
		if !allowed {
			return domainerr.Denied("no permission to edit this artist")
		}
		sourceLocale, err := loadCreativeDocumentAuthority(ctx, store, artistID, ShareLock)
		if err != nil {
			return err
		}
		snapshot, err := store.LockedContentSnapshot(ctx, documentID, sourceLocale)
		if err != nil {
			return normalizeCreativeContentBlockError(err)
		}
		if snapshot.SourceLocale != sourceLocale {
			return errors.New("content document source locale does not match translation authority")
		}
		if err := loadMetadata(ctx, store); err != nil {
			return err
		}
		output = creativeContentBootstrapSnapshot{Snapshot: snapshot, SourceLocale: sourceLocale}
		return nil
	})
	return output, err
}

// lockedContentFence admits only documentID and answers with the domain
// context the caller already authorized under its lock
// (contentblock.LockedFence).
func lockedContentFence(documentID uuid.UUID, domain contentblock.DomainContext) ContentFence {
	return func(_ context.Context, _ Store, requested uuid.UUID) (contentblock.DomainContext, error) {
		if requested != documentID {
			return contentblock.DomainContext{}, domainerr.Precondition(artistEntityName + " content document changed; reload before saving")
		}
		return domain, nil
	}
}

func internalCreativeContentFence(artistID string) ContentFence {
	return func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if err := lockCreativeContentDocumentRoot(ctx, store, artistID, documentID); err != nil {
			return contentblock.DomainContext{}, err
		}
		return lockCreativeTranslationSourceContext(ctx, store, artistID)
	}
}

// creativeContentDocumentFence locks the Artist row of the document the
// mutation, already authorized, removes.
func creativeContentDocumentFence(artistID string) ContentFence {
	return func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error) {
		return contentblock.DomainContext{}, lockCreativeContentDocumentRoot(ctx, store, artistID, documentID)
	}
}

// creativeContentCreationFence locks the new Artist row and names its source
// locale while the creation, already authorized, fills its document.
func creativeContentCreationFence(artistID string, sourceLocale string) ContentFence {
	return func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if err := lockCreativeContentDocumentRoot(ctx, store, artistID, documentID); err != nil {
			return contentblock.DomainContext{}, err
		}
		return contentblock.DomainContext{SourceLocale: sourceLocale}, nil
	}
}

func lockCreativeTranslationSourceContext(ctx context.Context, store Store, artistID string) (contentblock.DomainContext, error) {
	sourceLocale, err := loadCreativeDocumentAuthority(ctx, store, artistID, UpdateLock)
	if err != nil {
		return contentblock.DomainContext{}, err
	}
	return contentblock.DomainContext{SourceLocale: sourceLocale}, nil
}

// normalizeCreativeContentBlockError gives a raw content document failure
// its client answer; any other error keeps its own.
func normalizeCreativeContentBlockError(err error) error {
	if err == nil {
		return nil
	}
	entityType := artistContentEntity
	if _, classified := errors.AsType[*domainerr.Error](err); classified {
		return err
	}
	// A revision conflict does not keep its cause: callers branch on
	// errors.Is/As for raw stale and target-revision errors, and an
	// already-normalized conflict must not take those branches.
	var targetConflict *translation.TargetRevisionConflict
	switch {
	case errors.As(err, &targetConflict):
		return domainerr.CollaborationConflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED,
			entityType+" target locale changed since it was loaded; reload before saving", domainerr.CollaborationRevisions{},
		)
	case errors.Is(err, contentblock.ErrDocumentNotFound):
		return domainerr.New(domainerr.NotFound, "", entityType+" content document not found").WithCause(err)
	case errors.Is(err, contentblock.ErrStaleRevision):
		return domainerr.CollaborationConflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED,
			entityType+" content revision changed; reload before saving", domainerr.CollaborationRevisions{},
		)
	case errors.Is(err, contentblock.ErrCrossDocument):
		return domainerr.InvalidField("blocks", "a Block belongs to another document").WithCause(err)
	case errors.Is(err, contentblock.ErrFileReference):
		return domainerr.InvalidField("blocks", "compact documents cannot contain File Blocks").WithCause(err)
	case errors.Is(err, contentblock.ErrInvalidMutation):
		return domainerr.InvalidField("blocks", err.Error())
	default:
		return fmt.Errorf("%s content document: %w", entityType, err)
	}
}

func loadCreativeContentDocumentID(ctx context.Context, store Store, artistID string) (uuid.UUID, error) {
	return loadCreativeContentDocumentIDWithStrength(ctx, store, artistID, Unlocked)
}

// readCreativeContentDocumentID is loadCreativeContentDocumentID in its own
// read snapshot.
func (s *Service) readCreativeContentDocumentID(ctx context.Context, artistID string) (uuid.UUID, error) {
	var documentID uuid.UUID
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		documentID, err = loadCreativeContentDocumentID(ctx, store, artistID)
		return err
	})
	return documentID, err
}

func lockCreativeContentDocumentRoot(ctx context.Context, store Store, artistID string, expectedDocumentID uuid.UUID) error {
	documentID, err := loadCreativeContentDocumentIDWithStrength(ctx, store, artistID, UpdateLock)
	if err != nil {
		return err
	}
	if expectedDocumentID == uuid.Nil || documentID != expectedDocumentID {
		return domainerr.Precondition("content document ownership changed; reload before saving")
	}
	return nil
}

func loadCreativeContentDocumentIDWithStrength(ctx context.Context, store Store, artistID string, lock RowLock) (uuid.UUID, error) {
	stored, err := store.ContentDocumentID(ctx, artistID, lock)
	if errors.Is(err, ErrArtistNotFound) {
		return uuid.Nil, domainerr.ResourceNotFound(artistContentEntity, artistID)
	}
	if err != nil {
		return uuid.Nil, err
	}
	if stored == nil || *stored == "" {
		return uuid.Nil, domainerr.Precondition("content document is not initialized")
	}
	documentID, err := uuid.Parse(*stored)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid %s content_document_id: %w", artistContentEntity, err)
	}
	return documentID, nil
}
