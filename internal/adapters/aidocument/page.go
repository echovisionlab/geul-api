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
	errs "github.com/echovisionlab/geul-api/internal/errors"
	pagedomain "github.com/echovisionlab/geul-api/internal/page"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

const (
	pageMetadataBlockID     core.BlockID   = "document"
	pageMetadataBlockKind   core.BlockKind = "page"
	pageTitleField          core.FieldID   = "title"
	pageSummaryField        core.FieldID   = "summary"
	pageDocumentLayoutField core.FieldID   = "documentLayout"
)

type pagePort struct {
	application pageDocumentAPI
	codec       *PageCodec
	catalog     core.Catalog
}

type pageDocumentAPI interface {
	LoadAIDocument(context.Context, pagedomain.LoadAIDocumentQuery) (pagedomain.AIDocumentState, error)
	ExecuteAIDocumentMutation(context.Context, pagedomain.ExecuteAIDocumentMutationCommand) (pagedomain.AIDocumentMutationResult, error)
}

func NewPageRegistration(application *pagedomain.Service) (DomainRegistration, error) {
	if application == nil {
		return DomainRegistration{}, errors.New("page AI document port is required")
	}
	codec, err := NewPageCodec()
	if err != nil {
		return DomainRegistration{}, fmt.Errorf("create Page section codec: %w", err)
	}
	return DomainRegistration{Domain: core.DomainPage, Port: &pagePort{application: application, codec: codec, catalog: pageCatalog(codec)}}, nil
}

func pageCatalog(codec *PageCodec) core.Catalog {
	catalog := codec.Catalog()
	catalog.BlockKinds = append(catalog.BlockKinds, pageMetadataBlockKind)
	text := core.FieldSchema{Kind: core.ValueKindText, Ownership: core.FieldOwnershipShared}
	boolean := core.FieldSchema{Kind: core.ValueKindBoolean, Ownership: core.FieldOwnershipShared}
	layout := core.FieldSchema{Kind: core.ValueKindObject, Ownership: core.FieldOwnershipShared, Fields: []core.NestedFieldRule{
		{Field: "contentHeight", Schema: text}, {Field: "pageChrome", Schema: text}, {Field: "footer", Schema: text},
		{Field: "hideTableOfContents", Schema: boolean},
	}}
	catalog.Fields = append(catalog.Fields,
		core.FieldRule{BlockKind: pageMetadataBlockKind, Field: pageTitleField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true},
		core.FieldRule{BlockKind: pageMetadataBlockKind, Field: pageSummaryField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true},
		core.FieldRule{BlockKind: pageMetadataBlockKind, Field: pageDocumentLayoutField, ValueKind: core.ValueKindObject, Ownership: core.FieldOwnershipShared, Schema: &layout},
	)
	fingerprint := sha256.Sum256([]byte(catalog.Fingerprint + ":page-title-summary-document-layout:dcdp/1"))
	catalog.Fingerprint = hex.EncodeToString(fingerprint[:])
	return catalog
}

func (p *pagePort) Load(ctx context.Context, identity core.DocumentIdentity, locale core.Locale) (core.Document, error) {
	if err := validatePageIdentity(identity); err != nil {
		return core.Document{}, err
	}
	state, err := p.application.LoadAIDocument(ctx, pagedomain.LoadAIDocumentQuery{PageID: string(identity.Reference), Locale: string(locale)})
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

	return executeExactMutation(request, exactMutationSpec[pagedomain.AIDocumentState, pagedomain.AIDocumentMutation, pagedomain.AIDocumentMutationResult]{
		Domain:       "Page",
		ValidateOnly: mode == pagedomain.AIDocumentExecutionValidate,
		Execute: func(compiler func(pagedomain.AIDocumentState) (pagedomain.AIDocumentMutation, error)) (pagedomain.AIDocumentMutationResult, error) {
			return p.application.ExecuteAIDocumentMutation(ctx, pagedomain.ExecuteAIDocumentMutationCommand{
				PageID:  string(identity.Reference),
				Locale:  string(request.Locale),
				Mode:    mode,
				Compile: compiler,
			})
		},
		Project: func(state pagedomain.AIDocumentState) (core.Document, error) {
			return p.project(identity, request.Locale, state)
		},
		Compile: func(state pagedomain.AIDocumentState, current core.Document, command core.ValidatedApply) (pagedomain.AIDocumentMutation, []core.OperationIssue, error) {
			if issues := p.codec.validateRichOperations(current.Nodes, command.Operations); len(issues) != 0 {
				return pagedomain.AIDocumentMutation{}, issues, nil
			}
			contributor, err := canonicalPageContributor(state.ViewerMemberID)
			if err != nil {
				return pagedomain.AIDocumentMutation{}, nil, err
			}
			mutation, issues, err := p.compile(ctx, state, contributor, current, command.Operations)
			if err != nil {
				return pagedomain.AIDocumentMutation{}, nil, err
			}
			return mutation, issues, nil
		},
		Result: func(domainResult pagedomain.AIDocumentMutationResult, command core.ValidatedApply) (core.ApplyResult, error) {
			output := core.ApplyResult{
				DocumentRevision: core.Revision(domainResult.DocumentRevision),
				TargetRevision:   core.OptionalRevision(domainResult.TargetRevision),
				Changed:          domainResult.Changed,
			}
			output = withOperationChanges(command, output)

			return output, nil
		},
		DomainIssue: pageDomainIssue,
	})
}

func (p *pagePort) project(identity core.DocumentIdentity, locale core.Locale, state pagedomain.AIDocumentState) (core.Document, error) {
	nodes, err := p.codec.Project(state.Document)
	if err != nil {
		return core.Document{}, fmt.Errorf("project Page AI document: %w", err)
	}
	metadata := core.Node{ID: pageMetadataBlockID, Kind: pageMetadataBlockKind}
	layout, err := pagedomain.DocumentLayoutMessage(state.Page.DocumentLayout)
	if err != nil {
		return core.Document{}, fmt.Errorf("project Page document layout: %w", err)
	}
	metadata.Shared = []core.FieldValue{{ID: pageDocumentLayoutField, Value: core.Object(
		core.ObjectValue("contentHeight", core.Text(layout.ContentHeight.String())),
		core.ObjectValue("pageChrome", core.Text(layout.PageChrome.String())),
		core.ObjectValue("footer", core.Text(layout.Footer.String())),
		core.ObjectValue("hideTableOfContents", core.Boolean(layout.HideTableOfContents)),
	)}}
	if state.Title != nil {
		metadata.Localized = append(metadata.Localized, core.FieldValue{ID: pageTitleField, Value: core.Text(*state.Title)})
	}
	if state.Summary != nil {
		metadata.Localized = append(metadata.Localized, core.FieldValue{ID: pageSummaryField, Value: core.Text(*state.Summary)})
	}
	return core.Document{
		Identity: identity, DocumentRevision: core.Revision(state.Revision),
		TargetRevision: core.OptionalRevision(state.TargetRevision),
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

func (p *pagePort) compile(ctx context.Context, state pagedomain.AIDocumentState, contributor uuid.UUID, loaded core.Document, operations []core.Operation) (pagedomain.AIDocumentMutation, []core.OperationIssue, error) {
	expected, err := uuid.Parse(string(loaded.DocumentRevision))
	if err != nil || expected == uuid.Nil || expected.String() != string(loaded.DocumentRevision) {
		return pagedomain.AIDocumentMutation{}, nil, errors.New("page AI document revision is not a canonical UUID")
	}
	mutation := pagedomain.AIDocumentMutation{
		PageID: string(loaded.Identity.Reference), Locale: string(loaded.Locale), ExpectedRevision: expected,
		ExpectedTargetRevision: core.OptionalRevisionString(loaded.TargetRevision),
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
	contentIndexes := make([]int, 0, len(operations))
	for index, operation := range operations {
		if handled, issue := compilePageDocumentLayoutOperation(&mutation.Metadata, state.Page.DocumentLayout, loaded.Role(), operation, index); handled {
			if issue != nil {
				return pagedomain.AIDocumentMutation{}, []core.OperationIssue{*issue}, nil
			}
			continue
		}
		if handled, issue := compilePageMetadataOperation(&mutation.Metadata, operation, index); handled {
			if issue != nil {
				return pagedomain.AIDocumentMutation{}, []core.OperationIssue{*issue}, nil
			}
			continue
		}
		contentOperations = append(contentOperations, operation)
		contentIndexes = append(contentIndexes, index)
	}
	batch, issues, err := p.codec.Compile(ctx, state.Snapshot.Document.ID, state.Document, loaded.Role(), loaded.DocumentRevision, contributor, contentOperations)
	for index := range issues {
		if issues[index].Operation >= 0 && issues[index].Operation < len(contentIndexes) {
			issues[index].Operation = contentIndexes[issues[index].Operation]
		}
	}
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

func compilePageDocumentLayoutOperation(patch *pagedomain.AIDocumentMetadataPatch, current pagedomain.DocumentLayout, role core.LocaleRole, operation core.Operation, index int) (bool, *core.OperationIssue) {
	var target core.FieldTarget
	var value core.Value
	switch operation.Kind {
	case core.OperationSetField:
		target, value = operation.SetField.Target, operation.SetField.Value
	case core.OperationUnsetField:
		target = operation.UnsetField.Target
	default:
		return false, nil
	}
	if target.Block != pageMetadataBlockID || target.Field != pageDocumentLayoutField {
		return false, nil
	}
	issue := func(code core.IssueCode, message string) (bool, *core.OperationIssue) {
		return true, &core.OperationIssue{Operation: index, Code: code, Handle: "document:documentLayout", Message: message}
	}
	if role != core.LocaleRoleSource {
		return issue(core.IssueSourceAuthorityRequired, "only the source locale may change Page document layout")
	}
	if operation.Kind == core.OperationUnsetField {
		return issue(core.IssueInvalidOperation, "Page document layout and its fields are required")
	}
	next := &commonv1.DocumentLayout{}
	var fields []core.ObjectField
	if len(target.Path) == 0 {
		if value.Kind != core.ValueKindObject {
			return issue(core.IssueValueKindMismatch, "Page document layout must be an object")
		}
		fields = value.Object
	} else {
		if len(target.Path) != 1 || target.Path[0].Field == "" {
			return issue(core.IssueUnknownField, "Page document layout expects one field path")
		}
		if patch.SetDocumentLayout {
			current = patch.DocumentLayout
		}
		currentLayout, err := pagedomain.DocumentLayoutMessage(current)
		if err != nil {
			return issue(core.IssueInvalidOperation, "current Page document layout is invalid; set the whole layout")
		}
		next = currentLayout
		fields = []core.ObjectField{core.ObjectValue(target.Path[0].Field, value)}
	}
	hideTableOfContentsSet := false
	for _, field := range fields {
		if field.ID == "hideTableOfContents" {
			if field.Value.Kind != core.ValueKindBoolean {
				return issue(core.IssueValueKindMismatch, "Page document layout hideTableOfContents uses a boolean")
			}
			next.HideTableOfContents, hideTableOfContentsSet = field.Value.Boolean, true
			continue
		}
		if field.Value.Kind != core.ValueKindText {
			return issue(core.IssueValueKindMismatch, "Page document layout fields use native enum text")
		}
		switch field.ID {
		case "contentHeight":
			next.ContentHeight = commonv1.DocumentContentHeight(commonv1.DocumentContentHeight_value[field.Value.Text])
		case "pageChrome":
			next.PageChrome = commonv1.DocumentRegionPlacement(commonv1.DocumentRegionPlacement_value[field.Value.Text])
		case "footer":
			next.Footer = commonv1.DocumentRegionPlacement(commonv1.DocumentRegionPlacement_value[field.Value.Text])
		default:
			return issue(core.IssueUnknownField, "unsupported Page document layout field")
		}
	}
	// A replacement that omitted the flag would silently show a hidden table of contents again.
	if len(target.Path) == 0 && !hideTableOfContentsSet {
		return issue(core.IssueInvalidOperation, "Page document layout replacement requires hideTableOfContents")
	}
	layout, err := pagedomain.DocumentLayoutFromMessage(next)
	if err != nil {
		return issue(core.IssueInvalidOperation, err.Error())
	}
	patch.SetDocumentLayout, patch.DocumentLayout = true, layout
	return true, nil
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
		return true, &core.OperationIssue{Operation: index, Code: core.IssueInvalidOperation, Handle: strings.Join(operationIssuePath(operation), ":"), Message: "Page locale values use explicit empty and cannot be unset"}
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
	message := err.Error()
	if classified, ok := errs.Classified(err); ok {
		message = classified.Error()
	}
	return &core.OperationIssue{Operation: index, Code: code, Message: message}
}

var _ core.DomainPort = (*pagePort)(nil)
var _ core.ExactMutationPort = (*pagePort)(nil)
