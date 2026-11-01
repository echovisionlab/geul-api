package legal

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/translation"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

const legalContentDocumentProfile = "policy"

// lockFor is the lock a root read takes: for update, else shared.
func lockFor(forUpdate bool) RowLock {
	if forUpdate {
		return UpdateLock
	}
	return ShareLock
}

func loadLegalContentDocumentRoot(
	ctx context.Context,
	store Store,
	kind *policyKind,
	entityID string,
	forUpdate bool,
) (Root, error) {
	root, err := store.Root(ctx, kind.kind, entityID, lockFor(forUpdate))
	if err != nil {
		if errors.Is(err, ErrPolicyNotFound) {
			return Root{}, domainerr.ResourceNotFound(kind.entity(), entityID)
		}
		return Root{}, err
	}
	if root.ContentDocumentID == nil || *root.ContentDocumentID == uuid.Nil {
		return Root{}, domainerr.Precondition(kind.entity() + " content document has not been populated")
	}
	if strings.TrimSpace(root.SourceLocale) == "" {
		return Root{}, domainerr.Precondition(kind.entity() + " source locale is missing")
	}
	if _, err := canonicalLegalLocale(root.SourceLocale); err != nil {
		return Root{}, domainerr.Precondition(kind.entity() + " source locale is not canonical").WithCause(failurecause.ServerFault(err))
	}
	return root, nil
}

func loadLegalContentDocumentID(
	ctx context.Context,
	store Store,
	kind *policyKind,
	entityID string,
) (uuid.UUID, error) {
	root, err := loadLegalContentDocumentRoot(ctx, store, kind, entityID, false)
	if err != nil {
		return uuid.Nil, err
	}
	return *root.ContentDocumentID, nil
}

func loadLegalContentDomainContext(
	ctx context.Context,
	store Store,
	kind *policyKind,
	entityID string,
) (contentblock.DomainContext, error) {
	root, err := loadLegalContentDocumentRoot(ctx, store, kind, entityID, false)
	if err != nil {
		return contentblock.DomainContext{}, err
	}
	return contentblock.DomainContext{SourceLocale: root.SourceLocale}, nil
}

func legalCollaborationDocumentFence(
	kind *policyKind,
	entityID string,
	contributors []string,
	batch *contentblock.Batch,
) ContentFence {
	return func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error) {
		root, err := loadLegalContentDocumentRoot(ctx, store, kind, entityID, true)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if root.ContentDocumentID == nil || *root.ContentDocumentID != documentID {
			return contentblock.DomainContext{}, domainerr.Precondition(kind.entity() + " content document changed; reload before saving")
		}
		if err := store.RequireCurrentContributors(ctx, kind.kind, entityID, contributors); err != nil {
			return contentblock.DomainContext{}, err
		}
		if err := requireLegalTargetTranslationRows(ctx, store, kind, entityID, root.SourceLocale, batch); err != nil {
			return contentblock.DomainContext{}, err
		}
		return contentblock.DomainContext{SourceLocale: root.SourceLocale}, nil
	}
}

func requireLegalTargetTranslationRows(
	ctx context.Context,
	store Store,
	kind *policyKind,
	entityID string,
	sourceLocale string,
	batch *contentblock.Batch,
) error {
	for _, locale := range legalTargetLocales(sourceLocale, batch) {
		count, err := store.CountTranslations(ctx, kind.kind, entityID, locale)
		if err != nil {
			return err
		}
		if count != 1 {
			return domainerr.Precondition(kind.entity() + " target locale must be created before collaboration editing")
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
func legalTranslationJobApplyDocumentFence(kind *policyKind, entityID string) ContentFence {
	return func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error) {
		root, err := loadLegalContentDocumentRoot(ctx, store, kind, entityID, true)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if root.ContentDocumentID == nil || *root.ContentDocumentID != documentID {
			return contentblock.DomainContext{}, domainerr.Precondition(kind.entity() + " content document changed; translation job cannot be applied")
		}
		return loadLegalContentDomainContext(ctx, store, kind, entityID)
	}
}

func authorizeLegalBlockBootstrap(
	ctx context.Context,
	store Store,
	checker authz.AuthorizationDecisionChecker,
	kind *policyKind,
	entityID string,
	sessionID string,
) error {
	if strings.TrimSpace(sessionID) == "" {
		return domainerr.AuthenticationRequired()
	}
	root, err := loadLegalContentDocumentRoot(ctx, store, kind, entityID, true)
	if err != nil {
		return err
	}
	principal, err := store.ResolveSessionPrincipal(ctx, sessionID)
	if errors.Is(err, auth.ErrSessionPrincipalInvalid) {
		return domainerr.AuthenticationRequired().WithCause(failurecause.WithPrivate(err, sessionID))
	}
	if err != nil {
		return fmt.Errorf("resolve %s collaboration principal: %w", kind.entity(), err)
	}
	if principal == nil || !principal.Authenticated {
		return domainerr.AuthenticationRequired()
	}
	if principal.Banned {
		return domainerr.AccountBanned()
	}
	if !principal.Onboarded {
		return domainerr.NoPermission("edit", kind.entity())
	}
	return requireLegalPermissionForPrincipal(ctx, checker, kind.kind, entityID, legalViewAction(kind, root.Status), principal)
}

func legalDocumentOwnershipFence(kind *policyKind, entityID string) ContentFence {
	return func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error) {
		root, err := loadLegalContentDocumentRoot(ctx, store, kind, entityID, true)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if root.ContentDocumentID == nil || *root.ContentDocumentID != documentID {
			return contentblock.DomainContext{}, domainerr.Precondition(kind.entity() + " content document changed; reload before mutation")
		}
		return loadLegalContentDomainContext(ctx, store, kind, entityID)
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

func verifyLegalExpectedRevision(
	ctx context.Context,
	store Store,
	kind *policyKind,
	entityID string,
	expectedRevision string,
) error {
	expected, err := parseLegalContentUUID("expected_revision", expectedRevision)
	if err != nil {
		return err
	}
	root, err := loadLegalContentDocumentRoot(ctx, store, kind, entityID, true)
	if err != nil {
		return err
	}
	revision, err := store.ContentRevision(ctx, *root.ContentDocumentID)
	if err != nil {
		return normalizeLegalContentBlockError(kind.entity(), err)
	}
	if revision != expected {
		return normalizeLegalContentBlockError(kind.entity(), &contentblock.StaleRevisionError{CurrentRevision: revision})
	}
	return nil
}
