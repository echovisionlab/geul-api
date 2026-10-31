package legal

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/translation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// LoadTranslationInterchangeAIDocumentWithDB projects one locale after the
// Translation application has locked and authorized the Legal history root in
// the same caller-owned transaction.
func (s *AIDocumentService) LoadTranslationInterchangeAIDocumentWithDB(
	ctx context.Context,
	tx *gorm.DB,
	entityType string,
	entityID string,
	locale string,
) (AIDocument, error) {
	if err := validateAIDocumentIdentity(entityType, entityID, locale); err != nil {
		return AIDocument{}, err
	}
	if _, err := loadLegalContentDocumentRoot(ctx, tx, entityType, entityID, false); err != nil {
		return AIDocument{}, err
	}
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated || principal.MemberID == "" {
		return AIDocument{}, domainerr.AuthenticationRequired()
	}
	document, _, err := s.loadAIDocumentAfterAuthorization(
		ctx, tx, entityType, entityID, locale, principal.MemberID.String(),
	)
	return document, err
}

// ExecuteTranslationInterchangeMutationWithDB is the Legal-owned XLIFF write
// seam called after Translation has performed the lifecycle-aware mutation
// preflight in this transaction. It re-locks current facts, compiles under the
// lock, enforces aggregate CAS, persists and appends Audit. Generic Translation
// owns the single OG request after this method reports a changed locale.
func (s *AIDocumentService) ExecuteTranslationInterchangeMutationWithDB(
	ctx context.Context,
	tx *gorm.DB,
	entityType string,
	entityID string,
	locale string,
	compiler AIDocumentMutationCompiler,
) (AIDocumentMutationResult, error) {
	if compiler == nil {
		return AIDocumentMutationResult{}, domainerr.DependencyUnavailable("Legal translation interchange compiler")
	}
	if err := validateAIDocumentIdentity(entityType, entityID, locale); err != nil {
		return AIDocumentMutationResult{}, err
	}
	root, err := loadLegalContentDocumentRoot(ctx, tx, entityType, entityID, true)
	if err != nil {
		return AIDocumentMutationResult{}, err
	}
	principal, err := requireLegalAIPrincipal(ctx)
	if err != nil {
		return AIDocumentMutationResult{}, err
	}
	result, err := s.compileAndApplyAIDocumentMutationWithDB(
		ctx, tx, root, entityType, entityID, locale, principal.MemberID.String(), compiler, false, true,
	)
	if err == nil {
		err = s.recordInterchange(ctx, tx, result)
	}
	return legalAIDocumentResult(entityType, result, err)
}

// recordInterchange appends the interchange's locale content record and
// signals the change in the Translation application's transaction.
func (s *AIDocumentService) recordInterchange(ctx context.Context, tx *gorm.DB, result legalAIDocumentApplyResult) error {
	if change := result.localeChange; change != nil {
		if err := domainaudit.AppendMember(ctx, tx, s.auditWriter, change.memberID, sharedtelemetry.AuditLegalPolicyUpdated, change.record); err != nil {
			return err
		}
	}
	return result.publish(ctx, tx)
}

func (s *AIDocumentService) compileAndApplyAIDocumentMutationWithDB(
	ctx context.Context,
	tx *gorm.DB,
	root legalContentDocumentRoot,
	entityType string,
	entityID string,
	locale string,
	memberID string,
	compiler AIDocumentMutationCompiler,
	requestSavedOG bool,
	allowAuthoritativeTargetReplacement bool,
) (legalAIDocumentApplyResult, error) {
	state, sourceLocale, err := s.loadAIDocumentAfterAuthorization(
		ctx, tx, entityType, entityID, locale, memberID,
	)
	if err != nil {
		return legalAIDocumentApplyResult{}, err
	}
	mutation, err := compiler(state)
	if err != nil {
		return legalAIDocumentApplyResult{}, &legalAIDocumentCompilerError{cause: err}
	}
	if err := validateCompiledLegalAIDocumentMutation(
		state, mutation, allowAuthoritativeTargetReplacement,
	); err != nil {
		return legalAIDocumentApplyResult{}, err
	}
	result, err := s.applyAIDocumentAfterAuthorizationInTransaction(
		ctx, tx, root, state, sourceLocale, mutation, requestSavedOG,
	)
	result.CurrentDocumentRevision = state.Revision
	return result, err
}

func legalAIDocumentResult(
	entityType string,
	result legalAIDocumentApplyResult,
	err error,
) (AIDocumentMutationResult, error) {
	if err == nil {
		return result.output(), nil
	}
	var compilerErr *legalAIDocumentCompilerError
	var stale *contentblock.StaleRevisionError
	var targetConflict *translation.TargetRevisionConflict
	var revisionConflict *AIDocumentRevisionConflict
	switch {
	case errors.As(err, &compilerErr):
		return AIDocumentMutationResult{}, compilerErr.cause
	case errors.As(err, &stale):
		return AIDocumentMutationResult{}, &AIDocumentRevisionConflict{
			Kind: AIDocumentDocumentRevisionConflict, CurrentRevision: stale.CurrentRevision.String(),
		}
	case errors.As(err, &targetConflict):
		var currentTargetRevision *string
		if targetConflict.CurrentExists {
			current := targetConflict.CurrentRevision
			currentTargetRevision = &current
		}
		return AIDocumentMutationResult{}, &AIDocumentRevisionConflict{
			Kind:                  AIDocumentTargetRevisionConflict,
			CurrentRevision:       result.CurrentDocumentRevision,
			CurrentTargetRevision: currentTargetRevision,
		}
	case errors.As(err, &revisionConflict):
		return AIDocumentMutationResult{}, revisionConflict
	default:
		return AIDocumentMutationResult{}, normalizeLegalContentBlockError(entityType, err)
	}
}
