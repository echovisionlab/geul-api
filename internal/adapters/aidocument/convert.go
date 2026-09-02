package aidocumentadapter

import (
	"errors"
	"fmt"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func documentFromProto(value *managev1.AIDocumentReference) (core.DocumentIdentity, error) {
	if value == nil {
		return core.DocumentIdentity{}, errors.New("document is required")
	}
	domain, err := domainFromProto(value.Domain)
	if err != nil {
		return core.DocumentIdentity{}, err
	}
	return core.DocumentIdentity{Domain: domain, Reference: core.DocumentReference(value.Reference)}, nil
}

func documentToProto(value core.DocumentIdentity) *managev1.AIDocumentReference {
	return &managev1.AIDocumentReference{Domain: domainToProto(value.Domain), Reference: string(value.Reference)}
}

func localeFromProto(value *managev1.AIDocumentLocale) (core.Locale, error) {
	if value == nil {
		return "", errors.New("locale is required")
	}
	return core.Locale(value.Code), nil
}

func localeToProto(value core.Locale) *managev1.AIDocumentLocale {
	return &managev1.AIDocumentLocale{Code: string(value)}
}

func domainFromProto(value managev1.AIDocumentDomain) (core.Domain, error) {
	mapping := map[managev1.AIDocumentDomain]core.Domain{
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_POST:           core.DomainPost,
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_PAGE:           core.DomainPage,
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_WORK:           core.DomainWork,
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_PROGRAM_EVENT:  core.DomainProgramEvent,
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_MENU:           core.DomainMenu,
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_EMAIL_TEMPLATE: core.DomainEmailTemplate,
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_EMAIL_LAYOUT:   core.DomainEmailLayout,
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_CAMPAIGN:       core.DomainCampaign,
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_FORM:           core.DomainForm,
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_PRIVACY:        core.DomainPrivacy,
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_TERMS:          core.DomainTerms,
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_POST_SERIES:    core.DomainPostSeries,
	}
	domain, ok := mapping[value]
	if !ok {
		return "", fmt.Errorf("unsupported document domain %q", value)
	}
	return domain, nil
}

func domainToProto(value core.Domain) managev1.AIDocumentDomain {
	mapping := map[core.Domain]managev1.AIDocumentDomain{
		core.DomainPost:          managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_POST,
		core.DomainPage:          managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_PAGE,
		core.DomainWork:          managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_WORK,
		core.DomainProgramEvent:  managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_PROGRAM_EVENT,
		core.DomainMenu:          managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_MENU,
		core.DomainEmailTemplate: managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_EMAIL_TEMPLATE,
		core.DomainEmailLayout:   managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_EMAIL_LAYOUT,
		core.DomainCampaign:      managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_CAMPAIGN,
		core.DomainForm:          managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_FORM,
		core.DomainPrivacy:       managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_PRIVACY,
		core.DomainTerms:         managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_TERMS,
		core.DomainPostSeries:    managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_POST_SERIES,
	}
	return mapping[value]
}

func metadataToProto(value core.OpenMetadata) *managev1.AIDocumentMetadata {
	result := &managev1.AIDocumentMetadata{
		ProtocolVersion:    value.Protocol,
		Document:           documentToProto(value.Identity()),
		CatalogFingerprint: value.Catalog,
		DocumentRevision:   string(value.DocumentRevision),
		SourceLocale:       localeToProto(value.SourceLocale),
		RequestedLocale:    localeToProto(value.Locale),
		LocaleRole:         localeRoleToProto(value.LocaleRole),
		LocaleExists:       value.LocaleExists,
	}
	if value.TargetRevision != nil {
		targetRevision := string(*value.TargetRevision)
		result.TargetRevision = &targetRevision
	}
	return result
}

func localeRoleToProto(value core.LocaleRole) managev1.AIDocumentLocaleRole {
	switch value {
	case core.LocaleRoleSource:
		return managev1.AIDocumentLocaleRole_AI_DOCUMENT_LOCALE_ROLE_SOURCE
	case core.LocaleRoleNonSource:
		return managev1.AIDocumentLocaleRole_AI_DOCUMENT_LOCALE_ROLE_NON_SOURCE
	default:
		return managev1.AIDocumentLocaleRole_AI_DOCUMENT_LOCALE_ROLE_UNSPECIFIED
	}
}

func valueFromProto(value *managev1.AIDocumentValue) (core.Value, error) {
	if value == nil {
		return core.Value{}, errors.New("field value is required")
	}
	switch typed := value.Value.(type) {
	case *managev1.AIDocumentValue_Text:
		return core.Text(typed.Text), nil
	case *managev1.AIDocumentValue_Boolean:
		return core.Boolean(typed.Boolean), nil
	case *managev1.AIDocumentValue_Number:
		return core.Number(typed.Number), nil
	case *managev1.AIDocumentValue_Inline:
		if typed.Inline == nil {
			return core.Value{}, errors.New("inline value is required")
		}
		items, err := inlineItemsFromProto(typed.Inline.Items)
		if err != nil {
			return core.Value{}, err
		}
		return core.RichText(items...), nil
	case *managev1.AIDocumentValue_List:
		if typed.List == nil {
			return core.Value{}, errors.New("list value is required")
		}
		items := make([]core.ListItem, 0, len(typed.List.Items))
		for _, item := range typed.List.Items {
			if item == nil {
				return core.Value{}, errors.New("list item is required")
			}
			converted, err := valueFromProto(item.Value)
			if err != nil {
				return core.Value{}, err
			}
			items = append(items, core.StableItem(core.RelationItemID(item.ItemHandle), converted))
		}
		return core.List(items...), nil
	case *managev1.AIDocumentValue_Object:
		if typed.Object == nil {
			return core.Value{}, errors.New("object value is required")
		}
		fields := make([]core.ObjectField, 0, len(typed.Object.Fields))
		for _, field := range typed.Object.Fields {
			if field == nil {
				return core.Value{}, errors.New("object field is required")
			}
			converted, err := valueFromProto(field.Value)
			if err != nil {
				return core.Value{}, err
			}
			fields = append(fields, core.ObjectValue(core.FieldID(field.FieldHandle), converted))
		}
		return core.Object(fields...), nil
	default:
		return core.Value{}, errors.New("typed field value is required")
	}
}

func valueToProto(value core.Value) *managev1.AIDocumentValue {
	result := &managev1.AIDocumentValue{}
	switch value.Kind {
	case core.ValueKindText:
		result.Value = &managev1.AIDocumentValue_Text{Text: value.Text}
	case core.ValueKindBoolean:
		result.Value = &managev1.AIDocumentValue_Boolean{Boolean: value.Boolean}
	case core.ValueKindNumber:
		result.Value = &managev1.AIDocumentValue_Number{Number: value.Text}
	case core.ValueKindInline:
		result.Value = &managev1.AIDocumentValue_Inline{Inline: &managev1.AIDocumentInlineContent{Items: inlineItemsToProto(value.Inline)}}
	case core.ValueKindList:
		list := &managev1.AIDocumentListValue{}
		for _, item := range value.List {
			list.Items = append(list.Items, &managev1.AIDocumentListItem{ItemHandle: string(item.ID), Value: valueToProto(item.Value)})
		}
		result.Value = &managev1.AIDocumentValue_List{List: list}
	case core.ValueKindObject:
		object := &managev1.AIDocumentObjectValue{}
		for _, field := range value.Object {
			object.Fields = append(object.Fields, &managev1.AIDocumentFieldValue{FieldHandle: string(field.ID), Value: valueToProto(field.Value)})
		}
		result.Value = &managev1.AIDocumentValue_Object{Object: object}
	}
	return result
}

func inlineItemsFromProto(values []*managev1.AIDocumentInlineItem) ([]core.InlineItem, error) {
	result := make([]core.InlineItem, 0, len(values))
	for _, value := range values {
		if value == nil {
			return nil, errors.New("inline item is required")
		}
		var item core.InlineItem
		switch typed := value.Item.(type) {
		case *managev1.AIDocumentInlineItem_Text:
			item = core.InlineText(typed.Text)
		case *managev1.AIDocumentInlineItem_Mark:
			if typed.Mark == nil {
				return nil, errors.New("inline mark is required")
			}
			children, err := inlineItemsFromProto(typed.Mark.Children)
			if err != nil {
				return nil, err
			}
			parameterText := func() (string, error) {
				parameter, err := valueFromProto(typed.Mark.Parameter)
				if err != nil {
					return "", err
				}
				if parameter.Kind != core.ValueKindText {
					return "", errors.New("inline mark parameter must be text")
				}
				return parameter.Text, nil
			}
			switch typed.Mark.Mark {
			case "bold":
				if typed.Mark.Parameter != nil {
					return nil, errors.New("bold mark cannot contain a parameter")
				}
				item = core.Bold(children...)
			case "italic":
				if typed.Mark.Parameter != nil {
					return nil, errors.New("italic mark cannot contain a parameter")
				}
				item = core.Italic(children...)
			case "underline":
				if typed.Mark.Parameter != nil {
					return nil, errors.New("underline mark cannot contain a parameter")
				}
				item = core.Underline(children...)
			case "strike":
				if typed.Mark.Parameter != nil {
					return nil, errors.New("strike mark cannot contain a parameter")
				}
				item = core.Strike(children...)
			case "code":
				if typed.Mark.Parameter != nil {
					return nil, errors.New("code mark cannot contain a parameter")
				}
				item = core.InlineCode(children...)
			case "textColor":
				value, err := parameterText()
				if err != nil {
					return nil, fmt.Errorf("textColor mark: %w", err)
				}
				item = core.TextColor(value, children...)
			case "backgroundColor":
				value, err := parameterText()
				if err != nil {
					return nil, fmt.Errorf("backgroundColor mark: %w", err)
				}
				item = core.BackgroundColor(value, children...)
			default:
				return nil, fmt.Errorf("unsupported inline mark %q", typed.Mark.Mark)
			}
		case *managev1.AIDocumentInlineItem_Link:
			if typed.Link == nil {
				return nil, errors.New("inline link is required")
			}
			children, err := inlineItemsFromProto(typed.Link.Children)
			if err != nil {
				return nil, err
			}
			item = core.Link(typed.Link.Target, children...)
		case *managev1.AIDocumentInlineItem_HardBreak:
			if typed.HardBreak == nil {
				return nil, errors.New("hard break is required")
			}
			item = core.HardBreak()
		case *managev1.AIDocumentInlineItem_Math:
			item = core.InlineMath(typed.Math)
		case *managev1.AIDocumentInlineItem_PlaceholderHandle:
			item = core.Placeholder(typed.PlaceholderHandle)
		default:
			return nil, errors.New("typed inline item is required")
		}
		result = append(result, item)
	}
	if err := core.ValidateInlineItems(result); err != nil {
		return nil, err
	}
	return result, nil
}

func inlineItemsToProto(values []core.InlineItem) []*managev1.AIDocumentInlineItem {
	result := make([]*managev1.AIDocumentInlineItem, 0, len(values))
	for _, value := range values {
		item := &managev1.AIDocumentInlineItem{}
		switch value.Kind {
		case core.InlineKindText:
			item.Item = &managev1.AIDocumentInlineItem_Text{Text: value.Text}
		case core.InlineKindBold, core.InlineKindItalic, core.InlineKindUnderline,
			core.InlineKindStrike, core.InlineKindCode, core.InlineKindTextColor, core.InlineKindBackground:
			mark := map[core.InlineKind]string{
				core.InlineKindBold:       "bold",
				core.InlineKindItalic:     "italic",
				core.InlineKindUnderline:  "underline",
				core.InlineKindStrike:     "strike",
				core.InlineKindCode:       "code",
				core.InlineKindTextColor:  "textColor",
				core.InlineKindBackground: "backgroundColor",
			}[value.Kind]
			message := &managev1.AIDocumentInlineMark{Mark: mark, Children: inlineItemsToProto(value.Children)}
			if value.Kind == core.InlineKindTextColor || value.Kind == core.InlineKindBackground {
				message.Parameter = valueToProto(core.Text(value.Target))
			}
			item.Item = &managev1.AIDocumentInlineItem_Mark{Mark: message}
		case core.InlineKindLink:
			item.Item = &managev1.AIDocumentInlineItem_Link{Link: &managev1.AIDocumentInlineLink{Target: value.Target, Children: inlineItemsToProto(value.Children)}}
		case core.InlineKindHardBreak:
			item.Item = &managev1.AIDocumentInlineItem_HardBreak{HardBreak: &managev1.AIDocumentHardBreak{}}
		case core.InlineKindMath:
			item.Item = &managev1.AIDocumentInlineItem_Math{Math: value.Text}
		case core.InlineKindPlaceholder:
			item.Item = &managev1.AIDocumentInlineItem_PlaceholderHandle{PlaceholderHandle: value.Text}
		}
		result = append(result, item)
	}
	return result
}

func fieldTargetFromProto(value *managev1.AIDocumentFieldTarget) (core.FieldTarget, error) {
	if value == nil {
		return core.FieldTarget{}, errors.New("field target is required")
	}
	result := core.FieldTarget{Field: core.FieldID(value.FieldHandle)}
	switch owner := value.Owner.(type) {
	case *managev1.AIDocumentFieldTarget_BlockHandle:
		result.Block = core.BlockID(owner.BlockHandle)
	case *managev1.AIDocumentFieldTarget_RelationItem:
		if owner.RelationItem == nil {
			return core.FieldTarget{}, errors.New("relation item field owner is required")
		}
		result.Block = core.BlockID(owner.RelationItem.BlockHandle)
		result.Relation = core.RelationID(owner.RelationItem.RelationHandle)
		result.Item = core.RelationItemID(owner.RelationItem.ItemHandle)
	default:
		return core.FieldTarget{}, errors.New("field target owner is required")
	}
	path, err := fieldPathFromProto(value.Path)
	if err != nil {
		return core.FieldTarget{}, err
	}
	result.Path = path
	return result, nil
}

func fieldTargetToProto(value core.FieldTarget) *managev1.AIDocumentFieldTarget {
	result := &managev1.AIDocumentFieldTarget{FieldHandle: string(value.Field), Path: fieldPathToProto(value.Path)}
	if value.Relation != "" || value.Item != "" {
		result.Owner = &managev1.AIDocumentFieldTarget_RelationItem{RelationItem: relationItemReferenceToProto(value.Block, value.Relation, value.Item)}
	} else {
		result.Owner = &managev1.AIDocumentFieldTarget_BlockHandle{BlockHandle: string(value.Block)}
	}
	return result
}

func fieldPathFromProto(values []*managev1.AIDocumentFieldPathSegment) ([]core.FieldPathSegment, error) {
	if len(values) == 0 {
		return nil, nil
	}
	result := make([]core.FieldPathSegment, 0, len(values))
	for _, value := range values {
		if value == nil {
			return nil, errors.New("field path segment is required")
		}
		switch selector := value.Selector.(type) {
		case *managev1.AIDocumentFieldPathSegment_FieldHandle:
			result = append(result, core.ObjectPath(core.FieldID(selector.FieldHandle)))
		case *managev1.AIDocumentFieldPathSegment_ItemHandle:
			result = append(result, core.ListPath(core.RelationItemID(selector.ItemHandle)))
		default:
			return nil, errors.New("typed field path selector is required")
		}
	}
	return result, nil
}

func fieldPathToProto(values []core.FieldPathSegment) []*managev1.AIDocumentFieldPathSegment {
	if len(values) == 0 {
		return nil
	}
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

func relationItemReferenceToProto(block core.BlockID, relation core.RelationID, item core.RelationItemID) *managev1.AIDocumentRelationItemReference {
	return &managev1.AIDocumentRelationItemReference{BlockHandle: string(block), RelationHandle: string(relation), ItemHandle: string(item)}
}

func applyRequestFromProto(value *managev1.AIDocumentMutation) (core.ApplyRequest, error) {
	if value == nil {
		return core.ApplyRequest{}, errors.New("mutation is required")
	}
	document, err := documentFromProto(value.Document)
	if err != nil {
		return core.ApplyRequest{}, err
	}
	locale, err := localeFromProto(value.Locale)
	if err != nil {
		return core.ApplyRequest{}, err
	}
	operations := make([]core.Operation, 0, len(value.Operations))
	for index, operation := range value.Operations {
		converted, err := operationFromProto(operation)
		if err != nil {
			return core.ApplyRequest{}, fmt.Errorf("operation %d: %w", index, err)
		}
		operations = append(operations, converted)
	}
	result := core.ApplyRequest{
		Protocol: value.ProtocolVersion, Profile: document.Domain, Document: document.Reference,
		Locale: locale, ExpectedDocumentRevision: core.Revision(value.ExpectedDocumentRevision), Operations: operations,
	}
	if value.ExpectedTargetRevision != nil {
		targetRevision := core.Revision(*value.ExpectedTargetRevision)
		result.ExpectedTargetRevision = &targetRevision
	}
	return result, nil
}

func operationFromProto(value *managev1.AIDocumentOperation) (core.Operation, error) {
	if value == nil {
		return core.Operation{}, errors.New("operation is required")
	}
	switch typed := value.Operation.(type) {
	case *managev1.AIDocumentOperation_SetField:
		if typed.SetField == nil {
			return core.Operation{}, errors.New("set field operation is required")
		}
		target, err := fieldTargetFromProto(typed.SetField.Target)
		if err != nil {
			return core.Operation{}, err
		}
		fieldValue, err := valueFromProto(typed.SetField.Value)
		if err != nil {
			return core.Operation{}, err
		}
		return core.Operation{Kind: core.OperationSetField, SetField: &core.SetField{Target: target, Value: fieldValue}}, nil
	case *managev1.AIDocumentOperation_UnsetField:
		if typed.UnsetField == nil {
			return core.Operation{}, errors.New("unset field operation is required")
		}
		target, err := fieldTargetFromProto(typed.UnsetField.Target)
		if err != nil {
			return core.Operation{}, err
		}
		return core.Operation{Kind: core.OperationUnsetField, UnsetField: &core.UnsetField{Target: target}}, nil
	case *managev1.AIDocumentOperation_InsertBlock:
		op := typed.InsertBlock
		if op == nil {
			return core.Operation{}, errors.New("insert block operation is required")
		}
		return core.InsertBlockOperation(core.BlockID(op.BlockHandle), core.BlockKind(op.Kind), core.BlockID(op.GetParentBlockHandle()), core.BlockID(op.GetAfterBlockHandle())), nil
	case *managev1.AIDocumentOperation_DeleteBlock:
		if typed.DeleteBlock == nil {
			return core.Operation{}, errors.New("delete block operation is required")
		}
		return core.DeleteBlockOperation(core.BlockID(typed.DeleteBlock.BlockHandle)), nil
	case *managev1.AIDocumentOperation_MoveBlock:
		op := typed.MoveBlock
		if op == nil {
			return core.Operation{}, errors.New("move block operation is required")
		}
		return core.MoveBlockOperation(core.BlockID(op.BlockHandle), core.BlockID(op.GetParentBlockHandle()), core.BlockID(op.GetAfterBlockHandle())), nil
	case *managev1.AIDocumentOperation_ReplaceBlockKind:
		if typed.ReplaceBlockKind == nil {
			return core.Operation{}, errors.New("replace block kind operation is required")
		}
		return core.ReplaceBlockKindOperation(core.BlockID(typed.ReplaceBlockKind.BlockHandle), core.BlockKind(typed.ReplaceBlockKind.Kind)), nil
	case *managev1.AIDocumentOperation_AttachFile:
		if typed.AttachFile == nil {
			return core.Operation{}, errors.New("attach file operation is required")
		}
		target, err := fieldTargetFromProto(typed.AttachFile.Target)
		if err != nil {
			return core.Operation{}, err
		}
		return core.Operation{Kind: core.OperationAttachFile, AttachFile: &core.AttachFile{Target: target, File: core.FileReference(typed.AttachFile.FileHandle)}}, nil
	case *managev1.AIDocumentOperation_DetachFile:
		if typed.DetachFile == nil {
			return core.Operation{}, errors.New("detach file operation is required")
		}
		target, err := fieldTargetFromProto(typed.DetachFile.Target)
		if err != nil {
			return core.Operation{}, err
		}
		return core.Operation{Kind: core.OperationDetachFile, DetachFile: &core.DetachFile{Target: target}}, nil
	case *managev1.AIDocumentOperation_CreateTranslation:
		if typed.CreateTranslation == nil {
			return core.Operation{}, errors.New("create translation operation is required")
		}
		return core.CreateTranslationOperation(), nil
	case *managev1.AIDocumentOperation_DeleteTranslation:
		if typed.DeleteTranslation == nil {
			return core.Operation{}, errors.New("delete translation operation is required")
		}
		return core.DeleteTranslationOperation(), nil
	case *managev1.AIDocumentOperation_InsertRelationItem:
		op := typed.InsertRelationItem
		if op == nil {
			return core.Operation{}, errors.New("insert relation item operation is required")
		}
		return core.InsertRelationItemOperation(core.BlockID(op.BlockHandle), core.RelationID(op.RelationHandle), core.RelationItemID(op.ItemHandle), core.RelationItemKind(op.ItemKind), core.RelationItemID(op.GetAfterItemHandle())), nil
	case *managev1.AIDocumentOperation_DeleteRelationItem:
		op := typed.DeleteRelationItem
		if op == nil || op.Item == nil {
			return core.Operation{}, errors.New("delete relation item reference is required")
		}
		return core.DeleteRelationItemOperation(core.BlockID(op.Item.BlockHandle), core.RelationID(op.Item.RelationHandle), core.RelationItemID(op.Item.ItemHandle)), nil
	case *managev1.AIDocumentOperation_MoveRelationItem:
		op := typed.MoveRelationItem
		if op == nil || op.Item == nil {
			return core.Operation{}, errors.New("move relation item reference is required")
		}
		return core.MoveRelationItemOperation(core.BlockID(op.Item.BlockHandle), core.RelationID(op.Item.RelationHandle), core.RelationItemID(op.Item.ItemHandle), core.BlockID(op.DestinationBlockHandle), core.RelationID(op.DestinationRelationHandle), core.RelationItemID(op.GetAfterItemHandle())), nil
	default:
		return core.Operation{}, errors.New("typed operation is required")
	}
}

func operationsToProto(values []core.Operation) []*managev1.AIDocumentOperation {
	result := make([]*managev1.AIDocumentOperation, 0, len(values))
	for _, value := range values {
		result = append(result, operationToProto(value))
	}
	return result
}

func operationToProto(value core.Operation) *managev1.AIDocumentOperation {
	result := &managev1.AIDocumentOperation{}
	switch value.Kind {
	case core.OperationSetField:
		result.Operation = &managev1.AIDocumentOperation_SetField{SetField: &managev1.AIDocumentSetFieldOperation{Target: fieldTargetToProto(value.SetField.Target), Value: valueToProto(value.SetField.Value)}}
	case core.OperationUnsetField:
		result.Operation = &managev1.AIDocumentOperation_UnsetField{UnsetField: &managev1.AIDocumentUnsetFieldOperation{Target: fieldTargetToProto(value.UnsetField.Target)}}
	case core.OperationInsertBlock:
		op := value.InsertBlock
		protoOperation := &managev1.AIDocumentInsertBlockOperation{BlockHandle: string(op.Block), Kind: string(op.Kind)}
		protoOperation.ParentBlockHandle = optionalString(string(op.Parent))
		protoOperation.AfterBlockHandle = optionalString(string(op.After))
		result.Operation = &managev1.AIDocumentOperation_InsertBlock{InsertBlock: protoOperation}
	case core.OperationDeleteBlock:
		result.Operation = &managev1.AIDocumentOperation_DeleteBlock{DeleteBlock: &managev1.AIDocumentDeleteBlockOperation{BlockHandle: string(value.DeleteBlock.Block)}}
	case core.OperationMoveBlock:
		op := value.MoveBlock
		result.Operation = &managev1.AIDocumentOperation_MoveBlock{MoveBlock: &managev1.AIDocumentMoveBlockOperation{BlockHandle: string(op.Block), ParentBlockHandle: optionalString(string(op.Parent)), AfterBlockHandle: optionalString(string(op.After))}}
	case core.OperationReplaceBlockKind:
		result.Operation = &managev1.AIDocumentOperation_ReplaceBlockKind{ReplaceBlockKind: &managev1.AIDocumentReplaceBlockKindOperation{BlockHandle: string(value.ReplaceBlockKind.Block), Kind: string(value.ReplaceBlockKind.Kind)}}
	case core.OperationAttachFile:
		result.Operation = &managev1.AIDocumentOperation_AttachFile{AttachFile: &managev1.AIDocumentAttachFileOperation{Target: fieldTargetToProto(value.AttachFile.Target), FileHandle: string(value.AttachFile.File)}}
	case core.OperationDetachFile:
		result.Operation = &managev1.AIDocumentOperation_DetachFile{DetachFile: &managev1.AIDocumentDetachFileOperation{Target: fieldTargetToProto(value.DetachFile.Target)}}
	case core.OperationCreateTranslation:
		result.Operation = &managev1.AIDocumentOperation_CreateTranslation{CreateTranslation: &managev1.AIDocumentCreateTranslationOperation{}}
	case core.OperationDeleteTranslation:
		result.Operation = &managev1.AIDocumentOperation_DeleteTranslation{DeleteTranslation: &managev1.AIDocumentDeleteTranslationOperation{}}
	case core.OperationInsertRelationItem:
		op := value.InsertRelationItem
		result.Operation = &managev1.AIDocumentOperation_InsertRelationItem{InsertRelationItem: &managev1.AIDocumentInsertRelationItemOperation{BlockHandle: string(op.Block), RelationHandle: string(op.Relation), ItemHandle: string(op.Item), ItemKind: string(op.Kind), AfterItemHandle: optionalString(string(op.After))}}
	case core.OperationDeleteRelationItem:
		op := value.DeleteRelationItem
		result.Operation = &managev1.AIDocumentOperation_DeleteRelationItem{DeleteRelationItem: &managev1.AIDocumentDeleteRelationItemOperation{Item: relationItemReferenceToProto(op.Block, op.Relation, op.Item)}}
	case core.OperationMoveRelationItem:
		op := value.MoveRelationItem
		result.Operation = &managev1.AIDocumentOperation_MoveRelationItem{MoveRelationItem: &managev1.AIDocumentMoveRelationItemOperation{Item: relationItemReferenceToProto(op.Block, op.Relation, op.Item), DestinationBlockHandle: string(op.TargetBlock), DestinationRelationHandle: string(op.Target), AfterItemHandle: optionalString(string(op.After))}}
	}
	return result
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
