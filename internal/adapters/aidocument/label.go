package aidocumentadapter

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	labeldomain "github.com/echovisionlab/geul-api/internal/label"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
)

type labelDocumentAPI interface {
	ValidateAIDocumentDependencies() error
	LoadAIDocumentState(context.Context, string, string) (labeldomain.AIDocumentState, error)
	ExecuteAIDocumentMutation(
		context.Context,
		string,
		string,
		labeldomain.AIDocumentExecutionMode,
		labeldomain.AIDocumentMutationCompiler,
	) (labeldomain.AIDocumentMutationResult, error)
}

type labelPort struct {
	api   labelDocumentAPI
	codec *RichTextCodec
}

// NewLabelRegistration binds Label's owning authorization and persistence
// facade to the shared DCDP registry. The generated compact profile has no
// File slot, so a File UUID can never become Label attachment permission.
func NewLabelRegistration(api labelDocumentAPI) (DomainRegistration, error) {
	if labelAPIIsNil(api) {
		return DomainRegistration{}, errors.New("label AI document API is required")
	}
	if err := api.ValidateAIDocumentDependencies(); err != nil {
		return DomainRegistration{}, err
	}
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT)
	if err != nil {
		return DomainRegistration{}, fmt.Errorf("create Label compact Rich Text codec: %w", err)
	}
	return DomainRegistration{Domain: core.DomainLabel, Port: &labelPort{api: api, codec: codec}}, nil
}

func (p *labelPort) Load(ctx context.Context, identity core.DocumentIdentity, locale core.Locale) (core.Document, error) {
	if err := validateLabelIdentity(identity); err != nil {
		return core.Document{}, err
	}
	state, err := p.api.LoadAIDocumentState(ctx, string(identity.Reference), string(locale))
	if err != nil {
		return core.Document{}, err
	}
	return p.document(identity, locale, state)
}

func (p *labelPort) ValidateMutation(
	ctx context.Context,
	request core.ApplyRequest,
) (core.ValidationResult, error) {
	validation, _, err := p.executeMutation(ctx, request, labeldomain.AIDocumentExecutionValidate)
	return validation, err
}

func (p *labelPort) ExecuteMutation(
	ctx context.Context,
	request core.ApplyRequest,
) (core.ApplyResult, error) {
	_, result, err := p.executeMutation(ctx, request, labeldomain.AIDocumentExecutionApply)
	return result, err
}

func (p *labelPort) executeMutation(
	ctx context.Context,
	request core.ApplyRequest,
	mode labeldomain.AIDocumentExecutionMode,
) (core.ValidationResult, core.ApplyResult, error) {
	identity := request.Identity()
	if err := validateLabelIdentity(identity); err != nil {
		return core.ValidationResult{}, core.ApplyResult{}, err
	}

	run := newExactMutationRun("Label")
	domainResult, err := p.api.ExecuteAIDocumentMutation(
		ctx,
		string(identity.Reference),
		string(request.Locale),
		mode,
		func(state labeldomain.AIDocumentState) (labeldomain.AIDocumentMutation, error) {
			current, err := p.document(identity, request.Locale, state)
			if err != nil {
				return labeldomain.AIDocumentMutation{}, err
			}
			if err := run.validateLoaded(current, request); err != nil {
				return labeldomain.AIDocumentMutation{}, err
			}
			contributor, err := labelContributorID(state.ViewerMemberID)
			if err != nil {
				return labeldomain.AIDocumentMutation{}, err
			}
			mutation, issues, err := p.compileMutation(state, current, contributor, run.command.Operations)
			if err != nil {
				return labeldomain.AIDocumentMutation{}, err
			}
			if err := run.rejectIssues(issues); err != nil {
				return labeldomain.AIDocumentMutation{}, err
			}
			return mutation, nil
		},
	)
	if err != nil {
		if invalid, mapped, handled := handleExactMutationValidationError(
			err, mode == labeldomain.AIDocumentExecutionValidate,
		); handled {
			return invalid, core.ApplyResult{}, mapped
		}
		var conflict *labeldomain.AIDocumentRevisionConflictError
		if errors.As(err, &conflict) {
			code := core.ConflictDocumentRevision
			if conflict.Kind == labeldomain.AIDocumentTargetRevisionConflict {
				code = core.ConflictTargetRevision
			}
			return core.ValidationResult{}, core.ApplyResult{}, &core.ConflictError{Conflict: core.Conflict{
				Code:                    code,
				CurrentDocumentRevision: core.Revision(conflict.CurrentDocumentRevision),
				CurrentTargetRevision:   labelCoreTargetRevision(conflict.CurrentTargetRevision),
				AffectedHandles:         append([]string(nil), run.command.AffectedHandles...),
			}}
		}
		return core.ValidationResult{}, core.ApplyResult{}, err
	}
	if mode == labeldomain.AIDocumentExecutionValidate {
		return run.validation, core.ApplyResult{}, nil
	}

	output := core.ApplyResult{
		DocumentRevision: core.Revision(domainResult.Content.DocumentRevision.String()),
		Changed:          domainResult.Content.Changed,
	}
	output.TargetRevision = labelCoreTargetRevision(domainResult.TargetRevision)
	if domainResult.Content.Changed {
		for index, operation := range run.command.Operations {
			output.Changes = append(output.Changes, core.Change{
				Operation: index, Kind: operation.Kind,
				AffectedHandles: labelOperationHandles(operation, run.command.Locale),
			})
		}
	}
	accepted, err := run.accept(output)
	return run.validation, accepted, err
}

func (p *labelPort) compileMutation(
	state labeldomain.AIDocumentState,
	loaded core.Document,
	contributor uuid.UUID,
	operations []core.Operation,
) (labeldomain.AIDocumentMutation, []core.OperationIssue, error) {
	mutation := labeldomain.AIDocumentMutation{
		LabelID: string(loaded.Identity.Reference), Locale: string(loaded.Locale),
		ExpectedRevision: string(loaded.DocumentRevision), ExpectedSource: string(loaded.SourceLocale),
		ExpectedTargetRevision: labelDomainTargetRevision(loaded.TargetRevision),
		ExpectedPresence:       loaded.LocaleExists, ContributorMemberID: contributor,
	}
	if len(operations) == 1 && operations[0].Kind == core.OperationCreateTranslation {
		mutation.CreateTranslation = true
		return mutation, nil, nil
	}
	if len(operations) == 1 && operations[0].Kind == core.OperationDeleteTranslation {
		mutation.DeleteTranslation = true
		return mutation, nil, nil
	}
	batch, issues, err := p.codec.Compile(
		state.ContentDocumentID,
		state.Document,
		loaded.Role(),
		loaded.DocumentRevision,
		contributor,
		operations,
	)
	if err != nil || len(issues) != 0 {
		return labeldomain.AIDocumentMutation{}, issues, err
	}
	mutation.Batch = &batch
	return mutation, nil, nil
}

func (p *labelPort) document(identity core.DocumentIdentity, locale core.Locale, state labeldomain.AIDocumentState) (core.Document, error) {
	if state.LabelID != string(identity.Reference) || state.Locale != string(locale) {
		return core.Document{}, errors.New("label AI document facade returned a different identity or locale")
	}
	if state.ContentDocumentID == uuid.Nil || state.Document == nil {
		return core.Document{}, errors.New("label AI document facade returned no compact content document")
	}
	nodes, err := p.codec.Project(state.Document)
	if err != nil {
		return core.Document{}, fmt.Errorf("project Label compact document: %w", err)
	}
	return core.Document{
		Identity: identity, DocumentRevision: core.Revision(state.Revision), SourceLocale: core.Locale(state.SourceLocale),
		Locale: locale, LocaleExists: state.LocaleExists, Catalog: p.codec.Catalog(), Nodes: nodes,
		TargetRevision: labelCoreTargetRevision(state.TargetRevision),
	}, nil
}

func labelContributorID(value string) (uuid.UUID, error) {
	value = strings.TrimSpace(value)
	parsed, err := uuid.Parse(value)
	if err != nil || parsed == uuid.Nil || parsed.String() != value {
		return uuid.Nil, errors.New("label contributor Member must be a canonical UUID")
	}
	return parsed, nil
}

func validateLabelIdentity(identity core.DocumentIdentity) error {
	if identity.Domain != core.DomainLabel {
		return fmt.Errorf("label AI document requires domain %q", core.DomainLabel)
	}
	value := string(identity.Reference)
	parsed, err := uuid.Parse(value)
	if err != nil || parsed == uuid.Nil || parsed.String() != value {
		return errors.New("label AI document reference must be a canonical UUID")
	}
	return nil
}

func labelCoreTargetRevision(revision *string) *core.Revision {
	if revision == nil {
		return nil
	}
	converted := core.Revision(*revision)
	return &converted
}

func labelDomainTargetRevision(revision *core.Revision) *string {
	if revision == nil {
		return nil
	}
	converted := string(*revision)
	return &converted
}

func labelOperationHandles(operation core.Operation, locale core.Locale) []string {
	switch operation.Kind {
	case core.OperationSetField:
		return []string{"block:" + string(operation.SetField.Target.Block) + "/field:" + string(operation.SetField.Target.Field)}
	case core.OperationUnsetField:
		return []string{"block:" + string(operation.UnsetField.Target.Block) + "/field:" + string(operation.UnsetField.Target.Field)}
	case core.OperationInsertBlock:
		return []string{"block:" + string(operation.InsertBlock.Block)}
	case core.OperationDeleteBlock:
		return []string{"block:" + string(operation.DeleteBlock.Block)}
	case core.OperationMoveBlock:
		return []string{"block:" + string(operation.MoveBlock.Block)}
	case core.OperationReplaceBlockKind:
		return []string{"block:" + string(operation.ReplaceBlockKind.Block)}
	case core.OperationCreateTranslation, core.OperationDeleteTranslation:
		return []string{"translation:" + string(locale)}
	default:
		return []string{"label"}
	}
}

func labelAPIIsNil(api labelDocumentAPI) bool {
	if api == nil {
		return true
	}
	value := reflect.ValueOf(api)
	return value.Kind() == reflect.Pointer && value.IsNil()
}

var _ core.DomainPort = (*labelPort)(nil)
var _ core.ExactMutationPort = (*labelPort)(nil)
