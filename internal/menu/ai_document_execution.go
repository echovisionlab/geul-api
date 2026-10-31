package menu

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/optional"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type AIDocumentOperationKind string

const (
	AIDocumentNoop              AIDocumentOperationKind = "noop"
	AIDocumentSetName           AIDocumentOperationKind = "set_name"
	AIDocumentSetItemField      AIDocumentOperationKind = "set_item_field"
	AIDocumentUnsetItemField    AIDocumentOperationKind = "unset_item_field"
	AIDocumentInsertItem        AIDocumentOperationKind = "insert_item"
	AIDocumentDeleteItem        AIDocumentOperationKind = "delete_item"
	AIDocumentMoveItem          AIDocumentOperationKind = "move_item"
	AIDocumentCreateTranslation AIDocumentOperationKind = "create_translation"
	AIDocumentDeleteTranslation AIDocumentOperationKind = "delete_translation"
)

type AIDocumentValueKind string

const (
	AIDocumentText     AIDocumentValueKind = "text"
	AIDocumentBoolean  AIDocumentValueKind = "boolean"
	AIDocumentTextList AIDocumentValueKind = "text_list"
)

type AIDocumentValue struct {
	Kind    AIDocumentValueKind
	Text    string
	Boolean bool
	Texts   []string
}

// AIDocumentOperation is Menu's closed mutation vocabulary. ParentID equal to
// the Menu ID means a top-level item; every other parent is a stable item ID.
type AIDocumentOperation struct {
	Kind     AIDocumentOperationKind
	ItemID   string
	ParentID string
	AfterID  string
	Field    string
	Value    AIDocumentValue
}

type AIDocumentIssue struct {
	Operation int
	Code      AIDocumentIssueCode
	Handle    string
	Message   string
}

type AIDocumentIssueCode string

const (
	AIDocumentIssueInvalidOperation AIDocumentIssueCode = "invalid_operation"
	AIDocumentIssueTargetForbidden  AIDocumentIssueCode = "target_forbidden"
)

type AIDocumentApply struct {
	MenuID                   string
	Locale                   string
	ExpectedDocumentRevision string
	ExpectedTargetRevision   *string
	AffectedHandles          []string
	Operations               []AIDocumentOperation
}

type AIDocumentApplyResult struct {
	DocumentRevision string
	TargetRevision   *string
	Changed          bool
}

// AIDocumentMutationAction is the one exact Menu decision made before the
// authorized aggregate is exposed to the schema adapter compiler.
type AIDocumentMutationAction uint8

const (
	AIDocumentMutationEdit AIDocumentMutationAction = iota + 1
	AIDocumentMutationManage
)

func (action AIDocumentMutationAction) authorizationAction() (menuAction, error) {
	switch action {
	case AIDocumentMutationEdit:
		return policyv1.Menu.Edit, nil
	case AIDocumentMutationManage:
		return policyv1.Menu.Manage, nil
	default:
		return nil, fmt.Errorf("unsupported Menu AI document mutation action %d", action)
	}
}

type AIDocumentExecutionMode uint8

const (
	AIDocumentExecutionValidate AIDocumentExecutionMode = iota
	AIDocumentExecutionApply
)

// AIDocumentMutationCompiler is invoked only after the Menu root and active
// principal are locked and its exact Edit or Manage decision succeeds.
type AIDocumentMutationCompiler func(AIDocumentSnapshot) (AIDocumentApply, error)

type menuAIDocumentCompilerError struct{ cause error }

func (e *menuAIDocumentCompilerError) Error() string { return e.cause.Error() }
func (e *menuAIDocumentCompilerError) Unwrap() error { return e.cause }

// AIDocumentValidationError preserves Menu's operation-indexed domain issues
// across the adapter boundary.
type AIDocumentValidationError struct{ Issues []AIDocumentIssue }

func (e *AIDocumentValidationError) Error() string {
	return "Menu AI document operations are invalid"
}

type AIDocumentRevisionConflict struct {
	Target                  bool
	CurrentDocumentRevision string
	CurrentTargetRevision   *string
	AffectedHandles         []string
}

func (e *AIDocumentRevisionConflict) Error() string {
	return "Menu AI document revision conflict"
}

var errRollbackMenuAIDocumentValidation = errors.New("rollback Menu AI document validation")

// LoadAIDocument enforces Menu's exact object read boundary and projects one
// source or target locale without read-time fallback values.
func (s *Service) LoadAIDocument(
	ctx context.Context,
	menuID string,
	locale string,
) (AIDocumentSnapshot, error) {
	if err := s.requireMenuPermissionOrNotFound(ctx, menuID, policyv1.Menu.View); err != nil {
		return AIDocumentSnapshot{}, err
	}
	return loadMenuAIDocumentSnapshot(ctx, s.db, menuID, locale, false)
}

// ExecuteAIDocumentMutation is Menu's exact DCDP mutation boundary. It locks
// the root first, performs one exact Edit or Manage decision, and only then
// exposes the authorized snapshot to the adapter compiler. Validate executes
// this identical path, deliberately rolls the transaction back and returns an
// empty result.
func (s *Service) ExecuteAIDocumentMutation(
	ctx context.Context,
	menuID string,
	locale string,
	action AIDocumentMutationAction,
	mode AIDocumentExecutionMode,
	compiler AIDocumentMutationCompiler,
) (AIDocumentApplyResult, error) {
	menuID, locale = strings.TrimSpace(menuID), strings.TrimSpace(locale)
	if menuID == "" || locale == "" {
		return AIDocumentApplyResult{}, domainerr.InvalidField("document", "Menu ID and locale are required")
	}
	authorizationAction, err := action.authorizationAction()
	if err != nil {
		return AIDocumentApplyResult{}, domainerr.InvalidField("action", "is not supported").WithCause(err)
	}
	if mode != AIDocumentExecutionValidate && mode != AIDocumentExecutionApply {
		return AIDocumentApplyResult{}, domainerr.InvalidField("mode", "is not supported")
	}
	if compiler == nil {
		return AIDocumentApplyResult{}, domainerr.DependencyUnavailable("Menu AI document compiler")
	}

	var root *model.Menu
	var mutated menuAIDocumentMutation
	result, err := mutation.Run(ctx, s.mutations, mutation.Spec[AIDocumentApplyResult]{
		// The root is locked before the one exact Edit or Manage decision; a
		// caller without it learns nothing about the Menu.
		Access: mutation.Locked(func(ctx context.Context, tx *gorm.DB, require mutation.Require) error {
			var err error
			if root, err = lockMenuForUpdate(ctx, tx, menuID); err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return domainerr.ResourceNotFound("menu", menuID)
				}
				return err
			}
			if err := requireFreshMenuPermission(ctx, tx, require, menuID, authorizationAction); err != nil {
				if mutation.IsDenied(err) || mutation.IsUnauthenticated(err) {
					return domainerr.ResourceNotFound("menu", menuID).WithCause(err)
				}
				return err
			}
			return nil
		}),
		Change: func(ctx context.Context, tx *gorm.DB) (AIDocumentApplyResult, error) {
			var err error
			if mutated, err = s.mutateAIDocument(ctx, tx, *root, locale, action, compiler); err != nil {
				return AIDocumentApplyResult{}, err
			}
			if mode == AIDocumentExecutionValidate {
				return AIDocumentApplyResult{}, errRollbackMenuAIDocumentValidation
			}
			return mutated.result, nil
		},
		Audit: func(AIDocumentApplyResult) mutation.Audit {
			if !mutated.compiled.changed {
				return mutation.Unchanged()
			}
			return menuAIDocumentAudit(mutated.locked, mutated.compiled)
		},
		Outbox: func(ctx context.Context, tx *gorm.DB, result AIDocumentApplyResult) error {
			return contentsignal.PublishInteractive(ctx, tx, managev1.ContentEntityType_CONTENT_ENTITY_TYPE_MENU, mutated.command.MenuID, mutated.command.Locale, result.DocumentRevision, result.TargetRevision, result.Changed, mutated.compiled.deleteTranslation, contentsignal.InteractiveMemberID(ctx))
		},
	})
	if errors.Is(err, errRollbackMenuAIDocumentValidation) {
		// The rolled-back revision never becomes current. Like every AI
		// document domain's Validate, a dry run reports validity only.
		return AIDocumentApplyResult{}, nil
	}
	if err != nil {
		var compilerErr *menuAIDocumentCompilerError
		if errors.As(err, &compilerErr) {
			return AIDocumentApplyResult{}, compilerErr.cause
		}
		return AIDocumentApplyResult{}, err
	}
	return result, nil
}

// menuAIDocumentMutation is one compiled and persisted AI document mutation.
type menuAIDocumentMutation struct {
	locked   AIDocumentSnapshot
	command  AIDocumentApply
	compiled compiledMenuAIDocument
	result   AIDocumentApplyResult
}

// mutateAIDocument exposes the authorized root to the adapter compiler,
// checks the compiled command against the locked state and persists it.
func (s *Service) mutateAIDocument(
	ctx context.Context,
	tx *gorm.DB,
	root model.Menu,
	locale string,
	action AIDocumentMutationAction,
	compiler AIDocumentMutationCompiler,
) (menuAIDocumentMutation, error) {
	locked, err := loadMenuAIDocumentSnapshotFromRoot(ctx, tx, root, locale, true)
	if err != nil {
		return menuAIDocumentMutation{}, err
	}
	command, err := compiler(locked)
	if err != nil {
		return menuAIDocumentMutation{}, &menuAIDocumentCompilerError{cause: err}
	}
	if err := requireCompiledMenuCommand(locked, command, action); err != nil {
		return menuAIDocumentMutation{}, err
	}
	compiled, lockedIssues := s.compileAIDocument(locked, command.Operations)
	if len(lockedIssues) != 0 {
		return menuAIDocumentMutation{}, &AIDocumentValidationError{Issues: append([]AIDocumentIssue(nil), lockedIssues...)}
	}
	if compiled.sourceChanged {
		protoItems := modelItemsToProto(aiDocumentItemsToModel(compiled.snapshot.Items))
		if err := s.targets.ValidateAndLock(ctx, tx, collectMenuTargetReferences(protoItems)); err != nil {
			return menuAIDocumentMutation{}, err
		}
	}
	mutated := menuAIDocumentMutation{locked: locked, command: command, compiled: compiled}
	if !compiled.changed {
		mutated.result = AIDocumentApplyResult{
			DocumentRevision: locked.DocumentRevision,
			TargetRevision:   optional.Clone(locked.TargetRevision),
		}
		return mutated, nil
	}
	if err := s.persistAIDocument(ctx, tx, locked, compiled, time.Now().UTC()); err != nil {
		return menuAIDocumentMutation{}, err
	}
	next, err := loadMenuAIDocumentSnapshot(ctx, tx, command.MenuID, command.Locale, true)
	if err != nil {
		return menuAIDocumentMutation{}, err
	}
	if next.DocumentRevision == locked.DocumentRevision && optional.Equal(next.TargetRevision, locked.TargetRevision) {
		return menuAIDocumentMutation{}, errors.New("menu AI document mutation did not advance its authoritative revision")
	}
	mutated.result = AIDocumentApplyResult{
		DocumentRevision: next.DocumentRevision,
		TargetRevision:   optional.Clone(next.TargetRevision),
		Changed:          true,
	}
	return mutated, nil
}

// requireCompiledMenuCommand rejects a compiled command for another Menu,
// locale or action, or one compiled against stale revisions.
func requireCompiledMenuCommand(locked AIDocumentSnapshot, command AIDocumentApply, action AIDocumentMutationAction) error {
	if command.MenuID != locked.ID || command.Locale != locked.Locale {
		return domainerr.InvalidField("mutation", "compiled Menu identity and locale must match the locked state")
	}
	if menuAIDocumentAction(command.Operations) != action {
		return domainerr.InvalidField("mutation", "compiled Menu operations must match the authorized mutation action")
	}
	if locked.DocumentRevision != command.ExpectedDocumentRevision {
		return &AIDocumentRevisionConflict{
			CurrentDocumentRevision: locked.DocumentRevision,
			CurrentTargetRevision:   optional.Clone(locked.TargetRevision),
			AffectedHandles:         append([]string(nil), command.AffectedHandles...),
		}
	}
	if !optional.Equal(locked.TargetRevision, command.ExpectedTargetRevision) {
		return &AIDocumentRevisionConflict{
			Target: true, CurrentDocumentRevision: locked.DocumentRevision,
			CurrentTargetRevision: optional.Clone(locked.TargetRevision),
			AffectedHandles:       append([]string(nil), command.AffectedHandles...),
		}
	}
	return nil
}

// persistAIDocument writes a changed compiled document: the source items,
// the source label values and the content document revision they advance,
// or the target locale labels. A collaboration save shares it.
func (s *Service) persistAIDocument(ctx context.Context, tx *gorm.DB, previous AIDocumentSnapshot, compiled compiledMenuAIDocument, now time.Time) error {
	if compiled.sourceChanged {
		if err := s.persistAIDocumentSource(ctx, tx, previous, compiled.snapshot, now); err != nil {
			return err
		}
	}
	if compiled.sourceValuesChanged {
		if err := persistMenuAIDocumentSourceValues(ctx, tx, previous, compiled.snapshot, now); err != nil {
			return err
		}
	}
	if compiled.sourceChanged || compiled.sourceValuesChanged {
		expected, err := parseMenuContentDocumentUUID(previous.DocumentRevision, "content_document.revision")
		if err != nil {
			return err
		}
		if err := advanceMenuContentDocument(ctx, tx, previous.ID, previous.contentDocumentID, expected, now); err != nil {
			return err
		}
	}
	if compiled.targetChanged {
		return persistMenuAIDocumentTarget(ctx, tx, previous, compiled.snapshot, compiled.deleteTranslation, now)
	}
	return nil
}

func menuAIDocumentAction(operations []AIDocumentOperation) AIDocumentMutationAction {
	for _, operation := range operations {
		switch operation.Kind {
		case AIDocumentInsertItem, AIDocumentDeleteItem, AIDocumentMoveItem:
			return AIDocumentMutationManage
		}
	}
	return AIDocumentMutationEdit
}
