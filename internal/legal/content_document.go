package legal

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
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
	descriptor, err := legalKind(kind)
	if err != nil {
		return legalContentDocumentRoot{}, err
	}
	query := db.WithContext(ctx).Table(descriptor.table).
		Select("id", "title", "status", "version", "source_locale", "content_document_id").
		Where("id = ?", entityID)
	if forUpdate {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	} else {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthShare})
	}
	var root legalContentDocumentRoot
	if err := query.Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return legalContentDocumentRoot{}, domainerr.ResourceNotFound(kind, entityID)
		}
		return legalContentDocumentRoot{}, err
	}
	if root.ContentDocumentID == nil || *root.ContentDocumentID == uuid.Nil {
		return legalContentDocumentRoot{}, domainerr.Precondition(kind + " content document has not been populated")
	}
	if strings.TrimSpace(root.SourceLocale) == "" {
		return legalContentDocumentRoot{}, domainerr.Precondition(kind + " source locale is missing")
	}
	if _, err := canonicalLegalLocale(root.SourceLocale); err != nil {
		return legalContentDocumentRoot{}, domainerr.Precondition(kind + " source locale is not canonical").WithCause(failurecause.ServerFault(err))
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
) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		root, err := loadLegalContentDocumentRoot(ctx, tx, kind, entityID, true)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if root.ContentDocumentID == nil || *root.ContentDocumentID != documentID {
			return contentblock.DomainContext{}, domainerr.Precondition(kind + " content document changed; reload before saving")
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
			return err
		}
		if count != 1 {
			return domainerr.Precondition(kind + " target locale must be created before collaboration editing")
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

// legalTranslationJobApplyDocumentFence permits an already-accepted
// TranslationJob to finish against any still-existing policy root. Current
// administrator permissions continue to govern new requests and direct
// edits; completion reuses the authority accepted at request time.
func legalTranslationJobApplyDocumentFence(kind, entityID string) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		root, err := loadLegalContentDocumentRoot(ctx, tx, kind, entityID, true)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if root.ContentDocumentID == nil || *root.ContentDocumentID != documentID {
			return contentblock.DomainContext{}, domainerr.Precondition(kind + " content document changed; translation job cannot be applied")
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
		return domainerr.AuthenticationRequired()
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
		return domainerr.AuthenticationRequired().WithCause(failurecause.WithPrivate(err, principalMessage.GetSessionId()))
	}
	if err != nil {
		return fmt.Errorf("resolve %s collaboration principal: %w", kind, err)
	}
	if principal == nil || !principal.Authenticated {
		return domainerr.AuthenticationRequired()
	}
	if principal.Banned {
		return domainerr.AccountBanned()
	}
	if !principal.Onboarded {
		return domainerr.NoPermission("edit", kind)
	}
	descriptor, err := legalKind(kind)
	if err != nil {
		return err
	}
	return requireLegalPermissionForPrincipal(
		ctx, checker, legalCollaborationResourceType(kind), entityID,
		legalViewAction(descriptor, root.Status), principal,
	)
}

func legalDocumentOwnershipFence(kind string, entityID string) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		root, err := loadLegalContentDocumentRoot(ctx, tx, kind, entityID, true)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if root.ContentDocumentID == nil || *root.ContentDocumentID != documentID {
			return contentblock.DomainContext{}, domainerr.Precondition(kind + " content document changed; reload before mutation")
		}
		return loadLegalContentDomainContext(ctx, tx, kind, entityID)
	}
}

func parseLegalContentUUID(fieldName string, value string) (uuid.UUID, error) {
	normalized := strings.TrimSpace(value)
	parsed, err := uuid.Parse(normalized)
	if err != nil || parsed == uuid.Nil || parsed.String() != normalized {
		return uuid.Nil, domainerr.InvalidField(fieldName, "must be a canonical UUID")
	}
	return parsed, nil
}

func normalizeLegalContentBlockError(kind string, err error) error {
	if err == nil || mutation.IsClassified(err) {
		return err
	}
	switch {
	case errors.Is(err, contentblock.ErrDocumentNotFound):
		return domainerr.New(domainerr.NotFound, "", kind+" content document not found").WithCause(err)
	case errors.Is(err, contentblock.ErrStaleRevision):
		return domainerr.Precondition(kind + " content revision changed; reload before saving").WithCause(err)
	case errors.Is(err, contentblock.ErrCrossDocument):
		return domainerr.InvalidField("blocks", "a Block belongs to another document").WithCause(err)
	case errors.Is(err, contentblock.ErrFileReference):
		return domainerr.InvalidField("blocks", "File Blocks are not allowed in legal policy documents").WithCause(err)
	case errors.Is(err, contentblock.ErrInvalidMutation):
		return domainerr.InvalidField("blocks", err.Error())
	default:
		return fmt.Errorf("%s content document: %w", kind, err)
	}
}

func normalizeLegalCollaborationContentBlockError(kind string, err error) error {
	if err == nil || mutation.IsClassified(err) {
		return err
	}
	var targetConflict *translation.TargetRevisionConflict
	switch {
	case errors.As(err, &targetConflict):
		return domainerr.CollaborationConflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED,
			kind+" target locale changed since it was loaded; reload before saving", domainerr.CollaborationRevisionsOf(err),
		).WithCause(err)
	case errors.Is(err, contentblock.ErrStaleRevision):
		return domainerr.CollaborationConflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED,
			kind+" document changed since it was loaded; reload before saving", domainerr.CollaborationRevisionsOf(err),
		).WithCause(err)
	default:
		return normalizeLegalContentBlockError(kind, err)
	}
}

func verifyLegalExpectedRevisionWithDB(
	ctx context.Context,
	db *gorm.DB,
	kind string,
	entityID string,
	expectedRevision string,
) error {
	expected, err := parseLegalContentUUID("expected_revision", expectedRevision)
	if err != nil {
		return err
	}
	root, err := loadLegalContentDocumentRoot(ctx, db, kind, entityID, true)
	if err != nil {
		return err
	}
	revision, err := contentblock.LoadRevision(ctx, db, *root.ContentDocumentID)
	if err != nil {
		return normalizeLegalContentBlockError(kind, err)
	}
	if revision != expected {
		return normalizeLegalContentBlockError(kind, &contentblock.StaleRevisionError{CurrentRevision: revision})
	}
	return nil
}
