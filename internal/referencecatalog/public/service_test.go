package public_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	publicreferencecatalog "github.com/echovisionlab/geul-api/internal/referencecatalog/public"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// fakeStore serves the public reads from fixed rows and records the pages
// and asset sources it was asked for; reads change nothing, so its savepoint
// is a no-op.
type fakeStore struct {
	categories []publicreferencecatalog.Category
	tags       []publicreferencecatalog.Tag
	clients    []referencecatalog.Client
	places     []referencecatalog.MapPlace
	total      int64
	assets     map[string]*commonv1.AssetRef
	pages      []referencecatalog.ListPage
	sources    []referencecatalog.AssetSource
}

func (s *fakeStore) Savepoint() func() { return func() {} }

func (s *fakeStore) Categories(_ context.Context, page referencecatalog.ListPage) ([]publicreferencecatalog.Category, int64, error) {
	s.pages = append(s.pages, page)
	return s.categories, s.total, nil
}

func (s *fakeStore) Tags(_ context.Context, page referencecatalog.ListPage) ([]publicreferencecatalog.Tag, int64, error) {
	s.pages = append(s.pages, page)
	return s.tags, s.total, nil
}

func (s *fakeStore) Client(_ context.Context, id string) (referencecatalog.Client, error) {
	for _, client := range s.clients {
		if client.ID == id {
			return client, nil
		}
	}
	return referencecatalog.Client{}, referencecatalog.ErrClientNotFound
}

func (s *fakeStore) Clients(_ context.Context, page referencecatalog.ListPage) ([]referencecatalog.Client, int64, error) {
	s.pages = append(s.pages, page)
	return s.clients, s.total, nil
}

func (s *fakeStore) MapPlacesByID(context.Context, []string) ([]referencecatalog.MapPlace, error) {
	return s.places, nil
}

func (s *fakeStore) ReadyAsset(_ context.Context, source referencecatalog.AssetSource) *commonv1.AssetRef {
	s.sources = append(s.sources, source)
	return s.assets[source.FileID]
}

func newService(t *testing.T, store *fakeStore) *publicreferencecatalog.Service {
	t.Helper()
	service, err := publicreferencecatalog.NewService(publicreferencecatalog.Deps{Units: mutationtest.New[publicreferencecatalog.Store](store)})
	require.NoError(t, err)
	return service
}

func requireMessage(t *testing.T, want, got proto.Message) {
	t.Helper()
	require.True(t, proto.Equal(want, got), "want %v\ngot  %v", want, got)
}

func TestNewServiceRequiresUnits(t *testing.T) {
	_, err := publicreferencecatalog.NewService(publicreferencecatalog.Deps{})
	require.EqualError(t, err, "public reference catalog dependencies are required: Units")
}

func TestPublicCategoryAndTagListsDefaultTheirPageAndCountTheReturnedRows(t *testing.T) {
	description := "News"
	store := &fakeStore{
		categories: []publicreferencecatalog.Category{{ID: "c1", Name: "News", Slug: "news", Description: &description, PostCount: 2}},
		tags:       []publicreferencecatalog.Tag{{ID: "t1", Name: "Sound", Slug: "sound", PostCount: 3}},
		total:      3,
	}
	service := newService(t, store)

	categories, err := publicreferencecatalog.NewCategoryConnectHandler(service).List(t.Context(), connect.NewRequest(&openv1.ListCategoriesRequest{
		Pagination: &commonv1.PaginationRequest{Offset: -5},
	}))
	require.NoError(t, err)
	requireMessage(t, &openv1.ListCategoriesResponse{
		Categories: []*openv1.Category{{Id: "c1", Name: "News", Slug: "news", Description: &description, PostCount: 2}},
		Pagination: &commonv1.PaginationResponse{Total: 3, Limit: 100, HasMore: true},
	}, categories.Msg)

	tags, err := publicreferencecatalog.NewTagConnectHandler(service).List(t.Context(), connect.NewRequest(&openv1.ListTagsRequest{
		Pagination: &commonv1.PaginationRequest{Limit: 1, Offset: 2},
	}))
	require.NoError(t, err)
	requireMessage(t, &openv1.ListTagsResponse{
		Tags:       []*openv1.Tag{{Id: "t1", Name: "Sound", Slug: "sound", PostCount: 3}},
		Pagination: &commonv1.PaginationResponse{Total: 3, Limit: 1, Offset: 2},
	}, tags.Msg)
	require.Equal(t, []referencecatalog.ListPage{{Limit: 100}, {Limit: 1, Offset: 2}}, store.pages)
}

func TestPublicClientsServeTheirReadyLogos(t *testing.T) {
	createdAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	light, dark, website := "light-file", "dark-file", "https://client.example.test"
	logo := &commonv1.AssetRef{AssetId: "logo", Url: "https://cdn.example.test/logo.webp"}
	store := &fakeStore{
		clients: []referencecatalog.Client{{ID: "client", Name: "Client", Website: &website, LogoLightFileID: &light, LogoDarkFileID: &dark, CreatedAt: createdAt}},
		total:   1,
		assets:  map[string]*commonv1.AssetRef{light: logo},
	}
	handler := publicreferencecatalog.NewClientConnectHandler(newService(t, store))
	summary := &openv1.ClientSummary{Id: "client", Name: "Client", Website: &website, CreatedAt: timestamppb.New(createdAt), LogoLightAsset: logo}

	got, err := handler.Get(t.Context(), connect.NewRequest(&openv1.GetClientRequest{Id: "client"}))
	require.NoError(t, err)
	requireMessage(t, &openv1.GetClientResponse{Client: summary}, got.Msg)
	listed, err := handler.List(t.Context(), connect.NewRequest(&openv1.ListClientsRequest{}))
	require.NoError(t, err)
	requireMessage(t, &openv1.ListClientsResponse{
		Clients: []*openv1.ClientSummary{summary}, Pagination: &commonv1.PaginationResponse{Total: 1, Limit: 20},
	}, listed.Msg)
	require.Equal(t, referencecatalog.AssetSource{FileID: light, Kind: "logo"}, store.sources[0])

	_, err = handler.Get(t.Context(), connect.NewRequest(&openv1.GetClientRequest{Id: "missing"}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestPublicMapPlacesFallBackToImageAssets(t *testing.T) {
	createdAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	fileID, google, city := "image-file", "google", "Seoul"
	image := &commonv1.AssetRef{AssetId: "image", Url: "https://cdn.example.test/image.webp"}
	store := &fakeStore{
		places: []referencecatalog.MapPlace{
			{ID: "imaged", Name: "Imaged", Address: "Road", Lat: 1, Lng: 2, ImageFileID: &fileID, GooglePlaceID: &google,
				AddressComponents: &referencecatalog.AddressComponents{City: &city}, CreatedAt: createdAt},
			{ID: "bare", Name: "Bare", Address: "Road", CreatedAt: createdAt},
		},
		assets: map[string]*commonv1.AssetRef{fileID: image},
	}
	handler := publicreferencecatalog.NewMapPlaceConnectHandler(newService(t, store))

	got, err := handler.GetByIds(t.Context(), connect.NewRequest(&openv1.GetMapPlacesByIdsRequest{Ids: []string{"imaged", "bare"}}))
	require.NoError(t, err)
	requireMessage(t, &openv1.GetMapPlacesByIdsResponse{Places: []*openv1.MapPlace{
		{Id: "imaged", Name: "Imaged", Address: "Road", Lat: 1, Lng: 2, CreatedAt: timestamppb.New(createdAt),
			AddressComponents: &openv1.AddressComponents{City: &city}, GooglePlaceId: &google, ImageAsset: image},
		{Id: "bare", Name: "Bare", Address: "Road", CreatedAt: timestamppb.New(createdAt)},
	}}, got.Msg)
	require.Equal(t, []referencecatalog.AssetSource{{FileID: fileID, Kind: "map_image", FallbackKinds: []string{"image"}}}, store.sources)

	empty, err := handler.GetByIds(t.Context(), connect.NewRequest(&openv1.GetMapPlacesByIdsRequest{}))
	require.NoError(t, err)
	require.Empty(t, empty.Msg.Places)
}
