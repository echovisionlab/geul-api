package public

import (
	"context"

	"connectrpc.com/connect"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
)

// ConnectHandler serves the public Release RPCs.
type ConnectHandler struct {
	openv1connect.UnimplementedReleaseServiceHandler
	releases *Service
}

// NewConnectHandler returns the public Release Connect adapter.
func NewConnectHandler(releases *Service) *ConnectHandler {
	return &ConnectHandler{releases: releases}
}

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

func (h *ConnectHandler) Get(ctx context.Context, req *connect.Request[openv1.GetReleaseRequest]) (*connect.Response[openv1.GetReleaseResponse], error) {
	release, err := h.releases.GetRelease(ctx, GetReleaseQuery{
		SlugOrID: req.Msg.Slug, ShareToken: req.Msg.GetShareToken(), SharePassword: req.Msg.GetSharePassword(),
		AcceptLanguage: req.Header().Get("Accept-Language"),
	})
	return respond(&openv1.GetReleaseResponse{Release: release}, err)
}

func (h *ConnectHandler) List(ctx context.Context, req *connect.Request[openv1.ListReleasesRequest]) (*connect.Response[openv1.ListReleasesResponse], error) {
	page, err := h.releases.ListReleases(ctx, ListReleasesQuery{
		Filters: req.Msg.Filters, Sorts: req.Msg.Sorts,
		Limit: req.Msg.Pagination.GetLimit(), Offset: req.Msg.Pagination.GetOffset(),
	})
	return respond(&openv1.ListReleasesResponse{
		Releases: page.Releases,
		Pagination: &commonv1.PaginationResponse{
			Total: int32(page.Total), Limit: page.Limit, Offset: page.Offset,
			HasMore: page.Offset+int32(len(page.Releases)) < int32(page.Total),
		},
	}, err)
}
