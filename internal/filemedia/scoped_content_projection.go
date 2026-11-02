package filemedia

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/auth"
	fileref "github.com/echovisionlab/geul-api/internal/fileref"
	optional "github.com/echovisionlab/geul-api/internal/optional"
	"github.com/google/uuid"
)

// HydrateAuthorizedPageBlockMedia adds private editor delivery while
// the Page owner keeps its root, principal, document, File, and exact
// attachment witnesses in one transaction.
func (s *Service) HydrateAuthorizedPageBlockMedia(
	ctx context.Context,
	joined Joined,
	pageID string,
	documentID uuid.UUID,
	principal *auth.UserInfo,
	items []*fileref.BlockMediaItem,
) ([]*fileref.BlockMediaItem, error) {
	return s.hydrateScopedContentMedia(
		ctx, joined, scopedContentOwnerPage, pageID, documentID, principal, items,
	)
}

// HydrateAuthorizedPostBlockMedia adds private editor delivery while
// Post holds its owner, principal, document, and exact attachment witnesses.
func (s *Service) HydrateAuthorizedPostBlockMedia(
	ctx context.Context, joined Joined, postID string, documentID uuid.UUID,
	principal *auth.UserInfo, items []*fileref.BlockMediaItem,
) ([]*fileref.BlockMediaItem, error) {
	return s.hydrateScopedContentMedia(ctx, joined, scopedContentOwnerPost, postID, documentID, principal, items)
}

// HydrateAuthorizedWorkBlockMedia adds private editor delivery while
// the Work owner keeps its exact authorization and lifecycle fence.
func (s *Service) HydrateAuthorizedWorkBlockMedia(
	ctx context.Context,
	joined Joined,
	workID string,
	documentID uuid.UUID,
	principal *auth.UserInfo,
	items []*fileref.BlockMediaItem,
) ([]*fileref.BlockMediaItem, error) {
	return s.hydrateScopedContentMedia(
		ctx, joined, scopedContentOwnerWork, workID, documentID, principal, items,
	)
}

// HydrateAuthorizedProgramEventBlockMedia adds private editor delivery
// while Program Event keeps its exact authorization and lifecycle fence.
func (s *Service) HydrateAuthorizedProgramEventBlockMedia(
	ctx context.Context,
	joined Joined,
	eventID string,
	documentID uuid.UUID,
	principal *auth.UserInfo,
	items []*fileref.BlockMediaItem,
) ([]*fileref.BlockMediaItem, error) {
	return s.hydrateScopedContentMedia(
		ctx, joined, scopedContentOwnerProgramEvent, eventID, documentID, principal, items,
	)
}

func (s *Service) hydrateScopedContentMedia(ctx context.Context, joined Joined, owner scopedContentOwner, ownerID string, documentID uuid.UUID, principal *auth.UserInfo, items []*fileref.BlockMediaItem) ([]*fileref.BlockMediaItem, error) {
	values := make([]*ScopedContentMediaItem, 0, len(items))
	originals := make([]*fileref.BlockMediaItem, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		values = append(values, &ScopedContentMediaItem{FileID: optional.Value(item).Attachment.ActiveFileID(), BlockID: optional.Value(optional.Value(item).Selector).BlockID, ReferencePath: optional.Value(optional.Value(item).Selector).ReferencePath})
		originals = append(originals, item)
	}
	values, err := s.hydrateAuthorizedContentBlockMedia(ctx, joined, owner, ownerID, documentID, principal, values)
	if err != nil {
		return nil, err
	}
	result := make([]*fileref.BlockMediaItem, 0, len(values))
	for i, value := range values {
		item := originals[i].Clone()
		item.Delivery = value.Delivery
		item.DownloadAvailability = fileref.DownloadUnavailable
		item.DownloadAction = fileref.NoDownload
		if value.DownloadAllowed {
			item.DownloadAvailability = fileref.DownloadAvailable
			item.DownloadAction = fileref.Download
		}
		result = append(result, item)
	}
	return result, nil
}
