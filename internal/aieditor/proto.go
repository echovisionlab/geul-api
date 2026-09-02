package aieditor

import (
	"errors"

	"github.com/echovisionlab/geul-api/internal/aidocument"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func domainFromProto(value managev1.AIDocumentDomain) (aidocument.Domain, bool) {
	domain, ok := map[managev1.AIDocumentDomain]aidocument.Domain{
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_POST:           aidocument.DomainPost,
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_PAGE:           aidocument.DomainPage,
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_WORK:           aidocument.DomainWork,
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_PROGRAM_EVENT:  aidocument.DomainProgramEvent,
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_MENU:           aidocument.DomainMenu,
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_EMAIL_TEMPLATE: aidocument.DomainEmailTemplate,
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_EMAIL_LAYOUT:   aidocument.DomainEmailLayout,
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_CAMPAIGN:       aidocument.DomainCampaign,
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_FORM:           aidocument.DomainForm,
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_PRIVACY:        aidocument.DomainPrivacy,
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_TERMS:          aidocument.DomainTerms,
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_POST_SERIES:    aidocument.DomainPostSeries,
	}[value]
	return domain, ok
}

func domainToProto(value aidocument.Domain) managev1.AIDocumentDomain {
	return map[aidocument.Domain]managev1.AIDocumentDomain{
		aidocument.DomainPost:          managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_POST,
		aidocument.DomainPage:          managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_PAGE,
		aidocument.DomainWork:          managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_WORK,
		aidocument.DomainProgramEvent:  managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_PROGRAM_EVENT,
		aidocument.DomainMenu:          managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_MENU,
		aidocument.DomainEmailTemplate: managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_EMAIL_TEMPLATE,
		aidocument.DomainEmailLayout:   managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_EMAIL_LAYOUT,
		aidocument.DomainCampaign:      managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_CAMPAIGN,
		aidocument.DomainForm:          managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_FORM,
		aidocument.DomainPrivacy:       managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_PRIVACY,
		aidocument.DomainTerms:         managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_TERMS,
		aidocument.DomainPostSeries:    managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_POST_SERIES,
	}[value]
}

func mutationToProto(value aidocument.ApplyRequest) *managev1.AIDocumentMutation {
	result := &managev1.AIDocumentMutation{
		ProtocolVersion: value.Protocol,
		Document: &managev1.AIDocumentReference{
			Domain: domainToProto(value.Profile), Reference: string(value.Document),
		},
		Locale:                   &managev1.AIDocumentLocale{Code: string(value.Locale)},
		ExpectedDocumentRevision: string(value.ExpectedDocumentRevision),
		Operations:               operationsToProto(value.Operations),
	}
	if value.ExpectedTargetRevision != nil {
		targetRevision := string(*value.ExpectedTargetRevision)
		result.ExpectedTargetRevision = &targetRevision
	}
	return result
}

func operationsToProto(values []aidocument.Operation) []*managev1.AIDocumentOperation {
	result := make([]*managev1.AIDocumentOperation, 0, len(values))
	for _, value := range values {
		result = append(result, operationToProto(value))
	}
	return result
}

func operationToProto(value aidocument.Operation) *managev1.AIDocumentOperation {
	result := &managev1.AIDocumentOperation{}
	switch value.Kind {
	case aidocument.OperationSetField:
		result.Operation = &managev1.AIDocumentOperation_SetField{SetField: &managev1.AIDocumentSetFieldOperation{
			Target: fieldTargetToProto(value.SetField.Target), Value: valueToProto(value.SetField.Value),
		}}
	case aidocument.OperationUnsetField:
		result.Operation = &managev1.AIDocumentOperation_UnsetField{UnsetField: &managev1.AIDocumentUnsetFieldOperation{Target: fieldTargetToProto(value.UnsetField.Target)}}
	case aidocument.OperationInsertBlock:
		op := value.InsertBlock
		result.Operation = &managev1.AIDocumentOperation_InsertBlock{InsertBlock: &managev1.AIDocumentInsertBlockOperation{
			BlockHandle: string(op.Block), Kind: string(op.Kind), ParentBlockHandle: optionalString(string(op.Parent)), AfterBlockHandle: optionalString(string(op.After)),
		}}
	case aidocument.OperationDeleteBlock:
		result.Operation = &managev1.AIDocumentOperation_DeleteBlock{DeleteBlock: &managev1.AIDocumentDeleteBlockOperation{BlockHandle: string(value.DeleteBlock.Block)}}
	case aidocument.OperationMoveBlock:
		op := value.MoveBlock
		result.Operation = &managev1.AIDocumentOperation_MoveBlock{MoveBlock: &managev1.AIDocumentMoveBlockOperation{
			BlockHandle: string(op.Block), ParentBlockHandle: optionalString(string(op.Parent)), AfterBlockHandle: optionalString(string(op.After)),
		}}
	case aidocument.OperationReplaceBlockKind:
		result.Operation = &managev1.AIDocumentOperation_ReplaceBlockKind{ReplaceBlockKind: &managev1.AIDocumentReplaceBlockKindOperation{BlockHandle: string(value.ReplaceBlockKind.Block), Kind: string(value.ReplaceBlockKind.Kind)}}
	case aidocument.OperationInsertRelationItem:
		op := value.InsertRelationItem
		result.Operation = &managev1.AIDocumentOperation_InsertRelationItem{InsertRelationItem: &managev1.AIDocumentInsertRelationItemOperation{
			BlockHandle: string(op.Block), RelationHandle: string(op.Relation), ItemHandle: string(op.Item), ItemKind: string(op.Kind), AfterItemHandle: optionalString(string(op.After)),
		}}
	case aidocument.OperationDeleteRelationItem:
		op := value.DeleteRelationItem
		result.Operation = &managev1.AIDocumentOperation_DeleteRelationItem{DeleteRelationItem: &managev1.AIDocumentDeleteRelationItemOperation{Item: relationItemToProto(op.Block, op.Relation, op.Item)}}
	case aidocument.OperationMoveRelationItem:
		op := value.MoveRelationItem
		result.Operation = &managev1.AIDocumentOperation_MoveRelationItem{MoveRelationItem: &managev1.AIDocumentMoveRelationItemOperation{
			Item: relationItemToProto(op.Block, op.Relation, op.Item), DestinationBlockHandle: string(op.TargetBlock), DestinationRelationHandle: string(op.Target), AfterItemHandle: optionalString(string(op.After)),
		}}
	case aidocument.OperationAttachFile:
		result.Operation = &managev1.AIDocumentOperation_AttachFile{AttachFile: &managev1.AIDocumentAttachFileOperation{Target: fieldTargetToProto(value.AttachFile.Target), FileHandle: string(value.AttachFile.File)}}
	case aidocument.OperationDetachFile:
		result.Operation = &managev1.AIDocumentOperation_DetachFile{DetachFile: &managev1.AIDocumentDetachFileOperation{Target: fieldTargetToProto(value.DetachFile.Target)}}
	case aidocument.OperationCreateTranslation:
		result.Operation = &managev1.AIDocumentOperation_CreateTranslation{CreateTranslation: &managev1.AIDocumentCreateTranslationOperation{}}
	case aidocument.OperationDeleteTranslation:
		result.Operation = &managev1.AIDocumentOperation_DeleteTranslation{DeleteTranslation: &managev1.AIDocumentDeleteTranslationOperation{}}
	}
	return result
}

func fieldTargetToProto(value aidocument.FieldTarget) *managev1.AIDocumentFieldTarget {
	result := &managev1.AIDocumentFieldTarget{FieldHandle: string(value.Field), Path: fieldPathToProto(value.Path)}
	if value.Relation != "" || value.Item != "" {
		result.Owner = &managev1.AIDocumentFieldTarget_RelationItem{RelationItem: relationItemToProto(value.Block, value.Relation, value.Item)}
	} else {
		result.Owner = &managev1.AIDocumentFieldTarget_BlockHandle{BlockHandle: string(value.Block)}
	}
	return result
}

func fieldPathToProto(values []aidocument.FieldPathSegment) []*managev1.AIDocumentFieldPathSegment {
	result := make([]*managev1.AIDocumentFieldPathSegment, 0, len(values))
	for _, value := range values {
		segment := &managev1.AIDocumentFieldPathSegment{}
		if value.Field != "" {
			segment.Selector = &managev1.AIDocumentFieldPathSegment_FieldHandle{FieldHandle: string(value.Field)}
		} else {
			segment.Selector = &managev1.AIDocumentFieldPathSegment_ItemHandle{ItemHandle: string(value.Item)}
		}
		result = append(result, segment)
	}
	return result
}

func relationItemToProto(block aidocument.BlockID, relation aidocument.RelationID, item aidocument.RelationItemID) *managev1.AIDocumentRelationItemReference {
	return &managev1.AIDocumentRelationItemReference{BlockHandle: string(block), RelationHandle: string(relation), ItemHandle: string(item)}
}

func valueToProto(value aidocument.Value) *managev1.AIDocumentValue {
	result := &managev1.AIDocumentValue{}
	switch value.Kind {
	case aidocument.ValueKindText:
		result.Value = &managev1.AIDocumentValue_Text{Text: value.Text}
	case aidocument.ValueKindBoolean:
		result.Value = &managev1.AIDocumentValue_Boolean{Boolean: value.Boolean}
	case aidocument.ValueKindNumber:
		result.Value = &managev1.AIDocumentValue_Number{Number: value.Text}
	case aidocument.ValueKindInline:
		result.Value = &managev1.AIDocumentValue_Inline{Inline: &managev1.AIDocumentInlineContent{Items: inlineItemsToProto(value.Inline)}}
	case aidocument.ValueKindList:
		list := &managev1.AIDocumentListValue{}
		for _, item := range value.List {
			list.Items = append(list.Items, &managev1.AIDocumentListItem{ItemHandle: string(item.ID), Value: valueToProto(item.Value)})
		}
		result.Value = &managev1.AIDocumentValue_List{List: list}
	case aidocument.ValueKindObject:
		object := &managev1.AIDocumentObjectValue{}
		for _, field := range value.Object {
			object.Fields = append(object.Fields, &managev1.AIDocumentFieldValue{FieldHandle: string(field.ID), Value: valueToProto(field.Value)})
		}
		result.Value = &managev1.AIDocumentValue_Object{Object: object}
	}
	return result
}

func inlineItemsToProto(values []aidocument.InlineItem) []*managev1.AIDocumentInlineItem {
	result := make([]*managev1.AIDocumentInlineItem, 0, len(values))
	for _, value := range values {
		item := &managev1.AIDocumentInlineItem{}
		switch value.Kind {
		case aidocument.InlineKindText:
			item.Item = &managev1.AIDocumentInlineItem_Text{Text: value.Text}
		case aidocument.InlineKindBold, aidocument.InlineKindItalic, aidocument.InlineKindUnderline,
			aidocument.InlineKindStrike, aidocument.InlineKindCode, aidocument.InlineKindTextColor, aidocument.InlineKindBackground:
			mark := map[aidocument.InlineKind]string{
				aidocument.InlineKindBold: "bold", aidocument.InlineKindItalic: "italic", aidocument.InlineKindUnderline: "underline",
				aidocument.InlineKindStrike: "strike", aidocument.InlineKindCode: "code", aidocument.InlineKindTextColor: "textColor", aidocument.InlineKindBackground: "backgroundColor",
			}[value.Kind]
			message := &managev1.AIDocumentInlineMark{Mark: mark, Children: inlineItemsToProto(value.Children)}
			if value.Kind == aidocument.InlineKindTextColor || value.Kind == aidocument.InlineKindBackground {
				message.Parameter = valueToProto(aidocument.Text(value.Target))
			}
			item.Item = &managev1.AIDocumentInlineItem_Mark{Mark: message}
		case aidocument.InlineKindLink:
			item.Item = &managev1.AIDocumentInlineItem_Link{Link: &managev1.AIDocumentInlineLink{Target: value.Target, Children: inlineItemsToProto(value.Children)}}
		case aidocument.InlineKindHardBreak:
			item.Item = &managev1.AIDocumentInlineItem_HardBreak{HardBreak: &managev1.AIDocumentHardBreak{}}
		case aidocument.InlineKindMath:
			item.Item = &managev1.AIDocumentInlineItem_Math{Math: value.Text}
		case aidocument.InlineKindPlaceholder:
			item.Item = &managev1.AIDocumentInlineItem_PlaceholderHandle{PlaceholderHandle: value.Text}
		}
		result = append(result, item)
	}
	return result
}

func acceptedMutationToProto(value aidocument.ApplyResult) *managev1.AIDocumentAcceptedMutation {
	result := &managev1.AIDocumentAcceptedMutation{DocumentRevision: string(value.DocumentRevision)}
	if value.TargetRevision != nil {
		targetRevision := string(*value.TargetRevision)
		result.TargetRevision = &targetRevision
	}
	for _, change := range value.Changes {
		result.Changes = append(result.Changes, &managev1.AIDocumentAcceptedChange{
			OperationIndex: uint32(change.Operation), Kind: operationKindToProto(change.Kind), AffectedHandles: append([]string(nil), change.AffectedHandles...),
		})
	}
	return result
}

func operationKindToProto(value aidocument.OperationKind) managev1.AIDocumentOperationKind {
	return map[aidocument.OperationKind]managev1.AIDocumentOperationKind{
		aidocument.OperationSetField:           managev1.AIDocumentOperationKind_AI_DOCUMENT_OPERATION_KIND_SET_FIELD,
		aidocument.OperationUnsetField:         managev1.AIDocumentOperationKind_AI_DOCUMENT_OPERATION_KIND_UNSET_FIELD,
		aidocument.OperationInsertBlock:        managev1.AIDocumentOperationKind_AI_DOCUMENT_OPERATION_KIND_INSERT_BLOCK,
		aidocument.OperationDeleteBlock:        managev1.AIDocumentOperationKind_AI_DOCUMENT_OPERATION_KIND_DELETE_BLOCK,
		aidocument.OperationMoveBlock:          managev1.AIDocumentOperationKind_AI_DOCUMENT_OPERATION_KIND_MOVE_BLOCK,
		aidocument.OperationReplaceBlockKind:   managev1.AIDocumentOperationKind_AI_DOCUMENT_OPERATION_KIND_REPLACE_BLOCK_KIND,
		aidocument.OperationAttachFile:         managev1.AIDocumentOperationKind_AI_DOCUMENT_OPERATION_KIND_ATTACH_FILE,
		aidocument.OperationDetachFile:         managev1.AIDocumentOperationKind_AI_DOCUMENT_OPERATION_KIND_DETACH_FILE,
		aidocument.OperationCreateTranslation:  managev1.AIDocumentOperationKind_AI_DOCUMENT_OPERATION_KIND_CREATE_TRANSLATION,
		aidocument.OperationDeleteTranslation:  managev1.AIDocumentOperationKind_AI_DOCUMENT_OPERATION_KIND_DELETE_TRANSLATION,
		aidocument.OperationInsertRelationItem: managev1.AIDocumentOperationKind_AI_DOCUMENT_OPERATION_KIND_INSERT_RELATION_ITEM,
		aidocument.OperationDeleteRelationItem: managev1.AIDocumentOperationKind_AI_DOCUMENT_OPERATION_KIND_DELETE_RELATION_ITEM,
		aidocument.OperationMoveRelationItem:   managev1.AIDocumentOperationKind_AI_DOCUMENT_OPERATION_KIND_MOVE_RELATION_ITEM,
	}[value]
}

func rejectedMutationToProto(err error) (*managev1.AIDocumentValidation, bool) {
	var validationError *aidocument.ValidationError
	if errors.As(err, &validationError) {
		return validationToProto(validationError.Result), true
	}
	var conflictError *aidocument.ConflictError
	if errors.As(err, &conflictError) {
		return validationToProto(aidocument.ValidationResult{Conflict: &conflictError.Conflict}), true
	}
	return nil, false
}

func validationToProto(value aidocument.ValidationResult) *managev1.AIDocumentValidation {
	result := &managev1.AIDocumentValidation{NormalizedOperations: operationsToProto(value.Normalized)}
	for _, issue := range value.Issues {
		converted := &managev1.AIDocumentIssue{OperationIndex: uint32(max(issue.Operation, 0)), Code: issueCodeToProto(issue.Code)}
		converted.Handle = optionalString(issue.Handle)
		result.Issues = append(result.Issues, converted)
	}
	if value.Conflict != nil {
		code := map[aidocument.ConflictCode]managev1.AIDocumentConflictCode{
			aidocument.ConflictDocumentRevision: managev1.AIDocumentConflictCode_AI_DOCUMENT_CONFLICT_CODE_DOCUMENT_REVISION,
			aidocument.ConflictTargetRevision:   managev1.AIDocumentConflictCode_AI_DOCUMENT_CONFLICT_CODE_TARGET_REVISION,
		}[value.Conflict.Code]
		result.Conflict = &managev1.AIDocumentConflict{
			Code: code, CurrentDocumentRevision: string(value.Conflict.CurrentDocumentRevision),
			AffectedHandles: append([]string(nil), value.Conflict.AffectedHandles...),
		}
		if value.Conflict.CurrentTargetRevision != nil {
			targetRevision := string(*value.Conflict.CurrentTargetRevision)
			result.Conflict.CurrentTargetRevision = &targetRevision
		}
	}
	return result
}

func issueCodeToProto(value aidocument.IssueCode) managev1.AIDocumentIssueCode {
	return map[aidocument.IssueCode]managev1.AIDocumentIssueCode{
		aidocument.IssueInvalidOperation:            managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_INVALID_OPERATION,
		aidocument.IssueUnknownBlock:                managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_UNKNOWN_BLOCK,
		aidocument.IssueDuplicateBlock:              managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_DUPLICATE_BLOCK,
		aidocument.IssueUnknownBlockKind:            managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_UNKNOWN_BLOCK_KIND,
		aidocument.IssueUnknownField:                managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_UNKNOWN_FIELD,
		aidocument.IssueValueKindMismatch:           managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_VALUE_KIND_MISMATCH,
		aidocument.IssueSourceAuthorityRequired:     managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_SOURCE_AUTHORITY_REQUIRED,
		aidocument.IssueTargetFieldForbidden:        managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_TARGET_FIELD_FORBIDDEN,
		aidocument.IssueInvalidBlockRelation:        managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_INVALID_BLOCK_RELATION,
		aidocument.IssueBlockCycle:                  managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_BLOCK_CYCLE,
		aidocument.IssueInvalidFileReference:        managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_INVALID_FILE_REFERENCE,
		aidocument.IssueTranslationIsSource:         managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_TRANSLATION_IS_SOURCE,
		aidocument.IssueTranslationAlreadyExists:    managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_TRANSLATION_ALREADY_EXISTS,
		aidocument.IssueTranslationMissing:          managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_TRANSLATION_MISSING,
		aidocument.IssueLocaleOperationNotExclusive: managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_LOCALE_OPERATION_NOT_EXCLUSIVE,
		aidocument.IssueUnknownRelation:             managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_UNKNOWN_RELATION,
		aidocument.IssueUnknownRelationItem:         managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_UNKNOWN_RELATION_ITEM,
		aidocument.IssueDuplicateRelationItem:       managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_DUPLICATE_RELATION_ITEM,
		aidocument.IssueInvalidRelationItemMove:     managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_INVALID_RELATION_ITEM_MOVE,
	}[value]
}
