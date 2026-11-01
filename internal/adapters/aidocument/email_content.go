package aidocumentadapter

import (
	"context"
	"errors"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
)

// emailContentAIDocuments is the AI document seam of a Campaign or an Email
// Template.
type emailContentAIDocuments interface {
	Load(ctx context.Context, reference, locale string) (emailcontent.AIDocumentState, error)
	ExecuteAIDocumentMutation(
		ctx context.Context,
		reference string,
		locale string,
		mode emailcontent.AIDocumentExecutionMode,
		compiler emailcontent.AIDocumentMutationCompiler,
	) (emailcontent.AIDocumentMutationResult, error)
}

func newEmailContentRegistration(domain core.Domain, label string, service emailContentAIDocuments) (DomainRegistration, error) {
	port, err := newEmailRichTextPort(domain, label, &emailContentAIDocumentDomain{service: service})
	if err != nil {
		return DomainRegistration{}, err
	}
	return DomainRegistration{Domain: domain, Port: port}, nil
}

type emailContentAIDocumentDomain struct {
	service emailContentAIDocuments
}

func (d *emailContentAIDocumentDomain) Load(
	ctx context.Context,
	reference string,
	locale string,
) (emailRichTextState, error) {
	state, err := d.service.Load(ctx, reference, locale)
	if err != nil {
		return emailRichTextState{}, err
	}
	return emailContentState(state), nil
}

func emailContentState(state emailcontent.AIDocumentState) emailRichTextState {
	return emailRichTextState{
		Reference: state.Reference, DocumentID: state.DocumentID,
		DocumentRevision: state.DocumentRevision, TargetRevision: state.TargetRevision,
		SourceLocale: state.SourceLocale, Locale: state.Locale, LocaleExists: state.LocaleExists,
		ViewerMemberID: state.ViewerMemberID, Subject: state.Subject, Document: state.Document,
	}
}

func (d *emailContentAIDocumentDomain) Execute(
	ctx context.Context,
	reference string,
	locale string,
	mode emailRichTextExecutionMode,
	compiler emailRichTextMutationCompiler,
) (emailRichTextMutationResult, error) {
	domainMode := emailcontent.AIDocumentExecutionValidate
	if mode == emailRichTextExecutionApply {
		domainMode = emailcontent.AIDocumentExecutionApply
	}
	result, err := d.service.ExecuteAIDocumentMutation(
		ctx,
		reference,
		locale,
		domainMode,
		func(state emailcontent.AIDocumentState) (emailcontent.AIDocumentMutation, error) {
			mutation, err := compiler(emailContentState(state))
			if err != nil {
				return emailcontent.AIDocumentMutation{}, err
			}
			return emailContentMutation(mutation), nil
		},
	)
	if err != nil {
		var conflict *core.RevisionConflict
		if errors.As(err, &conflict) {
			code := core.ConflictDocumentRevision
			if conflict.Kind == core.ConflictTargetRevision {
				code = core.ConflictTargetRevision
			}
			return emailRichTextMutationResult{}, &emailRichTextRevisionConflict{
				kind: code, currentDocumentRevision: conflict.CurrentDocumentRevision,
				currentTargetRevision: conflict.CurrentTargetRevision,
			}
		}
		return emailRichTextMutationResult{}, err
	}
	return emailRichTextMutationResult{
		DocumentRevision: result.DocumentRevision, TargetRevision: result.TargetRevision,
		Changed: result.Changed,
	}, nil
}

func emailContentMutation(input emailRichTextMutation) emailcontent.AIDocumentMutation {
	return emailcontent.AIDocumentMutation{
		Reference: input.Reference, Locale: input.Locale,
		ExpectedDocumentRevision: input.ExpectedDocumentRevision,
		ExpectedTargetRevision:   input.ExpectedTargetRevision, ExpectedSource: input.ExpectedSource,
		ExpectedPresence: input.ExpectedPresence, ContributorMember: input.ContributorMemberID,
		Batch: cloneEmailRichTextBatch(input.Batch), SetSubject: input.SetSubject, Subject: input.Subject,
		CreateTranslation: input.CreateTranslation, DeleteTranslation: input.DeleteTranslation,
	}
}

func cloneEmailRichTextBatch(batch *contentblock.Batch) *contentblock.Batch {
	if batch == nil {
		return nil
	}
	cloned := contentblock.CloneBatch(*batch)
	return &cloned
}
