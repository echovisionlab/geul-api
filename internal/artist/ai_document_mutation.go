package artist

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/optional"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	"github.com/echovisionlab/geul-api/internal/translation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
)

var errRollbackArtistAIDocumentValidation = errors.New("rollback Artist AI document validation")

// ExecuteAIDocumentMutation owns Artist's exact mutation transaction. The
// locked lifecycle and one Edit decision are established before any document
// projection or adapter compiler executes. Validation runs the whole
// mutation and rolls it back.
func (s *Service) ExecuteAIDocumentMutation(
	ctx context.Context,
	artistID string,
	locale string,
	mode AIDocumentExecutionMode,
	compiler AIDocumentMutationCompiler,
) (AIDocumentMutationResult, error) {
	if compiler == nil {
		return AIDocumentMutationResult{}, domainerr.DependencyUnavailable("Artist AI document compiler")
	}
	if mode != AIDocumentExecutionValidate && mode != AIDocumentExecutionApply {
		return AIDocumentMutationResult{}, domainerr.InvalidField("mode", "is not supported")
	}
	canonicalArtistID, err := canonicalArtistUUID(artistID)
	if err != nil {
		return AIDocumentMutationResult{}, domainerr.InvalidField("artist_id", "must be a canonical UUID")
	}
	locale, err = normalizeArtistDocumentLocale(locale)
	if err != nil {
		return AIDocumentMutationResult{}, err
	}
	var root DocumentRoot
	var outcome artistAIDocumentOutcome
	_, err = mutation.Run(ctx, s.mutations, mutation.StoreSpec[artistAIDocumentOutcome, Store]{
		Lock: func(ctx context.Context, store Store) error {
			var err error
			if root, err = loadArtistAIDocumentRoot(ctx, store, canonicalArtistID.String(), UpdateLock); err != nil {
				return err
			}
			if _, valid := artistAIDocumentLifecycle(root.Status); !valid {
				return errors.New("Artist has an unsupported lifecycle status")
			}
			return nil
		},
		Access: mutation.Fenced(func(ctx context.Context, _ Store, lockActive func(*auth.UserInfo) (bool, error)) error {
			allowed, err := requireArtistAIDocumentMutationAuthority(ctx, lockActive, s.spiceDB, root.ID)
			if err != nil {
				return err
			}
			if !allowed {
				return domainerr.ResourceNotFound("artist", root.ID)
			}
			return nil
		}),
		Change: func(ctx context.Context, store Store) (artistAIDocumentOutcome, error) {
			var err error
			outcome, err = applyAIDocumentMutation(ctx, store, root, locale, compiler)
			return outcome, err
		},
		Audit: func(outcome artistAIDocumentOutcome) mutation.Audit { return outcome.audit() },
		Outbox: func(ctx context.Context, store Store, outcome artistAIDocumentOutcome) error {
			if mode == AIDocumentExecutionValidate {
				return errRollbackArtistAIDocumentValidation
			}
			if !outcome.result.Changed {
				return nil
			}
			return store.PublishContentUpdate(ctx, artistAIDocumentContentUpdate(outcome.input, outcome.result, outcome.input.ContributorMemberID.String()))
		},
	})
	if errors.Is(err, errRollbackArtistAIDocumentValidation) {
		return outcome.result, nil
	}
	if err != nil {
		var compilerErr *artistAIDocumentCompilerError
		var targetConflict *translation.TargetRevisionConflict
		if errors.As(err, &compilerErr) {
			return AIDocumentMutationResult{}, compilerErr.cause
		}
		if errors.As(err, &targetConflict) {
			return AIDocumentMutationResult{}, &AIDocumentRevisionConflictError{
				Kind: AIDocumentTargetRevisionConflict, CurrentRevision: outcome.input.ExpectedRevision,
				CurrentTargetRevision: artistTargetConflictRevision(targetConflict),
			}
		}
		return AIDocumentMutationResult{}, err
	}
	return outcome.result, nil
}

// artistAIDocumentOutcome is an applied AI-document mutation and the Member
// it is attributed to.
type artistAIDocumentOutcome struct {
	input    AIDocumentMutation
	memberID string
	result   AIDocumentMutationResult
}

// applyAIDocumentMutation compiles the mutation against the locked state and
// applies it as the principal's Member.
func applyAIDocumentMutation(
	ctx context.Context,
	store Store,
	root DocumentRoot,
	locale string,
	compiler AIDocumentMutationCompiler,
) (artistAIDocumentOutcome, error) {
	principal := auth.GetUser(ctx)
	if principal == nil {
		return artistAIDocumentOutcome{}, domainerr.ResourceNotFound("artist", root.ID)
	}
	memberID, err := canonicalArtistUUID(principal.MemberID.String())
	if err != nil {
		return artistAIDocumentOutcome{}, domainerr.ResourceNotFound("artist", root.ID).WithCause(err)
	}
	state, err := loadAIDocumentStateAfterAuthorization(ctx, store, root, locale, memberID.String())
	if err != nil {
		return artistAIDocumentOutcome{}, err
	}
	outcome := artistAIDocumentOutcome{memberID: memberID.String()}
	if outcome.input, err = compiler(state); err != nil {
		return outcome, &artistAIDocumentCompilerError{cause: err}
	}
	input := outcome.input
	expected, err := validateCompiledArtistAIDocumentMutation(state, input, memberID)
	if err != nil {
		return outcome, err
	}
	if err := store.RequireDocumentContributors(ctx, []string{memberID.String()}); err != nil {
		return outcome, err
	}
	fence := artistAuthorizedAIDocumentFence(root, input.Locale, input.ExpectedSource, input.ExpectedPresence)
	result, targetRevision, err := applyAIDocumentMode(ctx, store, input, expected, memberID, *root.DocumentID, fence)
	if err != nil {
		return outcome, mapArtistAIDocumentMutationError(err)
	}
	outcome.result = AIDocumentMutationResult{Revision: result.DocumentRevision.String(), TargetRevision: targetRevision, Changed: result.Changed}
	// A deleted target locale has no OG image to generate; requesting one
	// would fail on the missing locale row.
	if result.Changed && !input.DeleteTranslation {
		if _, err := store.RequestOG(ctx, root.ID, input.Locale, result.TranslationSourceChanged, "artist_ai_document_saved"); err != nil {
			return outcome, err
		}
	}
	return outcome, nil
}

// audit attributes a changed locale to the Member the mutation ran as: a
// target locale is created, updated or deleted, the source is updated.
func (o artistAIDocumentOutcome) audit() mutation.Audit {
	if !o.result.Changed {
		return mutation.Unchanged()
	}
	operation := sharedtelemetry.AuditItemOperationUpdated
	if o.input.Locale != o.input.ExpectedSource && o.input.CreateTranslation {
		operation = sharedtelemetry.AuditItemOperationCreated
	} else if o.input.Locale != o.input.ExpectedSource && o.input.DeleteTranslation {
		operation = sharedtelemetry.AuditItemOperationDeleted
	}
	return artistMemberLocaleAudit(o.memberID, o.input.ArtistID, o.input.Locale, operation)
}

func validateCompiledArtistAIDocumentMutation(
	state AIDocumentState,
	input AIDocumentMutation,
	memberID uuid.UUID,
) (uuid.UUID, error) {
	_, expected, err := validateArtistAIDocumentMutation(input)
	if err != nil {
		return uuid.Nil, err
	}
	if input.ArtistID != state.ArtistID || input.Locale != state.Locale ||
		input.ExpectedSource != state.SourceLocale || input.ExpectedPresence != state.LocaleExists ||
		input.ContributorMemberID != memberID || state.ViewerMemberID != memberID.String() {
		return uuid.Nil, domainerr.InvalidField(
			"mutation",
			"compiled Artist identity, locale, contributor, source observation, and locale presence must match the locked state",
		)
	}
	if state.Locale == state.SourceLocale {
		if state.TargetRevision != nil {
			return uuid.Nil, domainerr.Precondition("Artist source locale cannot carry a target revision")
		}
	} else if state.LocaleExists != (state.TargetRevision != nil) {
		return uuid.Nil, domainerr.Precondition("Artist target locale presence and revision are inconsistent")
	}
	if input.ExpectedRevision != state.Revision {
		return uuid.Nil, &AIDocumentRevisionConflictError{
			Kind: AIDocumentDocumentRevisionConflict, CurrentRevision: state.Revision,
		}
	}
	if !optional.Equal(input.ExpectedTargetRevision, state.TargetRevision) {
		return uuid.Nil, &AIDocumentRevisionConflictError{
			Kind: AIDocumentTargetRevisionConflict, CurrentRevision: state.Revision,
			CurrentTargetRevision: optional.Clone(state.TargetRevision),
		}
	}
	if input.Batch != nil {
		if input.Batch.DocumentID != state.DocumentID || input.Batch.ExpectedRevision != expected ||
			len(input.Batch.ContributorMemberIDs) != 1 || input.Batch.ContributorMemberIDs[0] != memberID {
			return uuid.Nil, domainerr.InvalidField(
				"mutation",
				"compiled Artist document, revision, and attribution must match the locked state",
			)
		}
	}
	return expected, nil
}

func applyAIDocumentMode(ctx context.Context, store Store, input AIDocumentMutation, expected, memberID, documentID uuid.UUID, fence ContentFence) (contentblock.Result, *string, error) {
	if input.Locale != input.ExpectedSource {
		if input.Batch != nil {
			batch := *input.Batch
			batch.ExpectedRevision, batch.ContributorMemberIDs = expected, []uuid.UUID{memberID}
			result, revision, err := applyArtistTargetLocaleMutation(ctx, store, artistTargetLocaleMutationInput{
				ArtistID: input.ArtistID, DocumentID: documentID, Locale: input.Locale, Batch: batch,
				ExpectedTargetRevision: input.ExpectedTargetRevision, Now: time.Now().UTC(), Fence: fence,
			})
			return result, &revision, err
		}
		if input.CreateTranslation {
			batch := contentblock.Batch{
				DocumentID: documentID, ExpectedRevision: expected,
				ContributorMemberIDs: []uuid.UUID{memberID},
			}
			result, revision, err := applyArtistTargetLocaleMutation(ctx, store, artistTargetLocaleMutationInput{
				ArtistID: input.ArtistID, DocumentID: documentID, Locale: input.Locale, Batch: batch,
				ExpectedTargetRevision: input.ExpectedTargetRevision, Policy: targetlocale.Policy{AllowCreate: true},
				Now: time.Now().UTC(), Fence: fence,
			})
			return result, &revision, err
		}
		result, err := deleteArtistTargetLocale(
			ctx, store, input.ArtistID, documentID, input.Locale, expected,
			input.ExpectedTargetRevision, []uuid.UUID{memberID}, fence,
		)
		return result, nil, err
	}
	if input.ExpectedTargetRevision != nil {
		return contentblock.Result{}, nil, domainerr.InvalidField("expected_target_revision", "must be omitted for the source locale")
	}
	if input.Batch != nil {
		batch := *input.Batch
		if batch.DocumentID != documentID {
			return contentblock.Result{}, nil, domainerr.InvalidField("document", "Artist content document does not match")
		}
		batch.ExpectedRevision, batch.ContributorMemberIDs = expected, []uuid.UUID{memberID}
		result, err := store.ApplyContentBatch(ctx, batch, fence)
		return result, nil, err
	}
	if input.CreateTranslation {
		advanced, err := store.AdvanceContentRevision(ctx, contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: expected}, fence, func(ctx context.Context, store Store) (contentblock.MetadataEffect, error) {
			created, err := store.EnsureTargetLocale(ctx, input.ArtistID, input.Locale)
			return contentblock.MetadataEffect{Changed: created, ChangedLocales: []string{input.Locale}}, err
		})
		return contentblock.Result{DocumentRevision: advanced.DocumentRevision, Changed: advanced.Changed, TranslationSourceChanged: advanced.TranslationSourceChanged}, nil, err
	}
	return contentblock.Result{}, nil, domainerr.InvalidField("locale", "source translation cannot be deleted")
}

func artistAuthorizedAIDocumentFence(
	root DocumentRoot,
	locale string,
	expectedSource string,
	expectedPresence bool,
) ContentFence {
	return func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if root.ID == "" || root.DocumentID == nil || documentID != *root.DocumentID {
			return contentblock.DomainContext{}, domainerr.Precondition("Artist content document changed; reload before saving")
		}
		source, err := lockCreativeTranslationSourceContext(ctx, store, root.ID)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		exists, err := store.LocaleExists(ctx, root.ID, locale)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if source.SourceLocale != expectedSource || exists != expectedPresence {
			current, err := store.CurrentContentRevision(ctx, documentID)
			if err != nil && !errors.Is(err, ErrDocumentRowNotFound) {
				return contentblock.DomainContext{}, err
			}
			return contentblock.DomainContext{}, &AIDocumentRevisionConflictError{CurrentRevision: current.String()}
		}
		return source, nil
	}
}

func requireArtistAIDocumentMutationAuthority(ctx context.Context, lockActive func(*auth.UserInfo) (bool, error), checker Authorizer, artistID string) (bool, error) {
	principal := auth.GetUser(ctx)
	active, err := lockActive(principal)
	if err != nil {
		return false, err
	}
	if !active {
		return false, nil
	}
	can, err := policyv1.Artist.Edit(artistID)
	if err != nil {
		return false, err
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return false, errAuthenticationRequired.WithCause(err)
	}
	allowed, err := checker.Can(ctx, decision)
	if err != nil {
		return false, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	return allowed, nil
}

func artistAIDocumentContentUpdate(input AIDocumentMutation, result AIDocumentMutationResult, contributor string) ContentUpdate {
	exists := input.Locale == input.ExpectedSource || !input.DeleteTranslation
	documentChanged := input.Locale == input.ExpectedSource
	return ContentUpdate{ArtistID: input.ArtistID, Kind: LocaleUpdate, Locale: LocaleChange{
		Fields:               []string{"content"},
		DocumentRevision:     result.Revision,
		Contributors:         []string{contributor},
		Source:               SourceAI,
		Locale:               input.Locale,
		LocaleDeleted:        !exists,
		TargetRevision:       result.TargetRevision,
		DocumentStateChanged: documentChanged,
	}}
}

func artistTargetConflictRevision(conflict *translation.TargetRevisionConflict) *string {
	if conflict == nil || !conflict.CurrentExists || conflict.CurrentRevision == "" {
		return nil
	}
	current := conflict.CurrentRevision
	return &current
}
