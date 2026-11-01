package audience

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// Page selects one page of a segment list: the shared query language's
// filters and sorts, and the normalized limit and offset.
type Page struct {
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
	Limit   int32
	Offset  int32
}

// GetSegmentQuery reads one segment.
type GetSegmentQuery struct {
	ID string
}

// GetSegment returns the segment with its configuration, reference counts
// and, when the counter answers, its estimated recipients.
func (s *Service) GetSegment(ctx context.Context, query GetSegmentQuery) (SegmentView, error) {
	can, err := policyv1.AudienceSegment.View(query.ID)
	if err != nil {
		return SegmentView{}, err
	}
	if err := authz.RequireAdminCan(ctx, s.spiceDB, can); err != nil {
		return SegmentView{}, err
	}
	var view SegmentView
	if err := s.units.Read(ctx, func(store Store) (err error) {
		view, err = segmentView(ctx, store, query.ID)
		return err
	}); err != nil {
		return SegmentView{}, err
	}
	if count, err := s.estimateCount(ctx, view.Segment); err == nil {
		view.EstimatedCount = &count
	} else {
		slog.ErrorContext(ctx, "Audience segment count estimate failed; returning segment without a count", "domain", "audience", "event", "audience.operation_failed", "operation", "estimate_segment_count", "outcome", "failed", "segment_id", view.Segment.ID, failurecause.Attr(err))
	}
	return view, nil
}

// ListSegmentsQuery pages through segments for administrators.
type ListSegmentsQuery struct {
	IncludeArchived bool
	// Limit defaults to 20 and is capped at 100.
	Limit   int32
	Offset  int32
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
}

// SegmentPage is one page of segments and the total the filters select.
type SegmentPage struct {
	Segments []SegmentView
	Total    int64
	Limit    int32
	Offset   int32
}

// ListSegments returns one page of segments with their configuration and
// reference counts.
func (s *Service) ListSegments(ctx context.Context, query ListSegmentsQuery) (SegmentPage, error) {
	can, err := policyv1.AudienceSegment.List()
	if err != nil {
		return SegmentPage{}, err
	}
	if err := authz.RequireAdminCan(ctx, s.spiceDB, can); err != nil {
		return SegmentPage{}, err
	}
	page := listPage(query.Limit, query.Offset, query.Filters, query.Sorts)
	result := SegmentPage{Limit: page.Limit, Offset: page.Offset}
	if err := s.units.Read(ctx, func(store Store) error {
		segments, total, err := store.ListSegments(ctx, query.IncludeArchived, page)
		if err != nil {
			return err
		}
		result.Total = total
		result.Segments, err = segmentViews(ctx, store, segments)
		return err
	}); err != nil {
		return SegmentPage{}, err
	}
	return result, nil
}

// ListAuthenticatedAccessSegmentsQuery pages through the active segments a
// download policy may grant access to.
type ListAuthenticatedAccessSegmentsQuery struct {
	// Limit defaults to 20 and is capped at 100.
	Limit   int32
	Offset  int32
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
}

// AccessSegmentPage is one page of segments without their filters, and the
// total the filters select.
type AccessSegmentPage struct {
	Segments []Segment
	Total    int64
	Limit    int32
	Offset   int32
}

// ListAuthenticatedAccessSegments returns the segments that can grant
// authenticated download access, for authors editing a policy.
func (s *Service) ListAuthenticatedAccessSegments(ctx context.Context, query ListAuthenticatedAccessSegmentsQuery) (AccessSegmentPage, error) {
	if err := requireDownloadPolicyAuthor(ctx, s.spiceDB); err != nil {
		return AccessSegmentPage{}, err
	}
	page := listPage(query.Limit, query.Offset, query.Filters, query.Sorts)
	result := AccessSegmentPage{Limit: page.Limit, Offset: page.Offset}
	if err := s.units.Read(ctx, func(store Store) (err error) {
		result.Segments, result.Total, err = store.ListAuthenticatedAccessSegments(ctx, page)
		return err
	}); err != nil {
		return AccessSegmentPage{}, err
	}
	for _, segment := range result.Segments {
		if !segment.GrantsAuthenticatedAccess() {
			return AccessSegmentPage{}, errors.New("authenticated access segment query returned an ineligible segment")
		}
	}
	return result, nil
}

func (s *Service) estimateCount(ctx context.Context, segment Segment) (int32, error) {
	switch segment.Type {
	case SegmentAllMembers, SegmentMemberTags, SegmentMembersByFilter:
	default:
		return 0, fmt.Errorf("unknown segment type: %s", segment.Type)
	}
	count, err := s.recipientCounter.Count(ctx, segment)
	if err != nil {
		return 0, err
	}
	return int32(count), nil
}

// listPage defaults the limit to 20, caps it at 100 and ignores a negative
// offset.
func listPage(requestedLimit, requestedOffset int32, filters []*commonv1.FilterSpec, sorts []*commonv1.SortSpec) Page {
	limit, offset := int32(20), int32(0)
	if requestedLimit > 0 {
		limit = requestedLimit
	}
	if requestedOffset > 0 {
		offset = requestedOffset
	}
	if limit > 100 {
		limit = 100
	}
	return Page{Filters: filters, Sorts: sorts, Limit: limit, Offset: offset}
}
