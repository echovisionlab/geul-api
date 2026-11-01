package audience

import (
	"context"
	"errors"

	"github.com/echovisionlab/geul-api/internal/domainerr"
)

// segmentViews returns segments with what targets each of them.
func segmentViews(ctx context.Context, store Store, segments []Segment) ([]SegmentView, error) {
	views := make([]SegmentView, len(segments))
	if len(segments) == 0 {
		return views, nil
	}
	ids := make([]string, len(segments))
	for i, segment := range segments {
		ids[i] = segment.ID
	}
	references, err := store.SegmentReferences(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i, segment := range segments {
		views[i] = SegmentView{Segment: segment, References: references[segment.ID]}
	}
	return views, nil
}

// segmentView reads a segment and what targets it, as a mutation returns it.
func segmentView(ctx context.Context, store Store, id string) (SegmentView, error) {
	segment, err := store.Segment(ctx, id)
	if err != nil {
		return SegmentView{}, segmentNotFound(err)
	}
	views, err := segmentViews(ctx, store, []Segment{segment})
	if err != nil {
		return SegmentView{}, err
	}
	return views[0], nil
}

func ensureSegmentMutableForActiveDelivery(ctx context.Context, store Store, segmentID string) error {
	campaign, deliveryRun, err := store.SegmentInActiveDelivery(ctx, segmentID)
	if err != nil {
		return err
	}
	if campaign {
		return domainerr.Precondition("audience segment is used by a scheduled or sending campaign")
	}
	if deliveryRun {
		return domainerr.Precondition("audience segment is frozen by an active delivery run")
	}
	return nil
}

// segmentNotFound answers a missing segment; other errors pass unchanged.
func segmentNotFound(err error) error {
	if errors.Is(err, ErrSegmentNotFound) {
		return errSegmentNotFound()
	}
	return err
}

func errSegmentNotFound() error {
	return domainerr.New(domainerr.NotFound, "", "segment not found")
}
