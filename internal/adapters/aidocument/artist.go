package aidocumentadapter

import (
	"context"
	"errors"
	"fmt"
	"strings"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	artistdomain "github.com/echovisionlab/geul-api/internal/artist"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
)

type artistPort struct {
	service artistDocumentAPI
	codec   *RichTextCodec
}

type artistDocumentAPI interface {
	LoadAIDocumentState(context.Context, string, string) (artistdomain.AIDocumentState, error)
	ExecuteAIDocumentMutation(
		context.Context,
		string,
		string,
		artistdomain.AIDocumentExecutionMode,
		artistdomain.AIDocumentMutationCompiler,
	) (artistdomain.AIDocumentMutationResult, error)
}

// NewArtistRegistration binds the already-composed Artist service. The
// adapter receives no DB/SpiceDB callback bag and owns only compact DCDP
// projection and compilation.
func NewArtistRegistration(service *artistdomain.InternalArtistService) (DomainRegistration, error) {
	if service == nil {
		return DomainRegistration{}, errors.New("artist AI document service is required")
	}
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT)
	if err != nil {
		return DomainRegistration{}, fmt.Errorf("create Artist compact Rich Text codec: %w", err)
	}
	return DomainRegistration{Domain: core.DomainArtist, Port: &artistPort{service: service, codec: codec}}, nil
}

func (p *artistPort) Load(ctx context.Context, identity core.DocumentIdentity, locale core.Locale) (core.Document, error) {
	if err := validateArtistDocumentIdentity(identity); err != nil {
		return core.Document{}, err
	}
	state, err := p.service.LoadAIDocumentState(ctx, string(identity.Reference), string(locale))
	if err != nil {
		return core.Document{}, err
	}
	return p.document(identity, locale, state)
}

func (p *artistPort) ValidateMutation(
	ctx context.Context,
	request core.ApplyRequest,
) (core.ValidationResult, error) {
	validation, _, err := p.executeMutation(ctx, request, artistdomain.AIDocumentExecutionValidate)
	return validation, err
}

func (p *artistPort) ExecuteMutation(
	ctx context.Context,
	request core.ApplyRequest,
) (core.ApplyResult, error) {
	_, result, err := p.executeMutation(ctx, request, artistdomain.AIDocumentExecutionApply)
	return result, err
}

func (p *artistPort) executeMutation(
	ctx context.Context,
	request core.ApplyRequest,
	mode artistdomain.AIDocumentExecutionMode,
) (core.ValidationResult, core.ApplyResult, error) {
	identity := request.Identity()
	if err := validateArtistDocumentIdentity(identity); err != nil {
		return core.ValidationResult{}, core.ApplyResult{}, err
	}

	run := newExactMutationRun("Artist")
	domainResult, err := p.service.ExecuteAIDocumentMutation(
		ctx,
		string(identity.Reference),
		string(request.Locale),
		mode,
		func(state artistdomain.AIDocumentState) (artistdomain.AIDocumentMutation, error) {
			current, err := p.document(identity, request.Locale, state)
			if err != nil {
				return artistdomain.AIDocumentMutation{}, err
			}
			if err := run.validateLoaded(current, request); err != nil {
				return artistdomain.AIDocumentMutation{}, err
			}
			contributor, err := artistContributorID(state.ViewerMemberID)
			if err != nil {
				return artistdomain.AIDocumentMutation{}, err
			}
			mutation, issues, err := p.compile(state, current, contributor, run.command.Operations)
			if err != nil {
				return artistdomain.AIDocumentMutation{}, err
			}
			if err := run.rejectIssues(issues); err != nil {
				return artistdomain.AIDocumentMutation{}, err
			}
			return mutation, nil
		},
	)
	if err != nil {
		if invalid, mapped, handled := handleExactMutationValidationError(
			err, mode == artistdomain.AIDocumentExecutionValidate,
		); handled {
			return invalid, core.ApplyResult{}, mapped
		}
		var conflict *artistdomain.AIDocumentRevisionConflictError
		if errors.As(err, &conflict) {
			code := core.ConflictDocumentRevision
			if conflict.Kind == artistdomain.AIDocumentTargetRevisionConflict {
				code = core.ConflictTargetRevision
			}
			return core.ValidationResult{}, core.ApplyResult{}, &core.ConflictError{Conflict: core.Conflict{
				Code: code, CurrentDocumentRevision: core.Revision(conflict.CurrentRevision),
				CurrentTargetRevision: artistTargetAIDocumentRevision(conflict.CurrentTargetRevision),
				AffectedHandles:       append([]string(nil), run.command.AffectedHandles...),
			}}
		}
		return core.ValidationResult{}, core.ApplyResult{}, err
	}
	if mode == artistdomain.AIDocumentExecutionValidate {
		return run.validation, core.ApplyResult{}, nil
	}

	output := core.ApplyResult{
		DocumentRevision: core.Revision(domainResult.Revision), Changed: domainResult.Changed,
		TargetRevision: artistTargetAIDocumentRevision(domainResult.TargetRevision),
	}
	if domainResult.Changed {
		for index, operation := range run.command.Operations {
			output.Changes = append(output.Changes, core.Change{
				Operation: index, Kind: operation.Kind,
				AffectedHandles: artistOperationHandles(operation, run.command.Locale),
			})
		}
	}
	accepted, err := run.accept(output)
	return run.validation, accepted, err
}

func (p *artistPort) document(identity core.DocumentIdentity, locale core.Locale, state artistdomain.AIDocumentState) (core.Document, error) {
	if state.ArtistID != string(identity.Reference) || state.Locale != string(locale) || state.DocumentID == uuid.Nil || state.Document == nil {
		return core.Document{}, errors.New("artist AI document facade returned inconsistent state")
	}
	nodes, err := p.codec.Project(state.Document)
	if err != nil {
		return core.Document{}, fmt.Errorf("project Artist compact document: %w", err)
	}
	return core.Document{
		Identity: identity, DocumentRevision: core.Revision(state.Revision),
		TargetRevision: artistTargetAIDocumentRevision(state.TargetRevision),
		SourceLocale:   core.Locale(state.SourceLocale), Locale: locale,
		LocaleExists: state.LocaleExists, Catalog: p.codec.Catalog(), Nodes: nodes,
	}, nil
}

func (p *artistPort) compile(state artistdomain.AIDocumentState, document core.Document, contributor uuid.UUID, operations []core.Operation) (artistdomain.AIDocumentMutation, []core.OperationIssue, error) {
	mutation := artistdomain.AIDocumentMutation{
		ArtistID: state.ArtistID, Locale: string(document.Locale),
		ExpectedRevision: string(document.DocumentRevision), ExpectedSource: state.SourceLocale,
		ExpectedTargetRevision: artistDomainTargetRevision(document.TargetRevision),
		ExpectedPresence:       state.LocaleExists, ContributorMemberID: contributor,
	}
	if len(operations) == 1 && operations[0].Kind == core.OperationCreateTranslation {
		mutation.CreateTranslation = true
		return mutation, nil, nil
	}
	if len(operations) == 1 && operations[0].Kind == core.OperationDeleteTranslation {
		mutation.DeleteTranslation = true
		return mutation, nil, nil
	}
	for index, operation := range operations {
		if operation.Kind == core.OperationUnsetField && operation.UnsetField != nil && (document.Role() == core.LocaleRoleNonSource || operation.UnsetField.Target.Field == richTextContentField) {
			return artistdomain.AIDocumentMutation{}, []core.OperationIssue{{Operation: index, Code: core.IssueInvalidOperation, Handle: strings.Join(artistOperationHandles(operation, document.Locale), "/"), Message: "Artist locale values use explicit empty instead of unset"}}, nil
		}
	}
	batch, issues, err := p.codec.Compile(state.DocumentID, state.Document, document.Role(), document.DocumentRevision, contributor, operations)
	if err == nil && len(issues) == 0 {
		mutation.Batch = &batch
	}
	return mutation, issues, err
}

func validateArtistDocumentIdentity(identity core.DocumentIdentity) error {
	if identity.Domain != core.DomainArtist {
		return fmt.Errorf("artist AI document requires domain %q", core.DomainArtist)
	}
	value := string(identity.Reference)
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil || id.String() != value {
		return errors.New("artist AI document reference must be a canonical UUID")
	}
	return nil
}

func artistContributorID(value string) (uuid.UUID, error) {
	value = strings.TrimSpace(value)
	contributor, err := uuid.Parse(value)
	if err != nil || contributor == uuid.Nil || contributor.String() != value {
		return uuid.Nil, errors.New("artist contributor Member must be a canonical UUID")
	}
	return contributor, nil
}

func artistTargetAIDocumentRevision(revision *string) *core.Revision {
	if revision == nil {
		return nil
	}
	converted := core.Revision(*revision)
	return &converted
}

func artistDomainTargetRevision(revision *core.Revision) *string {
	if revision == nil {
		return nil
	}
	converted := string(*revision)
	return &converted
}
func artistOperationHandles(operation core.Operation, locale core.Locale) []string {
	return compactOperationHandles(operation, locale, "artist")
}

var _ core.DomainPort = (*artistPort)(nil)
var _ core.ExactMutationPort = (*artistPort)(nil)
