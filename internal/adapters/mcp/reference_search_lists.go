package mcp

import (
	"context"
	"fmt"
	"slices"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/programevent"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type GenreReferenceDiscovery interface {
	ListGenresAdmin(context.Context, referencecatalog.ReferenceListQuery) (referencecatalog.Page[referencecatalog.ReferenceUsage], error)
}

type StyleReferenceDiscovery interface {
	ListStylesAdmin(context.Context, referencecatalog.ReferenceListQuery) (referencecatalog.Page[referencecatalog.ReferenceUsage], error)
}

type FormatReferenceDiscovery interface {
	ListFormatsAdmin(context.Context, referencecatalog.ReferenceListQuery) (referencecatalog.Page[referencecatalog.ReferenceUsage], error)
}

type ProgramEventTypeReferenceDiscovery interface {
	ListProgramEventTypesAdmin(context.Context, programevent.ListProgramEventTypesQuery) (programevent.TypeList, error)
}

type ProgramEventSeriesReferenceDiscovery interface {
	ListProgramEventSeriesAdmin(context.Context, programevent.ListProgramEventSeriesQuery) (programevent.SeriesList, error)
}

type MemberTagReader interface {
	ListMemberTags(context.Context, member.ListMemberTagsQuery) (member.TagPage, error)
}

// referenceListRequest asks a paged list for one page.
type referenceListRequest struct {
	Filters []*commonv1.FilterSpec
	Limit   int32
	Offset  int32
}

// referenceListPage is one page of a paged list.
type referenceListPage struct {
	Items   []map[string]any
	Total   int64
	HasMore bool
}

// pagedReferenceList is a reference type with an offset, and what it can filter by.
type pagedReferenceList struct {
	read       func(context.Context, referenceListRequest) (referenceListPage, error)
	statuses   []string
	noSearchBy bool
}

func newPagedReferenceLists(applications ReferenceSearchApplications) map[string]pagedReferenceList {
	return map[string]pagedReferenceList{
		"genre": {read: func(ctx context.Context, request referenceListRequest) (referenceListPage, error) {
			page, err := applications.Genres.ListGenresAdmin(ctx, request.catalogQuery())
			return catalogListPage(page, err, describedMusicReference)
		}},
		"style": {read: func(ctx context.Context, request referenceListRequest) (referenceListPage, error) {
			page, err := applications.Styles.ListStylesAdmin(ctx, request.catalogQuery())
			return catalogListPage(page, err, describedMusicReference)
		}},
		"format": {read: func(ctx context.Context, request referenceListRequest) (referenceListPage, error) {
			page, err := applications.Formats.ListFormatsAdmin(ctx, request.catalogQuery())
			return catalogListPage(page, err, projectFormatReference)
		}},
		"program_event_type": {
			noSearchBy: true,
			statuses: []string{
				managev1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_ACTIVE.String(),
				managev1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_INACTIVE.String(),
			},
			read: func(ctx context.Context, request referenceListRequest) (referenceListPage, error) {
				list, err := applications.EventTypes.ListProgramEventTypesAdmin(ctx, programevent.ListProgramEventTypesQuery{Limit: request.Limit, Offset: request.Offset, Filters: request.Filters})
				if err != nil {
					return referenceListPage{}, err
				}
				items := make([]map[string]any, 0, len(list.Types))
				for _, view := range list.Types {
					items = append(items, programEventTypeReferenceItem(programevent.TypeMessage(view)))
				}
				return referenceListPage{Items: items, Total: int64(list.Pagination.Total), HasMore: list.Pagination.HasMore}, nil
			},
		},
		"program_event_series": {
			statuses: []string{
				managev1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_DRAFT.String(),
				managev1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_PUBLISHED.String(),
			},
			read: func(ctx context.Context, request referenceListRequest) (referenceListPage, error) {
				list, err := applications.EventSeries.ListProgramEventSeriesAdmin(ctx, programevent.ListProgramEventSeriesQuery{Limit: request.Limit, Offset: request.Offset, Filters: request.Filters})
				if err != nil {
					return referenceListPage{}, err
				}
				items := make([]map[string]any, 0, len(list.Series))
				for _, series := range list.Series {
					item := programevent.SeriesMessage(series)
					items = append(items, map[string]any{"id": item.Id, "slug": item.Slug, "title": item.Title, "status": item.Status.String()})
				}
				return referenceListPage{Items: items, Total: int64(list.Pagination.Total), HasMore: list.Pagination.HasMore}, nil
			},
		},
		"member_tag": {read: func(ctx context.Context, request referenceListRequest) (referenceListPage, error) {
			page, err := applications.MemberTags.ListMemberTags(ctx, member.ListMemberTagsQuery{Limit: request.Limit, Offset: request.Offset, Filters: request.Filters})
			if err != nil {
				return referenceListPage{}, err
			}
			items := make([]map[string]any, 0, len(page.Tags))
			for _, tag := range page.Tags {
				items = append(items, map[string]any{"id": tag.ID, "name": tag.Name, "member_count": tag.MemberCount})
			}
			return referenceListPage{Items: items, Total: int64(page.Pagination.Total), HasMore: page.Pagination.HasMore}, nil
		}},
	}
}

// page checks input against what this list can filter by, reads the page, and gives
// it the shape of every other lookup.
func (list pagedReferenceList) page(ctx context.Context, input referenceSearchArguments) (referenceSearchOutput, error) {
	request, err := list.request(input)
	if err != nil {
		return referenceSearchOutput{}, &core.InputError{Message: err.Error()}
	}
	page, err := list.read(ctx, request)
	if err != nil {
		return referenceSearchOutput{}, err
	}
	output := referenceSearchOutput{Items: page.Items, Total: &page.Total, HasMore: &page.HasMore}
	if page.HasMore {
		next := int64(request.Offset) + int64(request.Limit)
		output.NextOffset = &next
	}
	return output, nil
}

func (list pagedReferenceList) request(input referenceSearchArguments) (referenceListRequest, error) {
	limit, err := input.limit(referenceSearchMaximumLimit)
	if err != nil {
		return referenceListRequest{}, err
	}
	request := referenceListRequest{Limit: limit}
	if input.Offset != nil {
		if *input.Offset < 0 {
			return referenceListRequest{}, fmt.Errorf("offset must be between 0 and %d", documentListMaxOffset)
		}
		request.Offset = *input.Offset
	}
	if list.noSearchBy && input.Query != nil {
		return referenceListRequest{}, fmt.Errorf("%s lists everything and takes no query", input.ReferenceType)
	}
	request.Filters = discoverySearchFilters(input.query())
	if input.Status != nil {
		if !slices.Contains(list.statuses, *input.Status) {
			return referenceListRequest{}, fmt.Errorf("unsupported status %q for %s", *input.Status, input.ReferenceType)
		}
		request.Filters = append(request.Filters, &commonv1.FilterSpec{Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: *input.Status})
	}
	return request, nil
}

func (request referenceListRequest) catalogQuery() referencecatalog.ReferenceListQuery {
	return referencecatalog.ReferenceListQuery{Filters: request.Filters, Limit: request.Limit, Offset: request.Offset}
}

func catalogListPage(page referencecatalog.Page[referencecatalog.ReferenceUsage], err error, project func(referencecatalog.Reference) map[string]any) (referenceListPage, error) {
	if err != nil {
		return referenceListPage{}, err
	}
	items := make([]map[string]any, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, project(item.Reference))
	}
	return referenceListPage{Items: items, Total: int64(page.Pagination.Total), HasMore: page.Pagination.HasMore}, nil
}

// describedMusicReference projects a Genre or Style.
func describedMusicReference(reference referencecatalog.Reference) map[string]any {
	projected := map[string]any{"id": reference.ID, "name": reference.Name, "slug": reference.Slug}
	if reference.Description != nil {
		projected["description"] = *reference.Description
	}
	return projected
}

func projectFormatReference(format referencecatalog.Reference) map[string]any {
	return map[string]any{"id": format.ID, "name": format.Name, "slug": format.Slug}
}

func programEventTypeReferenceItem(item *managev1.ProgramEventType) map[string]any {
	locales := make([]map[string]any, 0, len(item.Locales))
	for _, locale := range item.Locales {
		if locale != nil {
			localized := map[string]any{"locale": locale.Locale, "name": locale.Name}
			if locale.Description != nil {
				localized["description"] = *locale.Description
			}
			locales = append(locales, localized)
		}
	}
	return map[string]any{"id": item.Id, "slug": item.Slug, "status": item.Status.String(), "requires_place": item.RequiresPlace, "requires_stream_url": item.RequiresStreamUrl, "locales": locales}
}
