package legal

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/translation"
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
	if s == nil || s.contentBlocks == nil || tx == nil {
		return AIDocument{}, errs.DependencyUnavailable("Legal translation interchange")
	}
	if err := validateAIDocumentIdentity(entityType, entityID, locale); err != nil {
		return AIDocument{}, err
	}
	root, err := loadLegalContentDocumentRoot(ctx, tx, entityType, entityID, false)
	if err != nil {
		return AIDocument{}, err
	}
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated || principal.MemberID == "" {
		return AIDocument{}, errs.AuthenticationRequired()
	}
	document, _, err := s.loadAIDocumentAfterAuthorization(
		ctx, tx, root, entityType, entityID, locale, principal.MemberID.String(),
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
	if s == nil || s.contentBlocks == nil || s.legalOG == nil || s.auditWriter == nil || tx == nil {
		return AIDocumentMutationResult{}, errs.DependencyUnavailable("Legal translation interchange")
	}
	if compiler == nil {
		return AIDocumentMutationResult{}, errs.DependencyUnavailable("Legal translation interchange compiler")
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
	return legalAIDocumentResult(entityType, result, err)
}

func (s *AIDocumentService) executeAIDocumentMutationWithDB(
	ctx context.Context,
	tx *gorm.DB,
	entityType string,
	entityID string,
	locale string,
	compiler AIDocumentMutationCompiler,
	requestSavedOG bool,
	allowAuthoritativeTargetReplacement bool,
) (legalAIDocumentApplyResult, error) {
	root, err := loadLegalContentDocumentRoot(ctx, tx, entityType, entityID, true)
	if err != nil {
		return legalAIDocumentApplyResult{}, err
	}
	principal, err := requireLegalAIPrincipal(ctx)
	if err != nil {
		return legalAIDocumentApplyResult{}, legalAIDocumentMutationAccessError(entityType, entityID, err)
	}
	active, err := identitystate.LockActivePrincipal(ctx, tx, principal)
	if err != nil {
		return legalAIDocumentApplyResult{}, errs.Internal(err)
	}
	if !active {
		return legalAIDocumentApplyResult{}, errs.NotFound(entityType, entityID)
	}
	policy, err := legalDocumentPolicyForType(entityType)
	if err != nil {
		return legalAIDocumentApplyResult{}, err
	}
	if err := requireLegalPermission(
		ctx,
		s.spiceDB,
		entityType,
		entityID,
		legalMutationAction(policy, root.Status, legalActionEdit),
	); err != nil {
		return legalAIDocumentApplyResult{}, legalAIDocumentMutationAccessError(entityType, entityID, err)
	}
	return s.compileAndApplyAIDocumentMutationWithDB(
		ctx, tx, root, entityType, entityID, locale, principal.MemberID.String(), compiler, requestSavedOG,
		allowAuthoritativeTargetReplacement,
	)
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
	policy, err := legalDocumentPolicyForType(entityType)
	if err != nil {
		return legalAIDocumentApplyResult{}, err
	}
	state, sourceLocale, err := s.loadAIDocumentAfterAuthorization(
		ctx, tx, root, entityType, entityID, locale, memberID,
	)
	if err != nil {
		return legalAIDocumentApplyResult{}, err
	}
	if locale == sourceLocale && root.Status != policy.draftStatus && root.Status != policy.archivedStatus {
		return legalAIDocumentApplyResult{}, errs.FailedPrecondition("scheduled or active legal source documents are read-only")
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
		return AIDocumentMutationResult{
			Revision: result.Content.DocumentRevision.String(), TargetRevision: result.TargetRevision,
			Changed: result.Content.Changed,
		}, nil
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
