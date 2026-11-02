package aidocumentadapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strings"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	"github.com/google/uuid"
)

const (
	emailLayoutRootID         core.BlockID   = "document"
	emailLayoutRootKind       core.BlockKind = "email_layout"
	emailLayoutTextKind       core.BlockKind = "layout_text"
	emailLayoutAttributeKind  core.BlockKind = "layout_attribute"
	emailLayoutContentField   core.FieldID   = "content"
	emailLayoutElementField   core.FieldID   = "element"
	emailLayoutAttributeField core.FieldID   = "attribute"
)

var emailLayoutCatalog = func() core.Catalog {
	fingerprint := sha256.Sum256([]byte("dcdp/1:email-layout:stable-source-units:sparse-locale-values:v1"))
	return core.Catalog{
		Fingerprint: hex.EncodeToString(fingerprint[:]),
		BlockKinds:  []core.BlockKind{emailLayoutRootKind, emailLayoutTextKind, emailLayoutAttributeKind},
		Fields: []core.FieldRule{
			{BlockKind: emailLayoutTextKind, Field: emailLayoutContentField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true},
			{BlockKind: emailLayoutAttributeKind, Field: emailLayoutContentField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true},
			{BlockKind: emailLayoutAttributeKind, Field: emailLayoutElementField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipSource},
			{BlockKind: emailLayoutAttributeKind, Field: emailLayoutAttributeField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipSource},
		},
	}
}()

type emailLayoutAIDocumentDomain interface {
	Load(context.Context, string, string) (emailauthoring.EmailLayoutAIDocumentState, error)
	ExecuteAIDocumentMutation(
		context.Context,
		string,
		string,
		emailauthoring.EmailLayoutAIDocumentExecutionMode,
		emailauthoring.EmailLayoutAIDocumentMutationCompiler,
	) (emailauthoring.EmailLayoutAIDocumentMutationResult, error)
}

type emailLayoutPort struct{ service emailLayoutAIDocumentDomain }

func NewEmailLayoutRegistration(internal *emailauthoring.InternalService) (DomainRegistration, error) {
	if internal == nil {
		return DomainRegistration{}, errors.New("email layout AI document owner is required")
	}
	return DomainRegistration{
		Domain: core.DomainEmailLayout,
		Port:   &emailLayoutPort{service: internal.LayoutAIDocuments()},
	}, nil
}

func (p *emailLayoutPort) Load(
	ctx context.Context,
	identity core.DocumentIdentity,
	locale core.Locale,
) (core.Document, error) {
	if err := validateEmailLayoutIdentity(identity); err != nil {
		return core.Document{}, err
	}
	state, err := p.service.Load(ctx, string(identity.Reference), string(locale))
	if err != nil {
		return core.Document{}, err
	}
	return projectEmailLayoutDocument(identity, locale, state)
}

func (p *emailLayoutPort) ValidateMutation(
	ctx context.Context,
	request core.ApplyRequest,
) (core.ValidationResult, error) {
	validation, _, err := p.executeMutation(
		ctx,
		request,
		emailauthoring.EmailLayoutAIDocumentExecutionValidate,
	)
	return validation, err
}

func (p *emailLayoutPort) ExecuteMutation(
	ctx context.Context,
	request core.ApplyRequest,
) (core.ApplyResult, error) {
	_, result, err := p.executeMutation(
		ctx,
		request,
		emailauthoring.EmailLayoutAIDocumentExecutionApply,
	)
	return result, err
}

func (p *emailLayoutPort) executeMutation(
	ctx context.Context,
	request core.ApplyRequest,
	mode emailauthoring.EmailLayoutAIDocumentExecutionMode,
) (core.ValidationResult, core.ApplyResult, error) {
	identity := request.Identity()
	if err := validateEmailLayoutIdentity(identity); err != nil {
		return core.ValidationResult{}, core.ApplyResult{}, err
	}

	var changes []core.Change

	return executeExactMutation(request, exactMutationSpec[emailauthoring.EmailLayoutAIDocumentState, emailauthoring.EmailLayoutAIDocumentMutation, emailauthoring.EmailLayoutAIDocumentMutationResult]{
		Domain:       "Email Layout",
		ValidateOnly: mode == emailauthoring.EmailLayoutAIDocumentExecutionValidate,
		Execute: func(compiler func(emailauthoring.EmailLayoutAIDocumentState) (emailauthoring.EmailLayoutAIDocumentMutation, error)) (emailauthoring.EmailLayoutAIDocumentMutationResult, error) {
			return p.service.ExecuteAIDocumentMutation(ctx, string(identity.Reference), string(request.Locale), mode, compiler)
		},
		Project: func(state emailauthoring.EmailLayoutAIDocumentState) (core.Document, error) {
			return projectEmailLayoutDocument(identity, request.Locale, state)
		},
		Compile: func(state emailauthoring.EmailLayoutAIDocumentState, current core.Document, command core.ValidatedApply) (emailauthoring.EmailLayoutAIDocumentMutation, []core.OperationIssue, error) {
			contributor, err := uuid.Parse(state.ViewerMemberID)
			if err != nil || contributor == uuid.Nil || contributor.String() != state.ViewerMemberID {
				return emailauthoring.EmailLayoutAIDocumentMutation{}, nil, errors.New("email Layout AI document contributor Member UUID is invalid")
			}
			changes, err = emailLayoutSemanticChanges(current, command.Operations)
			if err != nil {
				return emailauthoring.EmailLayoutAIDocumentMutation{}, nil, err
			}
			mutation, issues, err := compileEmailLayoutMutation(state, current, contributor, command.Operations)
			if err != nil {
				return emailauthoring.EmailLayoutAIDocumentMutation{}, nil, err
			}
			return mutation, issues, nil
		},
		Result: func(domainResult emailauthoring.EmailLayoutAIDocumentMutationResult, command core.ValidatedApply) (core.ApplyResult, error) {
			if domainResult.Changed && len(changes) == 0 {
				return core.ApplyResult{}, errors.New("email Layout persisted without a semantic DCDP change")
			}
			result := core.ApplyResult{
				DocumentRevision: core.Revision(domainResult.DocumentRevision),
				TargetRevision:   core.OptionalRevision(domainResult.TargetRevision),
				Changed:          domainResult.Changed,
			}
			if domainResult.Changed {
				result.Changes = changes
			}

			return result, nil
		},
	})
}

func projectEmailLayoutDocument(
	identity core.DocumentIdentity,
	locale core.Locale,
	state emailauthoring.EmailLayoutAIDocumentState,
) (core.Document, error) {
	if state.LayoutID != string(identity.Reference) || state.Locale != string(locale) {
		return core.Document{}, errors.New("email Layout AI document facade returned inconsistent state")
	}
	nodes := []core.Node{{ID: emailLayoutRootID, Kind: emailLayoutRootKind}}
	for _, unit := range state.Units {
		node := core.Node{
			ID: core.BlockID(unit.Handle), Parent: emailLayoutRootID, Order: unit.Order,
		}
		switch unit.Kind {
		case "text":
			node.Kind = emailLayoutTextKind
		case "attribute":
			node.Kind = emailLayoutAttributeKind
			node.Shared = []core.FieldValue{
				{ID: emailLayoutElementField, Value: core.Text(unit.Element)},
				{ID: emailLayoutAttributeField, Value: core.Text(unit.Attribute)},
			}
		default:
			return core.Document{}, fmt.Errorf("unsupported Email Layout unit kind %q", unit.Kind)
		}
		if unit.LocaleValue != nil {
			node.Localized = []core.FieldValue{{ID: emailLayoutContentField, Value: core.Text(*unit.LocaleValue)}}
		}
		nodes = append(nodes, node)
	}
	return core.Document{
		Identity: identity, DocumentRevision: core.Revision(state.DocumentRevision),
		TargetRevision: core.OptionalRevision(state.TargetRevision),
		SourceLocale:   core.Locale(state.SourceLocale), Locale: locale,
		LocaleExists: state.LocaleExists, Catalog: emailLayoutCatalog, Nodes: nodes,
	}, nil
}

func compileEmailLayoutMutation(
	state emailauthoring.EmailLayoutAIDocumentState,
	document core.Document,
	contributor uuid.UUID,
	operations []core.Operation,
) (emailauthoring.EmailLayoutAIDocumentMutation, []core.OperationIssue, error) {
	mutation := emailauthoring.EmailLayoutAIDocumentMutation{
		LayoutID: state.LayoutID, Locale: string(document.Locale),
		ExpectedDocumentRevision: string(document.DocumentRevision),
		ExpectedTargetRevision:   core.OptionalRevisionString(document.TargetRevision),
		ExpectedSource:           state.SourceLocale,
		ExpectedPresence:         document.LocaleExists, ContributorMemberID: contributor,
	}
	if len(operations) == 1 && operations[0].Kind == core.OperationCreateTranslation {
		mutation.CreateTranslation = true
		return mutation, nil, nil
	}
	if len(operations) == 1 && operations[0].Kind == core.OperationDeleteTranslation {
		mutation.DeleteTranslation = true
		return mutation, nil, nil
	}
	for index, operation := range operations {
		if issue := validateEmailLayoutOperation(index, document, operation); issue != nil {
			return emailauthoring.EmailLayoutAIDocumentMutation{}, []core.OperationIssue{*issue}, nil
		}
	}
	next, err := core.DocumentAfterOperations(document, operations)
	if err != nil {
		return emailauthoring.EmailLayoutAIDocumentMutation{}, nil, err
	}
	currentValues, err := emailLayoutDocumentValues(document)
	if err != nil {
		return emailauthoring.EmailLayoutAIDocumentMutation{}, nil, err
	}
	nextValues, err := emailLayoutDocumentValues(next)
	if err != nil {
		return emailauthoring.EmailLayoutAIDocumentMutation{}, nil, err
	}
	if maps.Equal(currentValues, nextValues) {
		mutation.Noop = true
	} else {
		mutation.ReplaceValues = true
		mutation.Values = nextValues
	}
	return mutation, nil, nil
}

func validateEmailLayoutOperation(
	index int,
	document core.Document,
	operation core.Operation,
) *core.OperationIssue {
	invalid := func(message string) *core.OperationIssue {
		return &core.OperationIssue{
			Operation: index, Code: core.IssueInvalidOperation,
			Handle:  compactIssueHandle(operation, document.Locale, "email_layout"),
			Message: message,
		}
	}
	if operation.Kind != core.OperationSetField || operation.SetField == nil {
		return invalid("Email Layout supports locale value set operations only; structure is source-wrapper-owned")
	}
	target := operation.SetField.Target
	if target.Block == emailLayoutRootID || target.Relation != "" || target.Item != "" ||
		len(target.Path) != 0 || target.Field != emailLayoutContentField {
		return invalid("Email Layout operation does not target a locale-owned unit value")
	}
	if operation.SetField.Value.Kind != core.ValueKindText {
		return invalid("Email Layout unit value must be text")
	}
	found := false
	for _, node := range document.Nodes {
		if node.ID == target.Block && (node.Kind == emailLayoutTextKind || node.Kind == emailLayoutAttributeKind) {
			found = true
			break
		}
	}
	if !found {
		return invalid("Email Layout unit handle is not part of the current source structure")
	}
	return nil
}

func emailLayoutDocumentValues(document core.Document) (map[string]string, error) {
	values := make(map[string]string)
	for _, node := range document.Nodes {
		if node.ID == emailLayoutRootID {
			continue
		}
		if node.Kind != emailLayoutTextKind && node.Kind != emailLayoutAttributeKind {
			return nil, fmt.Errorf("unsupported Email Layout node kind %q", node.Kind)
		}
		for _, field := range node.Localized {
			if field.ID != emailLayoutContentField || field.Value.Kind != core.ValueKindText {
				return nil, fmt.Errorf("email Layout unit %q has an invalid locale field", node.ID)
			}
			values[string(node.ID)] = field.Value.Text
		}
	}
	return values, nil
}

func emailLayoutSemanticChanges(
	document core.Document,
	operations []core.Operation,
) ([]core.Change, error) {
	current, err := core.DocumentAfterOperations(document, nil)
	if err != nil {
		return nil, err
	}
	changes := make([]core.Change, 0, len(operations))
	for index, operation := range operations {
		next, err := core.DocumentAfterOperations(current, []core.Operation{operation})
		if err != nil {
			return nil, err
		}
		if current.LocaleExists != next.LocaleExists || !reflect.DeepEqual(current.Nodes, next.Nodes) {
			changes = append(changes, core.Change{
				Operation: index, Kind: operation.Kind,
				AffectedHandles: core.OperationHandles(operation, document.Locale),
			})
		}
		current = next
	}
	return changes, nil
}

func validateEmailLayoutIdentity(identity core.DocumentIdentity) error {
	if identity.Domain != core.DomainEmailLayout {
		return fmt.Errorf("email Layout AI document requires domain %q", core.DomainEmailLayout)
	}
	value := strings.TrimSpace(string(identity.Reference))
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil || id.String() != value {
		return errors.New("email Layout AI document reference must be a canonical UUID")
	}
	return nil
}

var _ core.DomainPort = (*emailLayoutPort)(nil)
var _ core.ExactMutationPort = (*emailLayoutPort)(nil)
