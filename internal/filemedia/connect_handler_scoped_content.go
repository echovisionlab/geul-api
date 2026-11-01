package filemedia

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/auth"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
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
	items []*contentv1.ContentBlockMediaItem,
) ([]*contentv1.ContentBlockMediaItem, error) {
	return s.hydrateScopedContentMediaMessage(
		ctx, joined, scopedContentOwnerPage, pageID, documentID, principal, items,
	)
}

// HydrateAuthorizedPostBlockMedia adds private editor delivery while
// Post holds its owner, principal, document, and exact attachment witnesses.
func (s *Service) HydrateAuthorizedPostBlockMedia(
	ctx context.Context, joined Joined, postID string, documentID uuid.UUID,
	principal *auth.UserInfo, items []*contentv1.ContentBlockMediaItem,
) ([]*contentv1.ContentBlockMediaItem, error) {
	return s.hydrateScopedContentMediaMessage(ctx, joined, scopedContentOwnerPost, postID, documentID, principal, items)
}

// HydrateAuthorizedWorkBlockMedia adds private editor delivery while
// the Work owner keeps its exact authorization and lifecycle fence.
func (s *Service) HydrateAuthorizedWorkBlockMedia(
	ctx context.Context,
	joined Joined,
	workID string,
	documentID uuid.UUID,
	principal *auth.UserInfo,
	items []*contentv1.ContentBlockMediaItem,
) ([]*contentv1.ContentBlockMediaItem, error) {
	return s.hydrateScopedContentMediaMessage(
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
	items []*contentv1.ContentBlockMediaItem,
) ([]*contentv1.ContentBlockMediaItem, error) {
	return s.hydrateScopedContentMediaMessage(
		ctx, joined, scopedContentOwnerProgramEvent, eventID, documentID, principal, items,
	)
}

func (s *Service) hydrateScopedContentMediaMessage(ctx context.Context, joined Joined, owner scopedContentOwner, ownerID string, documentID uuid.UUID, principal *auth.UserInfo, items []*contentv1.ContentBlockMediaItem) ([]*contentv1.ContentBlockMediaItem, error) {
	values := make([]*ScopedContentMediaItem, 0, len(items))
	originals := make([]*contentv1.ContentBlockMediaItem, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		values = append(values, &ScopedContentMediaItem{FileID: item.GetAttachment().GetActiveFileId(), BlockID: item.GetSelector().GetBlockId(), ReferencePath: item.GetSelector().GetReferencePath()})
		originals = append(originals, item)
	}
	values, err := s.hydrateAuthorizedContentBlockMedia(ctx, joined, owner, ownerID, documentID, principal, values)
	if err != nil {
		return nil, err
	}
	result := make([]*contentv1.ContentBlockMediaItem, 0, len(values))
	for i, value := range values {
		item := proto.Clone(originals[i]).(*contentv1.ContentBlockMediaItem)
		item.Delivery = DeliveryMessage(value.Delivery)
		item.DownloadAvailability = contentv1.ContentBlockDownloadAvailability_CONTENT_BLOCK_DOWNLOAD_AVAILABILITY_UNAVAILABLE
		item.DownloadAction = contentv1.ContentBlockDownloadAction_CONTENT_BLOCK_DOWNLOAD_ACTION_NONE
		if value.DownloadAllowed {
			item.DownloadAvailability = contentv1.ContentBlockDownloadAvailability_CONTENT_BLOCK_DOWNLOAD_AVAILABILITY_AVAILABLE
			item.DownloadAction = contentv1.ContentBlockDownloadAction_CONTENT_BLOCK_DOWNLOAD_ACTION_DOWNLOAD
		}
		result = append(result, item)
	}
	return result, nil
}
