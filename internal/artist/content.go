package artist

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
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
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

func loadCreativeManageContentProjection(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	entityType string,
	entityID string,
	loadRoot ...func(context.Context, *gorm.DB) error,
) (creativeManageContentProjection, error) {
	var projection creativeManageContentProjection
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, load := range loadRoot {
			if load != nil {
				if err := load(ctx, tx); err != nil {
					return err
				}
			}
		}
		snapshot, source, err := loadCreativeContentSnapshotInTransaction(
			ctx, tx, store, entityType, entityID,
		)
		if err != nil {
			return err
		}
		document, err := contentblock.SnapshotToRichTextDocument(snapshot)
		if err != nil {
			return normalizeCreativeContentBlockError(entityType, err)
		}
		title, err := loadCreativeSourceTitle(ctx, tx, entityType, entityID, source.SourceLocale)
		if err != nil {
			return err
		}
		var sourceOgAssetID *string
		if entityType == artistContentEntity {
			var row struct {
				OgAssetID *string `gorm:"column:og_asset_id"`
			}
			if queryErr := tx.WithContext(ctx).Table("artist_translation").
				Select("og_asset_id").
				Where("entity_id = ? AND locale = ?", entityID, source.SourceLocale).
				Take(&row).Error; queryErr != nil {
				return queryErr
			}
			sourceOgAssetID = row.OgAssetID
		}
		projection = creativeManageContentProjection{
			Document:        document,
			Revision:        snapshot.Document.Revision.String(),
			SourceTitle:     title,
			SourceOgAssetID: sourceOgAssetID,
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	return projection, err
}

func loadCreativeSourceTitle(
	ctx context.Context,
	db *gorm.DB,
	entityType string,
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
			return "", errs.NotFound(table, entityID)
		}
		return "", errs.Internal(err)
	}
	return row.Title, nil
}

func saveCreativeSourceLocaleMetadata(
	ctx context.Context,
	db *gorm.DB,
	entityType string,
	entityID string,
	locale string,
	input translationLocaleDocumentSaveInput,
) error {
	if input.Title == nil || strings.TrimSpace(*input.Title) == "" {
		return errs.InvalidArgument("title", "must not be empty")
	}
	if strings.TrimSpace(input.SourceLocale) == "" {
		return errs.FailedPrecondition("Artist source locale is not initialized")
	}
	if input.SourceLocale != locale {
		return errs.FailedPrecondition("source locale metadata must match translation source authority")
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
	entityType string,
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
		return nil, errs.Internal(result.Error)
	}
	return &row, nil
}

type creativeLocaleTitleUpdateResult struct {
	Advance        contentblock.AdvanceResult
	ChangedLocales []string
}

func updateCreativeLocaleTitle(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	runtime Runtime,
	entityType string,
	entityID string,
	locale string,
	title *string,
	expectedRevision string,
	contributors []string,
	ogEntityType managev1.OgEntityType,
	translationRuntime Translation,
	checkpoints persistencecheckpoint.ContributorFence,
) (creativeLocaleTitleUpdateResult, error) {
	if store == nil {
		return creativeLocaleTitleUpdateResult{}, errs.Internal(errors.New("content Block store is not configured"))
	}
	if title == nil {
		return creativeLocaleTitleUpdateResult{}, errs.InvalidArgument("title", "is required")
	}
	normalizedTitle := strings.TrimSpace(*title)
	if normalizedTitle == "" {
		return creativeLocaleTitleUpdateResult{}, errs.InvalidArgument("title", "must not be empty")
	}
	parsedRevision, err := uuid.Parse(expectedRevision)
	if err != nil {
		return creativeLocaleTitleUpdateResult{}, errs.InvalidArgument("expected_revision", "must be a UUID")
	}
	documentID, err := loadCreativeContentDocumentID(ctx, db, entityType, entityID)
	if err != nil {
		return creativeLocaleTitleUpdateResult{}, err
	}
	var output creativeLocaleTitleUpdateResult
	now := time.Now().UTC()
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := translationRuntime.RequireDocumentContributors(ctx, tx, contributors); err != nil {
			return err
		}
		if err := requireArtistCollaborationContributors(ctx, tx, checkpoints, entityID, contributors); err != nil {
			return err
		}
		advanced, err := store.AdvanceRevision(
			ctx,
			tx,
			contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: parsedRevision},
			artistSourceRoomFence(entityID, locale),
			func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
				sourceAuthority, err := lockCreativeTranslationSourceContext(ctx, tx, entityType, entityID)
				if err != nil {
					return contentblock.MetadataEffect{}, err
				}
				if sourceAuthority.SourceLocale != locale {
					return contentblock.MetadataEffect{}, errs.FailedPrecondition("only source locale metadata is editable")
				}
				current, err := loadCreativeLocaleMetadataState(ctx, tx, entityType, entityID, locale)
				if err != nil {
					return contentblock.MetadataEffect{}, err
				}
				if current != nil && current.Title != nil && *current.Title == normalizedTitle {
					return contentblock.MetadataEffect{}, nil
				}
				input := translationLocaleDocumentSaveInput{
					SourceLocale: sourceAuthority.SourceLocale,
					Title:        &normalizedTitle,
					Now:          now,
				}
				err = saveArtistSourceLocaleDocumentState(ctx, tx, entityID, locale, input)
				if err != nil {
					return contentblock.MetadataEffect{}, err
				}
				err = touchArtistRootUpdatedAt(ctx, tx, entityID, now)
				if err != nil {
					return contentblock.MetadataEffect{}, err
				}
				return contentblock.MetadataEffect{
					Changed:                  true,
					AffectsTranslationSource: sourceAuthority.SourceLocale == locale,
				}, nil
			},
		)
		if err != nil {
			return normalizeCreativeContentBlockError(entityType, err)
		}
		output.Advance = advanced
		if advanced.Changed {
			output.ChangedLocales = []string{locale}
		}
		if advanced.TranslationSourceChanged {
			_, err = runtime.RequestCurrentWithDB(
				ctx, tx, ogEntityType, entityID, "", false,
				entityType+"_source_title_saved",
			)
			return err
		}
		return nil
	})
	if err != nil {
		return creativeLocaleTitleUpdateResult{}, err
	}
	return output, nil
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
		return creativeDocumentAuthority{}, errs.Internal(err)
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
		return creativeDocumentAuthority{}, errs.NotFound(entityType, entityID)
	}
	if err != nil {
		return creativeDocumentAuthority{}, errs.Internal(err)
	}
	if strings.TrimSpace(state.SourceLocale) == "" {
		return creativeDocumentAuthority{}, errs.FailedPrecondition(entityType + " source locale is not initialized")
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
	if store == nil {
		return contentblock.Snapshot{}, creativeDocumentAuthority{}, errs.Internal(errors.New("content Block store is not configured"))
	}
	documentID, err := loadCreativeContentDocumentIDWithStrength(
		ctx, tx, entityType, entityID, "SHARE",
	)
	if err != nil {
		return contentblock.Snapshot{}, creativeDocumentAuthority{}, err
	}
	source, err := loadCreativeDocumentAuthorityWithStrength(
		ctx, tx, entityType, entityID, "SHARE",
	)
	if err != nil {
		return contentblock.Snapshot{}, creativeDocumentAuthority{}, err
	}
	snapshot, err := store.LoadSnapshotInTransaction(ctx, tx, documentID, source.SourceLocale)
	if err != nil {
		return contentblock.Snapshot{}, creativeDocumentAuthority{}, normalizeCreativeContentBlockError(entityType, err)
	}
	if strings.TrimSpace(snapshot.SourceLocale) != source.SourceLocale {
		return contentblock.Snapshot{}, creativeDocumentAuthority{}, errs.Internal(errors.New("content document source locale does not match translation authority"))
	}
	return snapshot, source, nil
}

func initializeCreativeContentDocument(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	entityType string,
	entityID string,
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
		return contentblock.Result{}, normalizeCreativeContentBlockError(entityType, err)
	}
	result, err := store.ReplaceSnapshot(
		ctx,
		tx,
		replace,
		creativeContentCreationFence(entityType, entityID, sourceLocale, authorize),
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
	resourceType intrav1.CollaborationResourceType,
	principalMessage *intrav1.CollaborationPrincipal,
	loadMetadata func(context.Context, *gorm.DB) error,
) (creativeContentBootstrapSnapshot, error) {
	if principalMessage == nil {
		return creativeContentBootstrapSnapshot{}, errs.AuthenticationRequired()
	}
	if store == nil {
		return creativeContentBootstrapSnapshot{}, errs.Internal(errors.New("content Block store is not configured"))
	}
	var output creativeContentBootstrapSnapshot
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		documentID, err := loadCreativeContentDocumentIDWithStrength(
			ctx, tx, entityType, entityID, "SHARE",
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
			return errs.Internal(fmt.Errorf("resolve %s collaboration principal: %w", entityType, err))
		}
		if principal == nil || !principal.Authenticated {
			return errs.AuthenticationRequired()
		}
		if principal.Banned {
			return errs.AccountBanned()
		}
		if !principal.Onboarded {
			return errs.NoPermission("edit", entityType)
		}
		can, err := policyv1.Artist.Edit(entityID)
		if err != nil {
			return errs.InvalidArgument("artist_id", "must be a canonical Artist UUID")
		}
		decision, err := auth.AuthorizationDecision(auth.WithUser(ctx, principal), can)
		if err != nil {
			return errs.AuthenticationRequired()
		}
		allowed, err := spiceDB.Can(ctx, decision)
		if err != nil {
			return errs.DependencyUnavailable("SpiceDB")
		}
		if !allowed {
			return errs.NoPermission("edit", "artist")
		}
		source, err := loadCreativeDocumentAuthorityWithStrength(
			ctx, tx, entityType, entityID, "SHARE",
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
			return errs.Internal(errors.New("content document source locale does not match translation authority"))
		}
		if loadMetadata != nil {
			if err := loadMetadata(ctx, tx); err != nil {
				return err
			}
		}
		output = creativeContentBootstrapSnapshot{Snapshot: snapshot, Source: source}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
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

func internalCreativeContentFence(entityType string, entityID string) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if err := lockCreativeContentDocumentRoot(
			ctx,
			tx,
			entityType,
			entityID,
			documentID,
		); err != nil {
			return contentblock.DomainContext{}, err
		}
		domain, err := lockCreativeTranslationSourceContext(ctx, tx, entityType, entityID)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		return domain, nil
	}
}

func creativeContentDocumentFence(
	entityType string,
	entityID string,
	authorize func(context.Context, *gorm.DB) error,
) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if err := lockCreativeContentDocumentRoot(ctx, tx, entityType, entityID, documentID); err != nil {
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

func creativeContentCreationFence(
	entityType string,
	entityID string,
	sourceLocale string,
	authorize func(context.Context, *gorm.DB) error,
) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if err := lockCreativeContentDocumentRoot(ctx, tx, entityType, entityID, documentID); err != nil {
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
		return contentblock.DomainContext{}, errs.Internal(err)
	}
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Table(table).
		Select("source_locale").
		Where("id = ?", entityID).
		Take(&source).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return contentblock.DomainContext{}, errs.NotFound(entityType, entityID)
		}
		return contentblock.DomainContext{}, errs.Internal(err)
	}
	if strings.TrimSpace(source.SourceLocale) == "" {
		return contentblock.DomainContext{}, errs.FailedPrecondition(entityType + " source locale is not initialized")
	}
	return contentblock.DomainContext{SourceLocale: source.SourceLocale}, nil
}

func normalizeCreativeContentBlockError(entityType string, err error) error {
	if err == nil || connect.CodeOf(err) != connect.CodeUnknown {
		return err
	}
	var targetConflict *translation.TargetRevisionConflict
	switch {
	case errors.As(err, &targetConflict):
		return errs.CollaborationConflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED,
			entityType+" target locale changed since it was loaded; reload before saving",
		)
	case errors.Is(err, contentblock.ErrDocumentNotFound):
		return errs.NotFoundMsg(entityType + " content document not found")
	case errors.Is(err, contentblock.ErrStaleRevision):
		return errs.CollaborationConflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED,
			entityType+" content revision changed; reload before saving",
		)
	case errors.Is(err, contentblock.ErrCrossDocument):
		return errs.InvalidArgument("blocks", "a Block belongs to another document")
	case errors.Is(err, contentblock.ErrFileReference):
		return errs.InvalidArgument("blocks", "compact documents cannot contain File Blocks")
	case errors.Is(err, contentblock.ErrInvalidMutation):
		return errs.InvalidArgument("blocks", err.Error())
	default:
		return errs.Internal(fmt.Errorf("%s content document: %w", entityType, err))
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
	entityType string,
	entityID string,
) (uuid.UUID, error) {
	return loadCreativeContentDocumentIDWithLock(ctx, db, entityType, entityID, false)
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
		return errs.FailedPrecondition("content document ownership changed; reload before saving")
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
		strength = "UPDATE"
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
		return uuid.Nil, errs.Internal(errors.New("content document database is required"))
	}
	table, err := creativeContentRootTable(entityType)
	if err != nil {
		return uuid.Nil, errs.Internal(err)
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
			return uuid.Nil, errs.NotFound(entityType, entityID)
		}
		return uuid.Nil, errs.Internal(err)
	}
	if !root.ContentDocumentID.Valid || root.ContentDocumentID.String == "" {
		return uuid.Nil, errs.FailedPrecondition("content document is not initialized")
	}
	documentID, err := uuid.Parse(root.ContentDocumentID.String)
	if err != nil {
		return uuid.Nil, errs.Internal(fmt.Errorf("invalid %s content_document_id: %w", entityType, err))
	}
	return documentID, nil
}
