package aidocumentadapter

import (
	"context"
	"errors"
	"testing"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	labeldomain "github.com/echovisionlab/geul-api/internal/label"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const (
	labelTestID         = "11111111-1111-4111-8111-111111111111"
	labelDocumentID     = "22222222-2222-4222-8222-222222222222"
	labelBlockID        = "33333333-3333-4333-8333-333333333333"
	labelRevision       = "44444444-4444-4444-8444-444444444444"
	labelNextRevision   = "55555555-5555-4555-8555-555555555555"
	labelMemberID       = "66666666-6666-4666-8666-666666666666"
	labelTargetRevision = "tr1_label-target"
)

type labelRegistrationAPI struct {
	state         labeldomain.AIDocumentState
	mutation      labeldomain.AIDocumentMutation
	result        labeldomain.AIDocumentMutationResult
	loadCalls     int
	executeCalls  int
	compilerCalls int
	loadErr       error
	applyErr      error
	validateErr   error
	authorizeErr  error
	dependencyErr error
}

func (a *labelRegistrationAPI) ValidateAIDocumentDependencies() error { return a.dependencyErr }

func (a *labelRegistrationAPI) LoadAIDocumentState(context.Context, string, string) (labeldomain.AIDocumentState, error) {
	a.loadCalls++
	return a.state, a.loadErr
}

func (a *labelRegistrationAPI) ExecuteAIDocumentMutation(
	_ context.Context,
	_ string,
	_ string,
	mode labeldomain.AIDocumentExecutionMode,
	compiler labeldomain.AIDocumentMutationCompiler,
) (labeldomain.AIDocumentMutationResult, error) {
	a.executeCalls++
	if a.authorizeErr != nil {
		return labeldomain.AIDocumentMutationResult{}, a.authorizeErr
	}
	a.compilerCalls++
	mutation, err := compiler(a.state)
	if err != nil {
		return labeldomain.AIDocumentMutationResult{}, err
	}
	a.mutation = mutation
	if mode == labeldomain.AIDocumentExecutionValidate {
		return a.result, a.validateErr
	}
	return a.result, a.applyErr
}

func TestNewLabelRegistrationProjectsGeneratedCompactValuesAndExplicitEmpty(t *testing.T) {
	api := &labelRegistrationAPI{state: labelSourceState()}
	registration, err := NewLabelRegistration(api)
	require.NoError(t, err)
	require.Equal(t, core.DomainLabel, registration.Domain)

	document, err := registration.Port.Load(t.Context(), labelIdentity(), "en")
	require.NoError(t, err)
	require.Equal(t, core.Revision(labelRevision), document.DocumentRevision)
	require.Equal(t, core.LocaleRoleSource, document.Role())
	require.True(t, document.LocaleExists)
	require.Len(t, document.Nodes, 1)
	require.Equal(t, core.BlockID(labelBlockID), document.Nodes[0].ID)
	require.Equal(t, core.BlockKind("paragraph"), document.Nodes[0].Kind)
	require.Equal(t, core.RichText(), document.Nodes[0].Localized[0].Value)
	for _, rule := range document.Catalog.Fields {
		require.False(t, rule.File, "compact Label catalog must not expose File fields")
	}

	_, err = NewLabelRegistration((*labelRegistrationAPI)(nil))
	require.Error(t, err)
	_, err = NewLabelRegistration(&labelRegistrationAPI{dependencyErr: context.Canceled})
	require.ErrorIs(t, err, context.Canceled)
}

func TestLabelExactValidationUsesGeneratedRangesAndValuesOnlyTargetRule(t *testing.T) {
	api := &labelRegistrationAPI{state: labelSourceState()}
	registration, err := NewLabelRegistration(api)
	require.NoError(t, err)
	service, err := core.NewService(registration.Port)
	require.NoError(t, err)
	document, err := registration.Port.Load(t.Context(), labelIdentity(), "en")
	require.NoError(t, err)

	validation, err := service.Validate(t.Context(), labelRequest("en",
		core.SetFieldOperation(labelBlockID, "previewWidth", core.Number("101")),
	))
	require.NoError(t, err)
	require.NotEmpty(t, validation.Issues)
	require.Equal(t, core.IssueInvalidOperation, validation.Issues[0].Code)
	validation, err = service.Validate(t.Context(), labelRequest("en",
		core.SetFieldOperation(labelBlockID, richTextContentField, core.RichText(core.InlineMath("x"))),
	))
	require.NoError(t, err)
	require.NotEmpty(t, validation.Issues, "compact generated inline catalog must reject math")

	fileValidation := core.ValidateOperations(document, core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainLabel, Document: labelTestID,
		Locale: "en", ExpectedDocumentRevision: labelRevision,
		Operations: []core.Operation{core.AttachFileOperation(labelBlockID, "attachment", "99999999-9999-4999-8999-999999999999")},
	})
	require.False(t, fileValidation.Valid())
	require.Equal(t, core.IssueUnknownField, fileValidation.Issues[0].Code)

	targetState := labelSourceState()
	targetState.Locale = "ko"
	targetState.LocaleExists = true
	targetState.TargetRevision = stringPointer(labelTargetRevision)
	targetState.Document.Locale = "ko"
	targetState.Document.LocaleOverlay.Locale = "ko"
	api.state = targetState
	target, err := registration.Port.Load(t.Context(), labelIdentity(), "ko")
	require.NoError(t, err)
	validation, err = service.Validate(t.Context(), labelRequest("ko",
		core.UnsetFieldOperation(labelBlockID, richTextContentField),
	))
	require.NoError(t, err)
	require.Equal(t, []core.OperationIssue{{
		Operation: 0, Code: core.IssueTargetFieldForbidden,
		Handle:  "field:" + labelBlockID + "/content",
		Message: "non-source locales cannot unset fields; set an explicit empty value or delete the translation",
	}}, validation.Issues)
	require.Equal(t, core.Locale("ko"), target.Locale)
}

func TestLabelExactApplyCompilesOnceAndMapsRevisionConflict(t *testing.T) {
	api := &labelRegistrationAPI{
		state:  labelSourceState(),
		result: labeldomain.AIDocumentMutationResult{Content: contentblockResult(labelNextRevision, true)},
	}
	registration, err := NewLabelRegistration(api)
	require.NoError(t, err)
	service, err := core.NewService(registration.Port)
	require.NoError(t, err)
	request := labelRequest("en", core.SetFieldOperation(
		labelBlockID, richTextContentField, core.RichText(core.InlineText("updated")),
	))
	result, err := service.Apply(t.Context(), request)
	require.NoError(t, err)
	require.True(t, result.Changed)
	require.Equal(t, core.Revision(labelNextRevision), result.DocumentRevision)
	require.NotNil(t, api.mutation.Batch)
	require.Equal(t, uuid.MustParse(labelDocumentID), api.mutation.Batch.DocumentID)
	require.Equal(t, uuid.MustParse(labelRevision), api.mutation.Batch.ExpectedRevision)
	require.Equal(t, []uuid.UUID{uuid.MustParse(labelMemberID)}, api.mutation.Batch.ContributorMemberIDs)

	api.applyErr = &labeldomain.AIDocumentRevisionConflictError{Kind: labeldomain.AIDocumentDocumentRevisionConflict, CurrentDocumentRevision: labelNextRevision}
	_, err = service.Apply(t.Context(), request)
	var conflict *core.ConflictError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, core.Revision(labelNextRevision), conflict.Conflict.CurrentDocumentRevision)
	require.Equal(t, []string{"field:" + labelBlockID + "/content"}, conflict.Conflict.AffectedHandles)
}

func TestLabelExactValidationDryRunsOwningMutationAndMapsConflict(t *testing.T) {
	api := &labelRegistrationAPI{state: labelSourceState()}
	registration, err := NewLabelRegistration(api)
	require.NoError(t, err)
	service, err := core.NewService(registration.Port)
	require.NoError(t, err)
	request := labelRequest("en",
		core.SetFieldOperation(labelBlockID, richTextContentField, core.RichText(core.InlineText("validated"))),
	)

	validation, err := service.Validate(t.Context(), request)
	require.NoError(t, err)
	require.True(t, validation.Valid())
	require.NotNil(t, api.mutation.Batch)
	require.Equal(t, uuid.MustParse(labelRevision), api.mutation.Batch.ExpectedRevision)

	api.validateErr = &labeldomain.AIDocumentRevisionConflictError{Kind: labeldomain.AIDocumentDocumentRevisionConflict, CurrentDocumentRevision: labelNextRevision}
	_, err = service.Validate(t.Context(), request)
	var conflict *core.ConflictError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, core.Revision(labelNextRevision), conflict.Conflict.CurrentDocumentRevision)
}

func TestLabelTranslationLifecycleRemainsExplicitAndExclusive(t *testing.T) {
	state := labelSourceState()
	state.Locale = "ko"
	state.LocaleExists = false
	state.Document.Locale = "ko"
	state.Document.LocaleOverlay = &contentv1.RichTextLocaleOverlay{Locale: "ko"}
	api := &labelRegistrationAPI{
		state:  state,
		result: labeldomain.AIDocumentMutationResult{Content: contentblockResult(labelNextRevision, true)},
	}
	registration, err := NewLabelRegistration(api)
	require.NoError(t, err)
	service, err := core.NewService(registration.Port)
	require.NoError(t, err)
	createRequest := labelRequest("ko", core.CreateTranslationOperation())
	createRequest.ExpectedTargetRevision = nil
	_, err = service.Apply(t.Context(), createRequest)
	require.NoError(t, err)
	require.True(t, api.mutation.CreateTranslation)
	require.Nil(t, api.mutation.Batch)
}

func TestLabelExactMutationSkipsPublicLoadAndUsesLockedViewer(t *testing.T) {
	api := &labelRegistrationAPI{
		state:  labelSourceState(),
		result: labeldomain.AIDocumentMutationResult{Content: contentblockResult(labelNextRevision, true)},
	}
	registration, err := NewLabelRegistration(api)
	require.NoError(t, err)
	service, err := core.NewService(registration.Port)
	require.NoError(t, err)
	request := core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainLabel, Document: labelTestID,
		Locale: "en", ExpectedDocumentRevision: labelRevision,
		Operations: []core.Operation{
			core.SetFieldOperation(labelBlockID, richTextContentField, core.RichText(core.InlineText("updated"))),
		},
	}

	validation, err := service.Validate(t.Context(), request)
	require.NoError(t, err)
	require.True(t, validation.Valid())
	require.Zero(t, api.loadCalls, "exact validation must not enter the public read path")
	require.Equal(t, 1, api.executeCalls)
	require.Equal(t, uuid.MustParse(labelMemberID), api.mutation.ContributorMemberID)

	result, err := service.Apply(t.Context(), request)
	require.NoError(t, err)
	require.True(t, result.Changed)
	require.Equal(t, core.Revision(labelNextRevision), result.DocumentRevision)
	require.Zero(t, api.loadCalls, "exact apply must not enter the public read path")
	require.Equal(t, 2, api.executeCalls)
	require.Equal(t, 2, api.compilerCalls)
}

func TestLabelExactMutationDeniesBeforeAdapterCompiler(t *testing.T) {
	denied := errors.New("label not found")
	api := &labelRegistrationAPI{authorizeErr: denied}
	registration, err := NewLabelRegistration(api)
	require.NoError(t, err)
	service, err := core.NewService(registration.Port)
	require.NoError(t, err)
	request := core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainLabel, Document: labelTestID,
		Locale: "en", ExpectedDocumentRevision: labelRevision,
		Operations: []core.Operation{core.DeleteBlockOperation("unknown-block")},
	}

	_, err = service.Validate(t.Context(), request)
	require.ErrorIs(t, err, denied)
	require.Equal(t, 1, api.executeCalls)
	require.Zero(t, api.compilerCalls)
	require.Zero(t, api.loadCalls)
}

func labelIdentity() core.DocumentIdentity {
	return core.DocumentIdentity{Domain: core.DomainLabel, Reference: labelTestID}
}

func labelRequest(locale core.Locale, operations ...core.Operation) core.ApplyRequest {
	request := core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainLabel, Document: labelTestID,
		Locale: locale, ExpectedDocumentRevision: labelRevision, Operations: operations,
	}
	if locale != "en" {
		revision := core.Revision(labelTargetRevision)
		request.ExpectedTargetRevision = &revision
	}
	return request
}

func contentblockResult(revision string, changed bool) contentblock.Result {
	return contentblock.Result{DocumentRevision: uuid.MustParse(revision), Changed: changed}
}

func labelSourceState() labeldomain.AIDocumentState {
	left := contentv1.ParagraphProps_TEXT_ALIGNMENT_LEFT
	width := int32(100)
	return labeldomain.AIDocumentState{
		LabelID: labelTestID, ContentDocumentID: uuid.MustParse(labelDocumentID), Revision: labelRevision,
		SourceLocale: "en", Locale: "en", LocaleExists: true,
		ViewerMemberID: labelMemberID,
		Document: &contentv1.LocalizedRichTextDocument{
			BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
			Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT, Locale: "en",
			Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
				Block:     &contentv1.RichTextBlock{Id: labelBlockID, Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{TextAlignment: &left, PreviewWidth: &width}}}},
				Placement: &contentv1.ContentBlockPlacement{},
			}}},
			LocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "en", Blocks: []*contentv1.RichTextBlockLocale{{
				BlockId: labelBlockID, Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{Props: &contentv1.ParagraphLocaleProps{}}},
			}}},
		},
	}
}

var _ labelDocumentAPI = (*labelRegistrationAPI)(nil)
