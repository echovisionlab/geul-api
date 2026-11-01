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
	LoadAIDocument(context.Context, string, string) (workdomain.AIDocumentState, error)
	ExecuteAIDocumentMutation(
		context.Context,
		string,
		string,
		workdomain.AIDocumentExecutionMode,
		workdomain.AIDocumentMutationCompiler,
	) (workdomain.AIDocumentMutationResult, error)
}

func NewWorkRegistration(application *workdomain.Service) (DomainRegistration, error) {
	if application == nil {
		return DomainRegistration{}, errors.New("work AI document service dependencies are required")
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
	state, err := p.application.LoadAIDocument(ctx, string(identity.Reference), string(locale))
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

	return executeExactMutation(request, exactMutationSpec[workdomain.AIDocumentState, workdomain.AIDocumentMutation, workdomain.AIDocumentMutationResult]{
		Domain:       "Work",
		ValidateOnly: mode == workdomain.AIDocumentExecutionValidate,
		Execute: func(compiler func(workdomain.AIDocumentState) (workdomain.AIDocumentMutation, error)) (workdomain.AIDocumentMutationResult, error) {
			return p.application.ExecuteAIDocumentMutation(ctx, string(identity.Reference), string(request.Locale), mode, compiler)
		},
		Project: func(state workdomain.AIDocumentState) (core.Document, error) {
			return p.project(identity, request.Locale, state)
		},
		Compile: func(state workdomain.AIDocumentState, current core.Document, command core.ValidatedApply) (workdomain.AIDocumentMutation, []core.OperationIssue, error) {
			contributor, err := canonicalWorkContributor(state.ViewerMemberID)
			if err != nil {
				return workdomain.AIDocumentMutation{}, nil, err
			}
			mutation, issues, err := p.compile(state, contributor, current, command.Operations)
			if err != nil {
				return workdomain.AIDocumentMutation{}, nil, err
			}
			return mutation, issues, nil
		},
		Result: func(domainResult workdomain.AIDocumentMutationResult, command core.ValidatedApply) (core.ApplyResult, error) {
			output := core.ApplyResult{
				DocumentRevision: core.Revision(domainResult.Content.DocumentRevision.String()),
				TargetRevision:   core.OptionalRevision(domainResult.TargetRevision),
				Changed:          domainResult.Content.Changed,
			}
			output = withOperationChanges(command, output)

			return output, nil
		},
		DomainIssue: func(err error, _ []core.Operation) *core.OperationIssue { return workDomainIssue(err) },
	})
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
		LocaleExists: state.LocaleExists, TargetRevision: core.OptionalRevision(state.TargetRevision),
		Catalog: p.catalog, Nodes: nodes,
	}, nil
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
		ExpectedTargetRevision: core.OptionalRevisionString(loaded.TargetRevision),
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
	contentIndexes := make([]int, 0, len(operations))
	issues := make([]core.OperationIssue, 0)
	for index, operation := range operations {
		if loaded.Role() == core.LocaleRoleNonSource && operation.Kind == core.OperationUnsetField {
			issues = append(issues, core.OperationIssue{
				Operation: index, Code: core.IssueInvalidOperation,
				Handle:  strings.Join(operationIssuePath(operation), ":"),
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
		contentIndexes = append(contentIndexes, index)
	}
	if len(issues) != 0 {
		return workdomain.AIDocumentMutation{}, issues, nil
	}
	batch, codecIssues, err := p.codec.Compile(
		state.Snapshot.Document.ID, state.Document, loaded.Role(), loaded.DocumentRevision,
		contributor, contentOperations,
	)
	for index := range codecIssues {
		if codecIssues[index].Operation >= 0 && codecIssues[index].Operation < len(contentIndexes) {
			codecIssues[index].Operation = contentIndexes[codecIssues[index].Operation]
		}
	}
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
	var target core.FieldTarget
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
		return true, &core.OperationIssue{Operation: index, Code: core.IssueInvalidOperation, Handle: strings.Join(operationIssuePath(operation), ":"), Message: "Work locale values use explicit empty and cannot be unset"}
	}
	if value == nil || value.Kind != core.ValueKindText {
		return true, &core.OperationIssue{Operation: index, Code: core.IssueValueKindMismatch, Handle: strings.Join(operationIssuePath(operation), ":"), Message: "Work metadata value must be text"}
	}
	text := value.Text
	switch target.Field {
	case workTitleField:
		patch.SetTitle, patch.Title = true, &text
	case workSummaryField:
		patch.SetSummary, patch.Summary = true, &text
	default:
		return true, &core.OperationIssue{Operation: index, Code: core.IssueUnknownField, Handle: strings.Join(operationIssuePath(operation), ":"), Message: "unsupported Work metadata field"}
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

// workDomainIssue turns an invalid Block mutation into an operation issue. A
// File reference rejection never reaches here: the Work domain answers it as
// FailedPrecondition without contentblock.ErrFileReference in the chain.
func workDomainIssue(err error) *core.OperationIssue {
	if !errors.Is(err, contentblock.ErrInvalidMutation) {
		return nil
	}
	return &core.OperationIssue{Operation: -1, Code: core.IssueInvalidOperation, Message: err.Error()}
}

var _ core.DomainPort = (*workPort)(nil)
var _ core.ExactMutationPort = (*workPort)(nil)
