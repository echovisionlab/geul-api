package public

import (
	"context"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/series"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
)

// ConnectHandler serves the public Series RPCs.
type ConnectHandler struct {
	openv1connect.UnimplementedSeriesServiceHandler
	series *Service
}

// NewConnectHandler returns the Connect adapter of series.
func NewConnectHandler(series *Service) *ConnectHandler {
	return &ConnectHandler{series: series}
}

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

func (h *ConnectHandler) List(ctx context.Context, req *connect.Request[openv1.ListSeriesRequest]) (*connect.Response[openv1.ListSeriesResponse], error) {
	page, err := h.series.ListSeries(ctx, ListSeriesQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(),
		Filters: req.Msg.Filters, Sorts: req.Msg.Sorts, AcceptLanguage: req.Header().Get("Accept-Language"),
	})
	items := make([]*openv1.Series, len(page.Items))
	for i := range page.Items {
		items[i] = seriesMessage(page.Items[i])
	}
	return respond(&openv1.ListSeriesResponse{
		Series: items,
		Pagination: &commonv1.PaginationResponse{
			Total: page.Total, Limit: page.Limit, Offset: page.Offset, HasMore: page.HasMore(),
		},
	}, err)
}

func (h *ConnectHandler) Get(ctx context.Context, req *connect.Request[openv1.GetSeriesRequest]) (*connect.Response[openv1.GetSeriesResponse], error) {
	found, err := h.series.GetSeries(ctx, GetSeriesQuery{IDOrSlug: req.Msg.Slug, AcceptLanguage: req.Header().Get("Accept-Language")})
	return respond(&openv1.GetSeriesResponse{Series: seriesMessage(found)}, err)
}

// seriesMessage is the public API form of a Series.
func seriesMessage(found Series) *openv1.Series {
	return &openv1.Series{
		Id:                 found.ID,
		Title:              found.Title,
		Slug:               found.Slug,
		Description:        found.Description,
		PostCount:          found.PostCount,
		OgAsset:            assetRef(found.OgAsset),
		FeaturedImageAsset: assetRef(found.FeaturedImageAsset),
		LocalizationInfo: &openv1.LocalizationInfo{
			RequestedLocale: found.Localization.RequestedLocale, DisplayedLocale: found.Localization.DisplayedLocale,
			SourceLocale: found.Localization.SourceLocale, IsFallback: found.Localization.IsFallback,
			IsOriginal: found.Localization.IsOriginal, FallbackReason: openv1.LocalizationFallbackReason(found.Localization.FallbackReason),
			AvailableLocales: found.Localization.AvailableLocales,
		},
	}
}

// assetRef is the API form of a ready asset; nil stays nil.
func assetRef(asset *series.Asset) *commonv1.AssetRef {
	if asset == nil {
		return nil
	}
	return &commonv1.AssetRef{
		AssetId: asset.ID, Url: asset.URL, Extension: asset.Extension, MimeType: asset.MIMEType,
		FileSize: asset.FileSize, Sha256: asset.SHA256, Disposition: commonv1.AssetDisposition(asset.Disposition),
		DownloadFilename: asset.DownloadFilename,
	}
}
