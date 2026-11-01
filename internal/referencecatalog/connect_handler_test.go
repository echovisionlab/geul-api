package referencecatalog_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func requireMessage(t *testing.T, want, got proto.Message) {
	t.Helper()
	require.True(t, proto.Equal(want, got), "want %v\ngot  %v", want, got)
}

func TestReferenceHandlersRenderEachKindsColumns(t *testing.T) {
	f, p := newFixture(t, nil, nil), newPrincipal(t)
	description := "described"
	category := f.store.SeedReference(referencecatalog.KindCategory, referencecatalog.Reference{Name: "Category", Slug: "category", Description: &description})
	f.store.SeedReferenceUse(referencecatalog.KindCategory, category.ID, 2)
	tag := f.store.SeedReference(referencecatalog.KindTag, referencecatalog.Reference{Name: "Tag", Slug: "tag"})
	format := f.store.SeedReference(referencecatalog.KindFormat, referencecatalog.Reference{Name: "Vinyl", Slug: "vinyl"})
	genre := f.store.SeedReference(referencecatalog.KindGenre, referencecatalog.Reference{Name: "Genre", Slug: "genre", Description: &description})

	categories, err := referencecatalog.NewCategoryConnectHandler(f.service).ListCategoriesAdmin(p.ctx, connect.NewRequest(&managev1.ListCategoriesAdminRequest{}))
	require.NoError(t, err)
	requireMessage(t, &managev1.ListCategoriesAdminResponse{
		Categories: []*managev1.CategoryWithStats{{Category: &managev1.Category{
			Id: category.ID, Name: "Category", Slug: new("category"), Description: &description,
			CreatedAt: timestamppb.New(category.CreatedAt), UpdatedAt: timestamppb.New(*category.UpdatedAt),
		}, PostCount: 2}},
		Pagination: &commonv1.PaginationResponse{Total: 1, Limit: 100},
	}, categories.Msg)

	tags, err := referencecatalog.NewTagConnectHandler(f.service).ListTags(p.ctx, connect.NewRequest(&managev1.ListTagsRequest{Pagination: &commonv1.PaginationRequest{Limit: 5}}))
	require.NoError(t, err)
	requireMessage(t, &managev1.ListTagsResponse{
		Tags:       []*managev1.Tag{{Id: tag.ID, Name: "Tag", Slug: new("tag"), CreatedAt: timestamppb.New(tag.CreatedAt)}},
		Pagination: &commonv1.PaginationResponse{Total: 1, Limit: 5},
	}, tags.Msg)

	formats, err := referencecatalog.NewFormatConnectHandler(f.service).ListFormatsAdmin(p.ctx, connect.NewRequest(&managev1.ListFormatsAdminRequest{}))
	require.NoError(t, err)
	requireMessage(t, &managev1.ListFormatsAdminResponse{
		Formats:    []*managev1.FormatWithStats{{Format: &managev1.Format{Id: format.ID, Name: "Vinyl", Slug: "vinyl"}}},
		Pagination: &commonv1.PaginationResponse{Total: 1, Limit: 100},
	}, formats.Msg)

	name := "Renamed"
	renamed, err := referencecatalog.NewCategoryConnectHandler(f.service).UpdateCategory(p.ctx, connect.NewRequest(&managev1.UpdateCategoryRequest{Id: category.ID, Name: &name}))
	require.NoError(t, err)
	stored, _ := f.store.StoredReference(referencecatalog.KindCategory, category.ID)
	requireMessage(t, &managev1.Category{
		Id: category.ID, Name: name, Slug: new("category"), Description: &description,
		CreatedAt: timestamppb.New(category.CreatedAt), UpdatedAt: timestamppb.New(*stored.UpdatedAt),
	}, renamed.Msg)

	created, err := referencecatalog.NewGenreConnectHandler(f.service).CreateGenre(p.ctx, connect.NewRequest(&managev1.CreateGenreRequest{Name: "Created", Description: &description}))
	require.NoError(t, err)
	requireMessage(t, &managev1.Genre{
		Id: created.Msg.Id, Name: "Created", Slug: "created", Description: &description, CreatedAt: created.Msg.CreatedAt,
	}, created.Msg)
	require.NotEqual(t, genre.ID, created.Msg.Id)

	deleted, err := referencecatalog.NewGenreConnectHandler(f.service).DeleteGenre(p.ctx, connect.NewRequest(&managev1.DeleteGenreRequest{Id: genre.ID}))
	require.NoError(t, err)
	require.True(t, deleted.Msg.Success)

	_, err = referencecatalog.NewGenreConnectHandler(f.service).DeleteGenre(p.ctx, connect.NewRequest(&managev1.DeleteGenreRequest{Id: genre.ID}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err), "the handler answers the Connect code")
}

func TestClientHandlerRendersLogosAndMapsThemeVariants(t *testing.T) {
	f, p := newFixture(t, nil, nil), newPrincipal(t)
	handler := referencecatalog.NewClientConnectHandler(f.service)
	website := "https://client.example.test"
	client := f.store.SeedClient(referencecatalog.Client{Name: "Client", Website: &website})
	f.store.SeedClientWork(client.ID)
	lightID, lightAsset := f.store.SeedReadyFile("logo")
	darkID, darkAsset := f.store.SeedReadyFile("logo")

	light, err := handler.SetClientLogo(p.ctx, connect.NewRequest(&managev1.SetClientLogoRequest{
		ClientId: client.ID, FileId: lightID, Variant: managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_UNSPECIFIED,
	}))
	require.NoError(t, err)
	requireMessage(t, &managev1.SetClientLogoResponse{LogoLightAsset: lightAsset}, light.Msg)
	dark, err := handler.SetClientLogo(p.ctx, connect.NewRequest(&managev1.SetClientLogoRequest{
		ClientId: client.ID, FileId: darkID, Variant: managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_DARK,
	}))
	require.NoError(t, err)
	requireMessage(t, &managev1.SetClientLogoResponse{LogoDarkAsset: darkAsset}, dark.Msg)
	_, err = handler.SetClientLogo(p.ctx, connect.NewRequest(&managev1.SetClientLogoRequest{ClientId: client.ID, FileId: darkID, Variant: 99}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	listed, err := handler.ListClientsAdmin(p.ctx, connect.NewRequest(&managev1.ListClientsAdminRequest{}))
	require.NoError(t, err)
	requireMessage(t, &managev1.ListClientsAdminResponse{
		Clients: []*managev1.ClientWithStats{{Client: &managev1.Client{
			Id: client.ID, Name: "Client", Website: &website, CreatedAt: timestamppb.New(client.CreatedAt),
			LogoLightAsset: lightAsset, LogoDarkAsset: darkAsset,
		}, WorkCount: 1}},
		Pagination: &commonv1.PaginationResponse{Total: 1, Limit: 20},
	}, listed.Msg)

	created, err := handler.CreateClient(p.ctx, connect.NewRequest(&managev1.CreateClientRequest{Name: "Bare"}))
	require.NoError(t, err)
	requireMessage(t, &managev1.Client{Id: created.Msg.Id, Name: "Bare", CreatedAt: created.Msg.CreatedAt}, created.Msg)
}

func TestMapPlaceHandlerRendersSetFieldsAndMemberSummaries(t *testing.T) {
	f, p := newFixture(t, nil, nil), newPrincipal(t)
	handler := referencecatalog.NewMapPlaceConnectHandler(f.service)
	f.store.SeedMemberWithID(p.memberID, "Author")
	empty, city := "", "Seoul"
	fileID, image := f.store.SeedReadyFile("map_image")
	place := f.store.SeedMapPlace(referencecatalog.MapPlace{
		Name: "Hall", Address: "Road", Lat: 37.5, Lng: 127, GooglePlaceID: &empty, ImageFileID: &fileID,
		AddressComponents: &referencecatalog.AddressComponents{City: &city}, CreatedByMemberID: new(p.memberID), UpdatedByMemberID: &empty,
	})

	got, err := handler.GetMapPlace(p.ctx, connect.NewRequest(&managev1.GetMapPlaceRequest{Id: place.ID}))
	require.NoError(t, err)
	requireMessage(t, &managev1.MapPlace{
		Id: place.ID, Name: "Hall", Address: "Road", Lat: 37.5, Lng: 127,
		CreatedAt: timestamppb.New(place.CreatedAt), UpdatedAt: timestamppb.New(place.UpdatedAt),
		AddressComponents: &managev1.AddressComponents{City: &city},
		ImageFileId:       &fileID, ImageAsset: image,
		CreatedByMemberId: new(p.memberID), CreatedByMember: &commonv1.MemberSummary{Id: p.memberID, Nickname: "Author"},
	}, got.Msg)

	found, err := handler.SearchMapPlaces(p.ctx, connect.NewRequest(&managev1.SearchMapPlacesRequest{Query: "hall"}))
	require.NoError(t, err)
	requireMessage(t, &managev1.SearchMapPlacesResponse{Places: []*managev1.MapPlaceBasic{{Id: place.ID, Name: "Hall", Address: "Road", Lat: 37.5, Lng: 127}}}, found.Msg)

	components := &managev1.AddressComponents{Street: new("Main")}
	created, err := handler.CreateMapPlace(p.ctx, connect.NewRequest(&managev1.CreateMapPlaceRequest{
		Name: "Created", Address: "Road", AddressComponents: components,
	}))
	require.NoError(t, err)
	requireMessage(t, components, created.Msg.AddressComponents)
	require.Equal(t, p.memberID, created.Msg.GetUpdatedByMemberId())
	require.Equal(t, "Author", created.Msg.GetUpdatedByMember().GetNickname())
}
