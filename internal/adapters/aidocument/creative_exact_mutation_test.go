package aidocumentadapter

import (
	"context"
	"errors"
	"testing"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	artistdomain "github.com/echovisionlab/geul-api/internal/artist"
	releasedomain "github.com/echovisionlab/geul-api/internal/release"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type exactReleaseDocumentAPI struct {
	state         releasedomain.AIDocumentState
	result        releasedomain.AIDocumentMutationResult
	authorizeErr  error
	mutation      releasedomain.AIDocumentMutation
	loadCalls     int
	executeCalls  int
	compilerCalls int
}

func (a *exactReleaseDocumentAPI) LoadAIDocumentState(
	context.Context,
	string,
	string,
) (releasedomain.AIDocumentState, error) {
	a.loadCalls++
	return a.state, nil
}

func (a *exactReleaseDocumentAPI) ExecuteAIDocumentMutation(
	_ context.Context,
	releaseID string,
	locale string,
	_ releasedomain.AIDocumentExecutionMode,
	compiler releasedomain.AIDocumentMutationCompiler,
) (releasedomain.AIDocumentMutationResult, error) {
	a.executeCalls++
	if a.authorizeErr != nil {
		return releasedomain.AIDocumentMutationResult{}, a.authorizeErr
	}
	if releaseID != a.state.ReleaseID || locale != a.state.Locale {
		return releasedomain.AIDocumentMutationResult{}, errors.New("unexpected Release identity or locale")
	}
	a.compilerCalls++
	mutation, err := compiler(a.state)
	if err != nil {
		return releasedomain.AIDocumentMutationResult{}, err
	}
	a.mutation = mutation
	return a.result, nil
}

type exactArtistDocumentAPI struct {
	state         artistdomain.AIDocumentState
	result        artistdomain.AIDocumentMutationResult
	authorizeErr  error
	mutation      artistdomain.AIDocumentMutation
	loadCalls     int
	executeCalls  int
	compilerCalls int
}

func (a *exactArtistDocumentAPI) LoadAIDocumentState(
	context.Context,
	string,
	string,
) (artistdomain.AIDocumentState, error) {
	a.loadCalls++
	return a.state, nil
}

func (a *exactArtistDocumentAPI) ExecuteAIDocumentMutation(
	_ context.Context,
	artistID string,
	locale string,
	_ artistdomain.AIDocumentExecutionMode,
	compiler artistdomain.AIDocumentMutationCompiler,
) (artistdomain.AIDocumentMutationResult, error) {
	a.executeCalls++
	if a.authorizeErr != nil {
		return artistdomain.AIDocumentMutationResult{}, a.authorizeErr
	}
	if artistID != a.state.ArtistID || locale != a.state.Locale {
		return artistdomain.AIDocumentMutationResult{}, errors.New("unexpected Artist identity or locale")
	}
	a.compilerCalls++
	mutation, err := compiler(a.state)
	if err != nil {
		return artistdomain.AIDocumentMutationResult{}, err
	}
	a.mutation = mutation
	return a.result, nil
}

func TestReleaseExactMutationUsesOnlyLockedOwningDomainBoundary(t *testing.T) {
	releaseID, documentID, blockID := uuid.New(), uuid.New(), uuid.New()
	revision, nextRevision, contributor := uuid.New(), uuid.New(), uuid.New()
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT)
	require.NoError(t, err)
	api := &exactReleaseDocumentAPI{
		state: releasedomain.AIDocumentState{
			ReleaseID: releaseID.String(), DocumentID: documentID, DocumentRevision: revision.String(),
			SourceLocale: "en", Locale: "en", LocaleExists: true,
			Document:          exactCompactDocument(blockID, "en"),
			RequestedMetadata: &releasedomain.AIDocumentLocaleMetadata{}, ViewerMemberID: contributor.String(),
		},
		result: releasedomain.AIDocumentMutationResult{DocumentRevision: nextRevision.String(), Changed: true},
	}
	port := &releasePort{service: api, codec: codec, catalog: releaseCatalog(codec)}
	service, err := core.NewService(port)
	require.NoError(t, err)
	request := exactCreativeRequest(core.DomainRelease, releaseID, blockID, revision)

	validation, err := service.Validate(t.Context(), request)
	require.NoError(t, err)
	require.True(t, validation.Valid())
	result, err := service.Apply(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, core.Revision(nextRevision.String()), result.DocumentRevision)
	require.Equal(t, 2, api.executeCalls)
	require.Equal(t, 2, api.compilerCalls)
	require.Zero(t, api.loadCalls)
	require.Equal(t, contributor, api.mutation.ContributorMemberID)
}

func TestArtistExactMutationUsesOnlyLockedOwningDomainBoundary(t *testing.T) {
	artistID, documentID, blockID := uuid.New(), uuid.New(), uuid.New()
	revision, nextRevision, contributor := uuid.New(), uuid.New(), uuid.New()
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT)
	require.NoError(t, err)
	api := &exactArtistDocumentAPI{
		state: artistdomain.AIDocumentState{
			ArtistID: artistID.String(), DocumentID: documentID, Revision: revision.String(),
			SourceLocale: "en", Locale: "en", LocaleExists: true,
			Document: exactCompactDocument(blockID, "en"), ViewerMemberID: contributor.String(),
		},
		result: artistdomain.AIDocumentMutationResult{Revision: nextRevision.String(), Changed: true},
	}
	port := &artistPort{service: api, codec: codec}
	service, err := core.NewService(port)
	require.NoError(t, err)
	request := exactCreativeRequest(core.DomainArtist, artistID, blockID, revision)

	validation, err := service.Validate(t.Context(), request)
	require.NoError(t, err)
	require.True(t, validation.Valid())
	result, err := service.Apply(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, core.Revision(nextRevision.String()), result.DocumentRevision)
	require.Nil(t, result.TargetRevision)
	require.Equal(t, 2, api.executeCalls)
	require.Equal(t, 2, api.compilerCalls)
	require.Zero(t, api.loadCalls)
	require.Equal(t, contributor, api.mutation.ContributorMemberID)
}

func TestCreativeExactMutationDeniesBeforeAdapterCompiler(t *testing.T) {
	t.Run("release", func(t *testing.T) {
		entityID, blockID, revision := uuid.New(), uuid.New(), uuid.New()
		denied := errors.New("release not found")
		api := &exactReleaseDocumentAPI{authorizeErr: denied}
		codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT)
		require.NoError(t, err)
		service, err := core.NewService(&releasePort{service: api, codec: codec, catalog: releaseCatalog(codec)})
		require.NoError(t, err)

		_, err = service.Validate(t.Context(), exactCreativeRequest(core.DomainRelease, entityID, blockID, revision))
		require.ErrorIs(t, err, denied)
		require.Equal(t, 1, api.executeCalls)
		require.Zero(t, api.compilerCalls)
		require.Zero(t, api.loadCalls)
	})

	t.Run("artist", func(t *testing.T) {
		entityID, blockID, revision := uuid.New(), uuid.New(), uuid.New()
		denied := errors.New("artist not found")
		api := &exactArtistDocumentAPI{authorizeErr: denied}
		codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT)
		require.NoError(t, err)
		service, err := core.NewService(&artistPort{service: api, codec: codec})
		require.NoError(t, err)

		_, err = service.Validate(t.Context(), exactCreativeRequest(core.DomainArtist, entityID, blockID, revision))
		require.ErrorIs(t, err, denied)
		require.Equal(t, 1, api.executeCalls)
		require.Zero(t, api.compilerCalls)
		require.Zero(t, api.loadCalls)
	})
}

func exactCreativeRequest(
	domain core.Domain,
	entityID uuid.UUID,
	blockID uuid.UUID,
	revision uuid.UUID,
) core.ApplyRequest {
	return core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: domain,
		Document: core.DocumentReference(entityID.String()), Locale: "en",
		ExpectedDocumentRevision: core.Revision(revision.String()),
		Operations: []core.Operation{
			core.SetFieldOperation(core.BlockID(blockID.String()), richTextContentField, core.RichText(core.InlineText("changed"))),
		},
	}
}

func exactCompactDocument(blockID uuid.UUID, locale string) *contentv1.LocalizedRichTextDocument {
	return &contentv1.LocalizedRichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT,
		Locale:                  locale,
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
			Locale: locale,
			Blocks: []*contentv1.RichTextBlockLocale{{
				BlockId: blockID.String(),
				Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
					Props: &contentv1.ParagraphLocaleProps{}, Content: []*contentv1.RichTextInline{},
				}},
			}},
		},
	}
}

var _ releaseDocumentAPI = (*exactReleaseDocumentAPI)(nil)
var _ artistDocumentAPI = (*exactArtistDocumentAPI)(nil)
