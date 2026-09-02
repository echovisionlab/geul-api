package series

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	errs "github.com/echovisionlab/geul-api/internal/errors"
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

func (s *AIDocumentService) Load(
	ctx context.Context,
	identity core.DocumentIdentity,
	locale core.Locale,
) (core.Document, error) {
	if err := validatePostSeriesAIDocumentIdentity(identity); err != nil {
		return core.Document{}, err
	}
	var document core.Document
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.access.RequirePermissionAndLock(ctx, tx, string(identity.Reference), policyv1.PostSeries.View); err != nil {
			return err
		}
		state, err := s.loadAIDocumentState(ctx, tx, string(identity.Reference), string(locale))
		if err != nil {
			return err
		}
		document = state.document(identity, locale)
		return nil
	})
	return document, err
}

func (s *AIDocumentService) validateAIDocumentDomain(
	ctx context.Context,
	db *gorm.DB,
	loaded core.Document,
	operations []core.Operation,
) ([]core.OperationIssue, error) {
	if err := validatePostSeriesAIDocumentIdentity(loaded.Identity); err != nil {
		return nil, err
	}
	issues := make([]core.OperationIssue, 0)
	issue := func(index int, code core.IssueCode, handle, message string) {
		issues = append(issues, core.OperationIssue{Operation: index, Code: code, Handle: handle, Message: message})
	}
	for index, operation := range operations {
		switch operation.Kind {
		case core.OperationSetField:
			field := operation.SetField.Target.Field
			handle := postSeriesAIOperationHandles(operation)[0]
			switch field {
			case postSeriesAIFieldTitle:
				if loaded.Role() == core.LocaleRoleSource && strings.TrimSpace(operation.SetField.Value.Text) == "" {
					issue(index, core.IssueValueKindMismatch, handle, "Post Series source title is required")
				}
			case postSeriesAIFieldSummary:
			case postSeriesAIFieldSlug:
				normalized, err := validateSeriesSlug(operation.SetField.Value.Text)
				if err != nil {
					issue(index, core.IssueValueKindMismatch, handle, err.Error())
					continue
				}
				if err := validateSeriesUpdateSlug(ctx, db, string(loaded.Identity.Reference), &normalized); err != nil {
					return nil, err
				}
			case postSeriesAIFieldStatus:
				if err := validateSeriesStatus(operation.SetField.Value.Text); err != nil {
					issue(index, core.IssueValueKindMismatch, handle, err.Error())
				}
			default:
				issue(index, core.IssueUnknownField, handle, "unsupported Post Series field")
			}
		case core.OperationUnsetField:
			handle := postSeriesAIOperationHandles(operation)[0]
			switch operation.UnsetField.Target.Field {
			case postSeriesAIFieldTitle:
				if loaded.Role() == core.LocaleRoleSource {
					issue(index, core.IssueValueKindMismatch, handle, "Post Series source title is required")
				} else {
					issue(index, core.IssueInvalidOperation, handle, "Post Series target fields use explicit empty instead of unset")
				}
			case postSeriesAIFieldSummary:
				if loaded.Role() == core.LocaleRoleNonSource {
					issue(index, core.IssueInvalidOperation, handle, "Post Series target fields use explicit empty instead of unset")
				}
			case postSeriesAIFieldSlug, postSeriesAIFieldStatus:
				issue(index, core.IssueInvalidOperation, handle, "Post Series slug and status cannot be unset")
			default:
				issue(index, core.IssueUnknownField, handle, "unsupported Post Series field")
			}
		case core.OperationInsertRelationItem:
			op := operation.InsertRelationItem
			if err := validatePostSeriesAIRelation(op.Block, op.Relation, op.Item, op.Kind); err != nil {
				issue(index, core.IssueInvalidOperation, postSeriesAIOperationHandles(operation)[0], err.Error())
				continue
			}
		case core.OperationDeleteRelationItem:
			op := operation.DeleteRelationItem
			if err := validatePostSeriesAIRelation(op.Block, op.Relation, op.Item, postSeriesAIDocumentPostKind); err != nil {
				issue(index, core.IssueInvalidOperation, postSeriesAIOperationHandles(operation)[0], err.Error())
			}
		case core.OperationMoveRelationItem:
			op := operation.MoveRelationItem
			if op.TargetBlock != postSeriesAIDocumentBlock || op.Target != postSeriesAIDocumentPosts {
				issue(index, core.IssueInvalidRelationItemMove, postSeriesAIOperationHandles(operation)[0], "Post can only move inside the Post Series order")
				continue
			}
			if err := validatePostSeriesAIRelation(op.Block, op.Relation, op.Item, postSeriesAIDocumentPostKind); err != nil {
				issue(index, core.IssueInvalidOperation, postSeriesAIOperationHandles(operation)[0], err.Error())
			}
		case core.OperationCreateTranslation, core.OperationDeleteTranslation:
		case core.OperationInsertBlock, core.OperationDeleteBlock, core.OperationMoveBlock,
			core.OperationReplaceBlockKind, core.OperationAttachFile, core.OperationDetachFile:
			issue(index, core.IssueInvalidOperation, postSeriesAIOperationHandles(operation)[0], "Post Series does not support this structural operation")
		default:
			issue(index, core.IssueInvalidOperation, "", "unsupported Post Series operation")
		}
	}
	return issues, nil
}

// ValidateMutation and ExecuteMutation are the hard-cut owning-domain seam
// used by the DCDP application. Both lock the Series root, choose one exact
// action from the requested operation batch, authorize it once, and only then
// normalize and compile against the current aggregate.
func (s *AIDocumentService) ValidateMutation(
	ctx context.Context,
	request core.ApplyRequest,
) (core.ValidationResult, error) {
	validation, _, err := s.executeMutation(ctx, request, postSeriesAIExecutionValidate)
	return validation, err
}

func (s *AIDocumentService) ExecuteMutation(
	ctx context.Context,
	request core.ApplyRequest,
) (core.ApplyResult, error) {
	_, result, err := s.executeMutation(ctx, request, postSeriesAIExecutionApply)
	return result, err
}

func (s *AIDocumentService) executeMutation(
	ctx context.Context,
	request core.ApplyRequest,
	mode postSeriesAIExecutionMode,
) (core.ValidationResult, core.ApplyResult, error) {
	identity := request.Identity()
	if err := validatePostSeriesAIDocumentIdentity(identity); err != nil {
		return core.ValidationResult{}, core.ApplyResult{}, err
	}
	if mode != postSeriesAIExecutionValidate && mode != postSeriesAIExecutionApply {
		return core.ValidationResult{}, core.ApplyResult{}, errs.InvalidArgument("mode", "is not supported")
	}

	var validation core.ValidationResult
	var command core.ValidatedApply
	var result core.ApplyResult
	seriesID := string(identity.Reference)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.access.RequirePermissionAndLock(
			ctx, tx, seriesID, postSeriesAIDocumentAction(request.Operations),
		); err != nil {
			switch connect.CodeOf(err) {
			case connect.CodeUnauthenticated, connect.CodePermissionDenied:
				return errs.NotFound("series", seriesID)
			default:
				return err
			}
		}
		state, err := s.loadAIDocumentState(ctx, tx, seriesID, string(request.Locale))
		if err != nil {
			return err
		}
		current := state.document(identity, request.Locale)
		command, validation = core.ValidateLoadedApply(current, request)
		if !validation.Valid() {
			return &postSeriesAIAdapterValidationError{result: validation}
		}
		issues, err := s.validateAIDocumentDomain(ctx, tx, current, command.Operations)
		if err != nil {
			return err
		}
		if len(issues) != 0 {
			validation.Issues = append(validation.Issues, issues...)
			return &postSeriesAIAdapterValidationError{result: validation}
		}
		result, err = s.applyAIDocumentCommand(ctx, tx, state, command)
		if err != nil {
			return err
		}
		if mode == postSeriesAIExecutionValidate {
			return errRollbackPostSeriesAIValidation
		}
		return nil
	})
	if errors.Is(err, errRollbackPostSeriesAIValidation) {
		return validation, result, nil
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

func (s *AIDocumentService) applyAIDocumentCommand(
	ctx context.Context,
	tx *gorm.DB,
	state postSeriesAIDocumentState,
	command core.ValidatedApply,
) (core.ApplyResult, error) {
	mutation := postSeriesAIMutation{state: state}
	service := &SeriesService{
		db: s.db, spiceDB: s.spiceDB, permissions: s.spiceDB, menuTargets: s.menuTargets,
		postAccess: s.postAccess,
		ogRefresh:  s.ogRefresh, auditWriter: s.auditWriter,
	}
	for index, operation := range command.Operations {
		changed, err := s.applyAIDocumentOperation(ctx, tx, service, &mutation, command, index, operation)
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
	if len(mutation.changes) == 0 {
		return core.AcceptValidatedApply(command, core.ApplyResult{
			DocumentRevision: state.documentRevision,
			TargetRevision:   clonePostSeriesRevision(state.targetRevision),
		})
	}
	if err := s.persistAIDocumentMutation(ctx, tx, service, &mutation, command); err != nil {
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
		return core.ApplyResult{}, errs.InternalMsg("Post Series AI document mutation did not advance an authoritative revision")
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
