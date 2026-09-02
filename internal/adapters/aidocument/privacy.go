package aidocumentadapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/legal"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

const (
	legalMetadataBlockID   core.BlockID   = "document"
	legalMetadataBlockKind core.BlockKind = "document"
	legalTitleField        core.FieldID   = "title"
)

type legalAIDocumentPort struct {
	domain      core.Domain
	entityType  string
	application legalAIDocumentAPI
	codec       *RichTextCodec
	catalog     core.Catalog
}

type legalAIDocumentAPI interface {
	LoadAIDocument(context.Context, string, string, string) (legal.AIDocument, error)
	ExecuteAIDocumentMutation(
		context.Context,
		string,
		string,
		string,
		legal.AIDocumentExecutionMode,
		legal.AIDocumentMutationCompiler,
	) (legal.AIDocumentMutationResult, error)
}

func NewPrivacyRegistration(application *legal.AIDocumentService) (DomainRegistration, error) {
	return newLegalRegistration(core.DomainPrivacy, "privacy", application)
}

func newLegalRegistration(
	domain core.Domain,
	entityType string,
	application *legal.AIDocumentService,
) (DomainRegistration, error) {
	if application == nil {
		return DomainRegistration{}, errors.New("legal AI document application is required")
	}
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_POLICY)
	if err != nil {
		return DomainRegistration{}, fmt.Errorf("create Legal Rich Text codec: %w", err)
	}
	catalog := codec.Catalog()
	catalog.BlockKinds = append(catalog.BlockKinds, legalMetadataBlockKind)
	catalog.Fields = append(catalog.Fields, core.FieldRule{
		BlockKind: legalMetadataBlockKind, Field: legalTitleField,
		ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true,
	})
	fingerprint := sha256.Sum256([]byte(catalog.Fingerprint + ":legal-document-title:dcdp/1"))
	catalog.Fingerprint = hex.EncodeToString(fingerprint[:])
	return DomainRegistration{Domain: domain, Port: &legalAIDocumentPort{
		domain: domain, entityType: entityType, application: application, codec: codec, catalog: catalog,
	}}, nil
}

func (p *legalAIDocumentPort) Load(
	ctx context.Context,
	identity core.DocumentIdentity,
	locale core.Locale,
) (core.Document, error) {
	if identity.Domain != p.domain {
		return core.Document{}, fmt.Errorf("legal AI document domain %q does not own %q", p.domain, identity.Domain)
	}
	loaded, err := p.application.LoadAIDocument(ctx, p.entityType, string(identity.Reference), string(locale))
	if err != nil {
		return core.Document{}, err
	}
	return p.project(identity, locale, loaded)
}

func (p *legalAIDocumentPort) ValidateMutation(
	ctx context.Context,
	request core.ApplyRequest,
) (core.ValidationResult, error) {
	validation, _, err := p.executeMutation(ctx, request, legal.AIDocumentExecutionValidate)
	return validation, err
}

func (p *legalAIDocumentPort) ExecuteMutation(
	ctx context.Context,
	request core.ApplyRequest,
) (core.ApplyResult, error) {
	_, result, err := p.executeMutation(ctx, request, legal.AIDocumentExecutionApply)
	return result, err
}

func (p *legalAIDocumentPort) executeMutation(
	ctx context.Context,
	request core.ApplyRequest,
	mode legal.AIDocumentExecutionMode,
) (core.ValidationResult, core.ApplyResult, error) {
	identity := request.Identity()
	if identity.Domain != p.domain {
		return core.ValidationResult{}, core.ApplyResult{}, fmt.Errorf(
			"legal AI document domain %q does not own %q",
			p.domain,
			identity.Domain,
		)
	}

	run := newExactMutationRun("Legal")
	domainResult, err := p.application.ExecuteAIDocumentMutation(
		ctx,
		p.entityType,
		string(identity.Reference),
		string(request.Locale),
		mode,
		func(raw legal.AIDocument) (legal.AIDocumentMutation, error) {
			current, err := p.project(identity, request.Locale, raw)
			if err != nil {
				return legal.AIDocumentMutation{}, err
			}
			if err := run.validateLoaded(current, request); err != nil {
				return legal.AIDocumentMutation{}, err
			}
			contributor, err := uuid.Parse(raw.ViewerMemberID)
			if err != nil || contributor == uuid.Nil || contributor.String() != raw.ViewerMemberID {
				return legal.AIDocumentMutation{}, errors.New("legal AI document contributor Member UUID is invalid")
			}
			compiled, issues, err := p.compile(raw, current, contributor, run.command.Operations)
			if err != nil {
				return legal.AIDocumentMutation{}, err
			}
			if err := run.rejectIssues(issues); err != nil {
				return legal.AIDocumentMutation{}, err
			}
			return legal.AIDocumentMutation{
				EntityType: p.entityType, EntityID: string(identity.Reference), Locale: string(request.Locale),
				ExpectedRevision: raw.Revision, ContributorMemberID: contributor.String(),
				ExpectedTargetRevision: legalStringRevision(run.command.ExpectedTargetRevision),
				Content:                compiled.content, SetTitle: compiled.setTitle, Title: compiled.title,
				Translation: compiled.translation,
			}, nil
		},
	)
	if err != nil {
		if invalid, mapped, handled := handleExactMutationValidationError(
			err, mode == legal.AIDocumentExecutionValidate,
		); handled {
			return invalid, core.ApplyResult{}, mapped
		}
		var conflict *legal.AIDocumentRevisionConflict
		if errors.As(err, &conflict) {
			code := core.ConflictDocumentRevision
			if conflict.Kind == legal.AIDocumentTargetRevisionConflict {
				code = core.ConflictTargetRevision
			}
			return core.ValidationResult{}, core.ApplyResult{}, &core.ConflictError{Conflict: core.Conflict{
				Code: code, CurrentDocumentRevision: core.Revision(conflict.CurrentRevision),
				CurrentTargetRevision: legalCoreRevision(conflict.CurrentTargetRevision),
				AffectedHandles:       legalRequestAffectedHandles(request),
			}}
		}
		return core.ValidationResult{}, core.ApplyResult{}, err
	}
	if mode == legal.AIDocumentExecutionValidate {
		return run.validation, core.ApplyResult{}, nil
	}

	accepted := core.ApplyResult{
		DocumentRevision: core.Revision(domainResult.Revision),
		TargetRevision:   legalCoreRevision(domainResult.TargetRevision),
		Changed:          domainResult.Changed,
	}
	if domainResult.Changed {
		for index, operation := range run.command.Operations {
			accepted.Changes = append(accepted.Changes, core.Change{
				Operation: index, Kind: operation.Kind,
				AffectedHandles: legalOperationHandles(operation),
			})
		}
	}
	result, err := run.accept(accepted)
	return run.validation, result, err
}

func legalRequestAffectedHandles(request core.ApplyRequest) []string {
	var handles []string
	for _, operation := range request.Operations {
		handles = append(handles, legalOperationHandles(operation)...)
	}
	return handles
}

func legalOperationHandles(operation core.Operation) []string {
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

type compiledLegalMutation struct {
	content     *contentblock.Batch
	setTitle    bool
	title       *string
	translation legal.AITranslationMutation
}

func (p *legalAIDocumentPort) compile(
	raw legal.AIDocument,
	document core.Document,
	contributor uuid.UUID,
	operations []core.Operation,
) (compiledLegalMutation, []core.OperationIssue, error) {
	result := compiledLegalMutation{}
	contentOperations := make([]core.Operation, 0, len(operations))
	contentIndexes := make([]int, 0, len(operations))
	for index, operation := range operations {
		if document.Role() == core.LocaleRoleNonSource && operation.Kind == core.OperationUnsetField {
			return compiledLegalMutation{}, []core.OperationIssue{*legalIssue(
				index,
				"Legal target locale values must use an explicit empty value rather than unset",
			)}, nil
		}
		if issue := validateLegalMetadataOperation(index, document, operation, &result); issue != nil {
			return compiledLegalMutation{}, []core.OperationIssue{*issue}, nil
		}
		if legalMetadataOperation(operation) || operation.Kind == core.OperationCreateTranslation || operation.Kind == core.OperationDeleteTranslation {
			continue
		}
		contentOperations = append(contentOperations, operation)
		contentIndexes = append(contentIndexes, index)
	}
	if len(contentOperations) == 0 {
		return result, nil, nil
	}
	if contributor == uuid.Nil {
		return compiledLegalMutation{}, nil, errors.New("legal AI document contributor must be a UUID")
	}
	localized, err := localizedLegalRichText(raw)
	if err != nil {
		return compiledLegalMutation{}, nil, err
	}
	batch, issues, err := p.codec.Compile(
		raw.DocumentID, localized, document.Role(), document.DocumentRevision, contributor, contentOperations,
	)
	if err != nil || len(issues) != 0 {
		for index := range issues {
			if issues[index].Operation >= 0 && issues[index].Operation < len(contentIndexes) {
				issues[index].Operation = contentIndexes[issues[index].Operation]
			}
		}
		return compiledLegalMutation{}, issues, err
	}
	result.content = &batch
	return result, nil, nil
}

func validateLegalMetadataOperation(
	index int,
	document core.Document,
	operation core.Operation,
	compiled *compiledLegalMutation,
) *core.OperationIssue {
	if operation.Kind == core.OperationCreateTranslation {
		compiled.translation = legal.AITranslationCreate
		return nil
	}
	if operation.Kind == core.OperationDeleteTranslation {
		compiled.translation = legal.AITranslationDelete
		return nil
	}
	if operation.Kind == core.OperationInsertBlock && operation.InsertBlock.Kind == legalMetadataBlockKind {
		return legalIssue(index, "the Legal document metadata node cannot be inserted")
	}
	if operation.Kind == core.OperationReplaceBlockKind &&
		(operation.ReplaceBlockKind.Block == legalMetadataBlockID || operation.ReplaceBlockKind.Kind == legalMetadataBlockKind) {
		return legalIssue(index, "the Legal document metadata node kind cannot be replaced")
	}
	if metadataStructuralOperation(operation) {
		return legalIssue(index, "the Legal document metadata node structure is immutable")
	}
	if !legalMetadataOperation(operation) {
		return nil
	}
	compiled.setTitle = true
	switch operation.Kind {
	case core.OperationSetField:
		value := operation.SetField.Value.Text
		if document.Role() == core.LocaleRoleSource && strings.TrimSpace(value) == "" {
			return legalIssue(index, "the source Legal title cannot be empty")
		}
		compiled.title = &value
	case core.OperationUnsetField:
		if document.Role() == core.LocaleRoleSource {
			return legalIssue(index, "the source Legal title cannot be absent")
		}
		return legalIssue(index, "the target Legal title must use an explicit empty value rather than unset")
	}
	return nil
}

func legalMetadataOperation(operation core.Operation) bool {
	switch operation.Kind {
	case core.OperationSetField:
		return operation.SetField.Target.Block == legalMetadataBlockID
	case core.OperationUnsetField:
		return operation.UnsetField.Target.Block == legalMetadataBlockID
	default:
		return false
	}
}

func metadataStructuralOperation(operation core.Operation) bool {
	switch operation.Kind {
	case core.OperationDeleteBlock:
		return operation.DeleteBlock.Block == legalMetadataBlockID
	case core.OperationMoveBlock:
		return operation.MoveBlock.Block == legalMetadataBlockID
	case core.OperationAttachFile:
		return operation.AttachFile.Target.Block == legalMetadataBlockID
	case core.OperationDetachFile:
		return operation.DetachFile.Target.Block == legalMetadataBlockID
	default:
		return false
	}
}

func legalIssue(index int, message string) *core.OperationIssue {
	return &core.OperationIssue{Operation: index, Code: core.IssueInvalidOperation, Handle: string(legalMetadataBlockID), Message: message}
}

func (p *legalAIDocumentPort) project(
	identity core.DocumentIdentity,
	locale core.Locale,
	raw legal.AIDocument,
) (core.Document, error) {
	localized, err := localizedLegalRichText(raw)
	if err != nil {
		return core.Document{}, err
	}
	nodes, err := p.codec.Project(localized)
	if err != nil {
		return core.Document{}, err
	}
	metadata := core.Node{ID: legalMetadataBlockID, Kind: legalMetadataBlockKind}
	if raw.Title != nil {
		metadata.Localized = []core.FieldValue{{ID: legalTitleField, Value: core.Text(*raw.Title)}}
	}
	nodes = append([]core.Node{metadata}, nodes...)
	return core.Document{
		Identity: identity, DocumentRevision: core.Revision(raw.Revision),
		TargetRevision: legalCoreRevision(raw.TargetRevision), SourceLocale: core.Locale(raw.SourceLocale),
		Locale: locale, LocaleExists: raw.LocaleExists, Catalog: p.catalog, Nodes: nodes,
	}, nil
}

func legalCoreRevision(value *string) *core.Revision {
	if value == nil {
		return nil
	}
	revision := core.Revision(*value)
	return &revision
}

func legalStringRevision(value *core.Revision) *string {
	if value == nil {
		return nil
	}
	revision := string(*value)
	return &revision
}

func localizedLegalRichText(raw legal.AIDocument) (*contentv1.LocalizedRichTextDocument, error) {
	document, err := contentv1.MaterializeRichTextDocumentStorage(
		contentv1.RichTextProfile_RICH_TEXT_PROFILE_POLICY, raw.SourceLocale, raw.Rows,
	)
	if err != nil {
		return nil, fmt.Errorf("materialize Legal Rich Text document: %w", err)
	}
	overlay := &contentv1.RichTextLocaleOverlay{Locale: raw.Locale}
	for _, candidate := range document.GetLocaleOverlays() {
		if candidate.GetLocale() == raw.Locale {
			overlay = proto.Clone(candidate).(*contentv1.RichTextLocaleOverlay)
			break
		}
	}
	return &contentv1.LocalizedRichTextDocument{
		BlockCatalogFingerprint: document.GetBlockCatalogFingerprint(), Profile: document.GetProfile(),
		Locale: raw.Locale, Base: proto.Clone(document.GetBase()).(*contentv1.RichTextBlockGraph), LocaleOverlay: overlay,
	}, nil
}

var _ core.DomainPort = (*legalAIDocumentPort)(nil)
var _ core.ExactMutationPort = (*legalAIDocumentPort)(nil)
