package aidocumentadapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	programeventdomain "github.com/echovisionlab/geul-api/internal/programevent"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

const (
	programEventMetadataBlockID   core.BlockID   = "document"
	programEventMetadataBlockKind core.BlockKind = "program_event"
	programEventTitleField        core.FieldID   = "title"
	programEventSummaryField      core.FieldID   = "summary"
)

func programEventCatalog(codec *RichTextCodec) core.Catalog {
	catalog := codec.Catalog()
	catalog.BlockKinds = append(catalog.BlockKinds, programEventMetadataBlockKind)
	catalog.Fields = append(catalog.Fields,
		core.FieldRule{BlockKind: programEventMetadataBlockKind, Field: programEventTitleField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipSource},
		core.FieldRule{BlockKind: programEventMetadataBlockKind, Field: programEventSummaryField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true},
	)
	fingerprint := sha256.Sum256([]byte(catalog.Fingerprint + ":program-event-title-summary:dcdp/1"))
	catalog.Fingerprint = hex.EncodeToString(fingerprint[:])
	return catalog
}

// NewProgramEventRegistration binds DCDP to the existing Program Event
// aggregate service. The adapter owns only generated Rich Text projection and
// compilation; Program Event owns authorization, lifecycle, transaction, CAS,
// locale presence and File reuse validation.
func NewProgramEventRegistration(service *programeventdomain.ProgramEventService) (DomainRegistration, error) {
	if service == nil {
		return DomainRegistration{}, errors.New("program Event AI document service is required")
	}
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_PROGRAM_EVENT)
	if err != nil {
		return DomainRegistration{}, fmt.Errorf("create Program Event Rich Text codec: %w", err)
	}
	return DomainRegistration{Domain: core.DomainProgramEvent, Port: &programEventPort{service: service, codec: codec}}, nil
}

type programEventPort struct {
	service programEventDocumentAPI
	codec   *RichTextCodec
}

type programEventDocumentAPI interface {
	LoadAIDocumentState(context.Context, string, string) (programeventdomain.AIDocumentState, error)
	ExecuteAIDocumentCommand(
		context.Context,
		string,
		string,
		programeventdomain.AIDocumentExecutionMode,
		programeventdomain.AIDocumentCommandCompiler,
	) (programeventdomain.AIDocumentResult, error)
}

func (p *programEventPort) Load(
	ctx context.Context,
	identity core.DocumentIdentity,
	locale core.Locale,
) (core.Document, error) {
	if err := validateProgramEventIdentity(identity); err != nil {
		return core.Document{}, err
	}
	state, err := p.service.LoadAIDocumentState(ctx, string(identity.Reference), string(locale))
	if err != nil {
		return core.Document{}, err
	}
	return p.project(identity, locale, state)
}

func (p *programEventPort) project(
	identity core.DocumentIdentity,
	locale core.Locale,
	state programeventdomain.AIDocumentState,
) (core.Document, error) {
	localized, err := programEventLocalizedDocument(state)
	if err != nil {
		return core.Document{}, err
	}
	nodes, err := p.codec.Project(localized)
	if err != nil {
		return core.Document{}, fmt.Errorf("project Program Event AI document: %w", err)
	}
	metadata := core.Node{ID: programEventMetadataBlockID, Kind: programEventMetadataBlockKind,
		Shared: []core.FieldValue{{ID: programEventTitleField, Value: core.Text(state.Title)}},
	}
	if state.LocaleExists && state.Summary != nil {
		metadata.Localized = append(metadata.Localized, core.FieldValue{ID: programEventSummaryField, Value: core.Text(*state.Summary)})
	}
	nodes = append([]core.Node{metadata}, nodes...)
	return core.Document{
		Identity: identity, DocumentRevision: core.Revision(state.DocumentRevision),
		TargetRevision: programEventCoreRevision(state.TargetRevision),
		SourceLocale:   core.Locale(state.SourceLocale), Locale: locale,
		LocaleExists: state.LocaleExists, Catalog: programEventCatalog(p.codec), Nodes: nodes,
	}, nil
}

func (p *programEventPort) ValidateMutation(
	ctx context.Context,
	request core.ApplyRequest,
) (core.ValidationResult, error) {
	validation, _, err := p.executeMutation(ctx, request, programeventdomain.AIDocumentExecutionValidate)
	return validation, err
}

func (p *programEventPort) ExecuteMutation(
	ctx context.Context,
	request core.ApplyRequest,
) (core.ApplyResult, error) {
	_, result, err := p.executeMutation(ctx, request, programeventdomain.AIDocumentExecutionApply)
	return result, err
}

func (p *programEventPort) executeMutation(
	ctx context.Context,
	request core.ApplyRequest,
	mode programeventdomain.AIDocumentExecutionMode,
) (core.ValidationResult, core.ApplyResult, error) {
	identity := request.Identity()
	if err := validateProgramEventIdentity(identity); err != nil {
		return core.ValidationResult{}, core.ApplyResult{}, err
	}

	run := newExactMutationRun("Program Event")
	var changes []core.Change
	domainResult, err := p.service.ExecuteAIDocumentCommand(
		ctx,
		string(identity.Reference),
		string(request.Locale),
		mode,
		func(state programeventdomain.AIDocumentState) (programeventdomain.AIDocumentCommand, error) {
			current, err := p.project(identity, request.Locale, state)
			if err != nil {
				return programeventdomain.AIDocumentCommand{}, err
			}
			if err := run.validateLoaded(current, request); err != nil {
				return programeventdomain.AIDocumentCommand{}, err
			}
			if err := run.rejectIssues(validateProgramEventOperations(current, run.command.Operations)); err != nil {
				return programeventdomain.AIDocumentCommand{}, err
			}
			contributor, err := uuid.Parse(state.ViewerMemberID)
			if err != nil || contributor == uuid.Nil || contributor.String() != state.ViewerMemberID {
				return programeventdomain.AIDocumentCommand{}, errors.New("program Event AI document contributor Member UUID is invalid")
			}
			localized, err := programEventLocalizedDocument(state)
			if err != nil {
				return programeventdomain.AIDocumentCommand{}, err
			}
			changes, err = programEventSemanticChanges(current, run.command.Operations)
			if err != nil {
				return programeventdomain.AIDocumentCommand{}, err
			}
			compiled, issues, err := p.compileCommand(state, localized, contributor, current, run.command.Operations)
			if err != nil {
				return programeventdomain.AIDocumentCommand{}, err
			}
			if err := run.rejectIssues(issues); err != nil {
				return programeventdomain.AIDocumentCommand{}, err
			}
			return compiled, nil
		},
	)
	if err != nil {
		if invalid, mapped, handled := handleExactMutationValidationError(
			err, mode == programeventdomain.AIDocumentExecutionValidate,
		); handled {
			return invalid, core.ApplyResult{}, mapped
		}
		var conflict *programeventdomain.AIDocumentConflict
		if errors.As(err, &conflict) {
			code := core.ConflictDocumentRevision
			if conflict.Kind == programeventdomain.AIDocumentTargetRevisionConflict {
				code = core.ConflictTargetRevision
			}
			return core.ValidationResult{}, core.ApplyResult{}, &core.ConflictError{Conflict: core.Conflict{
				Code: code, CurrentDocumentRevision: core.Revision(conflict.CurrentDocumentRevision),
				CurrentTargetRevision: programEventCoreRevision(conflict.CurrentTargetRevision),
				AffectedHandles:       append([]string(nil), programEventRequestAffectedHandles(request)...),
			}}
		}
		operations := run.command.Operations
		if len(operations) == 0 {
			operations = request.Operations
		}
		if issue := programEventDomainIssue(err, operations); issue != nil {
			run.validation.Issues = append(run.validation.Issues, *issue)
			if mode == programeventdomain.AIDocumentExecutionValidate {
				return run.validation, core.ApplyResult{}, nil
			}
			return core.ValidationResult{}, core.ApplyResult{}, &core.ValidationError{Result: run.validation}
		}
		return core.ValidationResult{}, core.ApplyResult{}, err
	}
	if mode == programeventdomain.AIDocumentExecutionValidate {
		return run.validation, core.ApplyResult{}, nil
	}

	applicationResult := core.ApplyResult{
		DocumentRevision: core.Revision(domainResult.DocumentRevision),
		TargetRevision:   programEventCoreRevision(domainResult.TargetRevision),
		Changed:          domainResult.Changed,
	}
	if domainResult.Changed {
		if len(changes) == 0 {
			return core.ValidationResult{}, core.ApplyResult{}, errors.New("program Event persisted a mutation without a semantic DCDP change")
		}
		applicationResult.Changes = changes
	}
	accepted, err := run.accept(applicationResult)
	return run.validation, accepted, err
}

func programEventRequestAffectedHandles(request core.ApplyRequest) []string {
	var handles []string
	for _, operation := range request.Operations {
		handles = append(handles, programEventOperationHandles(operation)...)
	}
	return handles
}

func (p *programEventPort) compileCommand(
	state programeventdomain.AIDocumentState,
	localized *contentv1.LocalizedRichTextDocument,
	contributor uuid.UUID,
	loaded core.Document,
	operations []core.Operation,
) (programeventdomain.AIDocumentCommand, []core.OperationIssue, error) {
	expected, err := uuid.Parse(string(loaded.DocumentRevision))
	if err != nil || expected == uuid.Nil || expected.String() != string(loaded.DocumentRevision) {
		return programeventdomain.AIDocumentCommand{}, nil, errors.New("program Event AI document revision is not a canonical UUID")
	}
	command := programeventdomain.AIDocumentCommand{
		EventID: string(loaded.Identity.Reference), RequestedLocale: string(loaded.Locale),
		ObservedSourceLocale: state.SourceLocale, ObservedLocaleExists: state.LocaleExists,
		ExpectedRevision: expected, ExpectedTargetRevision: programEventStringRevision(loaded.TargetRevision),
		ContributorMemberID: contributor,
	}
	if len(operations) == 1 && operations[0].Kind == core.OperationCreateTranslation {
		command.CreateTranslation = true
		return command, nil, nil
	}
	if len(operations) == 1 && operations[0].Kind == core.OperationDeleteTranslation {
		command.DeleteTranslation = true
		return command, nil, nil
	}
	contentOperations := make([]core.Operation, 0, len(operations))
	contentIndexes := make([]int, 0, len(operations))
	for index, operation := range operations {
		handled, issue := compileProgramEventMetadataOperation(&command.Metadata, loaded.Role(), operation, index)
		if issue != nil {
			return programeventdomain.AIDocumentCommand{}, []core.OperationIssue{*issue}, nil
		}
		if !handled {
			contentOperations = append(contentOperations, operation)
			contentIndexes = append(contentIndexes, index)
		}
	}
	if len(contentOperations) == 0 {
		command.Batch = &contentblock.Batch{DocumentID: state.ContentDocumentID, ExpectedRevision: expected, ContributorMemberIDs: []uuid.UUID{contributor}}
		return command, nil, nil
	}
	batch, issues, err := p.codec.Compile(
		state.ContentDocumentID, localized, loaded.Role(), loaded.DocumentRevision,
		contributor, contentOperations,
	)
	for index := range issues {
		if issues[index].Operation >= 0 && issues[index].Operation < len(contentIndexes) {
			issues[index].Operation = contentIndexes[issues[index].Operation]
		}
	}
	if err != nil || len(issues) != 0 {
		return programeventdomain.AIDocumentCommand{}, issues, err
	}
	command.Batch = &batch
	return command, nil, nil
}

func compileProgramEventMetadataOperation(patch *programeventdomain.AIDocumentMetadataPatch, role core.LocaleRole, operation core.Operation, index int) (bool, *core.OperationIssue) {
	var target core.FieldTarget
	var value *core.Value
	switch operation.Kind {
	case core.OperationSetField:
		target, value = operation.SetField.Target, &operation.SetField.Value
	case core.OperationUnsetField:
		target = operation.UnsetField.Target
	default:
		return false, nil
	}
	if target.Block != programEventMetadataBlockID {
		return false, nil
	}
	fail := func(code core.IssueCode, message string) (bool, *core.OperationIssue) {
		return true, &core.OperationIssue{Operation: index, Code: code, Handle: strings.Join(programEventOperationHandles(operation), ":"), Message: message}
	}
	if target.Relation != "" || target.Item != "" || len(target.Path) != 0 {
		return fail(core.IssueInvalidOperation, "Program Event metadata does not expose nested or relation targets")
	}
	if target.Field != programEventTitleField && target.Field != programEventSummaryField {
		return fail(core.IssueUnknownField, "unsupported Program Event metadata field")
	}
	if target.Field == programEventTitleField {
		if role != core.LocaleRoleSource {
			return fail(core.IssueInvalidOperation, "Program Event title is source-owned")
		}
		if value == nil || value.Kind != core.ValueKindText || strings.TrimSpace(value.Text) == "" {
			return fail(core.IssueInvalidOperation, "Program Event source title cannot be empty or removed")
		}
		title := strings.TrimSpace(value.Text)
		patch.SetTitle, patch.Title = true, &title
		return true, nil
	}
	if value == nil {
		patch.SetSummary, patch.Summary = true, nil
		return true, nil
	}
	if value.Kind != core.ValueKindText {
		return fail(core.IssueValueKindMismatch, "Program Event summary must be text")
	}
	summary := value.Text
	patch.SetSummary, patch.Summary = true, &summary
	return true, nil
}

func programEventLocalizedDocument(state programeventdomain.AIDocumentState) (*contentv1.LocalizedRichTextDocument, error) {
	if state.LocalizedDocument == nil || state.LocalizedDocument.GetBase() == nil {
		return nil, errors.New("program Event Rich Text document is required")
	}
	cloned, ok := proto.Clone(state.LocalizedDocument).(*contentv1.LocalizedRichTextDocument)
	if !ok || cloned == nil {
		return nil, errors.New("clone Program Event Rich Text document")
	}
	return cloned, nil
}

func programEventCoreRevision(value *string) *core.Revision {
	if value == nil {
		return nil
	}
	revision := core.Revision(*value)
	return &revision
}

func programEventStringRevision(value *core.Revision) *string {
	if value == nil {
		return nil
	}
	revision := string(*value)
	return &revision
}

func validateProgramEventIdentity(identity core.DocumentIdentity) error {
	if identity.Domain != core.DomainProgramEvent {
		return fmt.Errorf("program Event AI document received domain %q", identity.Domain)
	}
	reference := string(identity.Reference)
	id, err := uuid.Parse(reference)
	if err != nil || id == uuid.Nil || id.String() != reference {
		return errors.New("program Event AI document reference must be a canonical UUID")
	}
	return nil
}

func validateProgramEventOperations(document core.Document, operations []core.Operation) []core.OperationIssue {
	issues := make([]core.OperationIssue, 0)
	for index, operation := range operations {
		invalidStructure := false
		switch operation.Kind {
		case core.OperationInsertBlock:
			invalidStructure = operation.InsertBlock.Block == programEventMetadataBlockID || operation.InsertBlock.Kind == programEventMetadataBlockKind
		case core.OperationDeleteBlock:
			invalidStructure = operation.DeleteBlock.Block == programEventMetadataBlockID
		case core.OperationMoveBlock:
			invalidStructure = operation.MoveBlock.Block == programEventMetadataBlockID
		case core.OperationReplaceBlockKind:
			invalidStructure = operation.ReplaceBlockKind.Block == programEventMetadataBlockID || operation.ReplaceBlockKind.Kind == programEventMetadataBlockKind
		}
		if invalidStructure {
			issues = append(issues, core.OperationIssue{Operation: index, Code: core.IssueInvalidOperation, Handle: string(programEventMetadataBlockID), Message: "Program Event metadata structure is fixed"})
			continue
		}
		if operation.Kind != core.OperationUnsetField {
			continue
		}
		target := operation.UnsetField.Target
		if target.Block == programEventMetadataBlockID && target.Field == programEventSummaryField {
			continue
		}
		for _, node := range document.Nodes {
			if node.ID != target.Block {
				continue
			}
			rule, ok := findCatalogField(document.Catalog, node.Kind, target.Field)
			if ok && rule.Ownership == core.FieldOwnershipLocale {
				issues = append(issues, core.OperationIssue{
					Operation: index, Code: core.IssueInvalidOperation,
					Handle:  strings.Join(programEventOperationHandles(operation), ","),
					Message: "Program Event locale values use explicit empty; locale-owned fields cannot be unset",
				})
			}
			break
		}
	}
	return issues
}

func programEventDomainIssue(err error, operations []core.Operation) *core.OperationIssue {
	if !errors.Is(err, contentblock.ErrFileReference) {
		return nil
	}
	index := -1
	for candidate, operation := range operations {
		if operation.Kind != core.OperationAttachFile && operation.Kind != core.OperationDetachFile {
			continue
		}
		// A batch-level File error identifies an operation only when exactly
		// one File mutation is present. Preserve the original error otherwise.
		if index >= 0 {
			return nil
		}
		index = candidate
	}
	if index < 0 {
		return nil
	}
	return &core.OperationIssue{Operation: index, Code: core.IssueInvalidFileReference, Message: err.Error()}
}

func programEventOperationHandles(operation core.Operation) []string {
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
		return []string{string(operation.AttachFile.Target.Block) + ":" + string(operation.AttachFile.Target.Field), string(operation.AttachFile.File)}
	case core.OperationDetachFile:
		return []string{string(operation.DetachFile.Target.Block) + ":" + string(operation.DetachFile.Target.Field)}
	case core.OperationCreateTranslation, core.OperationDeleteTranslation:
		return []string{"locale"}
	default:
		return nil
	}
}

func programEventSemanticChanges(document core.Document, operations []core.Operation) ([]core.Change, error) {
	current, err := core.DocumentAfterOperations(document, nil)
	if err != nil {
		return nil, err
	}
	changes := make([]core.Change, 0, len(operations))
	for index, operation := range operations {
		next, err := core.DocumentAfterOperations(current, []core.Operation{operation})
		if err != nil {
			return nil, fmt.Errorf("derive Program Event semantic change %d: %w", index, err)
		}
		if current.LocaleExists != next.LocaleExists || !reflect.DeepEqual(current.Nodes, next.Nodes) {
			changes = append(changes, core.Change{
				Operation: index, Kind: operation.Kind,
				AffectedHandles: programEventOperationHandles(operation),
			})
		}
		current = next
	}
	return changes, nil
}

var _ core.DomainPort = (*programEventPort)(nil)
var _ core.ExactMutationPort = (*programEventPort)(nil)
