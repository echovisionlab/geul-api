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
	pagedomain "github.com/echovisionlab/geul-api/internal/page"
)

const (
	pageMetadataBlockID   core.BlockID   = "document"
	pageMetadataBlockKind core.BlockKind = "page"
	pageTitleField        core.FieldID   = "title"
	pageSummaryField      core.FieldID   = "summary"
)

type pagePort struct {
	application pageDocumentAPI
	codec       *PageCodec
	catalog     core.Catalog
}

type pageDocumentAPI interface {
	Load(context.Context, string, string) (pagedomain.AIDocumentState, error)
	ExecuteAIDocumentMutation(
		context.Context,
		string,
		string,
		pagedomain.AIDocumentExecutionMode,
		pagedomain.AIDocumentMutationCompiler,
	) (pagedomain.AIDocumentMutationResult, error)
}

func NewPageRegistration(internal *pagedomain.InternalPageService) (DomainRegistration, error) {
	application, err := pagedomain.NewAIDocumentService(internal)
	if err != nil {
		return DomainRegistration{}, err
	}
	codec, err := NewPageCodec()
	if err != nil {
		return DomainRegistration{}, fmt.Errorf("create Page section codec: %w", err)
	}
	catalog := codec.Catalog()
	catalog.BlockKinds = append(catalog.BlockKinds, pageMetadataBlockKind)
	catalog.Fields = append(catalog.Fields,
		core.FieldRule{BlockKind: pageMetadataBlockKind, Field: pageTitleField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true},
		core.FieldRule{BlockKind: pageMetadataBlockKind, Field: pageSummaryField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true},
	)
	fingerprint := sha256.Sum256([]byte(catalog.Fingerprint + ":page-title-summary:dcdp/1"))
	catalog.Fingerprint = hex.EncodeToString(fingerprint[:])
	return DomainRegistration{Domain: core.DomainPage, Port: &pagePort{application: application, codec: codec, catalog: catalog}}, nil
}

func (p *pagePort) Load(ctx context.Context, identity core.DocumentIdentity, locale core.Locale) (core.Document, error) {
	if err := validatePageIdentity(identity); err != nil {
		return core.Document{}, err
	}
	state, err := p.application.Load(ctx, string(identity.Reference), string(locale))
	if err != nil {
		return core.Document{}, err
	}
	return p.project(identity, locale, state)
}

func (p *pagePort) ValidateMutation(
	ctx context.Context,
	request core.ApplyRequest,
) (core.ValidationResult, error) {
	validation, _, err := p.executeMutation(ctx, request, pagedomain.AIDocumentExecutionValidate)
	return validation, err
}

func (p *pagePort) ExecuteMutation(
	ctx context.Context,
	request core.ApplyRequest,
) (core.ApplyResult, error) {
	_, result, err := p.executeMutation(ctx, request, pagedomain.AIDocumentExecutionApply)
	return result, err
}

func (p *pagePort) executeMutation(
	ctx context.Context,
	request core.ApplyRequest,
	mode pagedomain.AIDocumentExecutionMode,
) (core.ValidationResult, core.ApplyResult, error) {
	identity := request.Identity()
	if err := validatePageIdentity(identity); err != nil {
		return core.ValidationResult{}, core.ApplyResult{}, err
	}

	run := newExactMutationRun("Page")
	domainResult, err := p.application.ExecuteAIDocumentMutation(
		ctx,
		string(identity.Reference),
		string(request.Locale),
		mode,
		func(state pagedomain.AIDocumentState) (pagedomain.AIDocumentMutation, error) {
			current, err := p.project(identity, request.Locale, state)
			if err != nil {
				return pagedomain.AIDocumentMutation{}, err
			}
			if err := run.validateLoaded(current, request); err != nil {
				return pagedomain.AIDocumentMutation{}, err
			}
			contributor, err := canonicalPageContributor(state.ViewerMemberID)
			if err != nil {
				return pagedomain.AIDocumentMutation{}, err
			}
			mutation, issues, err := p.compile(state, contributor, current, run.command.Operations)
			if err != nil {
				return pagedomain.AIDocumentMutation{}, err
			}
			if err := run.rejectIssues(issues); err != nil {
				return pagedomain.AIDocumentMutation{}, err
			}
			return mutation, nil
		},
	)
	if err != nil {
		if invalid, mapped, handled := handleExactMutationValidationError(
			err, mode == pagedomain.AIDocumentExecutionValidate,
		); handled {
			return invalid, core.ApplyResult{}, mapped
		}
		var conflict *pagedomain.AIDocumentRevisionConflictError
		if errors.As(err, &conflict) {
			code := core.ConflictDocumentRevision
			if conflict.Kind == pagedomain.AIDocumentTargetRevisionConflict {
				code = core.ConflictTargetRevision
			}
			return core.ValidationResult{}, core.ApplyResult{}, &core.ConflictError{Conflict: core.Conflict{
				Code: code, CurrentDocumentRevision: core.Revision(conflict.CurrentRevision),
				CurrentTargetRevision: pageCoreTargetRevision(conflict.CurrentTargetRevision),
				AffectedHandles:       append([]string(nil), run.command.AffectedHandles...),
			}}
		}
		operations := run.command.Operations
		if len(operations) == 0 {
			operations = request.Operations
		}
		if issue := pageDomainIssue(err, operations); issue != nil {
			run.validation.Issues = append(run.validation.Issues, *issue)
			if mode == pagedomain.AIDocumentExecutionValidate {
				return run.validation, core.ApplyResult{}, nil
			}
			return core.ValidationResult{}, core.ApplyResult{}, &core.ValidationError{Result: run.validation}
		}
		return core.ValidationResult{}, core.ApplyResult{}, err
	}
	if mode == pagedomain.AIDocumentExecutionValidate {
		return run.validation, core.ApplyResult{}, nil
	}

	output := core.ApplyResult{
		DocumentRevision: core.Revision(domainResult.DocumentRevision),
		TargetRevision:   pageCoreTargetRevision(domainResult.TargetRevision),
		Changed:          domainResult.Changed,
	}
	if domainResult.Changed {
		for index, operation := range run.command.Operations {
			output.Changes = append(output.Changes, core.Change{
				Operation: index, Kind: operation.Kind, AffectedHandles: pageOperationHandles(operation),
			})
		}
	}
	accepted, err := run.accept(output)
	return run.validation, accepted, err
}

func (p *pagePort) project(identity core.DocumentIdentity, locale core.Locale, state pagedomain.AIDocumentState) (core.Document, error) {
	nodes, err := p.codec.Project(state.Document)
	if err != nil {
		return core.Document{}, fmt.Errorf("project Page AI document: %w", err)
	}
	metadata := core.Node{ID: pageMetadataBlockID, Kind: pageMetadataBlockKind}
	if state.Title != nil {
		metadata.Localized = append(metadata.Localized, core.FieldValue{ID: pageTitleField, Value: core.Text(*state.Title)})
	}
	if state.Summary != nil {
		metadata.Localized = append(metadata.Localized, core.FieldValue{ID: pageSummaryField, Value: core.Text(*state.Summary)})
	}
	return core.Document{
		Identity: identity, DocumentRevision: core.Revision(state.Revision),
		TargetRevision: pageCoreTargetRevision(state.TargetRevision),
		SourceLocale:   core.Locale(state.SourceLocale), Locale: locale, LocaleExists: state.LocaleExists,
		Catalog: p.catalog, Nodes: append([]core.Node{metadata}, nodes...),
	}, nil
}

func canonicalPageContributor(memberID string) (uuid.UUID, error) {
	contributor, err := uuid.Parse(memberID)
	if err != nil || contributor == uuid.Nil || contributor.String() != memberID {
		return uuid.Nil, errors.New("page AI document contributor Member UUID is invalid")
	}
	return contributor, nil
}

func (p *pagePort) compile(state pagedomain.AIDocumentState, contributor uuid.UUID, loaded core.Document, operations []core.Operation) (pagedomain.AIDocumentMutation, []core.OperationIssue, error) {
	expected, err := uuid.Parse(string(loaded.DocumentRevision))
	if err != nil || expected == uuid.Nil || expected.String() != string(loaded.DocumentRevision) {
		return pagedomain.AIDocumentMutation{}, nil, errors.New("page AI document revision is not a canonical UUID")
	}
	mutation := pagedomain.AIDocumentMutation{
		PageID: string(loaded.Identity.Reference), Locale: string(loaded.Locale), ExpectedRevision: expected,
		ExpectedTargetRevision: pageDomainTargetRevision(loaded.TargetRevision),
		ObservedSourceLocale:   string(loaded.SourceLocale), ObservedLocaleExists: loaded.LocaleExists,
		ContributorMemberID: contributor.String(),
		Batch:               contentblock.Batch{DocumentID: state.Snapshot.Document.ID, ExpectedRevision: expected, ContributorMemberIDs: []uuid.UUID{contributor}},
	}
	for index, operation := range operations {
		if (operation.Kind == core.OperationCreateTranslation || operation.Kind == core.OperationDeleteTranslation) && len(operations) != 1 {
			return pagedomain.AIDocumentMutation{}, []core.OperationIssue{{Operation: index, Code: core.IssueInvalidOperation, Message: "Page translation lifecycle operation must be exclusive"}}, nil
		}
	}
	if len(operations) == 1 && operations[0].Kind == core.OperationCreateTranslation {
		mutation.Metadata.EnsureLocale = true
		return mutation, nil, nil
	}
	if len(operations) == 1 && operations[0].Kind == core.OperationDeleteTranslation {
		mutation.DeleteTranslation = true
		return mutation, nil, nil
	}
	contentOperations := make([]core.Operation, 0, len(operations))
	for index, operation := range operations {
		if handled, issue := compilePageMetadataOperation(&mutation.Metadata, operation, index); handled {
			if issue != nil {
				return pagedomain.AIDocumentMutation{}, []core.OperationIssue{*issue}, nil
			}
			continue
		}
		contentOperations = append(contentOperations, operation)
	}
	batch, issues, err := p.codec.Compile(state.Snapshot.Document.ID, state.Document, loaded.Role(), loaded.DocumentRevision, contributor, contentOperations)
	if err != nil || len(issues) != 0 {
		return pagedomain.AIDocumentMutation{}, issues, err
	}
	mutation.Batch = batch
	if loaded.Role() == core.LocaleRoleNonSource && !loaded.LocaleExists &&
		(mutation.Metadata.SetTitle || mutation.Metadata.SetSummary || len(batch.LocaleGroups) != 0) {
		mutation.Metadata.EnsureLocale = true
	}
	return mutation, nil, nil
}

func pageCoreTargetRevision(revision *string) *core.Revision {
	if revision == nil {
		return nil
	}
	converted := core.Revision(*revision)
	return &converted
}

func pageDomainTargetRevision(revision *core.Revision) *string {
	if revision == nil {
		return nil
	}
	converted := string(*revision)
	return &converted
}

func compilePageMetadataOperation(patch *pagedomain.AIDocumentMetadataPatch, operation core.Operation, index int) (bool, *core.OperationIssue) {
	var target core.FieldTarget
	var value *core.Value
	switch operation.Kind {
	case core.OperationSetField:
		target, value = operation.SetField.Target, &operation.SetField.Value
	case core.OperationUnsetField:
		target = operation.UnsetField.Target
	default:
		if pageOperationTargetsMetadata(operation) {
			return true, &core.OperationIssue{Operation: index, Code: core.IssueInvalidOperation, Handle: string(pageMetadataBlockID), Message: "Page metadata structure cannot be changed"}
		}
		return false, nil
	}
	if target.Block != pageMetadataBlockID {
		return false, nil
	}
	if operation.Kind == core.OperationUnsetField {
		return true, &core.OperationIssue{Operation: index, Code: core.IssueInvalidOperation, Handle: strings.Join(pageOperationHandles(operation), ":"), Message: "Page locale values use explicit empty and cannot be unset"}
	}
	if value == nil || value.Kind != core.ValueKindText {
		return true, &core.OperationIssue{Operation: index, Code: core.IssueValueKindMismatch, Message: "Page metadata value must be text"}
	}
	text := value.Text
	switch target.Field {
	case pageTitleField:
		patch.SetTitle, patch.Title = true, &text
	case pageSummaryField:
		patch.SetSummary, patch.Summary = true, &text
	default:
		return true, &core.OperationIssue{Operation: index, Code: core.IssueUnknownField, Message: "unsupported Page metadata field"}
	}
	return true, nil
}

func pageOperationTargetsMetadata(operation core.Operation) bool {
	switch operation.Kind {
	case core.OperationDeleteBlock:
		return operation.DeleteBlock.Block == pageMetadataBlockID
	case core.OperationMoveBlock:
		return operation.MoveBlock.Block == pageMetadataBlockID
	case core.OperationReplaceBlockKind:
		return operation.ReplaceBlockKind.Block == pageMetadataBlockID
	default:
		return false
	}
}

func validatePageIdentity(identity core.DocumentIdentity) error {
	if identity.Domain != core.DomainPage {
		return fmt.Errorf("page AI document received domain %q", identity.Domain)
	}
	reference := string(identity.Reference)
	id, err := uuid.Parse(reference)
	if err != nil || id == uuid.Nil || id.String() != reference {
		return errors.New("page AI document reference must be a canonical UUID")
	}
	return nil
}

func pageDomainIssue(err error, operations []core.Operation) *core.OperationIssue {
	code, index := core.IssueInvalidOperation, -1
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

func pageOperationHandles(operation core.Operation) []string {
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

var _ core.DomainPort = (*pagePort)(nil)
var _ core.ExactMutationPort = (*pagePort)(nil)
