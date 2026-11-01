package legal

import (
	"context"
	"errors"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/translation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// LoadTranslationInterchangeAIDocument projects one locale after the
// Translation application has locked and authorized the Legal history root in
// the transaction joined binds.
func (s *AIDocumentService) LoadTranslationInterchangeAIDocument(
	ctx context.Context,
	joined Joined,
	entityType string,
	entityID string,
	locale string,
) (AIDocument, error) {
	if err := validateAIDocumentIdentity(entityType, entityID, locale); err != nil {
		return AIDocument{}, err
	}
	kind, err := legalKind(entityType)
	if err != nil {
		return AIDocument{}, err
	}
	if _, err := loadLegalContentDocumentRoot(ctx, joined.Store, kind, entityID, false); err != nil {
		return AIDocument{}, err
	}
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated || principal.MemberID == "" {
		return AIDocument{}, domainerr.AuthenticationRequired()
	}
	document, _, err := loadAIDocumentAfterAuthorization(
		ctx, joined.Store, kind, entityID, locale, principal.MemberID.String(),
	)
	return document, err
}

// ExecuteTranslationInterchangeMutation is the Legal-owned XLIFF write seam
// called after Translation has performed the lifecycle-aware mutation
// preflight in the transaction joined binds. It re-locks current facts,
// compiles under the lock, enforces aggregate CAS, persists and appends
// Audit. Generic Translation owns the single OG request after this method
// reports a changed locale.
func (s *AIDocumentService) ExecuteTranslationInterchangeMutation(
	ctx context.Context,
	joined Joined,
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
	kind, err := legalKind(entityType)
	if err != nil {
		return AIDocumentMutationResult{}, err
	}
	root, err := loadLegalContentDocumentRoot(ctx, joined.Store, kind, entityID, true)
	if err != nil {
		return AIDocumentMutationResult{}, err
	}
	principal, err := requireLegalAIPrincipal(ctx)
	if err != nil {
		return AIDocumentMutationResult{}, err
	}
	result, err := compileAndApplyAIDocumentMutation(
		ctx, joined.Store, root, kind, entityID, locale, principal.MemberID.String(), compiler, false, true,
	)
	if err == nil {
		err = recordInterchange(ctx, joined, result)
	}
	return legalAIDocumentResult(entityType, result, err)
}

// recordInterchange appends the interchange's locale content record and
// signals the change in the Translation application's transaction.
func recordInterchange(ctx context.Context, joined Joined, result legalAIDocumentApplyResult) error {
	if change := result.localeChange; change != nil {
		if err := domainaudit.AppendMember(ctx, nil, joined.Audit, change.memberID, sharedtelemetry.AuditLegalPolicyUpdated, change.record); err != nil {
			return err
		}
	}
	return result.publish(ctx, joined.Store)
}

func compileAndApplyAIDocumentMutation(
	ctx context.Context,
	store Store,
	root Root,
	kind *policyKind,
	entityID string,
	locale string,
	memberID string,
	compiler AIDocumentMutationCompiler,
	requestSavedOG bool,
	allowAuthoritativeTargetReplacement bool,
) (legalAIDocumentApplyResult, error) {
	state, sourceLocale, err := loadAIDocumentAfterAuthorization(
		ctx, store, kind, entityID, locale, memberID,
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
	result, err := applyAIDocumentAfterAuthorization(
		ctx, store, root, kind, state, sourceLocale, mutation, requestSavedOG,
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
