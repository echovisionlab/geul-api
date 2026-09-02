package aidocumentadapter

import (
	"context"
	"errors"
	"testing"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type exactPostDocumentAPI struct {
	state         postdomain.AIDocumentState
	result        postdomain.AIDocumentMutationResult
	authorizeErr  error
	mutation      postdomain.AIDocumentMutation
	loadCalls     int
	executeCalls  int
	compilerCalls int
}

func (a *exactPostDocumentAPI) LoadAIDocumentState(
	context.Context,
	string,
	string,
) (postdomain.AIDocumentState, error) {
	a.loadCalls++
	return a.state, nil
}

func (a *exactPostDocumentAPI) ExecuteAIDocumentMutation(
	_ context.Context,
	postID string,
	locale string,
	_ postdomain.AIDocumentExecutionMode,
	compiler postdomain.AIDocumentMutationCompiler,
) (postdomain.AIDocumentMutationResult, error) {
	a.executeCalls++
	if a.authorizeErr != nil {
		return postdomain.AIDocumentMutationResult{}, a.authorizeErr
	}
	if postID != a.state.PostID || locale != a.state.RequestedLocale {
		return postdomain.AIDocumentMutationResult{}, errors.New("unexpected Post identity or locale")
	}
	a.compilerCalls++
	mutation, err := compiler(a.state)
	if err != nil {
		return postdomain.AIDocumentMutationResult{}, err
	}
	a.mutation = mutation
	return a.result, nil
}

func TestNewPostRegistrationRejectsMissingOwner(t *testing.T) {
	_, err := NewPostRegistration(nil)
	require.Error(t, err)
}

func TestPostPortProjectsMissingAndExplicitEmptyLocaleMetadata(t *testing.T) {
	port := newPostPortForTest(t)
	identity := core.DocumentIdentity{Domain: core.DomainPost, Reference: core.DocumentReference(uuid.NewString())}
	blockID, documentID, revision := uuid.New(), uuid.New(), uuid.New()
	state := postdomain.AIDocumentState{
		PostID: string(identity.Reference), ContentDocumentID: documentID.String(),
		DocumentRevision: revision.String(), SourceLocale: "en", RequestedLocale: "ko",
		CategoryIDs: []string{uuid.NewString()}, TagIDs: []string{},
		LocalizedDocument: absentTargetParagraphDocument(blockID, "ko"),
	}
	missing, err := port.project(identity, "ko", state)
	require.NoError(t, err)
	require.False(t, missing.LocaleExists)
	require.Empty(t, missing.Nodes[0].Localized)

	empty := ""
	state.LocaleExists = true
	state.RequestedMetadata = &postdomain.AIDocumentLocaleMetadata{Title: &empty}
	present, err := port.project(identity, "ko", state)
	require.NoError(t, err)
	require.True(t, present.LocaleExists)
	require.Equal(t, core.Text(""), present.Nodes[0].Localized[0].Value)
}

func TestPostPortCompilesTargetFirstWriteAndSourceRequiredTitle(t *testing.T) {
	port := newPostPortForTest(t)
	postID, blockID, documentID, revision, contributor := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	identity := core.DocumentIdentity{Domain: core.DomainPost, Reference: core.DocumentReference(postID.String())}
	state := postdomain.AIDocumentState{
		PostID: postID.String(), ContentDocumentID: documentID.String(),
		DocumentRevision: revision.String(), SourceLocale: "en", RequestedLocale: "ko",
		LocalizedDocument: absentTargetParagraphDocument(blockID, "ko"),
	}
	target, err := port.project(identity, "ko", state)
	require.NoError(t, err)
	mutation, issues, err := port.compile(state, target, contributor, []core.Operation{
		core.SetFieldOperation(postMetadataBlockID, postTitleField, core.Text("")),
	})
	require.NoError(t, err)
	require.Empty(t, issues)
	require.True(t, mutation.Metadata.EnsureLocale)
	require.True(t, mutation.Metadata.SetTitle)
	require.NotNil(t, mutation.Metadata.Title)
	require.Empty(t, *mutation.Metadata.Title)
	require.Equal(t, []uuid.UUID{contributor}, mutation.Batch.ContributorMemberIDs)

	state.SourceLocale, state.RequestedLocale, state.LocaleExists = "en", "en", true
	title := "source"
	state.RequestedMetadata = &postdomain.AIDocumentLocaleMetadata{Title: &title}
	state.LocalizedDocument = localizedParagraphDocument(blockID, "source")
	source, err := port.project(identity, "en", state)
	require.NoError(t, err)
	_, issues, err = port.compile(state, source, contributor, []core.Operation{
		core.UnsetFieldOperation(postMetadataBlockID, postTitleField),
	})
	require.NoError(t, err)
	require.Len(t, issues, 1)
	require.Contains(t, issues[0].Message, "source title")
}

func TestPostPortAbsentTargetUnsetIsNoopWithoutCreatingTranslation(t *testing.T) {
	port := newPostPortForTest(t)
	postID, blockID, documentID, revision, contributor := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	identity := core.DocumentIdentity{Domain: core.DomainPost, Reference: core.DocumentReference(postID.String())}
	state := postdomain.AIDocumentState{
		PostID: postID.String(), ContentDocumentID: documentID.String(),
		DocumentRevision: revision.String(), SourceLocale: "en", RequestedLocale: "ko",
		LocalizedDocument: absentTargetParagraphDocument(blockID, "ko"),
	}
	target, err := port.project(identity, "ko", state)
	require.NoError(t, err)
	mutation, issues, err := port.compile(state, target, contributor, []core.Operation{
		core.UnsetFieldOperation(core.BlockID(blockID.String()), "content"),
	})
	require.NoError(t, err)
	require.Empty(t, issues)
	require.False(t, mutation.Metadata.EnsureLocale)
	require.Empty(t, mutation.Batch.LocaleGroups)
	require.False(t, mutation.DeleteTranslation)
}

func TestPostPortRejectsMetadataTopologyMutation(t *testing.T) {
	port := newPostPortForTest(t)
	postID, blockID, documentID, revision, contributor := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	identity := core.DocumentIdentity{Domain: core.DomainPost, Reference: core.DocumentReference(postID.String())}
	state := postdomain.AIDocumentState{
		PostID: postID.String(), ContentDocumentID: documentID.String(),
		DocumentRevision: revision.String(), SourceLocale: "en", RequestedLocale: "en", LocaleExists: true,
		LocalizedDocument: localizedParagraphDocument(blockID, "source"),
	}
	title := "source"
	state.RequestedMetadata = &postdomain.AIDocumentLocaleMetadata{Title: &title}
	loaded, err := port.project(identity, "en", state)
	require.NoError(t, err)
	_, issues, err := port.compile(state, loaded, contributor, []core.Operation{
		core.DeleteBlockOperation(postMetadataBlockID),
	})
	require.NoError(t, err)
	require.Len(t, issues, 1)
	require.Contains(t, issues[0].Message, "fixed")
}

func TestPostCatalogKeepsTaxonomySourceOwned(t *testing.T) {
	port := newPostPortForTest(t)
	for _, field := range []core.FieldID{postCategoryIDsField, postTagIDsField} {
		rule, ok := findCatalogField(port.catalog, postMetadataBlockKind, field)
		require.True(t, ok)
		require.Equal(t, core.FieldOwnershipSource, rule.Ownership)
		require.NotNil(t, rule.Schema)
		require.Equal(t, core.FieldOwnershipSource, rule.Schema.Ownership)
		require.NotNil(t, rule.Schema.Item)
		require.Equal(t, core.FieldOwnershipSource, rule.Schema.Item.Ownership)
	}
}

func TestPostExactMutationPathDoesNotEnterPublicLoad(t *testing.T) {
	port := newPostPortForTest(t)
	postID, blockID, documentID, revision, nextRevision, contributor :=
		uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	title := "source"
	api := &exactPostDocumentAPI{
		state: postdomain.AIDocumentState{
			PostID: postID.String(), ContentDocumentID: documentID.String(),
			DocumentRevision: revision.String(), SourceLocale: "en", RequestedLocale: "en", LocaleExists: true,
			RequestedMetadata: &postdomain.AIDocumentLocaleMetadata{Title: &title},
			LocalizedDocument: localizedParagraphDocument(blockID, "source"), ViewerMemberID: contributor.String(),
		},
		result: postdomain.AIDocumentMutationResult{
			Result: contentblock.Result{DocumentRevision: nextRevision, Changed: true},
		},
	}
	port.service = api
	service, err := core.NewService(port)
	require.NoError(t, err)
	request := core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainPost,
		Document: core.DocumentReference(postID.String()), Locale: "en",
		ExpectedDocumentRevision: core.Revision(revision.String()),
		Operations: []core.Operation{
			core.SetFieldOperation(core.BlockID(blockID.String()), "content", core.RichText(core.InlineText("changed"))),
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

func TestPostExactTargetMutationCarriesDocumentAndTargetCAS(t *testing.T) {
	port := newPostPortForTest(t)
	postID, blockID, documentID, revision, contributor := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	targetRevision, nextTargetRevision := "target-revision-1", "target-revision-2"
	document := localizedParagraphDocument(blockID, "translated")
	document.Locale = "ko"
	document.LocaleOverlay.Locale = "ko"
	api := &exactPostDocumentAPI{
		state: postdomain.AIDocumentState{
			PostID: postID.String(), ContentDocumentID: documentID.String(),
			DocumentRevision: revision.String(), TargetRevision: &targetRevision,
			SourceLocale: "en", RequestedLocale: "ko", LocaleExists: true,
			LocalizedDocument: document, ViewerMemberID: contributor.String(),
		},
		result: postdomain.AIDocumentMutationResult{
			Result:         contentblock.Result{DocumentRevision: revision, Changed: true},
			TargetRevision: &nextTargetRevision,
		},
	}
	port.service = api
	service, err := core.NewService(port)
	require.NoError(t, err)
	expectedTarget := core.Revision(targetRevision)
	request := core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainPost,
		Document: core.DocumentReference(postID.String()), Locale: "ko",
		ExpectedDocumentRevision: core.Revision(revision.String()),
		ExpectedTargetRevision:   &expectedTarget,
		Operations: []core.Operation{
			core.SetFieldOperation(core.BlockID(blockID.String()), "content", core.RichText(core.InlineText("changed"))),
		},
	}

	validation, err := service.Validate(context.Background(), request)
	require.NoError(t, err)
	require.True(t, validation.Valid())
	result, err := service.Apply(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, core.Revision(revision.String()), result.DocumentRevision)
	require.NotNil(t, result.TargetRevision)
	require.Equal(t, core.Revision(nextTargetRevision), *result.TargetRevision)
	require.NotNil(t, api.mutation.ExpectedTargetRevision)
	require.Equal(t, targetRevision, *api.mutation.ExpectedTargetRevision)
}

func TestPostExactTargetConflictMapsBothCurrentCASValues(t *testing.T) {
	port := newPostPortForTest(t)
	postID, documentRevision := uuid.New(), uuid.New()
	currentTargetRevision := "target-revision-current"
	api := &exactPostDocumentAPI{
		state: postdomain.AIDocumentState{PostID: postID.String(), RequestedLocale: "ko"},
		authorizeErr: &postdomain.AIDocumentRevisionConflictError{
			Kind:                    postdomain.AIDocumentTargetRevisionConflict,
			CurrentDocumentRevision: documentRevision.String(), CurrentTargetRevision: &currentTargetRevision,
		},
	}
	port.service = api
	service, err := core.NewService(port)
	require.NoError(t, err)
	expectedTarget := core.Revision("target-revision-stale")
	_, err = service.Validate(context.Background(), core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainPost,
		Document: core.DocumentReference(postID.String()), Locale: "ko",
		ExpectedDocumentRevision: core.Revision(documentRevision.String()),
		ExpectedTargetRevision:   &expectedTarget,
		Operations:               []core.Operation{core.DeleteBlockOperation("unknown-block")},
	})
	var conflict *core.ConflictError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, core.ConflictTargetRevision, conflict.Conflict.Code)
	require.Equal(t, core.Revision(documentRevision.String()), conflict.Conflict.CurrentDocumentRevision)
	require.NotNil(t, conflict.Conflict.CurrentTargetRevision)
	require.Equal(t, core.Revision(currentTargetRevision), *conflict.Conflict.CurrentTargetRevision)
}

func TestPostExactMutationAuthorizesBeforeExposingValidationResult(t *testing.T) {
	port := newPostPortForTest(t)
	postID := uuid.New()
	denied := errors.New("post not found")
	api := &exactPostDocumentAPI{
		state:        postdomain.AIDocumentState{PostID: postID.String(), RequestedLocale: "en"},
		authorizeErr: denied,
	}
	port.service = api
	service, err := core.NewService(port)
	require.NoError(t, err)
	request := core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainPost,
		Document: core.DocumentReference(postID.String()), Locale: "en",
		ExpectedDocumentRevision: core.Revision(uuid.NewString()),
		Operations: []core.Operation{
			core.DeleteBlockOperation("unknown-block"),
		},
	}

	_, err = service.Validate(context.Background(), request)
	require.ErrorIs(t, err, denied)
	require.Equal(t, 1, api.executeCalls)
	require.Zero(t, api.compilerCalls, "unauthorized request reached the adapter compiler")
	require.Zero(t, api.loadCalls, "unauthorized request entered the public read path")
}

func newPostPortForTest(t *testing.T) *postPort {
	t.Helper()
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST)
	require.NoError(t, err)
	return &postPort{codec: codec, catalog: postCatalog(codec)}
}

func absentTargetParagraphDocument(blockID uuid.UUID, locale string) *contentv1.LocalizedRichTextDocument {
	document := localizedParagraphDocument(blockID, "source")
	document.Locale = locale
	document.LocaleOverlay = &contentv1.RichTextLocaleOverlay{Locale: locale}
	return document
}
