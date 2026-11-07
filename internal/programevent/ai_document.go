package programevent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/uuidutil"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
)

// AIDocumentState is the Program Event-owned, authorized aggregate snapshot
// used by the provider-neutral AI document adapter. The Rich Text document is
// the complete stable Block graph; RequestedLocale and LocaleExists select the
// one locale projection exposed by DCDP.
type AIDocumentState struct {
	EventID           string
	ContentDocumentID uuid.UUID
	DocumentRevision  string
	TargetRevision    *string
	SourceLocale      string
	RequestedLocale   string
	LocaleExists      bool
	LocalizedDocument *contentv1.LocalizedRichTextDocument
	ViewerMemberID    string
	Title             string
	Summary           *string
}

// AIDocumentCommand is the already generated-catalog-compiled Program Event
// mutation. The adapter cannot supply SQL callbacks: Program Event repeats the
// locale observation, exact object authority, root lock and revision CAS itself.
type AIDocumentCommand struct {
	EventID                string
	RequestedLocale        string
	ObservedSourceLocale   string
	ObservedLocaleExists   bool
	ExpectedRevision       uuid.UUID
	ExpectedTargetRevision *string
	ContributorMemberID    uuid.UUID
	Batch                  *contentblock.Batch
	CreateTranslation      bool
	DeleteTranslation      bool
	Metadata               AIDocumentMetadataPatch
}

// AIDocumentMetadataPatch keeps Program Event-owned metadata separate from
// the generated Rich Text batch while sharing its authorization and CAS.
type AIDocumentMetadataPatch struct {
	SetTitle   bool
	Title      *string
	SetSummary bool
	Summary    *string
}

type AIDocumentResult struct {
	DocumentRevision string
	TargetRevision   *string
	Changed          bool
}

// AIDocumentExecutionMode selects whether the exact Program Event mutation
// transaction commits or deliberately rolls back after running the same
// authorization, compiler, CAS, File validation and persistence path.
type AIDocumentExecutionMode uint8

const (
	AIDocumentExecutionValidate AIDocumentExecutionMode = iota
	AIDocumentExecutionApply
)

// AIDocumentCommandCompiler belongs to the DCDP adapter. Program Event gives
// it only the current state loaded after the root lock and exact authorization;
// the compiled command is then checked and persisted in that same transaction.
type AIDocumentCommandCompiler func(AIDocumentState) (AIDocumentCommand, error)

type programEventAIDocumentCompilerError struct{ cause error }

func (e *programEventAIDocumentCompilerError) Error() string { return e.cause.Error() }
func (e *programEventAIDocumentCompilerError) Unwrap() error { return e.cause }

// AIDocumentStateQuery names the event and the locale an AI document opens.
type AIDocumentStateQuery struct {
	EventID string
	Locale  string
}

// LoadAIDocumentState applies Program Event account and aggregate locking rules
// before exposing a compact authoring projection. A read keeps to View in every
// lifecycle: an AI document names the caller's own documents, not every archived
// event an Author may see elsewhere. Public reads continue through the public
// Program Event service.
func (s *Service) LoadAIDocumentState(ctx context.Context, query AIDocumentStateQuery) (AIDocumentState, error) {
	locale, err := validateProgramEventAIDocumentIdentity(query.EventID, query.Locale)
	if err != nil {
		return AIDocumentState{}, err
	}
	var state AIDocumentState
	err = s.units.ReadCurrent(ctx, func(store Store) error {
		root, err := loadProgramEventAIDocumentRoot(ctx, store, query.EventID, ShareLock)
		if err != nil {
			return err
		}
		if err := requireActiveProgramEventPrincipal(ctx, readLockActive(ctx, store), "view", true); err != nil {
			return err
		}
		if err := requireProgramEventPermission(
			ctx, s.spiceDB, root.ID, policyv1.ProgramEvent.View,
		); err != nil {
			return err
		}
		principal := auth.GetUser(ctx)
		if principal == nil || principal.MemberID == "" {
			return authenticationRequired()
		}
		state, err = loadAIDocumentStateAfterAuthorization(
			ctx, store, root, locale, principal.MemberID.String(),
		)
		return err
	})
	return state, err
}

func validateProgramEventAIDocumentIdentity(eventID, requestedLocale string) (string, error) {
	if _, err := uuidutil.ParseCanonical(eventID, "event_id"); err != nil {
		return "", domainerr.InvalidField("event_id", "must be a canonical UUID")
	}
	locale := localization.NormalizeExactSupportedLocale(requestedLocale)
	if locale == nil {
		return "", domainerr.InvalidField("locale", "must be an exact canonical locale")
	}
	return *locale, nil
}

func loadProgramEventAIDocumentRoot(
	ctx context.Context,
	store Store,
	eventID string,
	lock RowLock,
) (DocumentRoot, error) {
	root, err := store.DocumentRoot(ctx, eventID, lock)
	if err != nil {
		return DocumentRoot{}, notFoundAs(err, domainerr.ResourceNotFound("program event", eventID))
	}
	if root.ContentDocumentID == nil || strings.TrimSpace(*root.ContentDocumentID) == "" {
		return DocumentRoot{}, domainerr.Precondition("Program Event content document has not been populated")
	}
	return root, nil
}

func loadAIDocumentStateAfterAuthorization(
	ctx context.Context,
	store Store,
	root DocumentRoot,
	locale string,
	viewerMemberID string,
) (AIDocumentState, error) {
	documentID, err := uuidutil.ParseCanonical(*root.ContentDocumentID, "content_document_id")
	if err != nil {
		return AIDocumentState{}, domainerr.Precondition("Program Event content document identity is invalid").WithCause(failurecause.ServerFault(err))
	}
	sourceState, err := loadProgramEventSourceLocale(ctx, store, root.ID, false)
	if err != nil {
		return AIDocumentState{}, err
	}
	if root.SourceLocale != sourceState.SourceLocale {
		return AIDocumentState{}, domainerr.Precondition("Program Event source locale changed; reload before editing")
	}
	localeState, err := loadProgramEventExactLocaleState(
		ctx, store, root.ID, documentID, locale, false,
	)
	if err != nil {
		return AIDocumentState{}, err
	}
	document, err := contentblock.SnapshotToLocalizedRichTextDocument(localeState.Snapshot, locale)
	if err != nil {
		return AIDocumentState{}, normalizeProgramEventContentBlockError(err)
	}
	localeExists := localeState.TargetMetadata != nil
	if locale == sourceState.SourceLocale && !localeExists {
		return AIDocumentState{}, domainerr.Precondition("Program Event source locale metadata is not initialized")
	}
	var summary *string
	if localeState.TargetMetadata != nil {
		summary = localeState.TargetMetadata.Summary
	}
	return AIDocumentState{
		EventID: root.ID, ContentDocumentID: documentID,
		DocumentRevision: localeState.Snapshot.Document.Revision.String(),
		SourceLocale:     sourceState.SourceLocale, RequestedLocale: locale,
		LocaleExists: localeExists, LocalizedDocument: document, ViewerMemberID: viewerMemberID,
		TargetRevision: optionalProgramEventTargetRevision(localeState, locale),
		Title:          root.Title, Summary: summary,
	}, nil
}

func optionalProgramEventTargetRevision(state programEventExactLocaleState, locale string) *string {
	if locale == state.SourceLocale || state.TargetMetadata == nil {
		return nil
	}
	revision := state.TargetRevision
	return &revision
}

var errRollbackAIDocumentValidation = errors.New("rollback Program Event AI document validation")

// ExecuteAIDocumentCommand runs one compiled AI document mutation of an
// event's locale. Mode Validate runs the identical path and rolls it back.
type ExecuteAIDocumentCommand struct {
	EventID  string
	Locale   string
	Mode     AIDocumentExecutionMode
	Compiler AIDocumentCommandCompiler
}

// aiDocumentChange is a committed AI document mutation and what its audit
// record and content signal name.
type aiDocumentChange struct {
	result    AIDocumentResult
	command   AIDocumentCommand
	operation sharedtelemetry.AuditItemOperation
}

// ExecuteAIDocument is the Program Event hard-cut DCDP mutation seam. It
// locks the root, selects Edit or EditArchived from that locked lifecycle,
// performs one fully-consistent SpiceDB decision, and only then invokes the
// adapter compiler. The compiler, revision CAS and persistence share the same
// owning-domain transaction; validation deliberately rolls that transaction
// back after executing the identical path.
func (s *Service) ExecuteAIDocument(ctx context.Context, command ExecuteAIDocumentCommand) (AIDocumentResult, error) {
	if command.Compiler == nil {
		return AIDocumentResult{}, domainerr.DependencyUnavailable("Program Event AI document compiler")
	}
	if command.Mode != AIDocumentExecutionValidate && command.Mode != AIDocumentExecutionApply {
		return AIDocumentResult{}, domainerr.InvalidField("mode", "is not supported")
	}
	locale, err := validateProgramEventAIDocumentIdentity(command.EventID, command.Locale)
	if err != nil {
		return AIDocumentResult{}, err
	}
	var root DocumentRoot
	var validated AIDocumentResult
	var compiled AIDocumentCommand
	change, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[aiDocumentChange, Store]{
		Lock: func(ctx context.Context, store Store) error {
			var err error
			root, err = loadProgramEventAIDocumentRoot(ctx, store, command.EventID, UpdateLock)
			return err
		},
		Access: mutation.Fenced(func(ctx context.Context, _ Store, lockActive func(*auth.UserInfo) (bool, error)) error {
			if err := authorizeLockedProgramEvent(ctx, lockActive, s.spiceDB, root.ID, root.Status, "edit", policyv1.ProgramEvent.Edit); err != nil {
				return programEventAIDocumentMutationAccessError(command.EventID, err)
			}
			if principal := auth.GetUser(ctx); principal == nil || principal.MemberID == "" {
				return domainerr.ResourceNotFound("program event", command.EventID)
			}
			return nil
		}),
		Change: func(ctx context.Context, store Store) (aiDocumentChange, error) {
			change, err := compileAndApplyAIDocument(ctx, store, root, locale, command.Compiler)
			compiled = change.command
			if err == nil && command.Mode == AIDocumentExecutionValidate {
				validated = change.result
				return aiDocumentChange{}, errRollbackAIDocumentValidation
			}
			return change, err
		},
		Audit: func(change aiDocumentChange) mutation.Audit {
			if !change.result.Changed {
				return mutation.Unchanged()
			}
			return localeContentAudit(change.command.ContributorMemberID.String(), change.command.EventID, change.command.RequestedLocale, change.operation)
		},
		Outbox: func(ctx context.Context, store Store, change aiDocumentChange) error {
			if !change.result.Changed {
				return nil
			}
			return store.PublishContentUpdate(ctx, aiDocumentContentUpdate(change.command, change.result))
		},
	})
	if errors.Is(err, errRollbackAIDocumentValidation) {
		return validated, nil
	}
	if err != nil {
		return AIDocumentResult{}, aiDocumentFailure(err, compiled)
	}
	return change.result, nil
}

// compileAndApplyAIDocument hands the compiler the state loaded after the
// root lock and authorization, then checks and persists its command.
func compileAndApplyAIDocument(
	ctx context.Context,
	store Store,
	root DocumentRoot,
	locale string,
	compiler AIDocumentCommandCompiler,
) (aiDocumentChange, error) {
	principal := auth.GetUser(ctx)
	state, err := loadAIDocumentStateAfterAuthorization(ctx, store, root, locale, principal.MemberID.String())
	if err != nil {
		return aiDocumentChange{}, err
	}
	command, err := compiler(state)
	if err != nil {
		return aiDocumentChange{}, &programEventAIDocumentCompilerError{cause: err}
	}
	if err := validateCompiledProgramEventAIDocumentCommand(state, command); err != nil {
		return aiDocumentChange{command: command}, err
	}
	change, err := applyAIDocumentCommandAfterAuthorizationInTransaction(ctx, store, root, state, command)
	change.command = command
	return change, err
}

// aiDocumentFailure answers a failed AI document mutation: the adapter's own
// compiler error, a revision conflict with the current tokens, or the
// normalized content document failure.
func aiDocumentFailure(err error, command AIDocumentCommand) error {
	var compilerErr *programEventAIDocumentCompilerError
	var stale *contentblock.StaleRevisionError
	var target *translation.TargetRevisionConflict
	switch {
	case errors.As(err, &compilerErr):
		return compilerErr.cause
	case errors.As(err, &stale):
		return &aidocument.RevisionConflict{
			Kind:                    aidocument.ConflictDocumentRevision,
			CurrentDocumentRevision: stale.CurrentRevision.String(),
			Message:                 fmt.Sprintf("Program Event AI document revision changed: current revision is %s", stale.CurrentRevision.String()),
		}
	case errors.As(err, &target):
		var current *string
		if target.CurrentExists {
			value := target.CurrentRevision
			current = &value
		}
		return &aidocument.RevisionConflict{
			Kind:                    aidocument.ConflictTargetRevision,
			CurrentDocumentRevision: command.ExpectedRevision.String(),
			CurrentTargetRevision:   current,
			Message:                 fmt.Sprintf("Program Event AI document revision changed: current revision is %s", command.ExpectedRevision.String()),
		}
	default:
		return normalizeProgramEventContentBlockError(err)
	}
}

func programEventAIDocumentMutationAccessError(eventID string, err error) error {
	if rejected(err, domainerr.Unauthenticated, domainerr.PermissionDenied) {
		return domainerr.ResourceNotFound("program event", eventID).WithCause(err)
	}
	return err
}

func validateCompiledProgramEventAIDocumentCommand(
	state AIDocumentState,
	command AIDocumentCommand,
) error {
	if command.ExpectedRevision == uuid.Nil || command.ContributorMemberID == uuid.Nil ||
		command.EventID != state.EventID || command.RequestedLocale != state.RequestedLocale ||
		command.ObservedSourceLocale != state.SourceLocale ||
		command.ObservedLocaleExists != state.LocaleExists ||
		command.ExpectedRevision.String() != state.DocumentRevision ||
		!programEventOptionalStringEqual(command.ExpectedTargetRevision, state.TargetRevision) ||
		command.ContributorMemberID.String() != state.ViewerMemberID {
		return domainerr.InvalidField(
			"mutation",
			"compiled Program Event identity, locale, contributor, source observation and revision must match the locked state",
		)
	}
	if command.Batch != nil && (command.Batch.DocumentID != state.ContentDocumentID ||
		command.Batch.ExpectedRevision != command.ExpectedRevision ||
		len(command.Batch.ContributorMemberIDs) != 1 ||
		command.Batch.ContributorMemberIDs[0] != command.ContributorMemberID) {
		return domainerr.InvalidField("operations", "compiled Program Event content batch must match the locked state")
	}
	if command.Metadata.SetTitle && command.RequestedLocale != state.SourceLocale {
		return domainerr.InvalidField("title", "Program Event title is source-owned")
	}
	return nil
}

func programEventOptionalStringEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func applyAIDocumentCommandAfterAuthorizationInTransaction(
	ctx context.Context,
	store Store,
	root DocumentRoot,
	lockedState AIDocumentState,
	command AIDocumentCommand,
) (aiDocumentChange, error) {
	sourceState, err := checkAIDocumentCommand(ctx, store, root, lockedState, command)
	if err != nil {
		return aiDocumentChange{}, err
	}
	locale := command.RequestedLocale
	now := time.Now().UTC()
	input := aiDocumentWrite{
		command: command, documentID: lockedState.ContentDocumentID, now: now,
		fence: programEventAuthorizedAIDocumentFence(root, lockedState, sourceState),
	}
	var output programEventTargetLocaleMutationResult
	switch {
	case command.CreateTranslation:
		output, err = createAIDocumentTranslation(ctx, store, input)
	case command.DeleteTranslation:
		output.Result, err = deleteProgramEventTargetLocale(
			ctx, store, command.EventID, input.documentID, locale,
			command.ExpectedRevision, command.ExpectedTargetRevision,
			[]uuid.UUID{command.ContributorMemberID}, now, input.fence,
		)
	default:
		output, err = applyAIDocumentBatch(ctx, store, input, sourceState.SourceLocale)
	}
	if err != nil {
		return aiDocumentChange{}, err
	}
	var targetRevision *string
	if !command.DeleteTranslation && locale != sourceState.SourceLocale {
		targetRevision = &output.TargetRevision
	}
	result := AIDocumentResult{DocumentRevision: output.Result.DocumentRevision.String(), TargetRevision: targetRevision}
	if !output.Result.Changed {
		return aiDocumentChange{result: result}, nil
	}
	result.Changed = true
	// A source edit is audited like a collaboration source save.
	operation := sharedtelemetry.AuditItemOperationUpdated
	if locale == sourceState.SourceLocale {
		if err := store.TouchEvent(ctx, command.EventID, now); err != nil {
			return aiDocumentChange{}, err
		}
	} else {
		operation = programEventTargetLocaleContentOperation(
			command.CreateTranslation, command.DeleteTranslation, lockedState.LocaleExists,
		)
	}
	return aiDocumentChange{result: result, operation: operation}, nil
}

// checkAIDocumentCommand repeats the source observation, revision and locale
// presence checks of command against the locked state and locks its
// contributor. It returns the locked source locale.
func checkAIDocumentCommand(
	ctx context.Context,
	store Store,
	root DocumentRoot,
	lockedState AIDocumentState,
	command AIDocumentCommand,
) (programEventSourceState, error) {
	if command.ExpectedRevision == uuid.Nil || command.ContributorMemberID == uuid.Nil {
		return programEventSourceState{}, domainerr.InvalidField("revision", "expected revision and contributor Member are required")
	}
	lifecycle := command.CreateTranslation || command.DeleteTranslation
	if (command.CreateTranslation && command.DeleteTranslation) ||
		(lifecycle && (command.Batch != nil || command.Metadata.SetTitle || command.Metadata.SetSummary)) ||
		(!lifecycle && command.Batch == nil) {
		return programEventSourceState{}, domainerr.InvalidField("operations", "translation lifecycle must be exclusive and content mutations require one compiled batch")
	}
	sourceState, err := loadProgramEventSourceLocale(ctx, store, command.EventID, true)
	if err != nil {
		return programEventSourceState{}, err
	}
	if root.SourceLocale != sourceState.SourceLocale {
		return programEventSourceState{}, domainerr.Precondition("Program Event source locale changed; reload before editing")
	}
	localeExists := lockedState.LocaleExists
	if lockedState.DocumentRevision != command.ExpectedRevision.String() ||
		sourceState.SourceLocale != command.ObservedSourceLocale || localeExists != command.ObservedLocaleExists {
		return programEventSourceState{}, &aidocument.RevisionConflict{
			Kind:                    aidocument.ConflictDocumentRevision,
			CurrentDocumentRevision: lockedState.DocumentRevision,
			CurrentTargetRevision:   lockedState.TargetRevision,
			Message:                 fmt.Sprintf("Program Event AI document revision changed: current revision is %s", lockedState.DocumentRevision),
		}
	}
	locale := command.RequestedLocale
	switch {
	case locale == sourceState.SourceLocale && !localeExists:
		return programEventSourceState{}, domainerr.Precondition("Program Event source locale metadata is not initialized")
	case lifecycle && locale == sourceState.SourceLocale:
		return programEventSourceState{}, domainerr.InvalidField("locale", "source Program Event translation cannot be created or deleted")
	case command.CreateTranslation && localeExists:
		return programEventSourceState{}, domainerr.InvalidField("locale", "Program Event translation already exists")
	case command.DeleteTranslation && !localeExists:
		return programEventSourceState{}, domainerr.InvalidField("locale", "Program Event translation does not exist")
	}
	if err := requireDocumentContributors(ctx, store, []string{command.ContributorMemberID.String()}); err != nil {
		return programEventSourceState{}, err
	}
	return sourceState, nil
}

// aiDocumentWrite is a checked AI document command and the authorized fence
// its content writes use.
type aiDocumentWrite struct {
	command    AIDocumentCommand
	documentID uuid.UUID
	now        time.Time
	fence      ContentFence
}

func createAIDocumentTranslation(ctx context.Context, store Store, input aiDocumentWrite) (programEventTargetLocaleMutationResult, error) {
	command := input.command
	return applyProgramEventTargetLocaleMutation(ctx, store, programEventTargetLocaleMutationInput{
		EventID: command.EventID, DocumentID: input.documentID, Locale: command.RequestedLocale,
		Batch: contentblock.Batch{
			DocumentID: input.documentID, ExpectedRevision: command.ExpectedRevision,
			ContributorMemberIDs: []uuid.UUID{command.ContributorMemberID},
		},
		ExpectedDocumentRevision: command.ExpectedRevision,
		ExpectedTargetRevision:   command.ExpectedTargetRevision,
		Policy:                   targetlocale.Policy{AllowCreate: true, SeedSource: true}, Now: input.now, Fence: input.fence,
	})
}

// applyAIDocumentBatch persists the compiled content batch and metadata of
// a source or target locale.
func applyAIDocumentBatch(
	ctx context.Context,
	store Store,
	input aiDocumentWrite,
	sourceLocale string,
) (programEventTargetLocaleMutationResult, error) {
	command := input.command
	locale := command.RequestedLocale
	batch := *command.Batch
	if batch.DocumentID != input.documentID || batch.ExpectedRevision != command.ExpectedRevision {
		return programEventTargetLocaleMutationResult{}, domainerr.InvalidField("operations", "compiled Program Event content identity or revision mismatch")
	}
	if len(batch.ContributorMemberIDs) != 1 || batch.ContributorMemberIDs[0] != command.ContributorMemberID {
		return programEventTargetLocaleMutationResult{}, domainerr.InvalidField("operations", "compiled Program Event contributor mismatch")
	}
	if locale != sourceLocale && (len(batch.Upserts) != 0 || len(batch.Deletes) != 0 || len(batch.Reorders) != 0) {
		return programEventTargetLocaleMutationResult{}, domainerr.InvalidField("operations", "non-source Program Event locale cannot change the Block graph")
	}
	for _, group := range batch.LocaleGroups {
		if group.Locale != locale {
			return programEventTargetLocaleMutationResult{}, domainerr.InvalidField("operations", "Program Event content batch contains another locale")
		}
	}
	if locale != sourceLocale {
		return applyProgramEventTargetLocaleMutation(ctx, store, programEventTargetLocaleMutationInput{
			EventID: command.EventID, DocumentID: input.documentID, Locale: locale,
			Batch: batch, ExpectedDocumentRevision: command.ExpectedRevision,
			ExpectedTargetRevision: command.ExpectedTargetRevision,
			Policy:                 targetlocale.Policy{AllowCreate: true, SeedSource: true}, Now: input.now, Fence: input.fence,
			SetSummary: command.Metadata.SetSummary, Summary: command.Metadata.Summary,
		})
	}
	if command.ExpectedTargetRevision != nil {
		return programEventTargetLocaleMutationResult{}, domainerr.InvalidField("expected_target_revision", "must be omitted for the source locale")
	}
	var result contentblock.Result
	var err error
	if command.Metadata.SetTitle || command.Metadata.SetSummary {
		result, err = store.ApplyContentBatchWithMetadata(ctx, batch, input.fence,
			func(ctx context.Context, store Store) (contentblock.MetadataEffect, error) {
				changed, sourceChanged, err := applyProgramEventLocaleMetadataMutation(
					ctx, store, command.EventID, locale, command.Metadata.Title, command.Metadata.SetTitle,
					command.Metadata.Summary, command.Metadata.SetSummary, input.now,
				)
				return contentblock.MetadataEffect{Changed: changed, AffectsTranslationSource: sourceChanged, ChangedLocales: []string{locale}}, err
			})
	} else {
		result, err = store.ApplyContentBatch(ctx, batch, input.fence)
	}
	return programEventTargetLocaleMutationResult{Result: result}, err
}

func programEventAuthorizedAIDocumentFence(
	root DocumentRoot,
	lockedState AIDocumentState,
	sourceState programEventSourceState,
) ContentFence {
	return func(_ context.Context, _ Store, requestedDocumentID uuid.UUID) (contentblock.DomainContext, error) {
		if root.ID == "" || root.ContentDocumentID == nil ||
			strings.TrimSpace(*root.ContentDocumentID) != lockedState.ContentDocumentID.String() ||
			requestedDocumentID != lockedState.ContentDocumentID {
			return contentblock.DomainContext{}, domainerr.Precondition(
				"Program Event content document changed; reload before saving",
			)
		}
		return contentblock.DomainContext{
			SourceLocale: sourceState.SourceLocale,
		}, nil
	}
}
