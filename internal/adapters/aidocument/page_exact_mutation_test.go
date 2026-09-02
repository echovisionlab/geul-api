package aidocumentadapter

import (
	"context"
	"errors"
	"testing"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/model"
	pagedomain "github.com/echovisionlab/geul-api/internal/page"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type exactPageDocumentAPI struct {
	state         pagedomain.AIDocumentState
	result        pagedomain.AIDocumentMutationResult
	executeErr    error
	mutations     []pagedomain.AIDocumentMutation
	authorizeErr  error
	loadCalls     int
	executeCalls  int
	compilerCalls int
}

func (a *exactPageDocumentAPI) Load(
	context.Context,
	string,
	string,
) (pagedomain.AIDocumentState, error) {
	a.loadCalls++
	return a.state, nil
}

func (a *exactPageDocumentAPI) ExecuteAIDocumentMutation(
	_ context.Context,
	pageID string,
	locale string,
	_ pagedomain.AIDocumentExecutionMode,
	compiler pagedomain.AIDocumentMutationCompiler,
) (pagedomain.AIDocumentMutationResult, error) {
	a.executeCalls++
	if a.authorizeErr != nil {
		return pagedomain.AIDocumentMutationResult{}, a.authorizeErr
	}
	if pageID != a.state.Page.ID || locale != a.state.Locale {
		return pagedomain.AIDocumentMutationResult{}, errors.New("unexpected Page identity or locale")
	}
	a.compilerCalls++
	mutation, err := compiler(a.state)
	if err != nil {
		return pagedomain.AIDocumentMutationResult{}, err
	}
	a.mutations = append(a.mutations, mutation)
	if a.executeErr != nil {
		return pagedomain.AIDocumentMutationResult{}, a.executeErr
	}
	return a.result, nil
}

func TestPageExactMutationPathDoesNotEnterPublicLoad(t *testing.T) {
	codec, err := NewPageCodec()
	require.NoError(t, err)
	catalog := codec.Catalog()
	catalog.BlockKinds = append(catalog.BlockKinds, pageMetadataBlockKind)
	catalog.Fields = append(catalog.Fields,
		core.FieldRule{BlockKind: pageMetadataBlockKind, Field: pageTitleField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true},
		core.FieldRule{BlockKind: pageMetadataBlockKind, Field: pageSummaryField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true},
	)
	pageID, contributor, nextRevision := uuid.New(), uuid.New(), uuid.New()
	state := pageStateForCodecTest(uuid.NewString())
	state.Page = model.Page{ID: pageID.String()}
	state.ViewerMemberID = contributor.String()
	api := &exactPageDocumentAPI{
		state:  state,
		result: pagedomain.AIDocumentMutationResult{DocumentRevision: nextRevision.String(), Changed: true},
	}
	port := &pagePort{application: api, codec: codec, catalog: catalog}
	service, err := core.NewService(port)
	require.NoError(t, err)
	request := core.ApplyRequest{
		Protocol: core.ProtocolVersion,
		Profile:  core.DomainPage,
		Document: core.DocumentReference(pageID.String()),
		Locale:   "en",
		ExpectedDocumentRevision: core.Revision(
			state.Snapshot.Document.Revision.String(),
		),
		Operations: []core.Operation{
			core.SetFieldOperation(pageMetadataBlockID, pageTitleField, core.Text("")),
		},
	}

	validation, err := service.Validate(context.Background(), request)
	require.NoError(t, err)
	require.True(t, validation.Valid())
	result, err := service.Apply(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, core.Revision(nextRevision.String()), result.DocumentRevision)
	require.Nil(t, result.TargetRevision)
	require.Len(t, api.mutations, 2)
	for _, mutation := range api.mutations {
		require.Equal(t, state.Snapshot.Document.Revision, mutation.ExpectedRevision)
		require.Nil(t, mutation.ExpectedTargetRevision)
		require.True(t, mutation.Metadata.SetTitle)
		require.NotNil(t, mutation.Metadata.Title)
		require.Empty(t, *mutation.Metadata.Title)
	}
	require.Equal(t, 2, api.executeCalls)
	require.Equal(t, 2, api.compilerCalls)
	require.Zero(t, api.loadCalls)
}

func TestPageExactMutationAuthorizesBeforeExposingValidationResult(t *testing.T) {
	codec, err := NewPageCodec()
	require.NoError(t, err)
	pageID := uuid.New()
	denied := errors.New("page not found")
	api := &exactPageDocumentAPI{
		state:        pagedomain.AIDocumentState{Page: model.Page{ID: pageID.String()}, Locale: "en"},
		authorizeErr: denied,
	}
	port := &pagePort{application: api, codec: codec, catalog: codec.Catalog()}
	service, err := core.NewService(port)
	require.NoError(t, err)
	request := core.ApplyRequest{
		Protocol: core.ProtocolVersion,
		Profile:  core.DomainPage,
		Document: core.DocumentReference(pageID.String()),
		Locale:   "en",
		ExpectedDocumentRevision: core.Revision(
			uuid.NewString(),
		),
		Operations: []core.Operation{core.DeleteBlockOperation("unknown-block")},
	}

	_, err = service.Validate(context.Background(), request)
	require.ErrorIs(t, err, denied)
	require.Equal(t, 1, api.executeCalls)
	require.Zero(t, api.compilerCalls, "unauthorized request reached the adapter compiler")
	require.Zero(t, api.loadCalls, "unauthorized request entered the public read path")
}

func TestPageExactTargetLifecycleUsesSplitRevisionCAS(t *testing.T) {
	t.Parallel()

	t.Run("create missing target", func(t *testing.T) {
		t.Parallel()
		port, api, identity := newExactPagePortForLocale(t, "ko", false)
		nextTarget := "tr1_ko-created"
		api.result = pagedomain.AIDocumentMutationResult{
			DocumentRevision: api.state.Revision, TargetRevision: &nextTarget, Changed: true,
		}
		service, err := core.NewService(port)
		require.NoError(t, err)
		request := pageExactApplyRequest(identity, api.state, nil, core.CreateTranslationOperation())

		validation, err := service.Validate(t.Context(), request)
		require.NoError(t, err)
		require.True(t, validation.Valid())
		result, err := service.Apply(t.Context(), request)
		require.NoError(t, err)
		require.Equal(t, core.Revision(api.state.Revision), result.DocumentRevision)
		require.Equal(t, core.Revision(nextTarget), *result.TargetRevision)
		require.Len(t, api.mutations, 2)
		for _, mutation := range api.mutations {
			require.True(t, mutation.Metadata.EnsureLocale)
			require.False(t, mutation.ObservedLocaleExists)
			require.Nil(t, mutation.ExpectedTargetRevision)
			require.False(t, mutation.DeleteTranslation)
		}
	})

	t.Run("update target explicit empty", func(t *testing.T) {
		t.Parallel()
		port, api, identity := newExactPagePortForLocale(t, "ko", true)
		nextTarget := "tr1_ko-updated"
		api.result = pagedomain.AIDocumentMutationResult{
			DocumentRevision: api.state.Revision, TargetRevision: &nextTarget, Changed: true,
		}
		service, err := core.NewService(port)
		require.NoError(t, err)
		request := pageExactApplyRequest(
			identity, api.state, api.state.TargetRevision,
			core.SetFieldOperation(pageMetadataBlockID, pageTitleField, core.Text("")),
		)

		result, err := service.Apply(t.Context(), request)
		require.NoError(t, err)
		require.Equal(t, core.Revision(api.state.Revision), result.DocumentRevision)
		require.Equal(t, core.Revision(nextTarget), *result.TargetRevision)
		require.Len(t, api.mutations, 1)
		mutation := api.mutations[0]
		require.False(t, mutation.Metadata.EnsureLocale)
		require.True(t, mutation.Metadata.SetTitle)
		require.NotNil(t, mutation.Metadata.Title)
		require.Empty(t, *mutation.Metadata.Title)
		require.Equal(t, api.state.TargetRevision, mutation.ExpectedTargetRevision)
	})

	t.Run("delete target", func(t *testing.T) {
		t.Parallel()
		port, api, identity := newExactPagePortForLocale(t, "ko", true)
		api.result = pagedomain.AIDocumentMutationResult{
			DocumentRevision: api.state.Revision, Changed: true,
		}
		service, err := core.NewService(port)
		require.NoError(t, err)
		request := pageExactApplyRequest(identity, api.state, api.state.TargetRevision, core.DeleteTranslationOperation())

		result, err := service.Apply(t.Context(), request)
		require.NoError(t, err)
		require.Equal(t, core.Revision(api.state.Revision), result.DocumentRevision)
		require.Nil(t, result.TargetRevision)
		require.Len(t, api.mutations, 1)
		mutation := api.mutations[0]
		require.True(t, mutation.DeleteTranslation)
		require.False(t, mutation.Metadata.EnsureLocale)
		require.Equal(t, api.state.TargetRevision, mutation.ExpectedTargetRevision)
	})
}

func TestPageExactMutationMapsSplitRevisionConflicts(t *testing.T) {
	t.Parallel()

	t.Run("document", func(t *testing.T) {
		t.Parallel()
		port, api, identity := newExactPagePortForLocale(t, "en", true)
		currentDocument := uuid.NewString()
		api.executeErr = &pagedomain.AIDocumentRevisionConflictError{
			Kind: pagedomain.AIDocumentDocumentRevisionConflict, CurrentRevision: currentDocument,
		}
		service, err := core.NewService(port)
		require.NoError(t, err)
		request := pageExactApplyRequest(
			identity, api.state, nil,
			core.SetFieldOperation(pageMetadataBlockID, pageTitleField, core.Text("changed")),
		)

		_, err = service.Validate(t.Context(), request)
		var conflict *core.ConflictError
		require.ErrorAs(t, err, &conflict)
		require.Equal(t, core.ConflictDocumentRevision, conflict.Conflict.Code)
		require.Equal(t, core.Revision(currentDocument), conflict.Conflict.CurrentDocumentRevision)
		require.Nil(t, conflict.Conflict.CurrentTargetRevision)
	})

	t.Run("target", func(t *testing.T) {
		t.Parallel()
		port, api, identity := newExactPagePortForLocale(t, "ko", true)
		currentTarget := "tr1_ko-current"
		api.executeErr = &pagedomain.AIDocumentRevisionConflictError{
			Kind: pagedomain.AIDocumentTargetRevisionConflict, CurrentRevision: api.state.Revision,
			CurrentTargetRevision: &currentTarget,
		}
		service, err := core.NewService(port)
		require.NoError(t, err)
		request := pageExactApplyRequest(
			identity, api.state, api.state.TargetRevision,
			core.SetFieldOperation(pageMetadataBlockID, pageSummaryField, core.Text("changed")),
		)

		_, err = service.Validate(t.Context(), request)
		var conflict *core.ConflictError
		require.ErrorAs(t, err, &conflict)
		require.Equal(t, core.ConflictTargetRevision, conflict.Conflict.Code)
		require.Equal(t, core.Revision(api.state.Revision), conflict.Conflict.CurrentDocumentRevision)
		require.NotNil(t, conflict.Conflict.CurrentTargetRevision)
		require.Equal(t, core.Revision(currentTarget), *conflict.Conflict.CurrentTargetRevision)
	})
}

func newExactPagePortForLocale(
	t *testing.T,
	locale string,
	exists bool,
) (*pagePort, *exactPageDocumentAPI, core.DocumentIdentity) {
	t.Helper()
	codec, err := NewPageCodec()
	require.NoError(t, err)
	state := pageStateForCodecTest(uuid.NewString())
	pageID, contributor := uuid.New(), uuid.New()
	state.Page = model.Page{ID: pageID.String()}
	state.ViewerMemberID = contributor.String()
	state.Locale = locale
	state.LocaleExists = exists
	state.Document.Locale = locale
	state.Document.LocaleOverlay.Locale = locale
	if locale != state.SourceLocale {
		state.Document.LocaleOverlay.Sections = nil
		state.Title = nil
		state.Summary = nil
		if exists {
			targetRevision := "tr1_" + locale + "-loaded"
			state.TargetRevision = &targetRevision
			title := "Target title"
			state.Title = &title
		}
	}
	catalog := codec.Catalog()
	catalog.BlockKinds = append(catalog.BlockKinds, pageMetadataBlockKind)
	catalog.Fields = append(catalog.Fields,
		core.FieldRule{BlockKind: pageMetadataBlockKind, Field: pageTitleField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true},
		core.FieldRule{BlockKind: pageMetadataBlockKind, Field: pageSummaryField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true},
	)
	api := &exactPageDocumentAPI{state: state}
	identity := core.DocumentIdentity{Domain: core.DomainPage, Reference: core.DocumentReference(pageID.String())}
	return &pagePort{application: api, codec: codec, catalog: catalog}, api, identity
}

func pageExactApplyRequest(
	identity core.DocumentIdentity,
	state pagedomain.AIDocumentState,
	targetRevision *string,
	operations ...core.Operation,
) core.ApplyRequest {
	request := core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainPage,
		Document: identity.Reference, Locale: core.Locale(state.Locale),
		ExpectedDocumentRevision: core.Revision(state.Revision), Operations: operations,
	}
	if targetRevision != nil {
		revision := core.Revision(*targetRevision)
		request.ExpectedTargetRevision = &revision
	}
	return request
}
