package aidocumentadapter

import (
	"context"
	"errors"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	emaildomain "github.com/echovisionlab/geul-api/internal/emailauthoring"
)

func NewEmailTemplateRegistration(
	internal *emaildomain.InternalEmailTemplateService,
) (DomainRegistration, error) {
	service, err := emaildomain.NewAIDocumentService(internal)
	if err != nil {
		return DomainRegistration{}, err
	}
	port, err := newEmailRichTextPort(
		core.DomainEmailTemplate, "Email Template", &emailTemplateAIDocumentDomain{service: service},
	)
	if err != nil {
		return DomainRegistration{}, err
	}
	return DomainRegistration{Domain: core.DomainEmailTemplate, Port: port}, nil
}

type emailTemplateAIDocumentDomain struct {
	service *emaildomain.AIDocumentService
}

func (d *emailTemplateAIDocumentDomain) Load(
	ctx context.Context,
	reference string,
	locale string,
) (emailRichTextState, error) {
	state, err := d.service.Load(ctx, reference, locale)
	if err != nil {
		return emailRichTextState{}, err
	}
	return emailTemplateState(state), nil
}

func emailTemplateState(state emaildomain.AIDocumentState) emailRichTextState {
	return emailRichTextState{
		Reference: state.TemplateID, DocumentID: state.DocumentID,
		DocumentRevision: state.DocumentRevision, TargetRevision: state.TargetRevision,
		SourceLocale: state.SourceLocale, Locale: state.Locale, LocaleExists: state.LocaleExists,
		ViewerMemberID: state.ViewerMemberID, Subject: state.Subject, Document: state.Document,
	}
}

func (d *emailTemplateAIDocumentDomain) Execute(
	ctx context.Context,
	reference string,
	locale string,
	mode emailRichTextExecutionMode,
	compiler emailRichTextMutationCompiler,
) (emailRichTextMutationResult, error) {
	domainMode := emaildomain.AIDocumentExecutionValidate
	if mode == emailRichTextExecutionApply {
		domainMode = emaildomain.AIDocumentExecutionApply
	}
	result, err := d.service.ExecuteAIDocumentMutation(
		ctx,
		reference,
		locale,
		domainMode,
		func(state emaildomain.AIDocumentState) (emaildomain.AIDocumentMutation, error) {
			mutation, err := compiler(emailTemplateState(state))
			if err != nil {
				return emaildomain.AIDocumentMutation{}, err
			}
			return emailTemplateMutation(mutation), nil
		},
	)
	if err != nil {
		var conflict *emaildomain.AIDocumentRevisionConflictError
		if errors.As(err, &conflict) {
			code := core.ConflictDocumentRevision
			if conflict.Kind == emaildomain.AIDocumentTargetRevisionConflict {
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

func emailTemplateMutation(input emailRichTextMutation) emaildomain.AIDocumentMutation {
	return emaildomain.AIDocumentMutation{
		TemplateID: input.Reference, Locale: input.Locale,
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
