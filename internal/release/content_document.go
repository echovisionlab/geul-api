package release

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	return projection, err
}

func loadReleaseSourceTitle(
	ctx context.Context,
	db *gorm.DB,
	releaseID string,
	locale string,
) (string, error) {
	table := "release_translation"
	var row struct {
		Title string `gorm:"column:title"`
	}
	if err := db.WithContext(ctx).
		Table(table).
		Select("title").
		Where("entity_id = ? AND locale = ?", releaseID, locale).
		Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", errs.NotFound(table, releaseID)
		}
		return "", errs.Internal(err)
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
		return errs.InvalidArgument("title", "must not be empty")
	}
	source, err := loadReleaseSourceLocale(ctx, db, releaseID)
	if err != nil {
		return err
	}
	if source.SourceLocale != locale {
		return errs.FailedPrecondition("source locale metadata must match translation source authority")
	}
	table := "release_translation"
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
		return nil, errs.Internal(result.Error)
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
	err := query.Take(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return releaseSourceLocale{}, errs.NotFound(releaseContentEntity, releaseID)
	}
	if err != nil {
		return releaseSourceLocale{}, errs.Internal(err)
	}
	return state, nil
}

func loadReleaseContentSnapshotInTransaction(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	releaseID string,
) (contentblock.Snapshot, releaseSourceLocale, error) {
	if store == nil {
		return contentblock.Snapshot{}, releaseSourceLocale{}, errs.Internal(errors.New("content Block store is not configured"))
	}
	documentID, err := loadReleaseContentDocumentIDWithStrength(
		ctx, tx, releaseID, "SHARE",
	)
	if err != nil {
		return contentblock.Snapshot{}, releaseSourceLocale{}, err
	}
	source, err := loadReleaseSourceLocaleWithStrength(
		ctx, tx, releaseID, "SHARE",
	)
	if err != nil {
		return contentblock.Snapshot{}, releaseSourceLocale{}, err
	}
	snapshot, err := store.LoadSnapshotInTransaction(ctx, tx, documentID, source.SourceLocale)
	if err != nil {
		return contentblock.Snapshot{}, releaseSourceLocale{}, normalizeReleaseContentBlockError(err)
	}
	if strings.TrimSpace(snapshot.SourceLocale) != source.SourceLocale {
		return contentblock.Snapshot{}, releaseSourceLocale{}, errs.Internal(errors.New("content document source locale does not match release root"))
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
		return contentblock.Result{}, errs.InvalidArgument("document.profile", "must be compact")
	}
	if input.GetSourceLocale() != sourceLocale {
		return contentblock.Result{}, errs.InvalidArgument("document.source_locale", "must match the server-selected source locale")
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

func loadReleaseContentBlockBootstrap(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	spiceDB CollaborationPermissionChecker,
	releaseID string,
	principalMessage *intrav1.CollaborationPrincipal,
	loadMetadata func(context.Context, *gorm.DB) error,
) (releaseContentBootstrapSnapshot, error) {
	if principalMessage == nil {
		return releaseContentBootstrapSnapshot{}, errs.AuthenticationRequired()
	}
	if store == nil {
		return releaseContentBootstrapSnapshot{}, errs.Internal(errors.New("content Block store is not configured"))
	}
	var output releaseContentBootstrapSnapshot
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		documentID, err := loadReleaseContentDocumentIDWithStrength(
			ctx, tx, releaseID, "SHARE",
		)
		if err != nil {
			return err
		}
		principal, err := auth.ResolveAuthenticatedPrincipalBySessionID(
			ctx, tx, principalMessage.GetSessionId(),
		)
		if errors.Is(err, auth.ErrSessionPrincipalInvalid) {
			return errs.AuthenticationRequired()
		}
		if err != nil {
			return errs.Internal(fmt.Errorf("resolve Release collaboration principal: %w", err))
		}
		if principal == nil || !principal.Authenticated {
			return errs.AuthenticationRequired()
		}
		if principal.Banned {
			return errs.AccountBanned()
		}
		if !principal.Onboarded {
			return errs.NoPermission("edit", releaseContentEntity)
		}
		if err := RequireCollaborationEdit(
			ctx, spiceDB, intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_RELEASE, releaseID, principal,
		); err != nil {
			return err
		}
		source, err := loadReleaseSourceLocaleWithStrength(
			ctx, tx, releaseID, "SHARE",
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
			return errs.Internal(errors.New("content document source locale does not match translation authority"))
		}
		if loadMetadata != nil {
			if err := loadMetadata(ctx, tx); err != nil {
				return err
			}
		}
		output = releaseContentBootstrapSnapshot{Snapshot: snapshot, Source: source}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	return output, err
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
			return contentblock.DomainContext{}, errs.Internal(errors.New("content document authorization is required"))
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
			return contentblock.DomainContext{}, errs.Internal(errors.New("content document authorization is required"))
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
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Table("release").
		Select("source_locale").
		Where("id = ?", releaseID).
		Take(&source).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return contentblock.DomainContext{}, errs.NotFound(releaseContentEntity, releaseID)
		}
		return contentblock.DomainContext{}, errs.Internal(err)
	}
	return contentblock.DomainContext{SourceLocale: source.SourceLocale}, nil
}

func normalizeReleaseContentBlockError(err error) error {
	if err == nil || connect.CodeOf(err) != connect.CodeUnknown {
		return err
	}
	switch {
	case errors.Is(err, contentblock.ErrDocumentNotFound):
		return errs.NotFoundMsg("release content document not found")
	case errors.Is(err, contentblock.ErrStaleRevision):
		return errs.FailedPrecondition("release content revision changed; reload before saving")
	case errors.Is(err, contentblock.ErrCrossDocument):
		return errs.InvalidArgument("blocks", "a Block belongs to another document")
	case errors.Is(err, contentblock.ErrFileReference):
		return errs.InvalidArgument("blocks", "compact documents cannot contain File Blocks")
	case errors.Is(err, contentblock.ErrInvalidMutation):
		return errs.InvalidArgument("blocks", err.Error())
	default:
		return errs.Internal(fmt.Errorf("release content document: %w", err))
	}
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
		return errs.FailedPrecondition("content document ownership changed; reload before saving")
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
		strength = "UPDATE"
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
	if db == nil {
		return uuid.Nil, errs.Internal(errors.New("content document database is required"))
	}
	query := db.WithContext(ctx).
		Table("release").
		Select("id", "content_document_id").
		Where("id = ?", releaseID)
	if strength != "" {
		query = query.Clauses(clause.Locking{Strength: strength})
	}
	var root releaseContentDocumentRoot
	if err := query.Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return uuid.Nil, errs.NotFound(releaseContentEntity, releaseID)
		}
		return uuid.Nil, errs.Internal(err)
	}
	if !root.ContentDocumentID.Valid || root.ContentDocumentID.String == "" {
		return uuid.Nil, errs.FailedPrecondition("content document is not initialized")
	}
	documentID, err := uuid.Parse(root.ContentDocumentID.String)
	if err != nil {
		return uuid.Nil, errs.Internal(fmt.Errorf("invalid release content_document_id: %w", err))
	}
	return documentID, nil
}
