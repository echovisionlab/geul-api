package release

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/collaborationconflict"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

type releaseManageContentProjection struct {
	Document        *contentv1.RichTextDocument
	Revision        string
	SnapshotDigest  string
	SourceTitle     string
	SourceOgAssetID *string
}

func loadReleaseManageContentProjection(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	releaseID string,
	localeAwareOG bool,
	loadRoot ...func(context.Context, *gorm.DB) error,
) (releaseManageContentProjection, error) {
	var projection releaseManageContentProjection
	err := mutation.ReadLocked(ctx, db, func(tx *gorm.DB) error {
		for _, load := range loadRoot {
			if load != nil {
				if err := load(ctx, tx); err != nil {
					return err
				}
			}
		}
		snapshot, source, err := loadReleaseContentSnapshotInTransaction(
			ctx, tx, store, releaseID,
		)
		if err != nil {
			return err
		}
		document, err := contentblock.SnapshotToRichTextDocument(snapshot)
		if err != nil {
			return normalizeReleaseContentBlockError(err)
		}
		title, err := loadReleaseSourceTitle(ctx, tx, releaseID, source.SourceLocale)
		if err != nil {
			return err
		}
		var sourceOgAssetID *string
		if localeAwareOG {
			var row struct {
				OgAssetID *string `gorm:"column:og_asset_id"`
			}
			if queryErr := tx.WithContext(ctx).Table("release_translation").
				Select("og_asset_id").
				Where("entity_id = ? AND locale = ?", releaseID, source.SourceLocale).
				Take(&row).Error; queryErr != nil {
				return queryErr
			}
			sourceOgAssetID = row.OgAssetID
		}
		projection = releaseManageContentProjection{
			Document:        document,
			Revision:        snapshot.Document.Revision.String(),
			SnapshotDigest:  snapshot.SnapshotDigest,
			SourceTitle:     title,
			SourceOgAssetID: sourceOgAssetID,
		}
		return nil
	})
	return projection, err
}

func loadReleaseSourceTitle(
	ctx context.Context,
	db *gorm.DB,
	releaseID string,
	locale string,
) (string, error) {
	var row struct {
		Title string `gorm:"column:title"`
	}
	if err := db.WithContext(ctx).
		Table("release_translation").
		Select("title").
		Where("entity_id = ? AND locale = ?", releaseID, locale).
		Take(&row).Error; err != nil {
		return "", missingAs(err, domainerr.ResourceNotFound("release_translation", releaseID))
	}
	return row.Title, nil
}

func saveReleaseSourceLocaleMetadata(
	ctx context.Context,
	db *gorm.DB,
	releaseID string,
	locale string,
	input translationLocaleDocumentSaveInput,
) error {
	if input.Title == nil || strings.TrimSpace(*input.Title) == "" {
		return domainerr.InvalidField("title", "must not be empty")
	}
	source, err := loadReleaseSourceLocale(ctx, db, releaseID)
	if err != nil {
		return err
	}
	if source.SourceLocale != locale {
		return domainerr.Precondition("source locale metadata must match translation source authority")
	}
	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return db.WithContext(ctx).Exec(
		`INSERT INTO release_translation (
		 entity_id, locale, title, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (entity_id, locale) DO UPDATE SET
		 title = EXCLUDED.title,
		 updated_at = EXCLUDED.updated_at`,
		releaseID, locale, strings.TrimSpace(*input.Title),
		now, now,
	).Error
}

type releaseLocaleMetadataState struct {
	Title *string `gorm:"column:title"`
}

func loadReleaseLocaleMetadataState(
	ctx context.Context,
	db *gorm.DB,
	releaseID string,
	locale string,
) (*releaseLocaleMetadataState, error) {
	var row releaseLocaleMetadataState
	result := db.WithContext(ctx).
		Table("release_translation").
		Select("title").
		Where("entity_id = ? AND locale = ?", releaseID, locale).
		Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if result.Error != nil {
		return nil, result.Error
	}
	return &row, nil
}

type releaseSourceLocale struct {
	SourceLocale string `gorm:"column:source_locale"`
}

func loadReleaseSourceLocale(
	ctx context.Context,
	db *gorm.DB,
	releaseID string,
) (releaseSourceLocale, error) {
	return loadReleaseSourceLocaleWithStrength(
		ctx, db, releaseID, "",
	)
}

func loadReleaseSourceLocaleWithStrength(
	ctx context.Context,
	db *gorm.DB,
	releaseID string,
	strength string,
) (releaseSourceLocale, error) {
	var state releaseSourceLocale
	query := db.WithContext(ctx).
		Table("release").
		Select("source_locale").
		Where("id = ?", releaseID)
	if strength != "" {
		query = query.Clauses(clause.Locking{Strength: strength})
	}
	if err := query.Take(&state).Error; err != nil {
		return releaseSourceLocale{}, missingAs(err, releaseNotFound(releaseID))
	}
	return state, nil
}

func loadReleaseContentSnapshotInTransaction(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	releaseID string,
) (contentblock.Snapshot, releaseSourceLocale, error) {
	documentID, err := loadReleaseContentDocumentIDWithStrength(
		ctx, tx, releaseID, clause.LockingStrengthShare,
	)
	if err != nil {
		return contentblock.Snapshot{}, releaseSourceLocale{}, err
	}
	source, err := loadReleaseSourceLocaleWithStrength(
		ctx, tx, releaseID, clause.LockingStrengthShare,
	)
	if err != nil {
		return contentblock.Snapshot{}, releaseSourceLocale{}, err
	}
	snapshot, err := store.LoadSnapshotInTransaction(ctx, tx, documentID, source.SourceLocale)
	if err != nil {
		return contentblock.Snapshot{}, releaseSourceLocale{}, normalizeReleaseContentBlockError(err)
	}
	if strings.TrimSpace(snapshot.SourceLocale) != source.SourceLocale {
		return contentblock.Snapshot{}, releaseSourceLocale{}, errors.New("content document source locale does not match release root")
	}
	return snapshot, source, nil
}

func initializeReleaseContentDocument(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	releaseID string,
	sourceLocale string,
	created contentblock.Snapshot,
	input *contentv1.RichTextDocument,
	authorize func(context.Context, *gorm.DB) error,
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
	result, err := store.ReplaceSnapshot(
		ctx,
		tx,
		replace,
		releaseContentCreationFence(releaseID, sourceLocale, authorize),
	)
	if err != nil {
		return contentblock.Result{}, normalizeReleaseContentBlockError(err)
	}
	return result, nil
}

type releaseContentBootstrapSnapshot struct {
	Snapshot contentblock.Snapshot
	Source   releaseSourceLocale
}

// loadReleaseContentBlockBootstrap reads the Release content snapshot a
// collaboration room opens, after resolving the room's principal from its
// session and checking its exact Release edit permission. loadMetadata
// reads the room's metadata in the same snapshot.
func loadReleaseContentBlockBootstrap(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	spiceDB CollaborationPermissionChecker,
	releaseID string,
	principal *CollaborationPrincipal,
	loadMetadata func(context.Context, *gorm.DB) error,
) (releaseContentBootstrapSnapshot, error) {
	if principal == nil {
		return releaseContentBootstrapSnapshot{}, authenticationRequired()
	}
	var output releaseContentBootstrapSnapshot
	err := mutation.ReadLocked(ctx, db, func(tx *gorm.DB) error {
		documentID, err := loadReleaseContentDocumentIDWithStrength(
			ctx, tx, releaseID, clause.LockingStrengthShare,
		)
		if err != nil {
			return err
		}
		if err := requireCollaborationSession(ctx, tx, spiceDB, releaseID, principal.SessionID); err != nil {
			return err
		}
		source, err := loadReleaseSourceLocaleWithStrength(
			ctx, tx, releaseID, clause.LockingStrengthShare,
		)
		if err != nil {
			return err
		}
		snapshot, err := store.LoadSnapshotInTransaction(
			ctx, tx, documentID, source.SourceLocale,
		)
		if err != nil {
			return normalizeReleaseContentBlockError(err)
		}
		if snapshot.SourceLocale != source.SourceLocale {
			return errors.New("content document source locale does not match translation authority")
		}
		if err := loadMetadata(ctx, tx); err != nil {
			return err
		}
		output = releaseContentBootstrapSnapshot{Snapshot: snapshot, Source: source}
		return nil
	})
	return output, err
}

// requireCollaborationSession resolves the principal of a collaboration
// room's session and requires its exact Release edit permission.
func requireCollaborationSession(
	ctx context.Context,
	tx *gorm.DB,
	spiceDB CollaborationPermissionChecker,
	releaseID string,
	sessionID string,
) error {
	principal, err := auth.ResolveAuthenticatedPrincipalBySessionID(ctx, tx, sessionID)
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
	return RequireCollaborationEdit(
		ctx, spiceDB, intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_RELEASE, releaseID, principal,
	)
}

const (
	releaseContentEntity  = "release"
	releaseContentProfile = "compact"
)

type releaseContentDocumentRoot struct {
	ID                string         `gorm:"column:id"`
	ContentDocumentID sql.NullString `gorm:"column:content_document_id"`
}

func internalReleaseContentFence(releaseID string) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if err := lockReleaseContentDocumentRoot(
			ctx,
			tx,
			releaseID,
			documentID,
		); err != nil {
			return contentblock.DomainContext{}, err
		}
		domain, err := lockReleaseTranslationSourceContext(ctx, tx, releaseID)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		return domain, nil
	}
}

func releaseContentDocumentFence(
	releaseID string,
	authorize func(context.Context, *gorm.DB) error,
) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if err := lockReleaseContentDocumentRoot(ctx, tx, releaseID, documentID); err != nil {
			return contentblock.DomainContext{}, err
		}
		if authorize == nil {
			return contentblock.DomainContext{}, errors.New("content document authorization is required")
		}
		if err := authorize(ctx, tx); err != nil {
			return contentblock.DomainContext{}, err
		}
		return contentblock.DomainContext{}, nil
	}
}

func releaseContentCreationFence(
	releaseID string,
	sourceLocale string,
	authorize func(context.Context, *gorm.DB) error,
) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if err := lockReleaseContentDocumentRoot(ctx, tx, releaseID, documentID); err != nil {
			return contentblock.DomainContext{}, err
		}
		if authorize == nil {
			return contentblock.DomainContext{}, errors.New("content document authorization is required")
		}
		if err := authorize(ctx, tx); err != nil {
			return contentblock.DomainContext{}, err
		}
		return contentblock.DomainContext{SourceLocale: sourceLocale}, nil
	}
}

func lockReleaseTranslationSourceContext(
	ctx context.Context,
	tx *gorm.DB,
	releaseID string,
) (contentblock.DomainContext, error) {
	var source struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Table("release").
		Select("source_locale").
		Where("id = ?", releaseID).
		Take(&source).Error; err != nil {
		return contentblock.DomainContext{}, missingAs(err, releaseNotFound(releaseID))
	}
	return contentblock.DomainContext{SourceLocale: source.SourceLocale}, nil
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
	var state collaborationconflict.State
	var revisions interface {
		CollaborationRevisions() (string, string, *bool)
	}
	if errors.As(cause, &revisions) {
		state.Document, state.Target, state.TargetExists = revisions.CollaborationRevisions()
	}
	return fmt.Errorf("%w: %w", collaborationconflict.Conflict(reason, message, state), cause)
}

func loadReleaseContentDocumentID(
	ctx context.Context,
	db *gorm.DB,
	releaseID string,
) (uuid.UUID, error) {
	return loadReleaseContentDocumentIDWithLock(ctx, db, releaseID, false)
}

func lockReleaseContentDocumentRoot(
	ctx context.Context,
	tx *gorm.DB,
	releaseID string,
	expectedDocumentID uuid.UUID,
) error {
	documentID, err := loadReleaseContentDocumentIDWithLock(
		ctx,
		tx,
		releaseID,
		true,
	)
	if err != nil {
		return err
	}
	if expectedDocumentID == uuid.Nil || documentID != expectedDocumentID {
		return domainerr.Precondition("content document ownership changed; reload before saving")
	}
	return nil
}

func loadReleaseContentDocumentIDWithLock(
	ctx context.Context,
	db *gorm.DB,
	releaseID string,
	lock bool,
) (uuid.UUID, error) {
	strength := ""
	if lock {
		strength = clause.LockingStrengthUpdate
	}
	return loadReleaseContentDocumentIDWithStrength(
		ctx, db, releaseID, strength,
	)
}

func loadReleaseContentDocumentIDWithStrength(
	ctx context.Context,
	db *gorm.DB,
	releaseID string,
	strength string,
) (uuid.UUID, error) {
	query := db.WithContext(ctx).
		Table("release").
		Select("id", "content_document_id").
		Where("id = ?", releaseID)
	if strength != "" {
		query = query.Clauses(clause.Locking{Strength: strength})
	}
	var root releaseContentDocumentRoot
	if err := query.Take(&root).Error; err != nil {
		return uuid.Nil, missingAs(err, releaseNotFound(releaseID))
	}
	if !root.ContentDocumentID.Valid || root.ContentDocumentID.String == "" {
		return uuid.Nil, domainerr.Precondition("content document is not initialized")
	}
	documentID, err := uuid.Parse(root.ContentDocumentID.String)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid release content_document_id: %w", err)
	}
	return documentID, nil
}
