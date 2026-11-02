package public

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/stretchr/testify/require"
)

type recordingReader struct {
	listPage       Page
	getItem        Series
	acceptLanguage string
	getIDOrSlug    string
}

func (r *recordingReader) List(_ context.Context, query ListSeriesQuery) (Page, error) {
	r.acceptLanguage = query.AcceptLanguage
	return r.listPage, nil
}

func (r *recordingReader) Get(_ context.Context, idOrSlug, acceptLanguage string) (Series, error) {
	r.getIDOrSlug = idOrSlug
	r.acceptLanguage = acceptLanguage
	return r.getItem, nil
}

func TestSeriesServiceListProjectsAdapterPagination(t *testing.T) {
	reader := &recordingReader{listPage: Page{
		Items:  []Series{{ID: "series-id", Localization: Localization{AvailableLocales: []string{"en", "ko"}}}},
		Total:  3,
		Limit:  1,
		Offset: 1,
	}}
	request := connect.NewRequest(&openv1.ListSeriesRequest{})
	request.Header().Set("Accept-Language", "ko")

	response, err := newTestHandler(t, reader).List(t.Context(), request)

	require.NoError(t, err)
	require.Equal(t, "ko", reader.acceptLanguage)
	require.Len(t, response.Msg.Series, 1)
	require.Equal(t, []string{"en", "ko"}, response.Msg.Series[0].GetLocalizationInfo().GetAvailableLocales())
	require.Nil(t, response.Msg.Series[0].OgAsset)
	require.EqualValues(t, 3, response.Msg.Pagination.Total)
	require.True(t, response.Msg.Pagination.HasMore)
}

func TestSeriesServiceGetValidatesAndDelegatesSlug(t *testing.T) {
	reader := &recordingReader{getItem: Series{ID: "series-id"}}
	service := newTestHandler(t, reader)

	_, err := service.Get(t.Context(), connect.NewRequest(&openv1.GetSeriesRequest{}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.Empty(t, reader.getIDOrSlug)

	request := connect.NewRequest(&openv1.GetSeriesRequest{Slug: " series-slug "})
	request.Header().Set("Accept-Language", "en")
	response, err := service.Get(t.Context(), request)

	require.NoError(t, err)
	require.Equal(t, "series-slug", reader.getIDOrSlug)
	require.Equal(t, "en", reader.acceptLanguage)
	require.Equal(t, "series-id", response.Msg.Series.Id)
}

func newTestHandler(t *testing.T, reader Reader) *ConnectHandler {
	t.Helper()
	service, err := NewService(reader)
	require.NoError(t, err)
	return NewConnectHandler(service)
}
