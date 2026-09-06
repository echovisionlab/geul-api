package aidocumentadapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	releasedomain "github.com/echovisionlab/geul-api/internal/release"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
)

type releasePort struct {
	service releaseDocumentAPI
	codec   *RichTextCodec
	catalog core.Catalog
}

type releaseDocumentAPI interface {
	LoadAIDocumentState(context.Context, string, string) (releasedomain.AIDocumentState, error)
	ExecuteAIDocumentMutation(
		context.Context,
		string,
		string,
		releasedomain.AIDocumentExecutionMode,
		releasedomain.AIDocumentMutationCompiler,
	) (releasedomain.AIDocumentMutationResult, error)
}

const (
	releaseMetadataBlockID core.BlockID   = "document"
	releaseMetadataKind    core.BlockKind = "release"
	releaseTitleField      core.FieldID   = "title"
	releaseCreditKind      core.BlockKind = "release_credit"
	releaseCreditNoteField core.FieldID   = "note"
)

func NewReleaseRegistration(service *releasedomain.InternalReleaseService) (DomainRegistration, error) {
	if service == nil {
		return DomainRegistration{}, errors.New("release AI document service is required")
	}
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT)
	if err != nil {
		return DomainRegistration{}, fmt.Errorf("create Release compact Rich Text codec: %w", err)
	}
	catalog := releaseCatalog(codec)
	return DomainRegistration{Domain: core.DomainRelease, Port: &releasePort{service: service, codec: codec, catalog: catalog}}, nil
}

func releaseCatalog(codec *RichTextCodec) core.Catalog {
	catalog := codec.Catalog()
	catalog.BlockKinds = append(append([]core.BlockKind(nil), catalog.BlockKinds...), releaseMetadataKind, releaseCreditKind)
	catalog.Fields = append(append([]core.FieldRule(nil), catalog.Fields...),
		core.FieldRule{BlockKind: releaseMetadataKind, Field: releaseTitleField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true},
		core.FieldRule{BlockKind: releaseCreditKind, Field: releaseCreditNoteField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true},
	)
	fingerprint := sha256.Sum256([]byte(catalog.Fingerprint + ":release-locale-metadata:dcdp/2"))
	catalog.Fingerprint = hex.EncodeToString(fingerprint[:])
	return catalog
}

func (p *releasePort) Load(ctx context.Context, identity core.DocumentIdentity, locale core.Locale) (core.Document, error) {
	if err := validateReleaseDocumentIdentity(identity); err != nil {
		return core.Document{}, err
	}
	state, err := p.service.LoadAIDocumentState(ctx, string(identity.Reference), string(locale))
	if err != nil {
		return core.Document{}, err
	}
	return p.document(identity, locale, state)
}

func (p *releasePort) ValidateMutation(
	ctx context.Context,
	request core.ApplyRequest,
) (core.ValidationResult, error) {
	validation, _, err := p.executeMutation(ctx, request, releasedomain.AIDocumentExecutionValidate)
	return validation, err
}

func (p *releasePort) ExecuteMutation(
	ctx context.Context,
	request core.ApplyRequest,
) (core.ApplyResult, error) {
	_, result, err := p.executeMutation(ctx, request, releasedomain.AIDocumentExecutionApply)
	return result, err
}

func (p *releasePort) executeMutation(
	ctx context.Context,
	request core.ApplyRequest,
	mode releasedomain.AIDocumentExecutionMode,
) (core.ValidationResult, core.ApplyResult, error) {
	identity := request.Identity()
	if err := validateReleaseDocumentIdentity(identity); err != nil {
		return core.ValidationResult{}, core.ApplyResult{}, err
	}

	run := newExactMutationRun("Release")
	domainResult, err := p.service.ExecuteAIDocumentMutation(
		ctx,
		string(identity.Reference),
		string(request.Locale),
		mode,
		func(state releasedomain.AIDocumentState) (releasedomain.AIDocumentMutation, error) {
			current, err := p.document(identity, request.Locale, state)
			if err != nil {
				return releasedomain.AIDocumentMutation{}, err
			}
			if err := run.validateLoaded(current, request); err != nil {
				return releasedomain.AIDocumentMutation{}, err
			}
			contributor, err := releaseContributorID(state.ViewerMemberID)
			if err != nil {
				return releasedomain.AIDocumentMutation{}, err
			}
			mutation, issues, err := p.compile(state, current, contributor, run.command.Operations)
			if err != nil {
				return releasedomain.AIDocumentMutation{}, err
			}
			if err := run.rejectIssues(issues); err != nil {
				return releasedomain.AIDocumentMutation{}, err
			}
			return mutation, nil
		},
	)
	if err != nil {
		if invalid, mapped, handled := handleExactMutationValidationError(
			err, mode == releasedomain.AIDocumentExecutionValidate,
		); handled {
			return invalid, core.ApplyResult{}, mapped
		}
		var conflict *releasedomain.AIDocumentRevisionConflictError
		if errors.As(err, &conflict) {
			code := core.ConflictDocumentRevision
			if conflict.Kind == releasedomain.AIDocumentTargetRevisionConflict {
				code = core.ConflictTargetRevision
			}
			return core.ValidationResult{}, core.ApplyResult{}, &core.ConflictError{Conflict: core.Conflict{
				Code: code, CurrentDocumentRevision: core.Revision(conflict.CurrentDocumentRevision),
				CurrentTargetRevision: releaseTargetRevision(conflict.CurrentTargetRevision),
				AffectedHandles:       append([]string(nil), run.command.AffectedHandles...),
			}}
		}
		return core.ValidationResult{}, core.ApplyResult{}, err
	}
	if mode == releasedomain.AIDocumentExecutionValidate {
		return run.validation, core.ApplyResult{}, nil
	}

	output := core.ApplyResult{
		DocumentRevision: core.Revision(domainResult.DocumentRevision), Changed: domainResult.Changed,
	}
	output.TargetRevision = releaseTargetRevision(domainResult.TargetRevision)
	if domainResult.Changed {
		for index, operation := range run.command.Operations {
			output.Changes = append(output.Changes, core.Change{
				Operation: index, Kind: operation.Kind,
				AffectedHandles: compactOperationHandles(operation, run.command.Locale, "release"),
			})
		}
	}
	accepted, err := run.accept(output)
	return run.validation, accepted, err
}

func (p *releasePort) document(identity core.DocumentIdentity, locale core.Locale, state releasedomain.AIDocumentState) (core.Document, error) {
	if state.ReleaseID != string(identity.Reference) || state.Locale != string(locale) || state.DocumentID == uuid.Nil || state.Document == nil {
		return core.Document{}, errors.New("release AI document facade returned inconsistent state")
	}
	nodes, err := p.codec.Project(state.Document)
	if err != nil {
		return core.Document{}, fmt.Errorf("project Release compact document: %w", err)
	}
	metadata := core.Node{ID: releaseMetadataBlockID, Kind: releaseMetadataKind}
	if state.RequestedMetadata != nil && state.RequestedMetadata.Title != nil {
		metadata.Localized = []core.FieldValue{{ID: releaseTitleField, Value: core.Text(*state.RequestedMetadata.Title)}}
	}
	nodes = append([]core.Node{metadata}, nodes...)
	for index, creditID := range state.CreditIDs {
		node := core.Node{ID: releaseCreditBlockID(creditID), Kind: releaseCreditKind, Order: len(nodes) + index}
		if state.RequestedMetadata != nil {
			if note, exists := state.RequestedMetadata.CreditNotes[creditID]; exists {
				node.Localized = []core.FieldValue{{ID: releaseCreditNoteField, Value: core.Text(note)}}
			}
		}
		nodes = append(nodes, node)
	}
	return core.Document{
		Identity: identity, DocumentRevision: core.Revision(state.DocumentRevision),
		TargetRevision: releaseTargetRevision(state.TargetRevision),
		SourceLocale:   core.Locale(state.SourceLocale), Locale: locale,
		LocaleExists: state.LocaleExists, Catalog: p.catalog, Nodes: nodes,
	}, nil
}

func (p *releasePort) compile(state releasedomain.AIDocumentState, document core.Document, contributor uuid.UUID, operations []core.Operation) (releasedomain.AIDocumentMutation, []core.OperationIssue, error) {
	mutation := releasedomain.AIDocumentMutation{
		ReleaseID: state.ReleaseID, Locale: string(document.Locale),
		ExpectedDocumentRevision: string(document.DocumentRevision),
		ExpectedTargetRevision:   releaseDomainTargetRevision(document.TargetRevision),
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
	contentOperations := make([]core.Operation, 0, len(operations))
	contentIndexes := make([]int, 0, len(operations))
	for index, operation := range operations {
		if operation.Kind == core.OperationUnsetField && operation.UnsetField != nil && (document.Role() == core.LocaleRoleNonSource || operation.UnsetField.Target.Field == richTextContentField || operation.UnsetField.Target.Block == releaseMetadataBlockID || strings.HasPrefix(string(operation.UnsetField.Target.Block), "credit-")) {
			return releasedomain.AIDocumentMutation{}, []core.OperationIssue{{Operation: index, Code: core.IssueInvalidOperation, Handle: strings.Join(compactOperationHandles(operation, document.Locale, "release"), "/"), Message: "Release locale values use explicit empty instead of unset"}}, nil
		}
		handled, issue := compileReleaseCreditNote(&mutation, operation, index)
		if handled {
			if issue != nil {
				return releasedomain.AIDocumentMutation{}, []core.OperationIssue{*issue}, nil
			}
			continue
		}
		if handled, issue := compileReleaseTitle(&mutation, operation, index); handled {
			if issue != nil {
				return releasedomain.AIDocumentMutation{}, []core.OperationIssue{*issue}, nil
			}
			continue
		}
		contentOperations = append(contentOperations, operation)
		contentIndexes = append(contentIndexes, index)
	}
	if len(contentOperations) == 0 {
		return mutation, nil, nil
	}
	batch, issues, err := p.codec.Compile(state.DocumentID, state.Document, document.Role(), document.DocumentRevision, contributor, contentOperations)
	for index := range issues {
		if issues[index].Operation >= 0 && issues[index].Operation < len(contentIndexes) {
			issues[index].Operation = contentIndexes[issues[index].Operation]
		}
	}
	if err == nil && len(issues) == 0 {
		mutation.Batch = &batch
	}
	return mutation, issues, err
}

func compileReleaseCreditNote(mutation *releasedomain.AIDocumentMutation, operation core.Operation, index int) (bool, *core.OperationIssue) {
	if operation.Kind == core.OperationSetField && operation.SetField != nil && strings.HasPrefix(string(operation.SetField.Target.Block), "credit-") {
		target := operation.SetField.Target
		if target.Field != releaseCreditNoteField || operation.SetField.Value.Kind != core.ValueKindText {
			return true, &core.OperationIssue{Operation: index, Code: core.IssueValueKindMismatch, Handle: "block:" + string(target.Block) + "/field:" + string(target.Field), Message: "Release credit note must be text"}
		}
		creditID := strings.TrimPrefix(string(target.Block), "credit-")
		parsed, err := uuid.Parse(creditID)
		if err != nil || parsed == uuid.Nil || parsed.String() != creditID {
			return true, &core.OperationIssue{Operation: index, Code: core.IssueInvalidOperation, Handle: "block:" + string(target.Block), Message: "Release credit identity is invalid"}
		}
		if mutation.CreditNotePatch == nil {
			mutation.CreditNotePatch = make(map[string]string)
		}
		mutation.CreditNotePatch[creditID] = operation.SetField.Value.Text
		return true, nil
	}
	var block core.BlockID
	switch operation.Kind {
	case core.OperationDeleteBlock:
		block = operation.DeleteBlock.Block
	case core.OperationMoveBlock:
		block = operation.MoveBlock.Block
	case core.OperationReplaceBlockKind:
		block = operation.ReplaceBlockKind.Block
	case core.OperationInsertBlock:
		if operation.InsertBlock.Kind == releaseCreditKind {
			block = operation.InsertBlock.Block
		}
	}
	if strings.HasPrefix(string(block), "credit-") || operation.Kind == core.OperationInsertBlock && operation.InsertBlock.Kind == releaseCreditKind {
		return true, &core.OperationIssue{Operation: index, Code: core.IssueInvalidOperation, Handle: "block:" + string(block), Message: "Release credit membership is owned by the Release relation API"}
	}
	return false, nil
}

func compileReleaseTitle(mutation *releasedomain.AIDocumentMutation, operation core.Operation, index int) (bool, *core.OperationIssue) {
	var block core.BlockID
	switch operation.Kind {
	case core.OperationSetField:
		if operation.SetField == nil || operation.SetField.Target.Block != releaseMetadataBlockID {
			return false, nil
		}
		if operation.SetField.Target.Field != releaseTitleField || operation.SetField.Value.Kind != core.ValueKindText {
			return true, &core.OperationIssue{Operation: index, Code: core.IssueValueKindMismatch, Handle: "block:document/field:title", Message: "Release title must be text"}
		}
		value := operation.SetField.Value.Text
		mutation.SetTitle, mutation.Title = true, &value
		return true, nil
	case core.OperationDeleteBlock:
		block = operation.DeleteBlock.Block
	case core.OperationMoveBlock:
		block = operation.MoveBlock.Block
	case core.OperationReplaceBlockKind:
		block = operation.ReplaceBlockKind.Block
	case core.OperationInsertBlock:
		if operation.InsertBlock.Kind == releaseMetadataKind {
			block = operation.InsertBlock.Block
		}
	}
	if block == releaseMetadataBlockID || operation.Kind == core.OperationInsertBlock && operation.InsertBlock.Kind == releaseMetadataKind {
		return true, &core.OperationIssue{Operation: index, Code: core.IssueInvalidOperation, Handle: "block:document", Message: "Release metadata identity is fixed by the Release domain"}
	}
	return false, nil
}

func releaseCreditBlockID(creditID string) core.BlockID { return core.BlockID("credit-" + creditID) }

func validateReleaseDocumentIdentity(identity core.DocumentIdentity) error {
	if identity.Domain != core.DomainRelease {
		return fmt.Errorf("release AI document requires domain %q", core.DomainRelease)
	}
	value := string(identity.Reference)
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil || id.String() != value {
		return errors.New("release AI document reference must be a canonical UUID")
	}
	return nil
}

func releaseContributorID(value string) (uuid.UUID, error) {
	value = strings.TrimSpace(value)
	contributor, err := uuid.Parse(value)
	if err != nil || contributor == uuid.Nil || contributor.String() != value {
		return uuid.Nil, errors.New("release contributor Member must be a canonical UUID")
	}
	return contributor, nil
}

func releaseTargetRevision(value *string) *core.Revision {
	if value == nil {
		return nil
	}
	revision := core.Revision(*value)
	return &revision
}

func releaseDomainTargetRevision(value *core.Revision) *string {
	if value == nil {
		return nil
	}
	revision := string(*value)
	return &revision
}

var _ core.DomainPort = (*releasePort)(nil)
var _ core.ExactMutationPort = (*releasePort)(nil)
