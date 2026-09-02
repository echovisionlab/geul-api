package aidocumentadapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

const (
	emailMetadataBlockID   core.BlockID   = "document"
	emailMetadataBlockKind core.BlockKind = "email_message"
	emailSubjectField      core.FieldID   = "subject"
)

type emailRichTextState struct {
	Reference        string
	DocumentID       uuid.UUID
	DocumentRevision string
	TargetRevision   *string
	SourceLocale     string
	Locale           string
	LocaleExists     bool
	ViewerMemberID   string
	Subject          *string
	Document         *contentv1.LocalizedRichTextDocument
}

type emailRichTextMutation struct {
	Reference                string
	Locale                   string
	ExpectedDocumentRevision string
	ExpectedTargetRevision   *string
	ExpectedSource           string
	ExpectedPresence         bool
	ContributorMemberID      uuid.UUID
	Batch                    *contentblock.Batch
	SetSubject               bool
	Subject                  string
	CreateTranslation        bool
	DeleteTranslation        bool
}

type emailRichTextMutationResult struct {
	DocumentRevision string
	TargetRevision   *string
	Changed          bool
}

type emailRichTextExecutionMode uint8

const (
	emailRichTextExecutionValidate emailRichTextExecutionMode = iota
	emailRichTextExecutionApply
)

type emailRichTextMutationCompiler func(emailRichTextState) (emailRichTextMutation, error)

type emailRichTextDomain interface {
	Load(context.Context, string, string) (emailRichTextState, error)
	Execute(
		context.Context,
		string,
		string,
		emailRichTextExecutionMode,
		emailRichTextMutationCompiler,
	) (emailRichTextMutationResult, error)
}

type emailRichTextRevisionConflict struct {
	kind                    core.ConflictCode
	currentDocumentRevision string
	currentTargetRevision   *string
}

func (e *emailRichTextRevisionConflict) Error() string {
	return fmt.Sprintf("Email profile AI document revision conflict: current revision is %q", e.currentDocumentRevision)
}

type emailRichTextPort struct {
	domain  core.Domain
	name    string
	service emailRichTextDomain
	codec   *RichTextCodec
	catalog core.Catalog
}

func newEmailRichTextPort(domain core.Domain, name string, service emailRichTextDomain) (*emailRichTextPort, error) {
	if service == nil {
		return nil, fmt.Errorf("%s AI document service is required", name)
	}
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_EMAIL)
	if err != nil {
		return nil, fmt.Errorf("create %s Email Rich Text codec: %w", name, err)
	}
	catalog := codec.Catalog()
	catalog.BlockKinds = append([]core.BlockKind(nil), catalog.BlockKinds...)
	catalog.Fields = append([]core.FieldRule(nil), catalog.Fields...)
	catalog.BlockKinds = append(catalog.BlockKinds, emailMetadataBlockKind)
	catalog.Fields = append(catalog.Fields, core.FieldRule{
		BlockKind: emailMetadataBlockKind, Field: emailSubjectField,
		ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true,
	})
	fingerprint := sha256.Sum256([]byte(catalog.Fingerprint + ":email-subject:dcdp/1"))
	catalog.Fingerprint = hex.EncodeToString(fingerprint[:])
	return &emailRichTextPort{
		domain: domain, name: name, service: service, codec: codec, catalog: catalog,
	}, nil
}

func (p *emailRichTextPort) Load(
	ctx context.Context,
	identity core.DocumentIdentity,
	locale core.Locale,
) (core.Document, error) {
	if err := p.validateIdentity(identity); err != nil {
		return core.Document{}, err
	}
	state, err := p.service.Load(ctx, string(identity.Reference), string(locale))
	if err != nil {
		return core.Document{}, err
	}
	return p.document(identity, locale, state)
}

func (p *emailRichTextPort) ValidateMutation(
	ctx context.Context,
	request core.ApplyRequest,
) (core.ValidationResult, error) {
	validation, _, err := p.executeMutation(ctx, request, emailRichTextExecutionValidate)
	return validation, err
}

func (p *emailRichTextPort) ExecuteMutation(
	ctx context.Context,
	request core.ApplyRequest,
) (core.ApplyResult, error) {
	_, result, err := p.executeMutation(ctx, request, emailRichTextExecutionApply)
	return result, err
}

func (p *emailRichTextPort) executeMutation(
	ctx context.Context,
	request core.ApplyRequest,
	mode emailRichTextExecutionMode,
) (core.ValidationResult, core.ApplyResult, error) {
	identity := request.Identity()
	if err := p.validateIdentity(identity); err != nil {
		return core.ValidationResult{}, core.ApplyResult{}, err
	}

	run := newExactMutationRun(p.name)
	var changes []core.Change
	domainResult, err := p.service.Execute(
		ctx,
		string(identity.Reference),
		string(request.Locale),
		mode,
		func(state emailRichTextState) (emailRichTextMutation, error) {
			current, err := p.document(identity, request.Locale, state)
			if err != nil {
				return emailRichTextMutation{}, err
			}
			if err := run.validateLoaded(current, request); err != nil {
				return emailRichTextMutation{}, err
			}
			contributor, err := uuid.Parse(state.ViewerMemberID)
			if err != nil || contributor == uuid.Nil || contributor.String() != state.ViewerMemberID {
				return emailRichTextMutation{}, fmt.Errorf("%s AI document contributor Member UUID is invalid", p.name)
			}
			changes, err = emailRichTextSemanticChanges(current, run.command.Operations)
			if err != nil {
				return emailRichTextMutation{}, err
			}
			mutation, issues, err := p.compile(state, current, contributor, run.command.Operations)
			if err != nil {
				return emailRichTextMutation{}, err
			}
			if err := run.rejectIssues(issues); err != nil {
				return emailRichTextMutation{}, err
			}
			return mutation, nil
		},
	)
	if err != nil {
		if invalid, mapped, handled := handleExactMutationValidationError(
			err, mode == emailRichTextExecutionValidate,
		); handled {
			return invalid, core.ApplyResult{}, mapped
		}
		var conflict *emailRichTextRevisionConflict
		if errors.As(err, &conflict) {
			return core.ValidationResult{}, core.ApplyResult{}, p.conflict(conflict, run.command.AffectedHandles)
		}
		operations := run.command.Operations
		if len(operations) == 0 {
			operations = request.Operations
		}
		if issue := emailRichTextDomainIssue(err, operations); issue != nil {
			run.validation.Issues = append(run.validation.Issues, *issue)
			if mode == emailRichTextExecutionValidate {
				return run.validation, core.ApplyResult{}, nil
			}
			return core.ValidationResult{}, core.ApplyResult{}, &core.ValidationError{Result: run.validation}
		}
		return core.ValidationResult{}, core.ApplyResult{}, err
	}
	if mode == emailRichTextExecutionValidate {
		return run.validation, core.ApplyResult{}, nil
	}
	if domainResult.Changed && len(changes) == 0 {
		return core.ValidationResult{}, core.ApplyResult{}, fmt.Errorf(
			"%s persisted a mutation without a semantic DCDP change",
			p.name,
		)
	}
	result := core.ApplyResult{
		DocumentRevision: core.Revision(domainResult.DocumentRevision),
		TargetRevision:   emailRichTextCoreRevision(domainResult.TargetRevision),
		Changed:          domainResult.Changed,
	}
	if domainResult.Changed {
		result.Changes = changes
	}
	accepted, err := run.accept(result)
	return run.validation, accepted, err
}

func (p *emailRichTextPort) document(
	identity core.DocumentIdentity,
	locale core.Locale,
	state emailRichTextState,
) (core.Document, error) {
	if state.Reference != string(identity.Reference) || state.Locale != string(locale) ||
		state.DocumentID == uuid.Nil || state.Document == nil {
		return core.Document{}, fmt.Errorf("%s AI document facade returned inconsistent state", p.name)
	}
	nodes, err := p.codec.Project(state.Document)
	if err != nil {
		return core.Document{}, fmt.Errorf("project %s Email Rich Text document: %w", p.name, err)
	}
	// DCDP exposes the domain metadata as the one fixed document root. Stored
	// Rich Text top-level Blocks remain rootless; compile unwraps this adapter-
	// only envelope before handing structural operations to the generated codec.
	for index := range nodes {
		if nodes[index].Parent == "" {
			nodes[index].Parent = emailMetadataBlockID
		}
	}
	metadata := core.Node{ID: emailMetadataBlockID, Kind: emailMetadataBlockKind}
	if state.Subject != nil {
		metadata.Localized = []core.FieldValue{{ID: emailSubjectField, Value: core.Text(*state.Subject)}}
	}
	nodes = append([]core.Node{metadata}, nodes...)
	return core.Document{
		Identity: identity, DocumentRevision: core.Revision(state.DocumentRevision),
		TargetRevision: emailRichTextCoreRevision(state.TargetRevision),
		SourceLocale:   core.Locale(state.SourceLocale), Locale: locale,
		LocaleExists: state.LocaleExists, Catalog: p.catalog, Nodes: nodes,
	}, nil
}

func (p *emailRichTextPort) compile(
	state emailRichTextState,
	document core.Document,
	contributor uuid.UUID,
	operations []core.Operation,
) (emailRichTextMutation, []core.OperationIssue, error) {
	mutation := emailRichTextMutation{
		Reference: state.Reference, Locale: string(document.Locale),
		ExpectedDocumentRevision: string(document.DocumentRevision),
		ExpectedTargetRevision:   emailRichTextStringRevision(document.TargetRevision),
		ExpectedSource:           state.SourceLocale,
		ExpectedPresence:         state.LocaleExists, ContributorMemberID: contributor,
	}
	if len(operations) == 1 && operations[0].Kind == core.OperationCreateTranslation {
		mutation.CreateTranslation = true
		return mutation, nil, nil
	}
	if len(operations) == 1 && operations[0].Kind == core.OperationDeleteTranslation {
		mutation.DeleteTranslation = true
		return mutation, nil, nil
	}

	blockOperations := make([]core.Operation, 0, len(operations))
	for index, operation := range operations {
		if issue := p.validateOperation(index, document, operation); issue != nil {
			return emailRichTextMutation{}, []core.OperationIssue{*issue}, nil
		}
		if operation.Kind == core.OperationSetField && operation.SetField.Target.Block == emailMetadataBlockID {
			mutation.SetSubject = true
			mutation.Subject = operation.SetField.Value.Text
			continue
		}
		blockOperations = append(blockOperations, unwrapEmailRichTextRoot(operation))
	}
	batch, issues, err := p.codec.Compile(
		state.DocumentID, state.Document, document.Role(), document.DocumentRevision,
		contributor, blockOperations,
	)
	if err == nil && len(issues) == 0 {
		mutation.Batch = &batch
	}
	return mutation, issues, err
}

func (p *emailRichTextPort) validateOperation(
	index int,
	document core.Document,
	operation core.Operation,
) *core.OperationIssue {
	invalid := func(message string) *core.OperationIssue {
		return &core.OperationIssue{
			Operation: index, Code: core.IssueInvalidOperation,
			Handle: strings.Join(emailRichTextOperationHandles(operation), "/"), Message: message,
		}
	}
	switch operation.Kind {
	case core.OperationSetField:
		if operation.SetField.Target.Block == emailMetadataBlockID {
			target := operation.SetField.Target
			if target.Field != emailSubjectField || target.Relation != "" || target.Item != "" || len(target.Path) != 0 {
				return invalid(p.name + " metadata exposes only the direct subject locale field")
			}
			if operation.SetField.Value.Kind != core.ValueKindText {
				return invalid(p.name + " subject must be text")
			}
		}
	case core.OperationUnsetField:
		if operation.UnsetField.Target.Block == emailMetadataBlockID {
			return invalid(p.name + " subject uses explicit empty and cannot be unset")
		}
		for _, node := range document.Nodes {
			if node.ID != operation.UnsetField.Target.Block {
				continue
			}
			rule, ok := findCatalogField(document.Catalog, node.Kind, operation.UnsetField.Target.Field)
			if ok && rule.Ownership == core.FieldOwnershipLocale {
				return invalid(p.name + " locale values use explicit empty; locale-owned fields cannot be unset")
			}
			break
		}
	case core.OperationInsertBlock:
		if operation.InsertBlock.Kind == emailMetadataBlockKind || operation.InsertBlock.Block == emailMetadataBlockID {
			return invalid(p.name + " metadata root cannot be inserted")
		}
		if operation.InsertBlock.Parent == "" {
			return invalid(p.name + " content Blocks must remain inside the fixed metadata root")
		}
	case core.OperationDeleteBlock:
		if operation.DeleteBlock.Block == emailMetadataBlockID {
			return invalid(p.name + " metadata root cannot be deleted")
		}
	case core.OperationMoveBlock:
		if operation.MoveBlock.Block == emailMetadataBlockID {
			return invalid(p.name + " metadata root cannot be moved")
		}
		if operation.MoveBlock.Parent == "" {
			return invalid(p.name + " content Blocks must remain inside the fixed metadata root")
		}
	case core.OperationReplaceBlockKind:
		if operation.ReplaceBlockKind.Block == emailMetadataBlockID || operation.ReplaceBlockKind.Kind == emailMetadataBlockKind {
			return invalid(p.name + " metadata root kind cannot be replaced")
		}
	case core.OperationCreateTranslation, core.OperationDeleteTranslation:
		return invalid(p.name + " translation lifecycle operation must be the only operation")
	}
	return nil
}

func unwrapEmailRichTextRoot(operation core.Operation) core.Operation {
	switch operation.Kind {
	case core.OperationInsertBlock:
		cloned := *operation.InsertBlock
		if cloned.Parent == emailMetadataBlockID {
			cloned.Parent = ""
		}
		operation.InsertBlock = &cloned
	case core.OperationMoveBlock:
		cloned := *operation.MoveBlock
		if cloned.Parent == emailMetadataBlockID {
			cloned.Parent = ""
		}
		operation.MoveBlock = &cloned
	}
	return operation
}

func (p *emailRichTextPort) validateIdentity(identity core.DocumentIdentity) error {
	if identity.Domain != p.domain {
		return fmt.Errorf("%s AI document requires domain %q", p.name, p.domain)
	}
	reference := strings.TrimSpace(string(identity.Reference))
	id, err := uuid.Parse(reference)
	if err != nil || id == uuid.Nil || id.String() != reference {
		return fmt.Errorf("%s AI document reference must be a canonical UUID", p.name)
	}
	return nil
}

func (p *emailRichTextPort) conflict(conflict *emailRichTextRevisionConflict, handles []string) error {
	return &core.ConflictError{Conflict: core.Conflict{
		Code:                    conflict.kind,
		CurrentDocumentRevision: core.Revision(conflict.currentDocumentRevision),
		CurrentTargetRevision:   emailRichTextCoreRevision(conflict.currentTargetRevision),
		AffectedHandles:         append([]string(nil), handles...),
	}}
}

func emailRichTextCoreRevision(value *string) *core.Revision {
	if value == nil {
		return nil
	}
	revision := core.Revision(*value)
	return &revision
}

func emailRichTextStringRevision(value *core.Revision) *string {
	if value == nil {
		return nil
	}
	revision := string(*value)
	return &revision
}

func emailRichTextDomainIssue(err error, operations []core.Operation) *core.OperationIssue {
	index := -1
	code := core.IssueInvalidOperation
	switch {
	case errors.Is(err, contentblock.ErrFileReference):
		code = core.IssueInvalidFileReference
		for candidate, operation := range operations {
			if operation.Kind == core.OperationAttachFile || operation.Kind == core.OperationDetachFile {
				index = candidate
				break
			}
		}
	case errors.Is(err, contentblock.ErrInvalidMutation):
	case connect.CodeOf(err) == connect.CodeInvalidArgument || connect.CodeOf(err) == connect.CodeFailedPrecondition:
	default:
		return nil
	}
	return &core.OperationIssue{Operation: index, Code: code, Message: err.Error()}
}

func emailRichTextSemanticChanges(document core.Document, operations []core.Operation) ([]core.Change, error) {
	current, err := core.DocumentAfterOperations(document, nil)
	if err != nil {
		return nil, err
	}
	changes := make([]core.Change, 0, len(operations))
	for index, operation := range operations {
		next, err := core.DocumentAfterOperations(current, []core.Operation{operation})
		if err != nil {
			return nil, fmt.Errorf("derive Email profile semantic change %d: %w", index, err)
		}
		if current.LocaleExists != next.LocaleExists || !reflect.DeepEqual(current.Nodes, next.Nodes) {
			changes = append(changes, core.Change{
				Operation: index, Kind: operation.Kind,
				AffectedHandles: emailRichTextOperationHandles(operation),
			})
		}
		current = next
	}
	return changes, nil
}

func emailRichTextOperationHandles(operation core.Operation) []string {
	switch operation.Kind {
	case core.OperationSetField:
		return []string{string(operation.SetField.Target.Block) + ":" + string(operation.SetField.Target.Field)}
	case core.OperationUnsetField:
		return []string{string(operation.UnsetField.Target.Block) + ":" + string(operation.UnsetField.Target.Field)}
	case core.OperationInsertBlock:
		return []string{string(operation.InsertBlock.Block)}
	case core.OperationDeleteBlock:
		return []string{string(operation.DeleteBlock.Block)}
	case core.OperationMoveBlock:
		return []string{string(operation.MoveBlock.Block)}
	case core.OperationReplaceBlockKind:
		return []string{string(operation.ReplaceBlockKind.Block)}
	case core.OperationAttachFile:
		return []string{string(operation.AttachFile.Target.Block), string(operation.AttachFile.File)}
	case core.OperationDetachFile:
		return []string{string(operation.DetachFile.Target.Block)}
	case core.OperationCreateTranslation, core.OperationDeleteTranslation:
		return []string{"locale"}
	default:
		return nil
	}
}

var _ core.DomainPort = (*emailRichTextPort)(nil)
var _ core.ExactMutationPort = (*emailRichTextPort)(nil)
