package aidocumentadapter

import (
	"context"
	"errors"
	"testing"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	workdomain "github.com/echovisionlab/geul-api/internal/work"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type exactWorkDocumentAPI struct {
	state         workdomain.AIDocumentState
	result        workdomain.AIDocumentMutationResult
	mutation      workdomain.AIDocumentMutation
	authorizeErr  error
	loadCalls     int
	executeCalls  int
	compilerCalls int
}

func (a *exactWorkDocumentAPI) Load(
	context.Context,
	string,
	string,
) (workdomain.AIDocumentState, error) {
	a.loadCalls++
	return a.state, nil
}

func (a *exactWorkDocumentAPI) ExecuteAIDocumentMutation(
	_ context.Context,
	workID string,
	locale string,
	_ workdomain.AIDocumentExecutionMode,
	compiler workdomain.AIDocumentMutationCompiler,
) (workdomain.AIDocumentMutationResult, error) {
	a.executeCalls++
	if a.authorizeErr != nil {
		return workdomain.AIDocumentMutationResult{}, a.authorizeErr
	}
	if workID != a.state.Work.ID || locale != a.state.Locale {
		return workdomain.AIDocumentMutationResult{}, errors.New("unexpected Work identity or locale")
	}
	a.compilerCalls++
	mutation, err := compiler(a.state)
	if err != nil {
		return workdomain.AIDocumentMutationResult{}, err
	}
	a.mutation = mutation
	return a.result, nil
}

func TestWorkExactMutationPathDoesNotEnterPublicLoad(t *testing.T) {
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK)
	require.NoError(t, err)
	workID, blockID, documentID := uuid.New(), uuid.New(), uuid.New()
	contributor, revision, nextRevision := uuid.New(), uuid.New(), uuid.New()
	title := "Work"
	state := workdomain.AIDocumentState{
		Work:             model.Work{ID: workID.String()},
		SourceLocale:     "en",
		Locale:           "en",
		LocaleExists:     true,
		Title:            &title,
		ViewerMemberID:   contributor.String(),
		DocumentRevision: revision.String(),
		Snapshot: contentblock.Snapshot{Document: contentblock.Document{
			ID: documentID, Revision: revision,
		}},
		Document: exactWorkLocalizedDocument(blockID),
	}
	api := &exactWorkDocumentAPI{
		state:  state,
		result: workdomain.AIDocumentMutationResult{Content: contentblock.Result{DocumentRevision: nextRevision, Changed: true}},
	}
	port := &workPort{application: api, codec: codec, catalog: workCatalog(codec)}
	service, err := core.NewService(port)
	require.NoError(t, err)
	request := core.ApplyRequest{
		Protocol:                 core.ProtocolVersion,
		Profile:                  core.DomainWork,
		Document:                 core.DocumentReference(workID.String()),
		Locale:                   "en",
		ExpectedDocumentRevision: core.Revision(revision.String()),
		Operations: []core.Operation{
			core.SetFieldOperation(workMetadataBlockID, workTitleField, core.Text("changed")),
		},
	}

	validation, err := service.Validate(context.Background(), request)
	require.NoError(t, err)
	require.True(t, validation.Valid())
	result, err := service.Apply(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, core.Revision(nextRevision.String()), result.DocumentRevision)
	require.Equal(t, 2, api.executeCalls)
	require.Equal(t, 2, api.compilerCalls)
	require.Zero(t, api.loadCalls)
}

func TestWorkExactMutationAuthorizesBeforeExposingValidationResult(t *testing.T) {
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK)
	require.NoError(t, err)
	workID := uuid.New()
	denied := errors.New("work not found")
	api := &exactWorkDocumentAPI{
		state:        workdomain.AIDocumentState{Work: model.Work{ID: workID.String()}, Locale: "en"},
		authorizeErr: denied,
	}
	port := &workPort{application: api, codec: codec, catalog: workCatalog(codec)}
	service, err := core.NewService(port)
	require.NoError(t, err)
	request := core.ApplyRequest{
		Protocol:                 core.ProtocolVersion,
		Profile:                  core.DomainWork,
		Document:                 core.DocumentReference(workID.String()),
		Locale:                   "en",
		ExpectedDocumentRevision: core.Revision(uuid.NewString()),
		Operations:               []core.Operation{core.DeleteBlockOperation("unknown-block")},
	}

	_, err = service.Validate(context.Background(), request)
	require.ErrorIs(t, err, denied)
	require.Equal(t, 1, api.executeCalls)
	require.Zero(t, api.compilerCalls, "unauthorized request reached the adapter compiler")
	require.Zero(t, api.loadCalls, "unauthorized request entered the public read path")
}

func TestWorkExactTargetMutationCarriesSplitCAS(t *testing.T) {
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK)
	require.NoError(t, err)
	workID, blockID, documentID, revision, contributor := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	targetRevision, nextTargetRevision := "work-target-1", "work-target-2"
	title := "translated"
	document := exactWorkLocalizedDocument(blockID)
	document.Locale, document.LocaleOverlay.Locale = "ko", "ko"
	api := &exactWorkDocumentAPI{
		state: workdomain.AIDocumentState{
			Work: model.Work{ID: workID.String()}, SourceLocale: "en", Locale: "ko", LocaleExists: true,
			DocumentRevision: revision.String(), TargetRevision: &targetRevision, Title: &title,
			ViewerMemberID: contributor.String(),
			Snapshot:       contentblock.Snapshot{Document: contentblock.Document{ID: documentID, Revision: revision}},
			Document:       document,
		},
		result: workdomain.AIDocumentMutationResult{
			Content:        contentblock.Result{DocumentRevision: revision, Changed: true},
			TargetRevision: &nextTargetRevision,
		},
	}
	port := &workPort{application: api, codec: codec, catalog: workCatalog(codec)}
	service, err := core.NewService(port)
	require.NoError(t, err)
	expectedTarget := core.Revision(targetRevision)
	result, err := service.Apply(context.Background(), core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainWork,
		Document: core.DocumentReference(workID.String()), Locale: "ko",
		ExpectedDocumentRevision: core.Revision(revision.String()), ExpectedTargetRevision: &expectedTarget,
		Operations: []core.Operation{
			core.SetFieldOperation(core.BlockID(blockID.String()), "content", core.RichText(core.InlineText("changed"))),
		},
	})
	require.NoError(t, err)
	require.Equal(t, core.Revision(revision.String()), result.DocumentRevision)
	require.NotNil(t, result.TargetRevision)
	require.Equal(t, core.Revision(nextTargetRevision), *result.TargetRevision)
	require.NotNil(t, api.mutation.ExpectedTargetRevision)
	require.Equal(t, targetRevision, *api.mutation.ExpectedTargetRevision)
}

func TestWorkExactTargetConflictMapsCurrentSplitCAS(t *testing.T) {
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK)
	require.NoError(t, err)
	workID, documentRevision := uuid.New(), uuid.New()
	currentTarget := "work-target-current"
	api := &exactWorkDocumentAPI{
		state: workdomain.AIDocumentState{Work: model.Work{ID: workID.String()}, Locale: "ko"},
		authorizeErr: &workdomain.AIDocumentRevisionConflictError{
			Kind: workdomain.AIDocumentTargetRevisionConflict, CurrentDocumentRevision: documentRevision.String(),
			CurrentTargetRevision: &currentTarget,
		},
	}
	port := &workPort{application: api, codec: codec, catalog: workCatalog(codec)}
	service, err := core.NewService(port)
	require.NoError(t, err)
	expectedTarget := core.Revision("work-target-stale")
	_, err = service.Validate(context.Background(), core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainWork,
		Document: core.DocumentReference(workID.String()), Locale: "ko",
		ExpectedDocumentRevision: core.Revision(documentRevision.String()), ExpectedTargetRevision: &expectedTarget,
		Operations: []core.Operation{core.DeleteBlockOperation("unknown-block")},
	})
	var conflict *core.ConflictError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, core.ConflictTargetRevision, conflict.Conflict.Code)
	require.Equal(t, core.Revision(documentRevision.String()), conflict.Conflict.CurrentDocumentRevision)
	require.NotNil(t, conflict.Conflict.CurrentTargetRevision)
	require.Equal(t, core.Revision(currentTarget), *conflict.Conflict.CurrentTargetRevision)
}

func exactWorkLocalizedDocument(blockID uuid.UUID) *contentv1.LocalizedRichTextDocument {
	return &contentv1.LocalizedRichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK,
		Locale:                  "en",
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
			Block: &contentv1.RichTextBlock{
				Id: blockID.String(),
				Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{
					Props: &contentv1.ParagraphProps{},
				}},
			},
			Placement: &contentv1.ContentBlockPlacement{},
		}}},
		LocaleOverlay: &contentv1.RichTextLocaleOverlay{
			Locale: "en",
			Blocks: []*contentv1.RichTextBlockLocale{{
				BlockId: blockID.String(),
				Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
					Props:   &contentv1.ParagraphLocaleProps{},
					Content: []*contentv1.RichTextInline{},
				}},
			}},
		},
	}
}
