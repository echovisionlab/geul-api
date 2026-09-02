package programevent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
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
}

type AIDocumentResult struct {
	DocumentRevision string
	TargetRevision   *string
	Changed          bool
}

// AIDocumentConflict preserves the authoritative current CAS token for the
// shared DCDP conflict response.
type AIDocumentConflictKind string

const (
	AIDocumentDocumentRevisionConflict AIDocumentConflictKind = "document_revision"
	AIDocumentTargetRevisionConflict   AIDocumentConflictKind = "target_revision"
)

type AIDocumentConflict struct {
	Kind                    AIDocumentConflictKind
	CurrentDocumentRevision string
	CurrentTargetRevision   *string
}

func (e *AIDocumentConflict) Error() string {
	return fmt.Sprintf("Program Event AI document revision changed: current revision is %s", e.CurrentDocumentRevision)
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

// LoadAIDocumentState applies Program Event account, lifecycle and aggregate
// locking rules before exposing a compact authoring projection. Program Event
// AI authoring uses the exact object read capability selected by lifecycle;
// public reads continue through the public Program Event service.
func (s *ProgramEventService) LoadAIDocumentState(
	ctx context.Context,
	eventID string,
	requestedLocale string,
) (AIDocumentState, error) {
	if s == nil || s.db == nil || s.spiceDB == nil || s.contentBlocks == nil {
		return AIDocumentState{}, errs.DependencyUnavailable("Program Event AI document")
	}
	locale, err := validateProgramEventAIDocumentIdentity(eventID, requestedLocale)
	if err != nil {
		return AIDocumentState{}, err
	}

	var state AIDocumentState
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		root, err := loadProgramEventAIDocumentRoot(ctx, tx, eventID, "SHARE")
		if err != nil {
			return err
		}
		if err := requireActiveProgramEventPrincipal(ctx, tx, "view", true); err != nil {
			return err
		}
		if err := requireProgramEventPermission(
			ctx, s.spiceDB, root.ID, programEventViewAction(root.Status),
		); err != nil {
			return err
		}
		principal := auth.GetUser(ctx)
		if principal == nil || principal.MemberID == "" {
			return errs.AuthenticationRequired()
		}
		state, err = s.loadAIDocumentStateAfterAuthorization(
			ctx, tx, root, locale, principal.MemberID.String(),
		)
		return err
	})
	return state, err
}

func validateProgramEventAIDocumentIdentity(eventID, requestedLocale string) (string, error) {
	if _, err := uuidutil.ParseCanonical(eventID, "event_id"); err != nil {
		return "", errs.InvalidArgument("event_id", "must be a canonical UUID")
	}
	locale := localization.NormalizeExactSupportedLocale(requestedLocale)
	if locale == nil {
		return "", errs.InvalidArgument("locale", "must be an exact canonical locale")
	}
	return *locale, nil
}

func loadProgramEventAIDocumentRoot(
	ctx context.Context,
	tx *gorm.DB,
	eventID string,
	lock string,
) (model.ProgramEvent, error) {
	query := tx.WithContext(ctx).
		Select("id", "content_document_id", "status", "source_locale").
		Where("id = ?", eventID)
	if lock != "" {
		query = query.Clauses(clause.Locking{Strength: lock})
	}
	var root model.ProgramEvent
	if err := query.Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.ProgramEvent{}, errs.NotFound("program event", eventID)
		}
		return model.ProgramEvent{}, errs.Internal(err)
	}
	if root.ContentDocumentID == nil || strings.TrimSpace(*root.ContentDocumentID) == "" {
		return model.ProgramEvent{}, errs.FailedPrecondition("Program Event content document has not been populated")
	}
	return root, nil
}

func (s *ProgramEventService) loadAIDocumentStateAfterAuthorization(
	ctx context.Context,
	tx *gorm.DB,
	root model.ProgramEvent,
	locale string,
	viewerMemberID string,
) (AIDocumentState, error) {
	documentID, err := uuidutil.ParseCanonical(*root.ContentDocumentID, "content_document_id")
	if err != nil {
		return AIDocumentState{}, errs.FailedPrecondition("Program Event content document identity is invalid")
	}
	sourceState, err := loadProgramEventSourceLocale(ctx, tx, root.ID, false)
	if err != nil {
		return AIDocumentState{}, err
	}
	if root.SourceLocale != sourceState.SourceLocale {
		return AIDocumentState{}, errs.FailedPrecondition("Program Event source locale changed; reload before editing")
	}
	localeState, err := loadProgramEventExactLocaleState(
		ctx, tx, s.contentBlocks, root.ID, documentID, locale, false,
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
		return AIDocumentState{}, errs.FailedPrecondition("Program Event source locale metadata is not initialized")
	}
	return AIDocumentState{
		EventID: root.ID, ContentDocumentID: documentID,
		DocumentRevision: localeState.Snapshot.Document.Revision.String(),
		SourceLocale:     sourceState.SourceLocale, RequestedLocale: locale,
		LocaleExists: localeExists, LocalizedDocument: document, ViewerMemberID: viewerMemberID,
		TargetRevision: optionalProgramEventTargetRevision(localeState, locale),
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

// ExecuteAIDocumentCommand is the Program Event hard-cut DCDP mutation seam.
// It locks the root, selects Edit or EditArchived from that locked lifecycle,
// performs one fully-consistent SpiceDB decision, and only then invokes the
// adapter compiler. The compiler, revision CAS and persistence share the same
// owning-domain transaction; validation deliberately rolls that transaction
// back after executing the identical path.
func (s *ProgramEventService) ExecuteAIDocumentCommand(
	ctx context.Context,
	eventID string,
	requestedLocale string,
	mode AIDocumentExecutionMode,
	compiler AIDocumentCommandCompiler,
) (AIDocumentResult, error) {
	if s == nil || s.db == nil || s.spiceDB == nil || s.contentBlocks == nil {
		return AIDocumentResult{}, errs.DependencyUnavailable("Program Event AI document")
	}
	if compiler == nil {
		return AIDocumentResult{}, errs.DependencyUnavailable("Program Event AI document compiler")
	}
	if mode != AIDocumentExecutionValidate && mode != AIDocumentExecutionApply {
		return AIDocumentResult{}, errs.InvalidArgument("mode", "is not supported")
	}
	if mode == AIDocumentExecutionApply && s.asyncPublisher == nil {
		return AIDocumentResult{}, errs.DependencyUnavailable("Program Event content update publisher")
	}
	locale, err := validateProgramEventAIDocumentIdentity(eventID, requestedLocale)
	if err != nil {
		return AIDocumentResult{}, err
	}

	var result AIDocumentResult
	var command AIDocumentCommand
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		root, err := loadProgramEventAIDocumentRoot(ctx, tx, eventID, "UPDATE")
		if err != nil {
			return err
		}
		if err := requireActiveProgramEventPrincipal(ctx, tx, "edit", false); err != nil {
			return programEventAIDocumentMutationAccessError(eventID, err)
		}
		if err := requireProgramEventPermission(
			ctx,
			s.spiceDB,
			root.ID,
			programEventMutationAction(root.Status, policyv1.ProgramEvent.Edit),
		); err != nil {
			return programEventAIDocumentMutationAccessError(eventID, err)
		}
		principal := auth.GetUser(ctx)
		if principal == nil || principal.MemberID == "" {
			return errs.NotFound("program event", eventID)
		}
		state, err := s.loadAIDocumentStateAfterAuthorization(
			ctx,
			tx,
			root,
			locale,
			principal.MemberID.String(),
		)
		if err != nil {
			return err
		}
		command, err = compiler(state)
		if err != nil {
			return &programEventAIDocumentCompilerError{cause: err}
		}
		if err := validateCompiledProgramEventAIDocumentCommand(state, command); err != nil {
			return err
		}
		result, err = s.applyAIDocumentCommandAfterAuthorizationInTransaction(ctx, tx, root, state, command)
		if err != nil {
			return err
		}
		if mode == AIDocumentExecutionValidate {
			return errRollbackAIDocumentValidation
		}
		return nil
	})
	if errors.Is(err, errRollbackAIDocumentValidation) {
		return result, nil
	}
	if err != nil {
		var compilerErr *programEventAIDocumentCompilerError
		var stale *contentblock.StaleRevisionError
		var target *translation.TargetRevisionConflict
		switch {
		case errors.As(err, &compilerErr):
			return AIDocumentResult{}, compilerErr.cause
		case errors.As(err, &stale):
			return AIDocumentResult{}, &AIDocumentConflict{
				Kind:                    AIDocumentDocumentRevisionConflict,
				CurrentDocumentRevision: stale.CurrentRevision.String(),
			}
		case errors.As(err, &target):
			var current *string
			if target.CurrentExists {
				value := target.CurrentRevision
				current = &value
			}
			return AIDocumentResult{}, &AIDocumentConflict{
				Kind:                    AIDocumentTargetRevisionConflict,
				CurrentDocumentRevision: command.ExpectedRevision.String(),
				CurrentTargetRevision:   current,
			}
		default:
			return AIDocumentResult{}, normalizeProgramEventContentBlockError(err)
		}
	}
	if mode == AIDocumentExecutionApply {
		return s.completeAIDocumentApply(ctx, command, result, nil)
	}
	return result, nil
}

func programEventAIDocumentMutationAccessError(eventID string, err error) error {
	switch connect.CodeOf(err) {
	case connect.CodeUnauthenticated, connect.CodePermissionDenied:
		return errs.NotFound("program event", eventID)
	default:
		return err
	}
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
		return errs.InvalidArgument(
			"mutation",
			"compiled Program Event identity, locale, contributor, source observation and revision must match the locked state",
		)
	}
	if command.Batch != nil && (command.Batch.DocumentID != state.ContentDocumentID ||
		command.Batch.ExpectedRevision != command.ExpectedRevision ||
		len(command.Batch.ContributorMemberIDs) != 1 ||
		command.Batch.ContributorMemberIDs[0] != command.ContributorMemberID) {
		return errs.InvalidArgument("operations", "compiled Program Event content batch must match the locked state")
	}
	return nil
}

func programEventOptionalStringEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// completeAIDocumentApply is deliberately outside the transaction callback:
// validation rollbacks and failed/CAS-conflicted transactions cannot reach the
// coalescible runtime signal. Publisher failure follows the existing Program
// Event contract and does not turn a committed mutation into a client error.
func (s *ProgramEventService) completeAIDocumentApply(
	ctx context.Context,
	command AIDocumentCommand,
	result AIDocumentResult,
	commitErr error,
) (AIDocumentResult, error) {
	if commitErr != nil {
		return AIDocumentResult{}, commitErr
	}
	if result.Changed {
		_ = publishContentUpdatedEvent(ctx, s.asyncPublisher, buildProgramEventAIDocumentContentUpdatedEvent(command, result))
	}
	return result, nil
}

func (s *ProgramEventService) applyAIDocumentCommandAfterAuthorizationInTransaction(
	ctx context.Context,
	tx *gorm.DB,
	root model.ProgramEvent,
	lockedState AIDocumentState,
	command AIDocumentCommand,
) (AIDocumentResult, error) {
	if s.contentBlocks == nil {
		return AIDocumentResult{}, errs.DependencyUnavailable("Program Event AI document")
	}
	locale := command.RequestedLocale
	if command.ExpectedRevision == uuid.Nil || command.ContributorMemberID == uuid.Nil {
		return AIDocumentResult{}, errs.InvalidArgument("revision", "expected revision and contributor Member are required")
	}
	lifecycleOperations := 0
	if command.CreateTranslation {
		lifecycleOperations++
	}
	if command.DeleteTranslation {
		lifecycleOperations++
	}
	if lifecycleOperations > 1 || (lifecycleOperations != 0 && command.Batch != nil) || (lifecycleOperations == 0 && command.Batch == nil) {
		return AIDocumentResult{}, errs.InvalidArgument("operations", "translation lifecycle must be exclusive and content mutations require one compiled batch")
	}

	documentID := lockedState.ContentDocumentID
	sourceState, err := loadProgramEventSourceLocale(ctx, tx, command.EventID, true)
	if err != nil {
		return AIDocumentResult{}, err
	}
	if root.SourceLocale != sourceState.SourceLocale {
		return AIDocumentResult{}, errs.FailedPrecondition("Program Event source locale changed; reload before editing")
	}

	localeExists := lockedState.LocaleExists
	if lockedState.DocumentRevision != command.ExpectedRevision.String() ||
		sourceState.SourceLocale != command.ObservedSourceLocale || localeExists != command.ObservedLocaleExists {
		return AIDocumentResult{}, &AIDocumentConflict{
			Kind:                    AIDocumentDocumentRevisionConflict,
			CurrentDocumentRevision: lockedState.DocumentRevision,
			CurrentTargetRevision:   lockedState.TargetRevision,
		}
	}
	if locale == sourceState.SourceLocale && !localeExists {
		return AIDocumentResult{}, errs.FailedPrecondition("Program Event source locale metadata is not initialized")
	}
	if lifecycleOperations != 0 && locale == sourceState.SourceLocale {
		return AIDocumentResult{}, errs.InvalidArgument("locale", "source Program Event translation cannot be created or deleted")
	}
	if command.CreateTranslation && localeExists {
		return AIDocumentResult{}, errs.InvalidArgument("locale", "Program Event translation already exists")
	}
	if command.DeleteTranslation && !localeExists {
		return AIDocumentResult{}, errs.InvalidArgument("locale", "Program Event translation does not exist")
	}
	if err := requireDocumentContributors(ctx, tx, []string{command.ContributorMemberID.String()}); err != nil {
		return AIDocumentResult{}, err
	}

	fence := programEventAuthorizedAIDocumentFence(root, lockedState, sourceState)
	now := time.Now().UTC()
	var blockResult contentblock.Result
	var targetRevision *string
	switch {
	case command.CreateTranslation:
		output, err := applyProgramEventTargetMutation(
			ctx, tx, s.contentBlocks,
			programEventTargetMutationInput{
				EventID: command.EventID, DocumentID: documentID, Locale: locale,
				Batch: contentblock.Batch{
					DocumentID: documentID, ExpectedRevision: command.ExpectedRevision,
					ContributorMemberIDs: []uuid.UUID{command.ContributorMemberID},
				},
				ExpectedDocumentRevision: command.ExpectedRevision,
				ExpectedTargetRevision:   command.ExpectedTargetRevision,
				AllowCreate:              true, SeedSourceOnCreate: true, Now: now, Fence: fence,
			},
		)
		if err != nil {
			return AIDocumentResult{}, err
		}
		blockResult = output.Result
		targetRevision = &output.TargetRevision
	case command.DeleteTranslation:
		blockResult, err = deleteProgramEventTargetLocale(
			ctx, tx, s.contentBlocks, command.EventID, documentID, locale,
			command.ExpectedRevision, command.ExpectedTargetRevision,
			[]uuid.UUID{command.ContributorMemberID}, now, fence,
		)
		if err != nil {
			return AIDocumentResult{}, err
		}
	default:
		batch := *command.Batch
		if batch.DocumentID != documentID || batch.ExpectedRevision != command.ExpectedRevision {
			return AIDocumentResult{}, errs.InvalidArgument("operations", "compiled Program Event content identity or revision mismatch")
		}
		if len(batch.ContributorMemberIDs) != 1 || batch.ContributorMemberIDs[0] != command.ContributorMemberID {
			return AIDocumentResult{}, errs.InvalidArgument("operations", "compiled Program Event contributor mismatch")
		}
		if locale != sourceState.SourceLocale && (len(batch.Upserts) != 0 || len(batch.Deletes) != 0 || len(batch.Reorders) != 0) {
			return AIDocumentResult{}, errs.InvalidArgument("operations", "non-source Program Event locale cannot change the Block graph")
		}
		for _, group := range batch.LocaleGroups {
			if group.Locale != locale {
				return AIDocumentResult{}, errs.InvalidArgument("operations", "Program Event content batch contains another locale")
			}
		}
		if locale == sourceState.SourceLocale {
			if command.ExpectedTargetRevision != nil {
				return AIDocumentResult{}, errs.InvalidArgument("expected_target_revision", "must be omitted for the source locale")
			}
			blockResult, err = s.contentBlocks.ApplyBatch(ctx, tx, batch, fence)
			if err != nil {
				return AIDocumentResult{}, err
			}
		} else {
			output, applyErr := applyProgramEventTargetMutation(
				ctx, tx, s.contentBlocks,
				programEventTargetMutationInput{
					EventID: command.EventID, DocumentID: documentID, Locale: locale,
					Batch: batch, ExpectedDocumentRevision: command.ExpectedRevision,
					ExpectedTargetRevision: command.ExpectedTargetRevision,
					AllowCreate:            true, SeedSourceOnCreate: true, Now: now, Fence: fence,
				},
			)
			if applyErr != nil {
				return AIDocumentResult{}, applyErr
			}
			blockResult = output.Result
			targetRevision = &output.TargetRevision
		}
	}

	if !blockResult.Changed {
		return AIDocumentResult{
			DocumentRevision: blockResult.DocumentRevision.String(), TargetRevision: targetRevision,
		}, nil
	}
	if locale == sourceState.SourceLocale {
		if err := tx.WithContext(ctx).Model(&model.ProgramEvent{}).
			Where("id = ?", command.EventID).
			Update("updated_at", now).Error; err != nil {
			return AIDocumentResult{}, errs.Internal(err)
		}
	}
	if locale != sourceState.SourceLocale {
		if err := appendProgramEventMemberLocaleContentAudit(
			ctx,
			tx,
			s.auditWriter,
			command.ContributorMemberID.String(),
			command.EventID,
			locale,
			programEventTargetLocaleContentOperation(
				command.CreateTranslation,
				command.DeleteTranslation,
				localeExists,
			),
		); err != nil {
			return AIDocumentResult{}, err
		}
	}
	output := AIDocumentResult{
		DocumentRevision: blockResult.DocumentRevision.String(), TargetRevision: targetRevision, Changed: true,
	}
	return output, nil
}

func programEventAuthorizedAIDocumentFence(
	root model.ProgramEvent,
	lockedState AIDocumentState,
	sourceState programEventSourceState,
) contentblock.DomainFence {
	return func(_ context.Context, _ *gorm.DB, requestedDocumentID uuid.UUID) (contentblock.DomainContext, error) {
		if root.ID == "" || root.ContentDocumentID == nil ||
			strings.TrimSpace(*root.ContentDocumentID) != lockedState.ContentDocumentID.String() ||
			requestedDocumentID != lockedState.ContentDocumentID {
			return contentblock.DomainContext{}, errs.FailedPrecondition(
				"Program Event content document changed; reload before saving",
			)
		}
		return contentblock.DomainContext{
			SourceLocale: sourceState.SourceLocale,
		}, nil
	}
}
