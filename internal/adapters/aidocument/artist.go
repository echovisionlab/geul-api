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
func NewArtistRegistration(service *artistdomain.Service) (DomainRegistration, error) {
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

	return executeExactMutation(request, exactMutationSpec[artistdomain.AIDocumentState, artistdomain.AIDocumentMutation, artistdomain.AIDocumentMutationResult]{
		Domain:       "Artist",
		ValidateOnly: mode == artistdomain.AIDocumentExecutionValidate,
		Execute: func(compiler func(artistdomain.AIDocumentState) (artistdomain.AIDocumentMutation, error)) (artistdomain.AIDocumentMutationResult, error) {
			return p.service.ExecuteAIDocumentMutation(ctx, string(identity.Reference), string(request.Locale), mode, compiler)
		},
		Project: func(state artistdomain.AIDocumentState) (core.Document, error) {
			return p.document(identity, request.Locale, state)
		},
		Compile: func(state artistdomain.AIDocumentState, current core.Document, command core.ValidatedApply) (artistdomain.AIDocumentMutation, []core.OperationIssue, error) {
			contributor, err := artistContributorID(state.ViewerMemberID)
			if err != nil {
				return artistdomain.AIDocumentMutation{}, nil, err
			}
			mutation, issues, err := p.compile(state, current, contributor, command.Operations)
			if err != nil {
				return artistdomain.AIDocumentMutation{}, nil, err
			}
			return mutation, issues, nil
		},
		Result: func(domainResult artistdomain.AIDocumentMutationResult, command core.ValidatedApply) (core.ApplyResult, error) {
			output := core.ApplyResult{
				DocumentRevision: core.Revision(domainResult.Revision),
				Changed:          domainResult.Changed,
				TargetRevision:   core.OptionalRevision(domainResult.TargetRevision),
			}
			output = withOperationChanges(command, output, func(operation core.Operation) []string { return artistOperationHandles(operation, command.Locale) })

			return output, nil
		},
	})
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
		TargetRevision: core.OptionalRevision(state.TargetRevision),
		SourceLocale:   core.Locale(state.SourceLocale), Locale: locale,
		LocaleExists: state.LocaleExists, Catalog: p.codec.Catalog(), Nodes: nodes,
	}, nil
}

func (p *artistPort) compile(state artistdomain.AIDocumentState, document core.Document, contributor uuid.UUID, operations []core.Operation) (artistdomain.AIDocumentMutation, []core.OperationIssue, error) {
	mutation := artistdomain.AIDocumentMutation{
		ArtistID: state.ArtistID, Locale: string(document.Locale),
		ExpectedRevision: string(document.DocumentRevision), ExpectedSource: state.SourceLocale,
		ExpectedTargetRevision: core.OptionalRevisionString(document.TargetRevision),
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

func artistOperationHandles(operation core.Operation, locale core.Locale) []string {
	return compactOperationHandles(operation, locale, "artist")
}

var _ core.DomainPort = (*artistPort)(nil)
var _ core.ExactMutationPort = (*artistPort)(nil)
