//go:build integration

package release_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestReleaseRelationSettersRequireObservedSnapshotIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, adminID, "Observed snapshot admin")
	ctx := releaseIntegrationAdminCtx(adminID)
	releaseService := newReleaseIntegrationService(t, db, adminID, &recordingArtistFileDeleter{})
	releaseResponse, err := releaseService.CreateRelease(ctx, connect.NewRequest(&managev1.CreateReleaseRequest{
		Title: "Missing Observed Snapshot Integration",
		Type:  managev1.ReleaseType_RELEASE_TYPE_ALBUM,
	}))
	require.NoError(t, err)
	releaseID := releaseResponse.Msg.Id

	tests := []struct {
		name string
		call func() error
	}{
		{"artists", func() error {
			_, err := releaseService.SetReleaseArtists(ctx, connect.NewRequest(&managev1.SetReleaseArtistsRequest{ReleaseId: releaseID}))
			return err
		}},
		{"labels", func() error {
			_, err := releaseService.SetReleaseLabels(ctx, connect.NewRequest(&managev1.SetReleaseLabelsRequest{ReleaseId: releaseID}))
			return err
		}},
		{"categories", func() error {
			_, err := releaseService.SetReleaseCategories(ctx, connect.NewRequest(&managev1.SetReleaseCategoriesRequest{ReleaseId: releaseID}))
			return err
		}},
		{"genres", func() error {
			_, err := releaseService.SetReleaseGenres(ctx, connect.NewRequest(&managev1.SetReleaseGenresRequest{ReleaseId: releaseID}))
			return err
		}},
		{"styles", func() error {
			_, err := releaseService.SetReleaseStyles(ctx, connect.NewRequest(&managev1.SetReleaseStylesRequest{ReleaseId: releaseID}))
			return err
		}},
		{"formats", func() error {
			_, err := releaseService.SetReleaseFormats(ctx, connect.NewRequest(&managev1.SetReleaseFormatsRequest{ReleaseId: releaseID}))
			return err
		}},
		{"credits", func() error {
			_, err := releaseService.SetReleaseCredits(ctx, connect.NewRequest(&managev1.SetReleaseCreditsRequest{ReleaseId: releaseID}))
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(test.call()))
		})
	}
}

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
		ReleaseId: releaseID, CategoryIds: []string{category.Msg.Id}, Observed: &managev1.StringIdSnapshot{},
	}))
	require.NoError(t, err)
	_, err = releaseService.SetReleaseGenres(ctx, connect.NewRequest(&managev1.SetReleaseGenresRequest{
		ReleaseId: releaseID, GenreIds: []string{genre.Msg.Id}, Observed: &managev1.StringIdSnapshot{},
	}))
	require.NoError(t, err)
	_, err = releaseService.SetReleaseStyles(ctx, connect.NewRequest(&managev1.SetReleaseStylesRequest{
		ReleaseId: releaseID, StyleIds: []string{style.Msg.Id}, Observed: &managev1.StringIdSnapshot{},
	}))
	require.NoError(t, err)

	requireRelationWhereCount(t, db, "release_category", "release_id = ? AND category_id = ?", 1, releaseID, category.Msg.Id)
	requireRelationWhereCount(t, db, "release_genre", "release_id = ? AND genre_id = ?", 1, releaseID, genre.Msg.Id)
	requireRelationWhereCount(t, db, "release_style", "release_id = ? AND style_id = ?", 1, releaseID, style.Msg.Id)

	_, err = releaseService.SetReleaseCategories(ctx, connect.NewRequest(&managev1.SetReleaseCategoriesRequest{
		ReleaseId: releaseID, Observed: &managev1.StringIdSnapshot{Ids: []string{category.Msg.Id}},
	}))
	require.NoError(t, err)
	_, err = releaseService.SetReleaseGenres(ctx, connect.NewRequest(&managev1.SetReleaseGenresRequest{
		ReleaseId: releaseID, Observed: &managev1.StringIdSnapshot{Ids: []string{genre.Msg.Id}},
	}))
	require.NoError(t, err)
	_, err = releaseService.SetReleaseStyles(ctx, connect.NewRequest(&managev1.SetReleaseStylesRequest{
		ReleaseId: releaseID, Observed: &managev1.StringIdSnapshot{Ids: []string{style.Msg.Id}},
	}))
	require.NoError(t, err)

	requireNoMapping(t, db, "release_category", "release_id", releaseID)
	requireNoMapping(t, db, "release_genre", "release_id", releaseID)
	requireNoMapping(t, db, "release_style", "release_id", releaseID)
}

func TestReleaseCategoryObservedSnapshotPreservesConcurrentAdditionIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, adminID, "Release observed category admin")
	ctx := releaseIntegrationAdminCtx(adminID)
	releaseService := newReleaseIntegrationService(t, db, adminID, &recordingArtistFileDeleter{})
	spiceDB := integrationSpiceDB(t)

	releaseResponse, err := releaseService.CreateRelease(ctx, connect.NewRequest(&managev1.CreateReleaseRequest{
		Title: "Observed Category Merge Integration",
		Type:  managev1.ReleaseType_RELEASE_TYPE_ALBUM,
	}))
	require.NoError(t, err)

	categoryService := newReferenceCategoryService(db, spiceDB)
	firstSlug := "observed-category-first-" + referenceShortSuffix()
	first, err := categoryService.CreateCategory(ctx, connect.NewRequest(&managev1.CreateCategoryRequest{
		Name: "Observed Category First", Slug: &firstSlug,
	}))
	require.NoError(t, err)
	peerSlug := "observed-category-peer-" + referenceShortSuffix()
	peer, err := categoryService.CreateCategory(ctx, connect.NewRequest(&managev1.CreateCategoryRequest{
		Name: "Observed Category Peer", Slug: &peerSlug,
	}))
	require.NoError(t, err)

	_, err = releaseService.SetReleaseCategories(ctx, connect.NewRequest(&managev1.SetReleaseCategoriesRequest{
		ReleaseId:   releaseResponse.Msg.Id,
		CategoryIds: []string{first.Msg.Id},
		Observed:    &managev1.StringIdSnapshot{},
	}))
	require.NoError(t, err)

	// A tab that observed no categories adds its selection without removing the first tab's row.
	_, err = releaseService.SetReleaseCategories(ctx, connect.NewRequest(&managev1.SetReleaseCategoriesRequest{
		ReleaseId:   releaseResponse.Msg.Id,
		CategoryIds: []string{peer.Msg.Id},
		Observed:    &managev1.StringIdSnapshot{},
	}))
	require.NoError(t, err)

	// The first tab's stale snapshot removes only the row it observed and omitted.
	_, err = releaseService.SetReleaseCategories(ctx, connect.NewRequest(&managev1.SetReleaseCategoriesRequest{
		ReleaseId: releaseResponse.Msg.Id,
		Observed:  &managev1.StringIdSnapshot{Ids: []string{first.Msg.Id}},
	}))
	require.NoError(t, err)

	requireRelationWhereCount(t, db, "release_category", "release_id = ? AND category_id = ?", 0, releaseResponse.Msg.Id, first.Msg.Id)
	requireRelationWhereCount(t, db, "release_category", "release_id = ? AND category_id = ?", 1, releaseResponse.Msg.Id, peer.Msg.Id)
}

func TestGetReleaseRelationsReturnsAuthorizedEditorSnapshotIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, adminID, "Release relations reader")
	ctx := releaseIntegrationAdminCtx(adminID)
	releaseService := newReleaseIntegrationService(t, db, adminID, &recordingArtistFileDeleter{})
	spiceDB := integrationSpiceDB(t)

	releaseResponse, err := releaseService.CreateRelease(ctx, connect.NewRequest(&managev1.CreateReleaseRequest{
		Title: "Release Relations Read Integration",
		Type:  managev1.ReleaseType_RELEASE_TYPE_ALBUM,
	}))
	require.NoError(t, err)

	categorySlug := "release-relations-category-" + referenceShortSuffix()
	category, err := newReferenceCategoryService(db, spiceDB).CreateCategory(ctx, connect.NewRequest(&managev1.CreateCategoryRequest{
		Name: "Release Relations Category", Slug: &categorySlug,
	}))
	require.NoError(t, err)
	genre, err := newReferenceGenreService(db, spiceDB).CreateGenre(ctx, connect.NewRequest(&managev1.CreateGenreRequest{
		Name: "Release Relations Genre " + referenceShortSuffix(),
	}))
	require.NoError(t, err)
	style, err := newReferenceStyleService(db, spiceDB).CreateStyle(ctx, connect.NewRequest(&managev1.CreateStyleRequest{
		Name: "Release Relations Style " + referenceShortSuffix(),
	}))
	require.NoError(t, err)
	format, err := referencecatalog.NewFormatService(db, spiceDB).CreateFormat(ctx, connect.NewRequest(&managev1.CreateFormatRequest{
		Name: "Release Relations Format " + referenceShortSuffix(),
	}))
	require.NoError(t, err)

	_, err = releaseService.SetReleaseCategories(ctx, connect.NewRequest(&managev1.SetReleaseCategoriesRequest{
		ReleaseId: releaseResponse.Msg.Id, CategoryIds: []string{category.Msg.Id}, Observed: &managev1.StringIdSnapshot{},
	}))
	require.NoError(t, err)
	_, err = releaseService.SetReleaseGenres(ctx, connect.NewRequest(&managev1.SetReleaseGenresRequest{
		ReleaseId: releaseResponse.Msg.Id, GenreIds: []string{genre.Msg.Id}, Observed: &managev1.StringIdSnapshot{},
	}))
	require.NoError(t, err)
	_, err = releaseService.SetReleaseStyles(ctx, connect.NewRequest(&managev1.SetReleaseStylesRequest{
		ReleaseId: releaseResponse.Msg.Id, StyleIds: []string{style.Msg.Id}, Observed: &managev1.StringIdSnapshot{},
	}))
	require.NoError(t, err)
	_, err = releaseService.SetReleaseFormats(ctx, connect.NewRequest(&managev1.SetReleaseFormatsRequest{
		ReleaseId: releaseResponse.Msg.Id,
		Formats:   []*managev1.ReleaseFormatInput{{FormatId: format.Msg.Id, FormatDescription: stringPtr("Gatefold")}},
		Observed:  &managev1.ReleaseFormatsSnapshot{},
	}))
	require.NoError(t, err)
	_, err = releaseService.SetReleaseCredits(ctx, connect.NewRequest(&managev1.SetReleaseCreditsRequest{
		ReleaseId: releaseResponse.Msg.Id,
		Credits:   []*managev1.ReleaseCreditInput{{CreditedName: stringPtr("Sleeve notes"), CreditRole: stringPtr("Writer"), SortOrder: 0}},
		Observed:  &managev1.ReleaseCreditsSnapshot{},
	}))
	require.NoError(t, err)

	response, err := releaseService.GetReleaseRelations(ctx, connect.NewRequest(&managev1.GetReleaseRelationsRequest{
		ReleaseId: releaseResponse.Msg.Id,
	}))
	require.NoError(t, err)
	require.Equal(t, releaseResponse.Msg.Id, response.Msg.ReleaseId)
	require.Equal(t, category.Msg.Id, response.Msg.Categories[0].Id)
	require.Equal(t, genre.Msg.Id, response.Msg.Genres[0].Id)
	require.Equal(t, style.Msg.Id, response.Msg.Styles[0].Id)
	require.Equal(t, format.Msg.Id, response.Msg.Formats[0].Id)
	require.Equal(t, "Gatefold", response.Msg.Formats[0].GetFormatDescription())
	require.NotEmpty(t, response.Msg.Credits[0].GetId())
	require.Equal(t, "Sleeve notes", response.Msg.Credits[0].GetCreditedName())
	require.Equal(t, "Writer", response.Msg.Credits[0].GetCreditRole())

	grantIntegrationGlobalRole(t, spiceDB, adminID, policyv1.Role.User())
	_, err = releaseService.GetReleaseRelations(ctx, connect.NewRequest(&managev1.GetReleaseRelationsRequest{
		ReleaseId: releaseResponse.Msg.Id,
	}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}
