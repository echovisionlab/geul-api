// Package aidocumentadapter translates the public generated DCDP/1 service
// contract to the schema-independent AI document application core.
package aidocumentadapter

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

type Application interface {
	Open(context.Context, core.OpenRequest) (core.OpenMetadata, error)
	Apply(context.Context, core.ApplyRequest) (core.ApplyResult, error)
}

// Service contains transport conversion only. Authentication, authorization,
// lifecycle checks, and aggregate loading remain in the injected application
// and owning-domain port.
type Service struct {
	managev1connect.UnimplementedAIDocumentServiceHandler
	application Application
}

func NewService(application Application) (*Service, error) {
	if application == nil {
		return nil, errors.New("AI document application is required")
	}
	return &Service{application: application}, nil
}

func (s *Service) OpenAIDocument(ctx context.Context, request *connect.Request[managev1.OpenAIDocumentRequest]) (*connect.Response[managev1.OpenAIDocumentResponse], error) {
	if request == nil || request.Msg == nil {
		return nil, invalidArgument(errors.New("request is required"))
	}
	document, err := documentFromProto(request.Msg.Document)
	if err != nil {
		return nil, invalidArgument(err)
	}
	locale, err := localeFromProto(request.Msg.Locale)
	if err != nil {
		return nil, invalidArgument(err)
	}
	metadata, err := s.application.Open(ctx, core.OpenRequest{Document: document, Locale: locale})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.OpenAIDocumentResponse{Metadata: metadataToProto(metadata)}), nil
}

func (s *Service) ApplyAIDocumentOperations(ctx context.Context, request *connect.Request[managev1.ApplyAIDocumentOperationsRequest]) (*connect.Response[managev1.ApplyAIDocumentOperationsResponse], error) {
	if request == nil || request.Msg == nil {
		return nil, invalidArgument(errors.New("request is required"))
	}
	mutation, err := applyRequestFromProto(request.Msg.Mutation)
	if err != nil {
		return nil, invalidArgument(err)
	}
	result, err := s.application.Apply(ctx, mutation)
	if err != nil {
		var validationError *core.ValidationError
		if errors.As(err, &validationError) {
			validation, conversionErr := validationToProto(validationError.Result)
			if conversionErr != nil {
				return nil, fmt.Errorf("convert AI document rejection: %w", conversionErr)
			}
			return connect.NewResponse(&managev1.ApplyAIDocumentOperationsResponse{Result: &managev1.ApplyAIDocumentOperationsResponse_Rejected{Rejected: validation}}), nil
		}
		var conflictError *core.ConflictError
		if errors.As(err, &conflictError) {
			validation, conversionErr := validationToProto(core.ValidationResult{Conflict: &conflictError.Conflict})
			if conversionErr != nil {
				return nil, fmt.Errorf("convert AI document conflict: %w", conversionErr)
			}
			return connect.NewResponse(&managev1.ApplyAIDocumentOperationsResponse{Result: &managev1.ApplyAIDocumentOperationsResponse_Rejected{Rejected: validation}}), nil
		}
		return nil, err
	}
	accepted := &managev1.AIDocumentAcceptedMutation{DocumentRevision: string(result.DocumentRevision)}
	if result.TargetRevision != nil {
		targetRevision := string(*result.TargetRevision)
		accepted.TargetRevision = &targetRevision
	}
	for _, change := range result.Changes {
		if change.Operation < 0 {
			return nil, errors.New("application returned a negative accepted operation index")
		}
		kind := operationKindToProto(change.Kind)
		if kind == managev1.AIDocumentOperationKind_AI_DOCUMENT_OPERATION_KIND_UNSPECIFIED {
			return nil, fmt.Errorf("application returned unsupported accepted operation kind %q", change.Kind)
		}
		accepted.Changes = append(accepted.Changes, &managev1.AIDocumentAcceptedChange{
			OperationIndex: uint32(change.Operation), Kind: kind,
			AffectedHandles: append([]string(nil), change.AffectedHandles...),
		})
	}
	return connect.NewResponse(&managev1.ApplyAIDocumentOperationsResponse{Result: &managev1.ApplyAIDocumentOperationsResponse_Accepted{Accepted: accepted}}), nil
}

func validationToProto(value core.ValidationResult) (*managev1.AIDocumentValidation, error) {
	result := &managev1.AIDocumentValidation{NormalizedOperations: operationsToProto(value.Normalized)}
	for _, issue := range value.Issues {
		if issue.Operation < 0 {
			return nil, errors.New("application returned a negative issue operation index")
		}
		code := issueCodeToProto(issue.Code)
		if code == managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_UNSPECIFIED {
			return nil, fmt.Errorf("application returned unsupported issue code %q", issue.Code)
		}
		converted := &managev1.AIDocumentIssue{OperationIndex: uint32(issue.Operation), Code: code}
		if issue.Handle != "" {
			handle := issue.Handle
			converted.Handle = &handle
		}
		result.Issues = append(result.Issues, converted)
	}
	if value.Conflict != nil {
		code := conflictCodeToProto(value.Conflict.Code)
		if code == managev1.AIDocumentConflictCode_AI_DOCUMENT_CONFLICT_CODE_UNSPECIFIED {
			return nil, fmt.Errorf("application returned unsupported conflict code %q", value.Conflict.Code)
		}
		result.Conflict = &managev1.AIDocumentConflict{
			Code: code, CurrentDocumentRevision: string(value.Conflict.CurrentDocumentRevision),
			AffectedHandles: append([]string(nil), value.Conflict.AffectedHandles...),
		}
		if value.Conflict.CurrentTargetRevision != nil {
			targetRevision := string(*value.Conflict.CurrentTargetRevision)
			result.Conflict.CurrentTargetRevision = &targetRevision
		}
	}
	return result, nil
}

func operationKindToProto(value core.OperationKind) managev1.AIDocumentOperationKind {
	mapping := map[core.OperationKind]managev1.AIDocumentOperationKind{
		core.OperationSetField:           managev1.AIDocumentOperationKind_AI_DOCUMENT_OPERATION_KIND_SET_FIELD,
		core.OperationUnsetField:         managev1.AIDocumentOperationKind_AI_DOCUMENT_OPERATION_KIND_UNSET_FIELD,
		core.OperationInsertBlock:        managev1.AIDocumentOperationKind_AI_DOCUMENT_OPERATION_KIND_INSERT_BLOCK,
		core.OperationDeleteBlock:        managev1.AIDocumentOperationKind_AI_DOCUMENT_OPERATION_KIND_DELETE_BLOCK,
		core.OperationMoveBlock:          managev1.AIDocumentOperationKind_AI_DOCUMENT_OPERATION_KIND_MOVE_BLOCK,
		core.OperationReplaceBlockKind:   managev1.AIDocumentOperationKind_AI_DOCUMENT_OPERATION_KIND_REPLACE_BLOCK_KIND,
		core.OperationAttachFile:         managev1.AIDocumentOperationKind_AI_DOCUMENT_OPERATION_KIND_ATTACH_FILE,
		core.OperationDetachFile:         managev1.AIDocumentOperationKind_AI_DOCUMENT_OPERATION_KIND_DETACH_FILE,
		core.OperationCreateTranslation:  managev1.AIDocumentOperationKind_AI_DOCUMENT_OPERATION_KIND_CREATE_TRANSLATION,
		core.OperationDeleteTranslation:  managev1.AIDocumentOperationKind_AI_DOCUMENT_OPERATION_KIND_DELETE_TRANSLATION,
		core.OperationInsertRelationItem: managev1.AIDocumentOperationKind_AI_DOCUMENT_OPERATION_KIND_INSERT_RELATION_ITEM,
		core.OperationDeleteRelationItem: managev1.AIDocumentOperationKind_AI_DOCUMENT_OPERATION_KIND_DELETE_RELATION_ITEM,
		core.OperationMoveRelationItem:   managev1.AIDocumentOperationKind_AI_DOCUMENT_OPERATION_KIND_MOVE_RELATION_ITEM,
	}
	return mapping[value]
}

func issueCodeToProto(value core.IssueCode) managev1.AIDocumentIssueCode {
	mapping := map[core.IssueCode]managev1.AIDocumentIssueCode{
		core.IssueInvalidOperation:            managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_INVALID_OPERATION,
		core.IssueUnknownBlock:                managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_UNKNOWN_BLOCK,
		core.IssueDuplicateBlock:              managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_DUPLICATE_BLOCK,
		core.IssueUnknownBlockKind:            managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_UNKNOWN_BLOCK_KIND,
		core.IssueUnknownField:                managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_UNKNOWN_FIELD,
		core.IssueValueKindMismatch:           managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_VALUE_KIND_MISMATCH,
		core.IssueSourceAuthorityRequired:     managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_SOURCE_AUTHORITY_REQUIRED,
		core.IssueTargetFieldForbidden:        managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_TARGET_FIELD_FORBIDDEN,
		core.IssueInvalidBlockRelation:        managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_INVALID_BLOCK_RELATION,
		core.IssueBlockCycle:                  managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_BLOCK_CYCLE,
		core.IssueInvalidFileReference:        managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_INVALID_FILE_REFERENCE,
		core.IssueTranslationIsSource:         managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_TRANSLATION_IS_SOURCE,
		core.IssueTranslationAlreadyExists:    managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_TRANSLATION_ALREADY_EXISTS,
		core.IssueTranslationMissing:          managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_TRANSLATION_MISSING,
		core.IssueLocaleOperationNotExclusive: managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_LOCALE_OPERATION_NOT_EXCLUSIVE,
		core.IssueUnknownRelation:             managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_UNKNOWN_RELATION,
		core.IssueUnknownRelationItem:         managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_UNKNOWN_RELATION_ITEM,
		core.IssueDuplicateRelationItem:       managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_DUPLICATE_RELATION_ITEM,
		core.IssueInvalidRelationItemMove:     managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_INVALID_RELATION_ITEM_MOVE,
	}
	return mapping[value]
}

func conflictCodeToProto(value core.ConflictCode) managev1.AIDocumentConflictCode {
	switch value {
	case core.ConflictDocumentRevision:
		return managev1.AIDocumentConflictCode_AI_DOCUMENT_CONFLICT_CODE_DOCUMENT_REVISION
	case core.ConflictTargetRevision:
		return managev1.AIDocumentConflictCode_AI_DOCUMENT_CONFLICT_CODE_TARGET_REVISION
	default:
		return managev1.AIDocumentConflictCode_AI_DOCUMENT_CONFLICT_CODE_UNSPECIFIED
	}
}

func invalidArgument(err error) error {
	return connect.NewError(connect.CodeInvalidArgument, err)
}

var _ managev1connect.AIDocumentServiceHandler = (*Service)(nil)
