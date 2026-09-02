package aidocumentadapter

import (
	"context"
	"errors"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	campaigndomain "github.com/echovisionlab/geul-api/internal/campaign"
)

func NewCampaignRegistration(
	internal *campaigndomain.InternalCampaignService,
) (DomainRegistration, error) {
	service, err := campaigndomain.NewAIDocumentService(internal)
	if err != nil {
		return DomainRegistration{}, err
	}
	port, err := newEmailRichTextPort(
		core.DomainCampaign, "Campaign", &campaignAIDocumentDomain{service: service},
	)
	if err != nil {
		return DomainRegistration{}, err
	}
	return DomainRegistration{Domain: core.DomainCampaign, Port: port}, nil
}

type campaignAIDocumentDomain struct {
	service *campaigndomain.AIDocumentService
}

func (d *campaignAIDocumentDomain) Load(
	ctx context.Context,
	reference string,
	locale string,
) (emailRichTextState, error) {
	state, err := d.service.Load(ctx, reference, locale)
	if err != nil {
		return emailRichTextState{}, err
	}
	return campaignState(state), nil
}

func campaignState(state campaigndomain.AIDocumentState) emailRichTextState {
	return emailRichTextState{
		Reference: state.CampaignID, DocumentID: state.DocumentID,
		DocumentRevision: state.DocumentRevision, TargetRevision: state.TargetRevision,
		SourceLocale: state.SourceLocale, Locale: state.Locale, LocaleExists: state.LocaleExists,
		ViewerMemberID: state.ViewerMemberID, Subject: state.Subject, Document: state.Document,
	}
}

func (d *campaignAIDocumentDomain) Execute(
	ctx context.Context,
	reference string,
	locale string,
	mode emailRichTextExecutionMode,
	compiler emailRichTextMutationCompiler,
) (emailRichTextMutationResult, error) {
	domainMode := campaigndomain.AIDocumentExecutionValidate
	if mode == emailRichTextExecutionApply {
		domainMode = campaigndomain.AIDocumentExecutionApply
	}
	result, err := d.service.ExecuteAIDocumentMutation(
		ctx,
		reference,
		locale,
		domainMode,
		func(state campaigndomain.AIDocumentState) (campaigndomain.AIDocumentMutation, error) {
			mutation, err := compiler(campaignState(state))
			if err != nil {
				return campaigndomain.AIDocumentMutation{}, err
			}
			return campaignMutation(mutation), nil
		},
	)
	if err != nil {
		var conflict *campaigndomain.AIDocumentRevisionConflictError
		if errors.As(err, &conflict) {
			code := core.ConflictDocumentRevision
			if conflict.Kind == campaigndomain.AIDocumentTargetRevisionConflict {
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

func campaignMutation(input emailRichTextMutation) campaigndomain.AIDocumentMutation {
	return campaigndomain.AIDocumentMutation{
		CampaignID: input.Reference, Locale: input.Locale,
		ExpectedDocumentRevision: input.ExpectedDocumentRevision,
		ExpectedTargetRevision:   input.ExpectedTargetRevision, ExpectedSource: input.ExpectedSource,
		ExpectedPresence: input.ExpectedPresence, ContributorMember: input.ContributorMemberID,
		Batch: cloneEmailRichTextBatch(input.Batch), SetSubject: input.SetSubject, Subject: input.Subject,
		CreateTranslation: input.CreateTranslation, DeleteTranslation: input.DeleteTranslation,
	}
}
