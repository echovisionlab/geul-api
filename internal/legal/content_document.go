package legal

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	"github.com/echovisionlab/geul-api/internal/translation"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

const legalContentDocumentProfile = "policy"

type legalContentDocumentRoot struct {
	ID                string     `gorm:"column:id"`
	Title             string     `gorm:"column:title"`
	Status            string     `gorm:"column:status"`
	Version           int        `gorm:"column:version"`
	SourceLocale      string     `gorm:"column:source_locale"`
	ContentDocumentID *uuid.UUID `gorm:"column:content_document_id;type:uuid"`
}

func loadLegalContentDocumentRoot(
	ctx context.Context,
	db *gorm.DB,
	kind string,
	entityID string,
	forUpdate bool,
) (legalContentDocumentRoot, error) {
	policy, err := legalDocumentPolicyForType(kind)
	if err != nil {
		return legalContentDocumentRoot{}, err
	}
	query := db.WithContext(ctx).Table(policy.table).
		Select("id", "title", "status", "version", "source_locale", "content_document_id").
		Where("id = ?", entityID)
	if forUpdate {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	} else {
		query = query.Clauses(clause.Locking{Strength: "SHARE"})
	}
	var root legalContentDocumentRoot
	if err := query.Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return legalContentDocumentRoot{}, errs.NotFound(kind, entityID)
		}
		return legalContentDocumentRoot{}, errs.Internal(err)
	}
	if root.ContentDocumentID == nil || *root.ContentDocumentID == uuid.Nil {
		return legalContentDocumentRoot{}, errs.FailedPrecondition(kind + " content document has not been populated")
	}
	if strings.TrimSpace(root.SourceLocale) == "" {
		return legalContentDocumentRoot{}, errs.FailedPrecondition(kind + " source locale is missing")
	}
	if _, err := canonicalLegalLocale(root.SourceLocale); err != nil {
		return legalContentDocumentRoot{}, errs.FailedPrecondition(kind + " source locale is not canonical")
	}
	return root, nil
}

func loadLegalContentDocumentID(
	ctx context.Context,
	db *gorm.DB,
	kind string,
	entityID string,
) (uuid.UUID, error) {
	root, err := loadLegalContentDocumentRoot(ctx, db, kind, entityID, false)
	if err != nil {
		return uuid.Nil, err
	}
	return *root.ContentDocumentID, nil
}

func loadLegalContentDomainContext(
	ctx context.Context,
	db *gorm.DB,
	kind string,
	entityID string,
) (contentblock.DomainContext, error) {
	root, err := loadLegalContentDocumentRoot(ctx, db, kind, entityID, false)
	if err != nil {
		return contentblock.DomainContext{}, err
	}
	return contentblock.DomainContext{SourceLocale: root.SourceLocale}, nil
}

func legalCollaborationDocumentFence(
	checkpoints persistencecheckpoint.ContributorFence,
	kind string,
	entityID string,
	contributors []string,
	batch *contentblock.Batch,
	sourceMetadata bool,
) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		root, err := loadLegalContentDocumentRoot(ctx, tx, kind, entityID, true)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		policy, err := legalDocumentPolicyForType(kind)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if root.ContentDocumentID == nil || *root.ContentDocumentID != documentID {
			return contentblock.DomainContext{}, errs.FailedPrecondition(kind + " content document changed; reload before saving")
		}
		if (sourceMetadata || legalBatchTouchesSource(batch, root.SourceLocale)) &&
			root.Status != policy.draftStatus && root.Status != policy.archivedStatus {
			return contentblock.DomainContext{}, errs.FailedPrecondition("scheduled or active legal source documents are read-only")
		}
		if err := checkpoints.RequireCurrentContributors(
			ctx,
			tx,
			legalCollaborationResourceType(kind),
			entityID,
			contributors,
		); err != nil {
			return contentblock.DomainContext{}, err
		}
		if err := requireLegalTargetTranslationRows(ctx, tx, kind, entityID, root.SourceLocale, batch); err != nil {
			return contentblock.DomainContext{}, err
		}
		return contentblock.DomainContext{SourceLocale: root.SourceLocale}, nil
	}
}

func requireLegalTargetTranslationRows(
	ctx context.Context,
	tx *gorm.DB,
	kind string,
	entityID string,
	sourceLocale string,
	batch *contentblock.Batch,
) error {
	for _, locale := range legalTargetLocales(sourceLocale, batch) {
		var count int64
		if err := tx.WithContext(ctx).Table(kind+"_translation").
			Where("entity_id = ? AND locale = ?", entityID, locale).
			Count(&count).Error; err != nil {
			return errs.Internal(err)
		}
		if count != 1 {
			return errs.FailedPrecondition(kind + " target locale must be created before collaboration editing")
		}
	}
	return nil
}

func legalTargetLocales(sourceLocale string, batch *contentblock.Batch) []string {
	if batch == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(batch.LocaleGroups))
	locales := make([]string, 0, len(batch.LocaleGroups))
	for _, group := range batch.LocaleGroups {
		locale := strings.TrimSpace(group.Locale)
		if locale == "" || locale == sourceLocale {
			continue
		}
		if _, exists := seen[locale]; exists {
			continue
		}
		seen[locale] = struct{}{}
		locales = append(locales, locale)
	}
	sort.Strings(locales)
	return locales
}

func legalBatchTouchesSource(batch *contentblock.Batch, sourceLocale string) bool {
	if batch == nil {
		return false
	}
	if len(batch.Upserts) != 0 || len(batch.Deletes) != 0 || len(batch.Reorders) != 0 {
		return true
	}
	for _, group := range batch.LocaleGroups {
		if group.Locale == sourceLocale {
			return true
		}
	}
	return false
}

// legalTranslationJobApplyDocumentFence permits an already-accepted
// TranslationJob to finish against any still-existing policy root. Current
// lifecycle and administrator rules continue to govern new requests and direct
// edits; completion reuses the authority accepted at request time.
func legalTranslationJobApplyDocumentFence(kind, entityID string) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		root, err := loadLegalContentDocumentRoot(ctx, tx, kind, entityID, true)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if root.ContentDocumentID == nil || *root.ContentDocumentID != documentID {
			return contentblock.DomainContext{}, errs.FailedPrecondition(kind + " content document changed; translation job cannot be applied")
		}
		return loadLegalContentDomainContext(ctx, tx, kind, entityID)
	}
}

func authorizeLegalBlockBootstrap(
	ctx context.Context,
	db *gorm.DB,
	checker CollaborationPermissionChecker,
	kind string,
	entityID string,
	principalMessage *intrav1.CollaborationPrincipal,
) error {
	if principalMessage == nil || strings.TrimSpace(principalMessage.GetSessionId()) == "" {
		return errs.AuthenticationRequired()
	}
	root, err := loadLegalContentDocumentRoot(ctx, db, kind, entityID, true)
	if err != nil {
		return err
	}
	principal, err := auth.ResolveAuthenticatedPrincipalBySessionID(
		ctx,
		db,
		principalMessage.GetSessionId(),
	)
	if errors.Is(err, auth.ErrSessionPrincipalInvalid) {
		return errs.AuthenticationRequired()
	}
	if err != nil {
		return errs.Internal(fmt.Errorf("resolve %s collaboration principal: %w", kind, err))
	}
	if principal == nil || !principal.Authenticated {
		return errs.AuthenticationRequired()
	}
	if principal.Banned {
		return errs.AccountBanned()
	}
	if !principal.Onboarded {
		return errs.NoPermission("edit", kind)
	}
	policy, err := legalDocumentPolicyForType(kind)
	if err != nil {
		return err
	}
	return requireLegalPermissionForPrincipal(
		ctx, checker, legalCollaborationResourceType(kind), entityID,
		legalViewAction(policy, root.Status), principal,
	)
}

func legalDocumentOwnershipFence(kind string, entityID string) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		root, err := loadLegalContentDocumentRoot(ctx, tx, kind, entityID, true)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if root.ContentDocumentID == nil || *root.ContentDocumentID != documentID {
			return contentblock.DomainContext{}, errs.FailedPrecondition(kind + " content document changed; reload before mutation")
		}
		return loadLegalContentDomainContext(ctx, tx, kind, entityID)
	}
}

func parseLegalContentUUID(fieldName string, value string) (uuid.UUID, error) {
	normalized := strings.TrimSpace(value)
	parsed, err := uuid.Parse(normalized)
	if err != nil || parsed == uuid.Nil || parsed.String() != normalized {
		return uuid.Nil, errs.InvalidArgument(fieldName, "must be a canonical UUID")
	}
	return parsed, nil
}

func normalizeLegalContentBlockError(kind string, err error) error {
	if err == nil || connect.CodeOf(err) != connect.CodeUnknown {
		return err
	}
	switch {
	case errors.Is(err, contentblock.ErrDocumentNotFound):
		return errs.NotFoundMsg(kind + " content document not found")
	case errors.Is(err, contentblock.ErrStaleRevision):
		return errs.FailedPrecondition(kind + " content revision changed; reload before saving")
	case errors.Is(err, contentblock.ErrCrossDocument):
		return errs.InvalidArgument("blocks", "a Block belongs to another document")
	case errors.Is(err, contentblock.ErrFileReference):
		return errs.InvalidArgument("blocks", "File Blocks are not allowed in legal policy documents")
	case errors.Is(err, contentblock.ErrInvalidMutation):
		return errs.InvalidArgument("blocks", err.Error())
	default:
		return errs.Internal(fmt.Errorf("%s content document: %w", kind, err))
	}
}

func normalizeLegalCollaborationContentBlockError(kind string, err error) error {
	if err == nil || connect.CodeOf(err) != connect.CodeUnknown {
		return err
	}
	var targetConflict *translation.TargetRevisionConflict
	switch {
	case errors.As(err, &targetConflict):
		return errs.CollaborationConflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED,
			kind+" target locale changed since it was loaded; reload before saving",
		)
	case errors.Is(err, contentblock.ErrStaleRevision):
		return errs.CollaborationConflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED,
			kind+" document changed since it was loaded; reload before saving",
		)
	default:
		return normalizeLegalContentBlockError(kind, err)
	}
}

func verifyLegalExpectedRevisionWithDB(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	kind string,
	entityID string,
	expectedRevision string,
) error {
	if store == nil {
		return errs.InternalMsg(kind + " content Block store is not configured")
	}
	expected, err := parseLegalContentUUID("expected_revision", expectedRevision)
	if err != nil {
		return err
	}
	root, err := loadLegalContentDocumentRoot(ctx, db, kind, entityID, true)
	if err != nil {
		return err
	}
	revision, err := loadLegalDocumentRevisionWithDB(ctx, db, *root.ContentDocumentID)
	if err != nil {
		return normalizeLegalContentBlockError(kind, err)
	}
	if revision != expected {
		return normalizeLegalContentBlockError(kind, contentblock.ErrStaleRevision)
	}
	return nil
}

func loadLegalDocumentRevisionWithDB(ctx context.Context, db *gorm.DB, documentID uuid.UUID) (uuid.UUID, error) {
	var row struct {
		Revision uuid.UUID `gorm:"column:revision"`
	}
	if err := db.WithContext(ctx).
		Table("content_document").
		Select("revision").
		Clauses(clause.Locking{Strength: "SHARE"}).
		Where("id = ?", documentID).
		Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return uuid.Nil, contentblock.ErrDocumentNotFound
		}
		return uuid.Nil, err
	}
	return row.Revision, nil
}

func regenerateLegalDerivedContent(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	spiceDB *auth.SpiceDBClient,
	legalOG OG,
	kind string,
	entityID string,
	expectedCanonicalHash string,
) (string, error) {
	if store == nil {
		return "", errs.InternalMsg(kind + " content Block store is not configured")
	}
	var canonicalHash string
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		root, err := loadLegalContentDocumentRoot(ctx, tx, kind, entityID, true)
		if err != nil {
			return err
		}
		policy, err := legalDocumentPolicyForType(kind)
		if err != nil {
			return err
		}
		if err := requireActiveLegalPrincipal(ctx, tx, "manage", false); err != nil {
			return err
		}
		if err := requireLegalPermission(
			ctx, spiceDB, kind, entityID,
			legalMutationAction(policy, root.Status, legalActionManage),
		); err != nil {
			return err
		}
		snapshot, err := store.LoadSnapshotInTransaction(ctx, tx, *root.ContentDocumentID, root.SourceLocale)
		if err != nil {
			return normalizeLegalContentBlockError(kind, err)
		}
		canonicalHash = snapshot.SnapshotDigest
		if strings.TrimSpace(expectedCanonicalHash) == "" || expectedCanonicalHash != canonicalHash {
			return errs.FailedPrecondition("legal derived-content regeneration targets a stale Block document")
		}
		if err := (internalLegalDocumentService{db: tx, kind: kind, contentBlocks: store, legalOG: legalOG}).
			refreshDerivedContentProjectionsWithDB(
				ctx, tx, entityID, snapshot, root.SourceLocale, time.Now().UTC(),
			); err != nil {
			return err
		}
		err = legalOG.RequestSaved(
			ctx,
			tx,
			kind,
			entityID,
			"",
			true,
			kind+"_derived_content_regenerated",
		)
		return err
	})
	return canonicalHash, err
}
