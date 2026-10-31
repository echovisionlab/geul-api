package artist

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
func loadCreativeManageContentProjection(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	entityID string,
	loadRoot func(context.Context, *gorm.DB) error,
) (creativeManageContentProjection, error) {
	var projection creativeManageContentProjection
	err := mutation.ReadLocked(ctx, db, func(tx *gorm.DB) error {
		if err := loadRoot(ctx, tx); err != nil {
			return err
		}
		snapshot, source, err := loadCreativeContentSnapshotInTransaction(
			ctx, tx, store, artistContentEntity, entityID,
		)
		if err != nil {
			return err
		}
		document, err := contentblock.SnapshotToRichTextDocument(snapshot)
		if err != nil {
			return normalizeCreativeContentBlockError(artistContentEntity, err)
		}
		title, err := loadCreativeSourceTitle(ctx, tx, entityID, source.SourceLocale)
		if err != nil {
			return err
		}
		var row struct {
			OgAssetID *string `gorm:"column:og_asset_id"`
		}
		if err := tx.WithContext(ctx).Table("artist_translation").
			Select("og_asset_id").
			Where("entity_id = ? AND locale = ?", entityID, source.SourceLocale).
			Take(&row).Error; err != nil {
			return err
		}
		projection = creativeManageContentProjection{
			Document:        document,
			Revision:        snapshot.Document.Revision.String(),
			SourceTitle:     title,
			SourceOgAssetID: row.OgAssetID,
		}
		return nil
	})
	return projection, err
}

func loadCreativeSourceTitle(
	ctx context.Context,
	db *gorm.DB,
	entityID string,
	locale string,
) (string, error) {
	table := "artist_translation"
	var row struct {
		Title string `gorm:"column:title"`
	}
	if err := db.WithContext(ctx).
		Table(table).
		Select("title").
		Where("entity_id = ? AND locale = ?", entityID, locale).
		Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", domainerr.ResourceNotFound(table, entityID)
		}
		return "", err
	}
	return row.Title, nil
}

func saveCreativeSourceLocaleMetadata(
	ctx context.Context,
	db *gorm.DB,
	entityID string,
	locale string,
	input translationLocaleDocumentSaveInput,
) error {
	if input.Title == nil || strings.TrimSpace(*input.Title) == "" {
		return domainerr.InvalidField("title", "must not be empty")
	}
	if strings.TrimSpace(input.SourceLocale) == "" {
		return domainerr.Precondition("Artist source locale is not initialized")
	}
	if input.SourceLocale != locale {
		return domainerr.Precondition("source locale metadata must match translation source authority")
	}
	table := "artist_translation"
	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return db.WithContext(ctx).Exec(
		fmt.Sprintf(
			`INSERT INTO %s (
			 entity_id, locale, title, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (entity_id, locale) DO UPDATE SET
			 title = EXCLUDED.title,
			 updated_at = EXCLUDED.updated_at`,
			table,
		),
		entityID, locale, strings.TrimSpace(*input.Title),
		now, now,
	).Error
}

type creativeLocaleMetadataState struct {
	Title *string `gorm:"column:title"`
}

func loadCreativeLocaleMetadataState(
	ctx context.Context,
	db *gorm.DB,
	entityID string,
	locale string,
) (*creativeLocaleMetadataState, error) {
	table := "artist_translation"
	var row creativeLocaleMetadataState
	result := db.WithContext(ctx).
		Table(table).
		Select("title").
		Where("entity_id = ? AND locale = ?", entityID, locale).
		Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if result.Error != nil {
		return nil, result.Error
	}
	return &row, nil
}

func requireArtistCollaborationContributors(
	ctx context.Context,
	tx *gorm.DB,
	checkpoints persistencecheckpoint.ContributorFence,
	artistID string,
	contributors []string,
) error {
	return checkpoints.RequireCurrentContributors(
		ctx,
		tx,
		intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_ARTIST,
		artistID,
		contributors,
	)
}

type creativeDocumentAuthority struct {
	SourceLocale string `gorm:"column:source_locale"`
}

func loadCreativeDocumentAuthority(
	ctx context.Context,
	db *gorm.DB,
	entityType string,
	entityID string,
) (creativeDocumentAuthority, error) {
	return loadCreativeDocumentAuthorityWithStrength(
		ctx, db, entityType, entityID, "",
	)
}

func loadCreativeDocumentAuthorityWithStrength(
	ctx context.Context,
	db *gorm.DB,
	entityType string,
	entityID string,
	strength string,
) (creativeDocumentAuthority, error) {
	var state creativeDocumentAuthority
	table, err := creativeContentRootTable(entityType)
	if err != nil {
		return creativeDocumentAuthority{}, err
	}
	query := db.WithContext(ctx).
		Table(table).
		Select("source_locale").
		Where("id = ?", entityID)
	if strength != "" {
		query = query.Clauses(clause.Locking{Strength: strength})
	}
	err = query.Take(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return creativeDocumentAuthority{}, domainerr.ResourceNotFound(entityType, entityID)
	}
	if err != nil {
		return creativeDocumentAuthority{}, err
	}
	if strings.TrimSpace(state.SourceLocale) == "" {
		return creativeDocumentAuthority{}, domainerr.Precondition(entityType + " source locale is not initialized")
	}
	return state, nil
}

func loadCreativeContentSnapshotInTransaction(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	entityType string,
	entityID string,
) (contentblock.Snapshot, creativeDocumentAuthority, error) {
	documentID, err := loadCreativeContentDocumentIDWithStrength(
		ctx, tx, entityType, entityID, clause.LockingStrengthShare,
	)
	if err != nil {
		return contentblock.Snapshot{}, creativeDocumentAuthority{}, err
	}
	source, err := loadCreativeDocumentAuthorityWithStrength(
		ctx, tx, entityType, entityID, clause.LockingStrengthShare,
	)
	if err != nil {
		return contentblock.Snapshot{}, creativeDocumentAuthority{}, err
	}
	snapshot, err := store.LoadSnapshotInTransaction(ctx, tx, documentID, source.SourceLocale)
	if err != nil {
		return contentblock.Snapshot{}, creativeDocumentAuthority{}, normalizeCreativeContentBlockError(entityType, err)
	}
	if strings.TrimSpace(snapshot.SourceLocale) != source.SourceLocale {
		return contentblock.Snapshot{}, creativeDocumentAuthority{}, errors.New("content document source locale does not match translation authority")
	}
	return snapshot, source, nil
}

func initializeCreativeContentDocument(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	entityID string,
	sourceLocale string,
	created contentblock.Snapshot,
	input *contentv1.RichTextDocument,
) (contentblock.Result, error) {
	entityType := artistContentEntity
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
		return contentblock.Result{}, normalizeCreativeContentBlockError(entityType, err)
	}
	result, err := store.ReplaceSnapshot(
		ctx,
		tx,
		replace,
		creativeContentCreationFence(entityType, entityID, sourceLocale),
	)
	if err != nil {
		return contentblock.Result{}, normalizeCreativeContentBlockError(entityType, err)
	}
	return result, nil
}

type creativeContentBootstrapSnapshot struct {
	Snapshot contentblock.Snapshot
	Source   creativeDocumentAuthority
}

func loadCreativeContentBlockBootstrap(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	spiceDB *auth.SpiceDBClient,
	entityType string,
	entityID string,
	sessionID string,
	loadMetadata func(context.Context, *gorm.DB) error,
) (creativeContentBootstrapSnapshot, error) {
	if sessionID == "" {
		return creativeContentBootstrapSnapshot{}, errAuthenticationRequired
	}
	var output creativeContentBootstrapSnapshot
	err := mutation.ReadLocked(ctx, db, func(tx *gorm.DB) error {
		documentID, err := loadCreativeContentDocumentIDWithStrength(
			ctx, tx, entityType, entityID, clause.LockingStrengthShare,
		)
		if err != nil {
			return err
		}
		principal, err := auth.ResolveAuthenticatedPrincipalBySessionID(ctx, tx, sessionID)
		if errors.Is(err, auth.ErrSessionPrincipalInvalid) {
			return errAuthenticationRequired.WithCause(err)
		}
		if err != nil {
			return fmt.Errorf("resolve %s collaboration principal: %w", entityType, err)
		}
		if principal == nil || !principal.Authenticated {
			return errAuthenticationRequired
		}
		if principal.Banned {
			return domainerr.Denied("account is banned")
		}
		if !principal.Onboarded {
			return domainerr.Denied(fmt.Sprintf("no permission to %s this %s", "edit", entityType))
		}
		can, err := policyv1.Artist.Edit(entityID)
		if err != nil {
			return domainerr.InvalidField("artist_id", "must be a canonical Artist UUID")
		}
		decision, err := auth.AuthorizationDecision(auth.WithUser(ctx, principal), can)
		if err != nil {
			return errAuthenticationRequired.WithCause(err)
		}
		allowed, err := spiceDB.Can(ctx, decision)
		if err != nil {
			return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
		}
		if !allowed {
			return domainerr.Denied("no permission to edit this artist")
		}
		source, err := loadCreativeDocumentAuthorityWithStrength(
			ctx, tx, entityType, entityID, clause.LockingStrengthShare,
		)
		if err != nil {
			return err
		}
		snapshot, err := store.LoadSnapshotInTransaction(
			ctx, tx, documentID, source.SourceLocale,
		)
		if err != nil {
			return normalizeCreativeContentBlockError(entityType, err)
		}
		if snapshot.SourceLocale != source.SourceLocale {
			return errors.New("content document source locale does not match translation authority")
		}
		if err := loadMetadata(ctx, tx); err != nil {
			return err
		}
		output = creativeContentBootstrapSnapshot{Snapshot: snapshot, Source: source}
		return nil
	})
	return output, err
}

const (
	artistContentEntity    = "artist"
	creativeContentProfile = "compact"
)

type creativeContentDocumentRoot struct {
	ID                string         `gorm:"column:id"`
	ContentDocumentID sql.NullString `gorm:"column:content_document_id"`
}

func internalCreativeContentFence(entityID string) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if err := lockCreativeContentDocumentRoot(
			ctx,
			tx,
			artistContentEntity,
			entityID,
			documentID,
		); err != nil {
			return contentblock.DomainContext{}, err
		}
		domain, err := lockCreativeTranslationSourceContext(ctx, tx, artistContentEntity, entityID)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		return domain, nil
	}
}

// creativeContentDocumentFence locks the Artist row of the document the
// mutation, already authorized, removes.
func creativeContentDocumentFence(entityType string, entityID string) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		return contentblock.DomainContext{}, lockCreativeContentDocumentRoot(ctx, tx, entityType, entityID, documentID)
	}
}

// creativeContentCreationFence locks the new Artist row and names its source
// locale while the creation, already authorized, fills its document.
func creativeContentCreationFence(entityType string, entityID string, sourceLocale string) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if err := lockCreativeContentDocumentRoot(ctx, tx, entityType, entityID, documentID); err != nil {
			return contentblock.DomainContext{}, err
		}
		return contentblock.DomainContext{SourceLocale: sourceLocale}, nil
	}
}

func lockCreativeTranslationSourceContext(
	ctx context.Context,
	tx *gorm.DB,
	entityType string,
	entityID string,
) (contentblock.DomainContext, error) {
	var source struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	table, err := creativeContentRootTable(entityType)
	if err != nil {
		return contentblock.DomainContext{}, err
	}
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Table(table).
		Select("source_locale").
		Where("id = ?", entityID).
		Take(&source).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return contentblock.DomainContext{}, domainerr.ResourceNotFound(entityType, entityID)
		}
		return contentblock.DomainContext{}, err
	}
	if strings.TrimSpace(source.SourceLocale) == "" {
		return contentblock.DomainContext{}, domainerr.Precondition(entityType + " source locale is not initialized")
	}
	return contentblock.DomainContext{SourceLocale: source.SourceLocale}, nil
}

// normalizeCreativeContentBlockError gives a raw content document failure
// its client answer; any other error keeps its own.
func normalizeCreativeContentBlockError(entityType string, err error) error {
	if err == nil {
		return nil
	}
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

func creativeContentRootTable(entityType string) (string, error) {
	switch entityType {
	case artistContentEntity:
		return entityType, nil
	default:
		return "", fmt.Errorf("unsupported creative content entity type %q", entityType)
	}
}

func loadCreativeContentDocumentID(
	ctx context.Context,
	db *gorm.DB,
	entityID string,
) (uuid.UUID, error) {
	return loadCreativeContentDocumentIDWithLock(ctx, db, artistContentEntity, entityID, false)
}

func lockCreativeContentDocumentRoot(
	ctx context.Context,
	tx *gorm.DB,
	entityType string,
	entityID string,
	expectedDocumentID uuid.UUID,
) error {
	documentID, err := loadCreativeContentDocumentIDWithLock(
		ctx,
		tx,
		entityType,
		entityID,
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

func loadCreativeContentDocumentIDWithLock(
	ctx context.Context,
	db *gorm.DB,
	entityType string,
	entityID string,
	lock bool,
) (uuid.UUID, error) {
	strength := ""
	if lock {
		strength = clause.LockingStrengthUpdate
	}
	return loadCreativeContentDocumentIDWithStrength(
		ctx, db, entityType, entityID, strength,
	)
}

func loadCreativeContentDocumentIDWithStrength(
	ctx context.Context,
	db *gorm.DB,
	entityType string,
	entityID string,
	strength string,
) (uuid.UUID, error) {
	if db == nil {
		return uuid.Nil, errors.New("content document database is required")
	}
	table, err := creativeContentRootTable(entityType)
	if err != nil {
		return uuid.Nil, err
	}
	query := db.WithContext(ctx).
		Table(table).
		Select("id", "content_document_id").
		Where("id = ?", entityID)
	if strength != "" {
		query = query.Clauses(clause.Locking{Strength: strength})
	}
	var root creativeContentDocumentRoot
	if err := query.Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return uuid.Nil, domainerr.ResourceNotFound(entityType, entityID)
		}
		return uuid.Nil, err
	}
	if !root.ContentDocumentID.Valid || root.ContentDocumentID.String == "" {
		return uuid.Nil, domainerr.Precondition("content document is not initialized")
	}
	documentID, err := uuid.Parse(root.ContentDocumentID.String)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid %s content_document_id: %w", entityType, err)
	}
	return documentID, nil
}
