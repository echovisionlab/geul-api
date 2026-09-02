package menu

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
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
func (s *MenuService) LoadAIDocument(
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
// this identical path and deliberately rolls the transaction back.
func (s *MenuService) ExecuteAIDocumentMutation(
	ctx context.Context,
	menuID string,
	locale string,
	action AIDocumentMutationAction,
	mode AIDocumentExecutionMode,
	compiler AIDocumentMutationCompiler,
) (AIDocumentApplyResult, error) {
	if s == nil || s.db == nil || s.permissions == nil || s.targets == nil {
		return AIDocumentApplyResult{}, errs.DependencyUnavailable("Menu AI document")
	}
	menuID, locale = strings.TrimSpace(menuID), strings.TrimSpace(locale)
	if menuID == "" || locale == "" {
		return AIDocumentApplyResult{}, errs.InvalidArgument("document", "Menu ID and locale are required")
	}
	authorizationAction, err := action.authorizationAction()
	if err != nil {
		return AIDocumentApplyResult{}, errs.InvalidArgument("action", "is not supported")
	}
	if mode != AIDocumentExecutionValidate && mode != AIDocumentExecutionApply {
		return AIDocumentApplyResult{}, errs.InvalidArgument("mode", "is not supported")
	}
	if compiler == nil {
		return AIDocumentApplyResult{}, errs.DependencyUnavailable("Menu AI document compiler")
	}

	var result AIDocumentApplyResult
	var command AIDocumentApply
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		root, err := lockMenuForUpdate(ctx, tx, menuID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.NotFound("menu", menuID)
		}
		if err != nil {
			return errs.Internal(err)
		}
		if err := requireFreshMenuPermission(ctx, tx, s.permissions, menuID, authorizationAction); err != nil {
			switch connect.CodeOf(err) {
			case connect.CodeUnauthenticated, connect.CodePermissionDenied:
				return errs.NotFound("menu", menuID)
			default:
				return err
			}
		}
		locked, err := loadMenuAIDocumentSnapshotFromRoot(ctx, tx, *root, locale, true)
		if err != nil {
			return err
		}
		command, err = compiler(locked)
		if err != nil {
			return &menuAIDocumentCompilerError{cause: err}
		}
		if command.MenuID != locked.ID || command.Locale != locked.Locale {
			return errs.InvalidArgument(
				"mutation",
				"compiled Menu identity and locale must match the locked state",
			)
		}
		if menuAIDocumentAction(command.Operations) != action {
			return errs.InvalidArgument(
				"mutation",
				"compiled Menu operations must match the authorized mutation action",
			)
		}
		if locked.DocumentRevision != command.ExpectedDocumentRevision {
			return &AIDocumentRevisionConflict{
				CurrentDocumentRevision: locked.DocumentRevision,
				CurrentTargetRevision:   cloneMenuRevision(locked.TargetRevision),
				AffectedHandles:         append([]string(nil), command.AffectedHandles...),
			}
		}
		if !equalMenuRevision(locked.TargetRevision, command.ExpectedTargetRevision) {
			return &AIDocumentRevisionConflict{
				Target: true, CurrentDocumentRevision: locked.DocumentRevision,
				CurrentTargetRevision: cloneMenuRevision(locked.TargetRevision),
				AffectedHandles:       append([]string(nil), command.AffectedHandles...),
			}
		}
		compiled, lockedIssues := s.compileAIDocument(locked, command.Operations)
		if len(lockedIssues) != 0 {
			return &AIDocumentValidationError{Issues: append([]AIDocumentIssue(nil), lockedIssues...)}
		}
		if compiled.sourceChanged {
			protoItems := s.modelItemsToProto(aiDocumentItemsToModel(compiled.snapshot.Items))
			if err := s.targets.ValidateAndLock(ctx, tx, collectMenuTargetReferences(protoItems)); err != nil {
				return err
			}
		}
		if !compiled.changed {
			result = AIDocumentApplyResult{
				DocumentRevision: locked.DocumentRevision,
				TargetRevision:   cloneMenuRevision(locked.TargetRevision),
			}
		} else {
			now := time.Now().UTC()
			if compiled.sourceChanged {
				if err := s.persistAIDocumentSource(ctx, tx, locked, compiled.snapshot, now); err != nil {
					return err
				}
			}
			if compiled.sourceValuesChanged {
				if err := persistMenuAIDocumentSourceValues(ctx, tx, locked, compiled.snapshot, now); err != nil {
					return err
				}
			}
			if compiled.sourceChanged || compiled.sourceValuesChanged {
				expectedRevision, err := parseMenuContentDocumentUUID(
					locked.DocumentRevision,
					"content_document.revision",
				)
				if err != nil {
					return err
				}
				if _, err := advanceMenuContentDocument(
					ctx,
					tx,
					locked.ID,
					locked.contentDocumentID,
					expectedRevision,
					now,
				); err != nil {
					return err
				}
			}
			if compiled.targetChanged {
				if err := persistMenuAIDocumentTarget(ctx, tx, locked, compiled.snapshot, compiled.deleteTranslation, now); err != nil {
					return err
				}
			}
			if err := appendMenuAIDocumentAudit(
				ctx,
				tx,
				s.auditWriter,
				locked,
				compiled,
			); err != nil {
				return err
			}
			next, err := loadMenuAIDocumentSnapshot(ctx, tx, command.MenuID, command.Locale, true)
			if err != nil {
				return err
			}
			if next.DocumentRevision == locked.DocumentRevision && equalMenuRevision(next.TargetRevision, locked.TargetRevision) {
				return errs.Internal(errors.New("menu AI document mutation did not advance its authoritative revision"))
			}
			result = AIDocumentApplyResult{
				DocumentRevision: next.DocumentRevision,
				TargetRevision:   cloneMenuRevision(next.TargetRevision),
				Changed:          true,
			}
		}
		if mode == AIDocumentExecutionValidate {
			return errRollbackMenuAIDocumentValidation
		}
		return nil
	})
	if errors.Is(err, errRollbackMenuAIDocumentValidation) {
		return result, nil
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

func cloneMenuRevision(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func equalMenuRevision(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
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
