package aidocumentadapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
)

const (
	postMetadataBlockID   core.BlockID   = "document"
	postMetadataBlockKind core.BlockKind = "post"
	postTitleField        core.FieldID   = "title"
	postSummaryField      core.FieldID   = "summary"
	postCategoryIDsField  core.FieldID   = "categoryIds"
	postTagIDsField       core.FieldID   = "tagIds"
)

type postPort struct {
	service postDocumentAPI
	codec   *RichTextCodec
	catalog core.Catalog
}

type postDocumentAPI interface {
	LoadAIDocumentState(context.Context, string, string) (postdomain.AIDocumentState, error)
	ExecuteAIDocumentMutation(
		context.Context,
		string,
		string,
		postdomain.AIDocumentExecutionMode,
		postdomain.AIDocumentMutationCompiler,
	) (postdomain.AIDocumentMutationResult, error)
}

func NewPostRegistration(service *postdomain.PostService) (DomainRegistration, error) {
	if service == nil {
		return DomainRegistration{}, errors.New("post AI document service is required")
	}
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST)
	if err != nil {
		return DomainRegistration{}, fmt.Errorf("create Post Rich Text codec: %w", err)
	}
	return DomainRegistration{
		Domain: core.DomainPost,
		Port:   &postPort{service: service, codec: codec, catalog: postCatalog(codec)},
	}, nil
}

func postCatalog(codec *RichTextCodec) core.Catalog {
	catalog := codec.Catalog()
	catalog.BlockKinds = append(catalog.BlockKinds, postMetadataBlockKind)
	item := core.FieldSchema{Kind: core.ValueKindText, Ownership: core.FieldOwnershipSource}
	list := core.FieldSchema{
		Kind: core.ValueKindList, Ownership: core.FieldOwnershipSource,
		Item: &item, Identity: core.ListIdentityRule{Kind: core.ListIdentityValue},
	}
	catalog.Fields = append(catalog.Fields,
		core.FieldRule{BlockKind: postMetadataBlockKind, Field: postTitleField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true},
		core.FieldRule{BlockKind: postMetadataBlockKind, Field: postSummaryField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true},
		core.FieldRule{BlockKind: postMetadataBlockKind, Field: postCategoryIDsField, ValueKind: core.ValueKindList, Ownership: core.FieldOwnershipSource, Schema: &list},
		core.FieldRule{BlockKind: postMetadataBlockKind, Field: postTagIDsField, ValueKind: core.ValueKindList, Ownership: core.FieldOwnershipSource, Schema: &list},
	)
	fingerprint := sha256.Sum256([]byte(catalog.Fingerprint + ":post-metadata:dcdp/1"))
	catalog.Fingerprint = hex.EncodeToString(fingerprint[:])
	return catalog
}

func (p *postPort) Load(ctx context.Context, identity core.DocumentIdentity, locale core.Locale) (core.Document, error) {
	if err := validatePostIdentity(identity); err != nil {
		return core.Document{}, err
	}
	state, err := p.service.LoadAIDocumentState(ctx, string(identity.Reference), string(locale))
	if err != nil {
		return core.Document{}, err
	}
	return p.project(identity, locale, state)
}

func (p *postPort) ValidateMutation(
	ctx context.Context,
	request core.ApplyRequest,
) (core.ValidationResult, error) {
	validation, _, err := p.executeMutation(ctx, request, postdomain.AIDocumentExecutionValidate)
	return validation, err
}

func (p *postPort) ExecuteMutation(
	ctx context.Context,
	request core.ApplyRequest,
) (core.ApplyResult, error) {
	_, result, err := p.executeMutation(ctx, request, postdomain.AIDocumentExecutionApply)
	return result, err
}

func (p *postPort) executeMutation(
	ctx context.Context,
	request core.ApplyRequest,
	mode postdomain.AIDocumentExecutionMode,
) (core.ValidationResult, core.ApplyResult, error) {
	identity := request.Identity()
	if err := validatePostIdentity(identity); err != nil {
		return core.ValidationResult{}, core.ApplyResult{}, err
	}

	run := newExactMutationRun("Post")
	domainResult, err := p.service.ExecuteAIDocumentMutation(
		ctx,
		string(identity.Reference),
		string(request.Locale),
		mode,
		func(state postdomain.AIDocumentState) (postdomain.AIDocumentMutation, error) {
			current, err := p.project(identity, request.Locale, state)
			if err != nil {
				return postdomain.AIDocumentMutation{}, err
			}
			if err := run.validateLoaded(current, request); err != nil {
				return postdomain.AIDocumentMutation{}, err
			}
			contributor, err := uuid.Parse(state.ViewerMemberID)
			if err != nil || contributor == uuid.Nil || contributor.String() != state.ViewerMemberID {
				return postdomain.AIDocumentMutation{}, errors.New("post AI document contributor Member UUID is invalid")
			}
			mutation, issues, err := p.compile(state, current, contributor, run.command.Operations)
			if err != nil {
				return postdomain.AIDocumentMutation{}, err
			}
			if err := run.rejectIssues(issues); err != nil {
				return postdomain.AIDocumentMutation{}, err
			}
			return mutation, nil
		},
	)
	if err != nil {
		if invalid, mapped, handled := handleExactMutationValidationError(
			err, mode == postdomain.AIDocumentExecutionValidate,
		); handled {
			return invalid, core.ApplyResult{}, mapped
		}
		var conflict *postdomain.AIDocumentRevisionConflictError
		if errors.As(err, &conflict) {
			code := core.ConflictDocumentRevision
			if conflict.Kind == postdomain.AIDocumentTargetRevisionConflict {
				code = core.ConflictTargetRevision
			}
			return core.ValidationResult{}, core.ApplyResult{}, &core.ConflictError{Conflict: core.Conflict{
				Code: code, CurrentDocumentRevision: core.Revision(conflict.CurrentDocumentRevision),
				CurrentTargetRevision: postTargetAIDocumentRevision(conflict.CurrentTargetRevision),
				AffectedHandles:       append([]string(nil), run.command.AffectedHandles...),
			}}
		}
		operations := run.command.Operations
		if len(operations) == 0 {
			operations = request.Operations
		}
		if issue := postDomainIssue(err, operations); issue != nil {
			run.validation.Issues = append(run.validation.Issues, *issue)
			if mode == postdomain.AIDocumentExecutionValidate {
				return run.validation, core.ApplyResult{}, nil
			}
			return core.ValidationResult{}, core.ApplyResult{}, &core.ValidationError{Result: run.validation}
		}
		return core.ValidationResult{}, core.ApplyResult{}, err
	}
	if mode == postdomain.AIDocumentExecutionValidate {
		return run.validation, core.ApplyResult{}, nil
	}

	output := core.ApplyResult{
		DocumentRevision: core.Revision(domainResult.Result.DocumentRevision.String()),
		Changed:          domainResult.Result.Changed,
	}
	if domainResult.TargetRevision != nil {
		targetRevision := core.Revision(*domainResult.TargetRevision)
		output.TargetRevision = &targetRevision
	}
	if domainResult.Result.Changed {
		for index, operation := range run.command.Operations {
			output.Changes = append(output.Changes, core.Change{
				Operation: index, Kind: operation.Kind, AffectedHandles: postOperationHandles(operation),
			})
		}
	}
	accepted, err := run.accept(output)
	return run.validation, accepted, err
}

func (p *postPort) project(
	identity core.DocumentIdentity,
	locale core.Locale,
	state postdomain.AIDocumentState,
) (core.Document, error) {
	nodes, err := p.codec.Project(state.LocalizedDocument)
	if err != nil {
		return core.Document{}, fmt.Errorf("project Post AI document: %w", err)
	}
	metadata := core.Node{
		ID: postMetadataBlockID, Kind: postMetadataBlockKind,
		Shared: []core.FieldValue{
			{ID: postCategoryIDsField, Value: postIDList(state.CategoryIDs)},
			{ID: postTagIDsField, Value: postIDList(state.TagIDs)},
		},
	}
	if state.RequestedMetadata != nil {
		if state.RequestedMetadata.Title != nil {
			metadata.Localized = append(metadata.Localized, core.FieldValue{ID: postTitleField, Value: core.Text(*state.RequestedMetadata.Title)})
		}
		if state.RequestedMetadata.Summary != nil {
			metadata.Localized = append(metadata.Localized, core.FieldValue{ID: postSummaryField, Value: core.Text(*state.RequestedMetadata.Summary)})
		}
	}
	nodes = append([]core.Node{metadata}, nodes...)
	return core.Document{
		Identity: identity, DocumentRevision: core.Revision(state.DocumentRevision),
		SourceLocale: core.Locale(state.SourceLocale), Locale: locale,
		LocaleExists: state.LocaleExists, Catalog: p.catalog, Nodes: nodes,
		TargetRevision: postTargetAIDocumentRevision(state.TargetRevision),
	}, nil
}

func postTargetAIDocumentRevision(revision *string) *core.Revision {
	if revision == nil {
		return nil
	}
	converted := core.Revision(*revision)
	return &converted
}

func postIDList(ids []string) core.Value {
	items := make([]core.ListItem, 0, len(ids))
	for _, id := range ids {
		items = append(items, core.StableItem(core.RelationItemID(id), core.Text(id)))
	}
	return core.List(items...)
}

func (p *postPort) compile(
	state postdomain.AIDocumentState,
	loaded core.Document,
	contributor uuid.UUID,
	operations []core.Operation,
) (postdomain.AIDocumentMutation, []core.OperationIssue, error) {
	expected, err := uuid.Parse(string(loaded.DocumentRevision))
	if err != nil || expected == uuid.Nil || expected.String() != string(loaded.DocumentRevision) {
		return postdomain.AIDocumentMutation{}, nil, errors.New("post AI document revision is not a canonical UUID")
	}
	documentID, err := uuid.Parse(state.ContentDocumentID)
	if err != nil || documentID == uuid.Nil || documentID.String() != state.ContentDocumentID {
		return postdomain.AIDocumentMutation{}, nil, errors.New("post AI content document UUID is invalid")
	}
	mutation := postdomain.AIDocumentMutation{
		PostID: string(loaded.Identity.Reference), Locale: string(loaded.Locale),
		ObservedSourceLocale: string(loaded.SourceLocale), ObservedLocaleExists: loaded.LocaleExists,
		ExpectedRevision: expected, ContributorMemberID: contributor,
		ExpectedTargetRevision: postDomainTargetRevision(loaded.TargetRevision),
		Batch: contentblock.Batch{
			DocumentID: documentID, ExpectedRevision: expected,
			ContributorMemberIDs: []uuid.UUID{contributor},
		},
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
		if issue := validatePostMetadataStructure(operation, index); issue != nil {
			return postdomain.AIDocumentMutation{}, []core.OperationIssue{*issue}, nil
		}
		handled, issue := compilePostMetadataOperation(&mutation.Metadata, loaded.Role(), operation, index)
		if issue != nil {
			return postdomain.AIDocumentMutation{}, []core.OperationIssue{*issue}, nil
		}
		if !handled {
			contentOperations = append(contentOperations, operation)
		}
	}
	batch, issues, err := p.codec.Compile(
		documentID, state.LocalizedDocument, loaded.Role(), loaded.DocumentRevision, contributor, contentOperations,
	)
	if err != nil || len(issues) != 0 {
		return postdomain.AIDocumentMutation{}, issues, err
	}
	mutation.Batch = batch
	if loaded.Role() == core.LocaleRoleNonSource && !loaded.LocaleExists &&
		(mutation.Metadata.SetTitle || mutation.Metadata.SetSummary || len(batch.LocaleGroups) != 0) {
		mutation.Metadata.EnsureLocale = true
	}
	return mutation, nil, nil
}

func postDomainTargetRevision(revision *core.Revision) *string {
	if revision == nil {
		return nil
	}
	converted := string(*revision)
	return &converted
}

func compilePostMetadataOperation(
	patch *postdomain.AIDocumentMetadataPatch,
	role core.LocaleRole,
	operation core.Operation,
	index int,
) (bool, *core.OperationIssue) {
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
	if target.Block != postMetadataBlockID {
		return false, nil
	}
	fail := func(code core.IssueCode, message string) (bool, *core.OperationIssue) {
		return true, &core.OperationIssue{
			Operation: index, Code: code,
			Handle: strings.Join(postOperationHandles(operation), ":"), Message: message,
		}
	}
	if target.Relation != "" || target.Item != "" || len(target.Path) != 0 {
		return fail(core.IssueInvalidOperation, "Post metadata does not expose nested or relation targets")
	}
	switch target.Field {
	case postTitleField, postSummaryField:
		if operation.Kind == core.OperationUnsetField {
			if target.Field == postTitleField && role == core.LocaleRoleSource {
				return fail(core.IssueInvalidOperation, "Post source title cannot be removed")
			}
			if target.Field == postTitleField {
				patch.SetTitle, patch.Title = true, nil
			} else {
				patch.SetSummary, patch.Summary = true, nil
			}
			return true, nil
		}
		if value == nil || value.Kind != core.ValueKindText {
			return fail(core.IssueValueKindMismatch, "Post locale metadata must be text")
		}
		text := value.Text
		if target.Field == postTitleField {
			if role == core.LocaleRoleSource && strings.TrimSpace(text) == "" {
				return fail(core.IssueInvalidOperation, "Post source title cannot be empty")
			}
			patch.SetTitle, patch.Title = true, &text
		} else {
			patch.SetSummary, patch.Summary = true, &text
		}
		return true, nil
	case postCategoryIDsField, postTagIDsField:
		if operation.Kind == core.OperationUnsetField {
			value = new(core.Value)
			*value = core.List()
		}
		if value == nil || value.Kind != core.ValueKindList {
			return fail(core.IssueValueKindMismatch, "Post taxonomy metadata must be a UUID list")
		}
		ids := make([]string, 0, len(value.List))
		for _, item := range value.List {
			id, err := uuid.Parse(item.Value.Text)
			if err != nil || id == uuid.Nil || id.String() != item.Value.Text {
				return fail(core.IssueValueKindMismatch, "Post taxonomy IDs must be canonical UUIDs")
			}
			ids = append(ids, id.String())
		}
		if target.Field == postCategoryIDsField {
			patch.SetCategories, patch.CategoryIDs = true, ids
		} else {
			patch.SetTags, patch.TagIDs = true, ids
		}
		return true, nil
	default:
		return fail(core.IssueUnknownField, "unsupported Post metadata field")
	}
}

func validatePostMetadataStructure(operation core.Operation, index int) *core.OperationIssue {
	invalid := false
	switch operation.Kind {
	case core.OperationInsertBlock:
		invalid = operation.InsertBlock.Block == postMetadataBlockID || operation.InsertBlock.Kind == postMetadataBlockKind
	case core.OperationDeleteBlock:
		invalid = operation.DeleteBlock.Block == postMetadataBlockID
	case core.OperationMoveBlock:
		invalid = operation.MoveBlock.Block == postMetadataBlockID
	case core.OperationReplaceBlockKind:
		invalid = operation.ReplaceBlockKind.Block == postMetadataBlockID || operation.ReplaceBlockKind.Kind == postMetadataBlockKind
	}
	if !invalid {
		return nil
	}
	return &core.OperationIssue{
		Operation: index, Code: core.IssueInvalidOperation,
		Handle: string(postMetadataBlockID), Message: "Post metadata structure is fixed",
	}
}

func validatePostIdentity(identity core.DocumentIdentity) error {
	if identity.Domain != core.DomainPost {
		return fmt.Errorf("post AI document received domain %q", identity.Domain)
	}
	reference := string(identity.Reference)
	id, err := uuid.Parse(reference)
	if err != nil || id == uuid.Nil || id.String() != reference {
		return errors.New("post AI document reference must be a canonical UUID")
	}
	return nil
}

func postDomainIssue(err error, operations []core.Operation) *core.OperationIssue {
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

func postOperationHandles(operation core.Operation) []string {
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

var _ core.DomainPort = (*postPort)(nil)
var _ core.ExactMutationPort = (*postPort)(nil)
