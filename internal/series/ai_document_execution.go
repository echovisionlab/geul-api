package series

import (
	"context"
	"errors"
	"strings"

	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"

	"gorm.io/gorm"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type postSeriesAIExecutionMode uint8

const (
	postSeriesAIExecutionValidate postSeriesAIExecutionMode = iota
	postSeriesAIExecutionApply
)

type postSeriesAIAdapterValidationError struct{ result core.ValidationResult }

func (e *postSeriesAIAdapterValidationError) Error() string {
	return "Post Series AI document operations are invalid"
}

var errRollbackPostSeriesAIValidation = errors.New("rollback Post Series AI document validation")

func (s *Service) Load(
	ctx context.Context,
	identity core.DocumentIdentity,
	locale core.Locale,
) (core.Document, error) {
	if err := validatePostSeriesAIDocumentIdentity(identity); err != nil {
		return core.Document{}, err
	}
	var document core.Document
	access := mutation.Locked(func(ctx context.Context, tx *gorm.DB, require mutation.Require) error {
		return s.access.RequirePermissionAndLock(ctx, tx, require, string(identity.Reference), policyv1.PostSeries.View)
	})
	err := s.readLocked(ctx, access, func(ctx context.Context, tx *gorm.DB) error {
		state, err := s.loadAIDocumentState(ctx, tx, string(identity.Reference), string(locale))
		if err != nil {
			return err
		}
		document = state.document(identity, locale)
		return nil
	})
	return document, err
}

func (s *Service) validateAIDocumentDomain(
	ctx context.Context,
	db *gorm.DB,
	loaded core.Document,
	operations []core.Operation,
) ([]core.OperationIssue, error) {
	if err := validatePostSeriesAIDocumentIdentity(loaded.Identity); err != nil {
		return nil, err
	}
	issues := make([]core.OperationIssue, 0)
	for index, operation := range operations {
		issue, err := postSeriesAIOperationIssue(ctx, db, loaded, operation)
		if err != nil {
			return nil, err
		}
		if issue != nil {
			issue.Operation = index
			issues = append(issues, *issue)
		}
	}
	return issues, nil
}

// postSeriesAIOperationIssue is the Series rule an operation breaks, if
// any.
func postSeriesAIOperationIssue(ctx context.Context, db *gorm.DB, loaded core.Document, operation core.Operation) (*core.OperationIssue, error) {
	handle := func() string { return postSeriesAIOperationHandles(operation)[0] }
	issue := func(code core.IssueCode, message string) *core.OperationIssue {
		return &core.OperationIssue{Code: code, Handle: handle(), Message: message}
	}
	switch operation.Kind {
	case core.OperationSetField:
		return postSeriesAISetFieldIssue(ctx, db, loaded, operation.SetField, issue)
	case core.OperationUnsetField:
		return postSeriesAIUnsetFieldIssue(loaded, operation.UnsetField.Target.Field, issue), nil
	case core.OperationInsertRelationItem:
		op := operation.InsertRelationItem
		if err := validatePostSeriesAIRelation(op.Block, op.Relation, op.Item, op.Kind); err != nil {
			return issue(core.IssueInvalidOperation, err.Error()), nil
		}
	case core.OperationDeleteRelationItem:
		op := operation.DeleteRelationItem
		if err := validatePostSeriesAIRelation(op.Block, op.Relation, op.Item, postSeriesAIDocumentPostKind); err != nil {
			return issue(core.IssueInvalidOperation, err.Error()), nil
		}
	case core.OperationMoveRelationItem:
		op := operation.MoveRelationItem
		if op.TargetBlock != postSeriesAIDocumentBlock || op.Target != postSeriesAIDocumentPosts {
			return issue(core.IssueInvalidRelationItemMove, "Post can only move inside the Post Series order"), nil
		}
		if err := validatePostSeriesAIRelation(op.Block, op.Relation, op.Item, postSeriesAIDocumentPostKind); err != nil {
			return issue(core.IssueInvalidOperation, err.Error()), nil
		}
	case core.OperationCreateTranslation, core.OperationDeleteTranslation:
	case core.OperationInsertBlock, core.OperationDeleteBlock, core.OperationMoveBlock,
		core.OperationReplaceBlockKind, core.OperationAttachFile, core.OperationDetachFile:
		return issue(core.IssueInvalidOperation, "Post Series does not support this structural operation"), nil
	default:
		return &core.OperationIssue{Code: core.IssueInvalidOperation, Message: "unsupported Post Series operation"}, nil
	}
	return nil, nil
}

func postSeriesAISetFieldIssue(
	ctx context.Context,
	db *gorm.DB,
	loaded core.Document,
	set *core.SetField,
	issue func(core.IssueCode, string) *core.OperationIssue,
) (*core.OperationIssue, error) {
	switch set.Target.Field {
	case postSeriesAIFieldTitle:
		if loaded.Role() == core.LocaleRoleSource && strings.TrimSpace(set.Value.Text) == "" {
			return issue(core.IssueValueKindMismatch, "Post Series source title is required"), nil
		}
	case postSeriesAIFieldSummary:
	case postSeriesAIFieldSlug:
		normalized, err := validateSeriesSlug(set.Value.Text)
		if err != nil {
			return issue(core.IssueValueKindMismatch, err.Error()), nil
		}
		if err := validateSeriesUpdateSlug(ctx, db, string(loaded.Identity.Reference), &normalized); err != nil {
			return nil, err
		}
	case postSeriesAIFieldStatus:
		if _, err := validateSeriesStatus(set.Value.Text); err != nil {
			return issue(core.IssueValueKindMismatch, err.Error()), nil
		}
	default:
		return issue(core.IssueUnknownField, "unsupported Post Series field"), nil
	}
	return nil, nil
}

func postSeriesAIUnsetFieldIssue(loaded core.Document, field core.FieldID, issue func(core.IssueCode, string) *core.OperationIssue) *core.OperationIssue {
	switch field {
	case postSeriesAIFieldTitle:
		if loaded.Role() == core.LocaleRoleSource {
			return issue(core.IssueValueKindMismatch, "Post Series source title is required")
		}
		return issue(core.IssueInvalidOperation, "Post Series target fields use explicit empty instead of unset")
	case postSeriesAIFieldSummary:
		if loaded.Role() == core.LocaleRoleNonSource {
			return issue(core.IssueInvalidOperation, "Post Series target fields use explicit empty instead of unset")
		}
		return nil
	case postSeriesAIFieldSlug, postSeriesAIFieldStatus:
		return issue(core.IssueInvalidOperation, "Post Series slug and status cannot be unset")
	default:
		return issue(core.IssueUnknownField, "unsupported Post Series field")
	}
}

// ValidateMutation and ExecuteMutation are the hard-cut owning-domain seam
// used by the DCDP application. Both lock the Series root, choose one exact
// action from the requested operation batch, authorize it once, and only then
// normalize and compile against the current aggregate.
func (s *Service) ValidateMutation(
	ctx context.Context,
	request core.ApplyRequest,
) (core.ValidationResult, error) {
	validation, _, err := s.executeMutation(ctx, request, postSeriesAIExecutionValidate)
	return validation, err
}

func (s *Service) ExecuteMutation(
	ctx context.Context,
	request core.ApplyRequest,
) (core.ApplyResult, error) {
	_, result, err := s.executeMutation(ctx, request, postSeriesAIExecutionApply)
	return result, err
}

func (s *Service) executeMutation(
	ctx context.Context,
	request core.ApplyRequest,
	mode postSeriesAIExecutionMode,
) (core.ValidationResult, core.ApplyResult, error) {
	identity := request.Identity()
	if err := validatePostSeriesAIDocumentIdentity(identity); err != nil {
		return core.ValidationResult{}, core.ApplyResult{}, err
	}
	if mode != postSeriesAIExecutionValidate && mode != postSeriesAIExecutionApply {
		return core.ValidationResult{}, core.ApplyResult{}, domainerr.InvalidField("mode", "is not supported")
	}

	var validation core.ValidationResult
	var command core.ValidatedApply
	var audits seriesAudits
	seriesID := string(identity.Reference)
	result, err := mutation.Run(ctx, s.mutations, mutation.Spec[core.ApplyResult]{
		// The root is locked before one exact action chosen from the batch;
		// a caller without it learns nothing about the Series.
		Access: mutation.Locked(func(ctx context.Context, tx *gorm.DB, require mutation.Require) error {
			err := s.access.RequirePermissionAndLock(ctx, tx, require, seriesID, postSeriesAIDocumentAction(request.Operations))
			if mutation.IsDenied(err) || mutation.IsUnauthenticated(err) {
				return domainerr.ResourceNotFound("series", seriesID)
			}
			return err
		}),
		Change: func(ctx context.Context, tx *gorm.DB) (core.ApplyResult, error) {
			var result core.ApplyResult
			var err error
			command, validation, result, err = s.compileAndApplyAIDocument(ctx, tx, request, &audits)
			if err != nil {
				return core.ApplyResult{}, err
			}
			if mode == postSeriesAIExecutionValidate {
				return core.ApplyResult{}, errRollbackPostSeriesAIValidation
			}
			return result, nil
		},
		Audit: func(core.ApplyResult) mutation.Audit { return audits.audit() },
		Outbox: func(ctx context.Context, tx *gorm.DB, result core.ApplyResult) error {
			deletesTranslation := len(command.Operations) == 1 && command.Operations[0].Kind == core.OperationDeleteTranslation
			return contentsignal.PublishInteractive(ctx, tx, managev1.ContentEntityType_CONTENT_ENTITY_TYPE_POST_SERIES, seriesID, string(request.Locale), string(result.DocumentRevision), postSeriesSignalTarget(result.TargetRevision), result.Changed, deletesTranslation, contentsignal.InteractiveMemberID(ctx))
		},
	})
	if errors.Is(err, errRollbackPostSeriesAIValidation) {
		return validation, core.ApplyResult{}, nil
	}
	if err != nil {
		var invalid *postSeriesAIAdapterValidationError
		if errors.As(err, &invalid) {
			if mode == postSeriesAIExecutionValidate {
				return invalid.result, core.ApplyResult{}, nil
			}
			if invalid.result.Conflict != nil {
				return core.ValidationResult{}, core.ApplyResult{}, &core.ConflictError{Conflict: *invalid.result.Conflict}
			}
			return core.ValidationResult{}, core.ApplyResult{}, &core.ValidationError{Result: invalid.result}
		}
		return core.ValidationResult{}, core.ApplyResult{}, err
	}
	return validation, result, nil
}

// compileAndApplyAIDocument validates the request against the locked
// document and the Series rules, then applies it.
func (s *Service) compileAndApplyAIDocument(
	ctx context.Context,
	tx *gorm.DB,
	request core.ApplyRequest,
	audits *seriesAudits,
) (core.ValidatedApply, core.ValidationResult, core.ApplyResult, error) {
	identity := request.Identity()
	state, err := s.loadAIDocumentState(ctx, tx, string(identity.Reference), string(request.Locale))
	if err != nil {
		return core.ValidatedApply{}, core.ValidationResult{}, core.ApplyResult{}, err
	}
	current := state.document(identity, request.Locale)
	command, validation := core.ValidateLoadedApply(current, request)
	if !validation.Valid() {
		return command, validation, core.ApplyResult{}, &postSeriesAIAdapterValidationError{result: validation}
	}
	issues, err := s.validateAIDocumentDomain(ctx, tx, current, command.Operations)
	if err != nil {
		return command, validation, core.ApplyResult{}, err
	}
	if len(issues) != 0 {
		validation.Issues = append(validation.Issues, issues...)
		return command, validation, core.ApplyResult{}, &postSeriesAIAdapterValidationError{result: validation}
	}
	result, err := s.applyAIDocumentCommand(ctx, tx, state, command, audits)
	return command, validation, result, err
}

func (s *Service) applyAIDocumentCommand(
	ctx context.Context,
	tx *gorm.DB,
	state postSeriesAIDocumentState,
	command core.ValidatedApply,
	audits *seriesAudits,
) (core.ApplyResult, error) {
	mutation := postSeriesAIMutation{state: state}
	defer func() { audits.records = append(audits.records, mutation.audits.records...) }()
	for index, operation := range command.Operations {
		changed, err := s.applyAIDocumentOperation(ctx, tx, &mutation, command, index, operation)
		if err != nil {
			return core.ApplyResult{}, err
		}
		if changed {
			mutation.changes = append(mutation.changes, core.Change{
				Operation: index, Kind: operation.Kind,
				AffectedHandles: postSeriesAIOperationHandles(operation),
			})
		}
	}
	// A later metadata assignment can restore the original value. Do not
	// persist or report those intermediate assignments as an aggregate change.
	changes := mutation.changes[:0]
	for _, change := range mutation.changes {
		operation := command.Operations[change.Operation]
		if operation.SetField != nil &&
			(operation.SetField.Target.Field == postSeriesAIFieldSlug && !mutation.slugChanged ||
				operation.SetField.Target.Field == postSeriesAIFieldStatus && !mutation.statusChanged) {
			continue
		}
		changes = append(changes, change)
	}
	mutation.changes = changes
	if len(mutation.changes) == 0 {
		return core.AcceptValidatedApply(command, core.ApplyResult{
			DocumentRevision: state.documentRevision,
			TargetRevision:   clonePostSeriesRevision(state.targetRevision),
		})
	}
	if err := s.persistAIDocumentMutation(ctx, tx, &mutation, command); err != nil {
		return core.ApplyResult{}, err
	}
	next, err := s.loadAIDocumentState(
		ctx, tx, state.root.ID, string(command.Locale),
	)
	if err != nil {
		return core.ApplyResult{}, err
	}
	if next.documentRevision == state.documentRevision &&
		equalPostSeriesRevision(next.targetRevision, state.targetRevision) {
		return core.ApplyResult{}, errors.New("Post Series AI document mutation did not advance an authoritative revision")
	}
	return core.AcceptValidatedApply(command, core.ApplyResult{
		DocumentRevision: next.documentRevision,
		TargetRevision:   clonePostSeriesRevision(next.targetRevision),
		Changed:          true, Changes: mutation.changes,
	})
}

func equalPostSeriesRevision(left, right *core.Revision) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func postSeriesAIDocumentAction(operations []core.Operation) seriesAction {
	action := policyv1.PostSeries.Edit
	for _, operation := range operations {
		switch operation.Kind {
		case core.OperationInsertRelationItem, core.OperationDeleteRelationItem, core.OperationMoveRelationItem:
			return policyv1.PostSeries.Manage
		case core.OperationSetField:
			if operation.SetField != nil && operation.SetField.Target.Field == postSeriesAIFieldStatus {
				action = policyv1.PostSeries.Publish
			}
		case core.OperationUnsetField:
			if operation.UnsetField != nil && operation.UnsetField.Target.Field == postSeriesAIFieldStatus {
				action = policyv1.PostSeries.Publish
			}
		}
	}
	return action
}

func postSeriesSignalTarget(target *core.Revision) *string {
	if target == nil {
		return nil
	}
	value := string(*target)
	return &value
}
