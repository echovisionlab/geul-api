//go:build integration

package release_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestReleaseReferenceSettersReplaceAndClearMappingsIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, adminID, "Release reference admin")
	ctx := releaseIntegrationAdminCtx(adminID)
	releaseService := newReleaseIntegrationService(t, db, adminID, &recordingArtistFileDeleter{})
	spiceDB := integrationSpiceDB(t)

	releaseResponse, err := releaseService.CreateRelease(ctx, connect.NewRequest(&managev1.CreateReleaseRequest{
		Title: "Release Reference Set Integration",
		Type:  managev1.ReleaseType_RELEASE_TYPE_ALBUM,
	}))
	require.NoError(t, err)
	require.NotNil(t, releaseResponse.Msg.Document)
	require.NotEmpty(t, releaseResponse.Msg.Revision)
	releaseID := releaseResponse.Msg.Id

	categorySlug := "release-reference-category-" + referenceShortSuffix()
	category, err := newReferenceCategoryService(db, spiceDB).CreateCategory(ctx, connect.NewRequest(&managev1.CreateCategoryRequest{
		Name: "Release Reference Category", Slug: &categorySlug,
	}))
	require.NoError(t, err)
	genre, err := newReferenceGenreService(db, spiceDB).CreateGenre(ctx, connect.NewRequest(&managev1.CreateGenreRequest{
		Name: "Release Reference Genre " + referenceShortSuffix(),
	}))
	require.NoError(t, err)
	style, err := newReferenceStyleService(db, spiceDB).CreateStyle(ctx, connect.NewRequest(&managev1.CreateStyleRequest{
		Name: "Release Reference Style " + referenceShortSuffix(),
	}))
	require.NoError(t, err)

	_, err = releaseService.SetReleaseCategories(ctx, connect.NewRequest(&managev1.SetReleaseCategoriesRequest{
		ReleaseId: releaseID, CategoryIds: []string{category.Msg.Id},
	}))
	require.NoError(t, err)
	_, err = releaseService.SetReleaseGenres(ctx, connect.NewRequest(&managev1.SetReleaseGenresRequest{
		ReleaseId: releaseID, GenreIds: []string{genre.Msg.Id},
	}))
	require.NoError(t, err)
	_, err = releaseService.SetReleaseStyles(ctx, connect.NewRequest(&managev1.SetReleaseStylesRequest{
		ReleaseId: releaseID, StyleIds: []string{style.Msg.Id},
	}))
	require.NoError(t, err)

	requireRelationWhereCount(t, db, "release_category", "release_id = ? AND category_id = ?", 1, releaseID, category.Msg.Id)
	requireRelationWhereCount(t, db, "release_genre", "release_id = ? AND genre_id = ?", 1, releaseID, genre.Msg.Id)
	requireRelationWhereCount(t, db, "release_style", "release_id = ? AND style_id = ?", 1, releaseID, style.Msg.Id)

	_, err = releaseService.SetReleaseCategories(ctx, connect.NewRequest(&managev1.SetReleaseCategoriesRequest{ReleaseId: releaseID}))
	require.NoError(t, err)
	_, err = releaseService.SetReleaseGenres(ctx, connect.NewRequest(&managev1.SetReleaseGenresRequest{ReleaseId: releaseID}))
	require.NoError(t, err)
	_, err = releaseService.SetReleaseStyles(ctx, connect.NewRequest(&managev1.SetReleaseStylesRequest{ReleaseId: releaseID}))
	require.NoError(t, err)

	requireNoMapping(t, db, "release_category", "release_id", releaseID)
	requireNoMapping(t, db, "release_genre", "release_id", releaseID)
	requireNoMapping(t, db, "release_style", "release_id", releaseID)
}
