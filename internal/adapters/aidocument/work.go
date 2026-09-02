package aidocumentadapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	workdomain "github.com/echovisionlab/geul-api/internal/work"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

const (
	workMetadataBlockID   core.BlockID   = "document"
	workMetadataBlockKind core.BlockKind = "work"
	workTitleField        core.FieldID   = "title"
	workSummaryField      core.FieldID   = "summary"
)

type workPort struct {
	application workDocumentAPI
	codec       *RichTextCodec
	catalog     core.Catalog
}

type workDocumentAPI interface {
	Load(context.Context, string, string) (workdomain.AIDocumentState, error)
	ExecuteAIDocumentMutation(
		context.Context,
		string,
		string,
		workdomain.AIDocumentExecutionMode,
		workdomain.AIDocumentMutationCompiler,
	) (workdomain.AIDocumentMutationResult, error)
}

func NewWorkRegistration(internal *workdomain.InternalWorkService) (DomainRegistration, error) {
	application, err := workdomain.NewAIDocumentService(internal)
	if err != nil {
		return DomainRegistration{}, err
	}
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK)
	if err != nil {
		return DomainRegistration{}, fmt.Errorf("create Work Rich Text codec: %w", err)
	}
	catalog := workCatalog(codec)
	return DomainRegistration{Domain: core.DomainWork, Port: &workPort{
		application: application, codec: codec, catalog: catalog,
	}}, nil
}

func workCatalog(codec *RichTextCodec) core.Catalog {
	catalog := codec.Catalog()
	catalog.BlockKinds = append(catalog.BlockKinds, workMetadataBlockKind)
	catalog.Fields = append(catalog.Fields,
		core.FieldRule{BlockKind: workMetadataBlockKind, Field: workTitleField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true},
		core.FieldRule{BlockKind: workMetadataBlockKind, Field: workSummaryField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true},
	)
	fingerprint := sha256.Sum256([]byte(catalog.Fingerprint + ":work-title-summary:dcdp/1"))
	catalog.Fingerprint = hex.EncodeToString(fingerprint[:])
	return catalog
}

func (p *workPort) Load(ctx context.Context, identity core.DocumentIdentity, locale core.Locale) (core.Document, error) {
	if err := validateWorkIdentity(identity); err != nil {
		return core.Document{}, err
	}
	state, err := p.application.Load(ctx, string(identity.Reference), string(locale))
	if err != nil {
		return core.Document{}, err
	}
	return p.project(identity, locale, state)
}

func (p *workPort) ValidateMutation(
	ctx context.Context,
	request core.ApplyRequest,
) (core.ValidationResult, error) {
	validation, _, err := p.executeMutation(ctx, request, workdomain.AIDocumentExecutionValidate)
	return validation, err
}

func (p *workPort) ExecuteMutation(
	ctx context.Context,
	request core.ApplyRequest,
) (core.ApplyResult, error) {
	_, result, err := p.executeMutation(ctx, request, workdomain.AIDocumentExecutionApply)
	return result, err
}

func (p *workPort) executeMutation(
	ctx context.Context,
	request core.ApplyRequest,
	mode workdomain.AIDocumentExecutionMode,
) (core.ValidationResult, core.ApplyResult, error) {
	identity := request.Identity()
	if err := validateWorkIdentity(identity); err != nil {
		return core.ValidationResult{}, core.ApplyResult{}, err
	}

	run := newExactMutationRun("Work")
	domainResult, err := p.application.ExecuteAIDocumentMutation(
		ctx,
		string(identity.Reference),
		string(request.Locale),
		mode,
		func(state workdomain.AIDocumentState) (workdomain.AIDocumentMutation, error) {
			current, err := p.project(identity, request.Locale, state)
			if err != nil {
				return workdomain.AIDocumentMutation{}, err
			}
			if err := run.validateLoaded(current, request); err != nil {
				return workdomain.AIDocumentMutation{}, err
			}
			contributor, err := canonicalWorkContributor(state.ViewerMemberID)
			if err != nil {
				return workdomain.AIDocumentMutation{}, err
			}
			mutation, issues, err := p.compile(state, contributor, current, run.command.Operations)
			if err != nil {
				return workdomain.AIDocumentMutation{}, err
			}
			if err := run.rejectIssues(issues); err != nil {
				return workdomain.AIDocumentMutation{}, err
			}
			return mutation, nil
		},
	)
	if err != nil {
		if invalid, mapped, handled := handleExactMutationValidationError(
			err, mode == workdomain.AIDocumentExecutionValidate,
		); handled {
			return invalid, core.ApplyResult{}, mapped
		}
		var conflict *workdomain.AIDocumentRevisionConflictError
		if errors.As(err, &conflict) {
			code := core.ConflictDocumentRevision
			if conflict.Kind == workdomain.AIDocumentTargetRevisionConflict {
				code = core.ConflictTargetRevision
			}
			return core.ValidationResult{}, core.ApplyResult{}, &core.ConflictError{Conflict: core.Conflict{
				Code:                    code,
				CurrentDocumentRevision: core.Revision(conflict.CurrentDocumentRevision),
				CurrentTargetRevision:   workCoreTargetRevision(conflict.CurrentTargetRevision),
				AffectedHandles:         append([]string(nil), run.command.AffectedHandles...),
			}}
		}
		operations := run.command.Operations
		if len(operations) == 0 {
			operations = request.Operations
		}
		if issue := workDomainIssue(err, operations); issue != nil {
			run.validation.Issues = append(run.validation.Issues, *issue)
			if mode == workdomain.AIDocumentExecutionValidate {
				return run.validation, core.ApplyResult{}, nil
			}
			return core.ValidationResult{}, core.ApplyResult{}, &core.ValidationError{Result: run.validation}
		}
		return core.ValidationResult{}, core.ApplyResult{}, err
	}
	if mode == workdomain.AIDocumentExecutionValidate {
		return run.validation, core.ApplyResult{}, nil
	}

	output := core.ApplyResult{
		DocumentRevision: core.Revision(domainResult.Content.DocumentRevision.String()),
		TargetRevision:   workCoreTargetRevision(domainResult.TargetRevision),
		Changed:          domainResult.Content.Changed,
	}
	if domainResult.Content.Changed {
		for index, operation := range run.command.Operations {
			output.Changes = append(output.Changes, core.Change{
				Operation: index, Kind: operation.Kind, AffectedHandles: workOperationHandles(operation),
			})
		}
	}
	accepted, err := run.accept(output)
	return run.validation, accepted, err
}

func (p *workPort) project(identity core.DocumentIdentity, locale core.Locale, state workdomain.AIDocumentState) (core.Document, error) {
	nodes, err := p.codec.Project(state.Document)
	if err != nil {
		return core.Document{}, fmt.Errorf("project Work AI document: %w", err)
	}
	metadata := core.Node{ID: workMetadataBlockID, Kind: workMetadataBlockKind}
	if state.Title != nil {
		metadata.Localized = append(metadata.Localized, core.FieldValue{ID: workTitleField, Value: core.Text(*state.Title)})
	}
	if state.Summary != nil {
		metadata.Localized = append(metadata.Localized, core.FieldValue{ID: workSummaryField, Value: core.Text(*state.Summary)})
	}
	nodes = append([]core.Node{metadata}, nodes...)
	return core.Document{
		Identity: identity, DocumentRevision: core.Revision(state.DocumentRevision),
		SourceLocale: core.Locale(state.SourceLocale), Locale: locale,
		LocaleExists: state.LocaleExists, TargetRevision: workCoreTargetRevision(state.TargetRevision),
		Catalog: p.catalog, Nodes: nodes,
	}, nil
}

func workCoreTargetRevision(revision *string) *core.Revision {
	if revision == nil {
		return nil
	}
	converted := core.Revision(*revision)
	return &converted
}

func workDomainTargetRevision(revision *core.Revision) *string {
	if revision == nil {
		return nil
	}
	converted := string(*revision)
	return &converted
}

func canonicalWorkContributor(memberID string) (uuid.UUID, error) {
	contributor, err := uuid.Parse(memberID)
	if err != nil || contributor == uuid.Nil || contributor.String() != memberID {
		return uuid.Nil, errors.New("work AI document contributor Member UUID is invalid")
	}
	return contributor, nil
}

func (p *workPort) compile(
	state workdomain.AIDocumentState,
	contributor uuid.UUID,
	loaded core.Document,
	operations []core.Operation,
) (workdomain.AIDocumentMutation, []core.OperationIssue, error) {
	expected, err := uuid.Parse(string(loaded.DocumentRevision))
	if err != nil || expected == uuid.Nil || expected.String() != string(loaded.DocumentRevision) {
		return workdomain.AIDocumentMutation{}, nil, errors.New("work AI document revision is not a canonical UUID")
	}
	mutation := workdomain.AIDocumentMutation{
		WorkID: string(loaded.Identity.Reference), Locale: string(loaded.Locale),
		ExpectedRevision: expected, ObservedSourceLocale: string(loaded.SourceLocale),
		ExpectedTargetRevision: workDomainTargetRevision(loaded.TargetRevision),
		ObservedLocaleExists:   loaded.LocaleExists,
		ContributorMemberID:    contributor.String(),
		Batch: contentblock.Batch{
			DocumentID: state.Snapshot.Document.ID, ExpectedRevision: expected,
			ContributorMemberIDs: []uuid.UUID{contributor},
		},
	}
	for index, operation := range operations {
		if (operation.Kind == core.OperationCreateTranslation || operation.Kind == core.OperationDeleteTranslation) && len(operations) != 1 {
			return workdomain.AIDocumentMutation{}, []core.OperationIssue{{
				Operation: index, Code: core.IssueInvalidOperation,
				Message: "Work translation lifecycle operation must be exclusive",
			}}, nil
		}
	}
	if len(operations) == 1 && operations[0].Kind == core.OperationCreateTranslation {
		mutation.CreateTranslation = true
		return mutation, nil, nil
	}
	if len(operations) == 1 && operations[0].Kind == core.OperationDeleteTranslation {
		mutation.DeleteTranslation = true
		return mutation, nil, nil
	}
	contentOperations := make([]core.Operation, 0, len(operations))
	issues := make([]core.OperationIssue, 0)
	for index, operation := range operations {
		if loaded.Role() == core.LocaleRoleNonSource && operation.Kind == core.OperationUnsetField {
			issues = append(issues, core.OperationIssue{
				Operation: index, Code: core.IssueInvalidOperation,
				Handle:  strings.Join(workOperationHandles(operation), ":"),
				Message: "Work target locale values use explicit empty and cannot be unset",
			})
			continue
		}
		if handled, issue := compileWorkMetadataOperation(&mutation.Metadata, operation, index); handled {
			if issue != nil {
				issues = append(issues, *issue)
			}
			continue
		}
		contentOperations = append(contentOperations, operation)
	}
	if len(issues) != 0 {
		return workdomain.AIDocumentMutation{}, issues, nil
	}
	batch, codecIssues, err := p.codec.Compile(
		state.Snapshot.Document.ID, state.Document, loaded.Role(), loaded.DocumentRevision,
		contributor, contentOperations,
	)
	if err != nil || len(codecIssues) != 0 {
		return workdomain.AIDocumentMutation{}, codecIssues, err
	}
	mutation.Batch = batch
	if loaded.Role() == core.LocaleRoleNonSource && !loaded.LocaleExists &&
		(mutation.Metadata.SetTitle || mutation.Metadata.SetSummary || len(batch.LocaleGroups) != 0) {
		mutation.Metadata.EnsureLocale = true
	}
	return mutation, nil, nil
}

func compileWorkMetadataOperation(
	patch *workdomain.AIDocumentMetadataPatch,
	operation core.Operation,
	index int,
) (bool, *core.OperationIssue) {
	target := core.FieldTarget{}
	var value *core.Value
	switch operation.Kind {
	case core.OperationSetField:
		target = operation.SetField.Target
		value = &operation.SetField.Value
	case core.OperationUnsetField:
		target = operation.UnsetField.Target
	default:
		if workOperationTargetsMetadata(operation) {
			return true, &core.OperationIssue{Operation: index, Code: core.IssueInvalidOperation, Handle: string(workMetadataBlockID), Message: "Work metadata structure cannot be changed"}
		}
		return false, nil
	}
	if target.Block != workMetadataBlockID {
		return false, nil
	}
	if operation.Kind == core.OperationUnsetField {
		return true, &core.OperationIssue{Operation: index, Code: core.IssueInvalidOperation, Handle: strings.Join(workOperationHandles(operation), ":"), Message: "Work locale values use explicit empty and cannot be unset"}
	}
	if value == nil || value.Kind != core.ValueKindText {
		return true, &core.OperationIssue{Operation: index, Code: core.IssueValueKindMismatch, Handle: strings.Join(workOperationHandles(operation), ":"), Message: "Work metadata value must be text"}
	}
	text := value.Text
	switch target.Field {
	case workTitleField:
		patch.SetTitle, patch.Title = true, &text
	case workSummaryField:
		patch.SetSummary, patch.Summary = true, &text
	default:
		return true, &core.OperationIssue{Operation: index, Code: core.IssueUnknownField, Handle: strings.Join(workOperationHandles(operation), ":"), Message: "unsupported Work metadata field"}
	}
	return true, nil
}

func workOperationTargetsMetadata(operation core.Operation) bool {
	switch operation.Kind {
	case core.OperationDeleteBlock:
		return operation.DeleteBlock.Block == workMetadataBlockID
	case core.OperationMoveBlock:
		return operation.MoveBlock.Block == workMetadataBlockID
	case core.OperationReplaceBlockKind:
		return operation.ReplaceBlockKind.Block == workMetadataBlockID
	default:
		return false
	}
}

func validateWorkIdentity(identity core.DocumentIdentity) error {
	if identity.Domain != core.DomainWork {
		return fmt.Errorf("work AI document received domain %q", identity.Domain)
	}
	reference := string(identity.Reference)
	id, err := uuid.Parse(reference)
	if err != nil || id == uuid.Nil || id.String() != reference {
		return errors.New("work AI document reference must be a canonical UUID")
	}
	return nil
}

func workDomainIssue(err error, operations []core.Operation) *core.OperationIssue {
	code := core.IssueInvalidOperation
	index := -1
	if errors.Is(err, contentblock.ErrFileReference) {
		code = core.IssueInvalidFileReference
		for candidate, operation := range operations {
			if operation.Kind == core.OperationAttachFile || operation.Kind == core.OperationDetachFile {
				index = candidate
				break
			}
		}
	} else if !errors.Is(err, contentblock.ErrInvalidMutation) {
		return nil
	}
	return &core.OperationIssue{Operation: index, Code: code, Message: err.Error()}
}

func workOperationHandles(operation core.Operation) []string {
	switch operation.Kind {
	case core.OperationSetField:
		return []string{string(operation.SetField.Target.Block), string(operation.SetField.Target.Field)}
	case core.OperationUnsetField:
		return []string{string(operation.UnsetField.Target.Block), string(operation.UnsetField.Target.Field)}
	case core.OperationInsertBlock:
		return []string{string(operation.InsertBlock.Block)}
	case core.OperationDeleteBlock:
		return []string{string(operation.DeleteBlock.Block)}
	case core.OperationMoveBlock:
		return []string{string(operation.MoveBlock.Block)}
	case core.OperationReplaceBlockKind:
		return []string{string(operation.ReplaceBlockKind.Block)}
	case core.OperationAttachFile:
		return []string{string(operation.AttachFile.Target.Block), string(operation.AttachFile.Target.Field), string(operation.AttachFile.File)}
	case core.OperationDetachFile:
		return []string{string(operation.DetachFile.Target.Block), string(operation.DetachFile.Target.Field)}
	case core.OperationCreateTranslation, core.OperationDeleteTranslation:
		return []string{"locale"}
	default:
		return nil
	}
}

var _ core.DomainPort = (*workPort)(nil)
var _ core.ExactMutationPort = (*workPort)(nil)
