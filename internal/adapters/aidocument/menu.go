package aidocumentadapter

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	menudomain "github.com/echovisionlab/geul-api/internal/menu"
)

const (
	menuBlockKind     core.BlockKind = "menu"
	menuItemBlockKind core.BlockKind = "menu_item"

	menuFieldName             core.FieldID = "name"
	menuFieldLabel            core.FieldID = "label"
	menuFieldLinkType         core.FieldID = "link_type"
	menuFieldURL              core.FieldID = "url"
	menuFieldTargetID         core.FieldID = "target_id"
	menuFieldTargetSlug       core.FieldID = "target_slug"
	menuFieldOpenInNewTab     core.FieldID = "open_in_new_tab"
	menuFieldVisibilityMode   core.FieldID = "visibility_mode"
	menuFieldVisibilityRoles  core.FieldID = "visibility_roles"
	menuFieldLocalizationMode core.FieldID = "localization_mode"
	menuFieldFixedLocale      core.FieldID = "fixed_locale"
)

var menuCatalog = core.Catalog{
	Fingerprint: "menu-dcdp-1",
	BlockKinds:  []core.BlockKind{menuBlockKind, menuItemBlockKind},
	Fields: []core.FieldRule{
		{BlockKind: menuBlockKind, Field: menuFieldName, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipSource},
		{BlockKind: menuItemBlockKind, Field: menuFieldLabel, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true},
		{BlockKind: menuItemBlockKind, Field: menuFieldLinkType, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipSource},
		{BlockKind: menuItemBlockKind, Field: menuFieldURL, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipSource},
		{BlockKind: menuItemBlockKind, Field: menuFieldTargetID, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipSource},
		{BlockKind: menuItemBlockKind, Field: menuFieldTargetSlug, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipSource},
		{BlockKind: menuItemBlockKind, Field: menuFieldOpenInNewTab, ValueKind: core.ValueKindBoolean, Ownership: core.FieldOwnershipSource},
		{BlockKind: menuItemBlockKind, Field: menuFieldVisibilityMode, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipSource},
		{
			BlockKind: menuItemBlockKind, Field: menuFieldVisibilityRoles,
			ValueKind: core.ValueKindList, Ownership: core.FieldOwnershipSource,
			Schema: &core.FieldSchema{
				Kind: core.ValueKindList, Ownership: core.FieldOwnershipSource,
				Identity: core.ListIdentityRule{Kind: core.ListIdentityValue},
				Item:     &core.FieldSchema{Kind: core.ValueKindText, Ownership: core.FieldOwnershipSource},
			},
		},
		{BlockKind: menuItemBlockKind, Field: menuFieldLocalizationMode, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipSource},
		{BlockKind: menuItemBlockKind, Field: menuFieldFixedLocale, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipSource},
	},
}

type menuAIDocumentDomain interface {
	LoadAIDocument(context.Context, string, string) (menudomain.AIDocumentSnapshot, error)
	ExecuteAIDocumentMutation(
		context.Context,
		string,
		string,
		menudomain.AIDocumentMutationAction,
		menudomain.AIDocumentExecutionMode,
		menudomain.AIDocumentMutationCompiler,
	) (menudomain.AIDocumentApplyResult, error)
}

type menuAIDocumentPort struct{ domain menuAIDocumentDomain }

// NewMenuRegistration binds the shared DCDP application to Menu's owning
// application boundary. The Menu service, not this adapter, owns permission,
// lifecycle, locking, CAS and persistence.
func NewMenuRegistration(domain menuAIDocumentDomain) (DomainRegistration, error) {
	if interfaceIsNil(domain) {
		return DomainRegistration{}, errors.New("menu AI document domain is required")
	}
	return DomainRegistration{Domain: core.DomainMenu, Port: &menuAIDocumentPort{domain: domain}}, nil
}

func (p *menuAIDocumentPort) Load(
	ctx context.Context,
	identity core.DocumentIdentity,
	locale core.Locale,
) (core.Document, error) {
	if identity.Domain != core.DomainMenu {
		return core.Document{}, fmt.Errorf("menu AI document adapter cannot load domain %q", identity.Domain)
	}
	snapshot, err := p.domain.LoadAIDocument(ctx, string(identity.Reference), string(locale))
	if err != nil {
		return core.Document{}, err
	}
	if snapshot.ID != string(identity.Reference) || snapshot.Locale != string(locale) {
		return core.Document{}, errors.New("menu AI document domain returned a different identity or locale")
	}
	return projectMenuAIDocument(snapshot), nil
}

func (p *menuAIDocumentPort) ValidateMutation(
	ctx context.Context,
	request core.ApplyRequest,
) (core.ValidationResult, error) {
	validation, _, err := p.executeMutation(ctx, request, menudomain.AIDocumentExecutionValidate)
	return validation, err
}

func (p *menuAIDocumentPort) ExecuteMutation(
	ctx context.Context,
	request core.ApplyRequest,
) (core.ApplyResult, error) {
	_, result, err := p.executeMutation(ctx, request, menudomain.AIDocumentExecutionApply)
	return result, err
}

func (p *menuAIDocumentPort) executeMutation(
	ctx context.Context,
	request core.ApplyRequest,
	mode menudomain.AIDocumentExecutionMode,
) (core.ValidationResult, core.ApplyResult, error) {
	identity := request.Identity()
	if identity.Domain != core.DomainMenu {
		return core.ValidationResult{}, core.ApplyResult{}, fmt.Errorf("menu AI document adapter cannot mutate domain %q", identity.Domain)
	}
	action := menuMutationAction(request.Operations)

	return executeExactMutation(request, exactMutationSpec[menudomain.AIDocumentSnapshot, menudomain.AIDocumentApply, menudomain.AIDocumentApplyResult]{
		Domain:       "Menu",
		ValidateOnly: mode == menudomain.AIDocumentExecutionValidate,
		Execute: func(compiler func(menudomain.AIDocumentSnapshot) (menudomain.AIDocumentApply, error)) (menudomain.AIDocumentApplyResult, error) {
			return p.domain.ExecuteAIDocumentMutation(ctx, string(identity.Reference), string(request.Locale), action, mode, compiler)
		},
		Project: func(snapshot menudomain.AIDocumentSnapshot) (core.Document, error) {
			if snapshot.ID != string(identity.Reference) || snapshot.Locale != string(request.Locale) {
				return core.Document{}, errors.New("menu AI document domain returned a different identity or locale")
			}
			current := projectMenuAIDocument(snapshot)

			return current, nil
		},
		Compile: func(snapshot menudomain.AIDocumentSnapshot, current core.Document, command core.ValidatedApply) (menudomain.AIDocumentApply, []core.OperationIssue, error) {
			operations, issues := compileMenuAIDocumentOperations(current, command.Operations)
			return menudomain.AIDocumentApply{
				MenuID:                   string(identity.Reference),
				Locale:                   string(request.Locale),
				ExpectedDocumentRevision: string(command.ExpectedDocumentRevision),
				ExpectedTargetRevision:   core.OptionalRevisionString(command.ExpectedTargetRevision),
				AffectedHandles:          append([]string(nil), command.AffectedHandles...),
				Operations:               operations,
			}, issues, nil
		},
		Result: func(domainResult menudomain.AIDocumentApplyResult, command core.ValidatedApply) (core.ApplyResult, error) {
			result := core.ApplyResult{
				DocumentRevision: core.Revision(domainResult.DocumentRevision),
				TargetRevision:   core.OptionalRevision(domainResult.TargetRevision),
				Changed:          domainResult.Changed,
			}
			result = withOperationChanges(command, result)

			return result, nil
		},
		ConflictHandles: func(conflict *core.RevisionConflict, _ core.ValidatedApply) []string { return conflict.AffectedHandles },
		PreConflictIssues: func(err error) ([]core.OperationIssue, bool) {
			var invalid *menudomain.AIDocumentValidationError
			if !errors.As(err, &invalid) {
				return nil, false
			}
			return menuDomainIssues(invalid.Issues), true
		},
	})
}

func menuMutationAction(operations []core.Operation) menudomain.AIDocumentMutationAction {
	for _, operation := range operations {
		switch operation.Kind {
		case core.OperationInsertBlock, core.OperationDeleteBlock, core.OperationMoveBlock:
			return menudomain.AIDocumentMutationManage
		}
	}
	return menudomain.AIDocumentMutationEdit
}

func menuDomainIssues(issues []menudomain.AIDocumentIssue) []core.OperationIssue {
	result := make([]core.OperationIssue, 0, len(issues))
	for _, issue := range issues {
		code := core.IssueInvalidOperation
		if issue.Code == menudomain.AIDocumentIssueTargetForbidden {
			code = core.IssueTargetFieldForbidden
		}
		result = append(result, core.OperationIssue{
			Operation: issue.Operation, Code: code, Handle: issue.Handle, Message: issue.Message,
		})
	}
	return result
}

func projectMenuAIDocument(snapshot menudomain.AIDocumentSnapshot) core.Document {
	nodes := []core.Node{{
		ID: core.BlockID(snapshot.ID), Kind: menuBlockKind,
		Shared: []core.FieldValue{{ID: menuFieldName, Value: core.Text(snapshot.Name)}},
	}}
	var appendItems func([]menudomain.AIDocumentItem, core.BlockID)
	appendItems = func(items []menudomain.AIDocumentItem, parent core.BlockID) {
		for order, item := range items {
			node := core.Node{
				ID: core.BlockID(item.ID), Kind: menuItemBlockKind, Parent: parent, Order: order,
				Shared: projectMenuItemSourceFields(item),
			}
			if item.OwnsLabel(snapshot.Locale) {
				if label, exists := snapshot.Labels[item.ID]; exists {
					node.Localized = []core.FieldValue{{ID: menuFieldLabel, Value: core.Text(label)}}
				}
			}
			nodes = append(nodes, node)
			appendItems(item.Children, node.ID)
		}
	}
	appendItems(snapshot.Items, core.BlockID(snapshot.ID))
	return core.Document{
		Identity:         core.DocumentIdentity{Domain: core.DomainMenu, Reference: core.DocumentReference(snapshot.ID)},
		DocumentRevision: core.Revision(snapshot.DocumentRevision),
		TargetRevision:   core.OptionalRevision(snapshot.TargetRevision), SourceLocale: core.Locale(snapshot.SourceLocale),
		Locale: core.Locale(snapshot.Locale), LocaleExists: snapshot.LocaleExists,
		Catalog: menuCatalog, Nodes: nodes,
	}
}

func projectMenuItemSourceFields(item menudomain.AIDocumentItem) []core.FieldValue {
	values := []core.FieldValue{{ID: menuFieldLinkType, Value: core.Text(item.LinkType)}}
	appendText := func(field core.FieldID, value *string) {
		if value != nil {
			values = append(values, core.FieldValue{ID: field, Value: core.Text(*value)})
		}
	}
	appendText(menuFieldURL, item.URL)
	appendText(menuFieldTargetID, item.TargetID)
	appendText(menuFieldTargetSlug, item.TargetSlug)
	if item.OpenInNewTab != nil {
		values = append(values, core.FieldValue{ID: menuFieldOpenInNewTab, Value: core.Boolean(*item.OpenInNewTab)})
	}
	appendText(menuFieldVisibilityMode, item.VisibilityMode)
	if len(item.VisibilityRoles) != 0 {
		roles := make([]core.ListItem, len(item.VisibilityRoles))
		for index, role := range item.VisibilityRoles {
			roles[index] = core.StableItem(core.RelationItemID(role), core.Text(role))
		}
		values = append(values, core.FieldValue{ID: menuFieldVisibilityRoles, Value: core.List(roles...)})
	}
	appendText(menuFieldLocalizationMode, item.LocalizationMode)
	appendText(menuFieldFixedLocale, item.FixedLocale)
	return values
}

func compileMenuAIDocumentOperations(
	document core.Document,
	operations []core.Operation,
) ([]menudomain.AIDocumentOperation, []core.OperationIssue) {
	result := make([]menudomain.AIDocumentOperation, 0, len(operations))
	issue := func(index int, handle, message string) ([]menudomain.AIDocumentOperation, []core.OperationIssue) {
		return nil, []core.OperationIssue{{Operation: index, Code: core.IssueInvalidOperation, Handle: handle, Message: message}}
	}
	root := core.BlockID(document.Identity.Reference)
	for index, operation := range operations {
		switch operation.Kind {
		case core.OperationSetField:
			target := operation.SetField.Target
			if target.Relation != "" || target.Item != "" || len(target.Path) != 0 {
				return issue(index, "menu", "Menu does not support relation or nested fields")
			}
			if target.Block == root {
				if target.Field != menuFieldName || operation.SetField.Value.Kind != core.ValueKindText {
					return issue(index, "menu:"+string(root), "Menu root supports only text name updates")
				}
				result = append(result, menudomain.AIDocumentOperation{
					Kind:  menudomain.AIDocumentSetName,
					Value: menudomain.AIDocumentValue{Kind: menudomain.AIDocumentText, Text: operation.SetField.Value.Text},
				})
				continue
			}
			value, err := menuAIDocumentValue(operation.SetField.Value)
			if err != nil {
				return issue(index, "item:"+string(target.Block)+"/"+string(target.Field), err.Error())
			}
			result = append(result, menudomain.AIDocumentOperation{
				Kind: menudomain.AIDocumentSetItemField, ItemID: string(target.Block),
				Field: string(target.Field), Value: value,
			})
		case core.OperationUnsetField:
			target := operation.UnsetField.Target
			if target.Block == root {
				return issue(index, "menu:"+string(root)+"/name", "Menu name cannot be unset")
			}
			if target.Relation != "" || target.Item != "" || len(target.Path) != 0 {
				return issue(index, "menu", "Menu does not support relation or nested fields")
			}
			result = append(result, menudomain.AIDocumentOperation{
				Kind: menudomain.AIDocumentUnsetItemField, ItemID: string(target.Block), Field: string(target.Field),
			})
		case core.OperationInsertBlock:
			op := operation.InsertBlock
			if op.Kind != menuItemBlockKind || op.Block == root {
				return issue(index, "block:"+string(op.Block), "Menu source graph accepts only stable menu_item blocks")
			}
			result = append(result, menudomain.AIDocumentOperation{
				Kind: menudomain.AIDocumentInsertItem, ItemID: string(op.Block),
				ParentID: string(op.Parent), AfterID: string(op.After),
			})
		case core.OperationDeleteBlock:
			if operation.DeleteBlock.Block == root {
				return issue(index, "menu:"+string(root), "Menu root cannot be deleted through document operations")
			}
			result = append(result, menudomain.AIDocumentOperation{Kind: menudomain.AIDocumentDeleteItem, ItemID: string(operation.DeleteBlock.Block)})
		case core.OperationMoveBlock:
			op := operation.MoveBlock
			if op.Block == root {
				return issue(index, "menu:"+string(root), "Menu root cannot be moved")
			}
			result = append(result, menudomain.AIDocumentOperation{
				Kind: menudomain.AIDocumentMoveItem, ItemID: string(op.Block),
				ParentID: string(op.Parent), AfterID: string(op.After),
			})
		case core.OperationReplaceBlockKind:
			if operation.ReplaceBlockKind.Block == root || operation.ReplaceBlockKind.Kind != menuItemBlockKind {
				return issue(index, "block:"+string(operation.ReplaceBlockKind.Block), "Menu item kind cannot be replaced")
			}
			// Menu has one item kind. Replacing it with itself is a semantic no-op.
			result = append(result, menudomain.AIDocumentOperation{Kind: menudomain.AIDocumentNoop})
		case core.OperationCreateTranslation:
			result = append(result, menudomain.AIDocumentOperation{Kind: menudomain.AIDocumentCreateTranslation})
		case core.OperationDeleteTranslation:
			result = append(result, menudomain.AIDocumentOperation{Kind: menudomain.AIDocumentDeleteTranslation})
		default:
			return issue(index, "menu:"+string(root), "Menu does not support relation or file operations")
		}
	}
	return result, nil
}

func menuAIDocumentValue(value core.Value) (menudomain.AIDocumentValue, error) {
	switch value.Kind {
	case core.ValueKindText:
		return menudomain.AIDocumentValue{Kind: menudomain.AIDocumentText, Text: value.Text}, nil
	case core.ValueKindBoolean:
		return menudomain.AIDocumentValue{Kind: menudomain.AIDocumentBoolean, Boolean: value.Boolean}, nil
	case core.ValueKindList:
		texts := make([]string, 0, len(value.List))
		for _, item := range value.List {
			if item.Value.Kind != core.ValueKindText || item.ID != core.RelationItemID(item.Value.Text) {
				return menudomain.AIDocumentValue{}, errors.New("menu list fields require value-identified text items")
			}
			texts = append(texts, item.Value.Text)
		}
		return menudomain.AIDocumentValue{Kind: menudomain.AIDocumentTextList, Texts: texts}, nil
	default:
		return menudomain.AIDocumentValue{}, fmt.Errorf("unsupported Menu value kind %q", value.Kind)
	}
}

func interfaceIsNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var _ core.DomainPort = (*menuAIDocumentPort)(nil)
var _ core.ExactMutationPort = (*menuAIDocumentPort)(nil)
