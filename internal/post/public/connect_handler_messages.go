package public

import (
	fileref "github.com/echovisionlab/geul-api/internal/fileref"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/memberref"
	"github.com/echovisionlab/geul-api/internal/post"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"

	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// postStatusToProto maps a stored Post status to the public API enum. A
// value the API does not know is UNSPECIFIED.
func postStatusToProto(status post.Status) openv1.PostStatus {
	switch status {
	case post.StatusDraft:
		return openv1.PostStatus_POST_STATUS_DRAFT
	case post.StatusScheduled:
		return openv1.PostStatus_POST_STATUS_SCHEDULED
	case post.StatusPublished:
		return openv1.PostStatus_POST_STATUS_PUBLISHED
	case post.StatusArchived:
		return openv1.PostStatus_POST_STATUS_ARCHIVED
	default:
		return openv1.PostStatus_POST_STATUS_UNSPECIFIED
	}
}

func localizationInfo(selection LocalizedContentSelection) *openv1.LocalizationInfo {
	return &openv1.LocalizationInfo{
		RequestedLocale: selection.RequestedLocale, DisplayedLocale: selection.DisplayedLocale,
		SourceLocale: selection.SourceLocale, IsFallback: selection.IsFallback,
		IsOriginal:     selection.IsOriginal,
		FallbackReason: selection.FallbackReason, AvailableLocales: selection.AvailableLocales,
	}
}

var contentHeights = map[string]commonv1.DocumentContentHeight{
	post.ContentHeightContent:  commonv1.DocumentContentHeight_DOCUMENT_CONTENT_HEIGHT_CONTENT,
	post.ContentHeightViewport: commonv1.DocumentContentHeight_DOCUMENT_CONTENT_HEIGHT_VIEWPORT,
}

var placements = map[string]commonv1.DocumentRegionPlacement{
	post.PlacementFlow:   commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_FLOW,
	post.PlacementPinned: commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_PINNED,
}

func postMessage(view PostView) *openv1.GetPostResponse {
	p := view.Post
	localization := view.Localization
	message := &openv1.Post{
		Id:              p.ID,
		Title:           p.Title,
		Status:          postStatusToProto(p.Status),
		CommentsEnabled: p.CommentsEnabled,
		DocumentLayout: &commonv1.DocumentLayout{
			ContentHeight: contentHeights[p.DocumentLayout.ContentHeight], PageChrome: placements[p.DocumentLayout.PageChrome],
			Footer: placements[p.DocumentLayout.Footer], HideTableOfContents: p.DocumentLayout.HideTableOfContents,
		},
		CreatedAt: timestamppb.New(p.CreatedAt),
		UpdatedAt: timestamppb.New(p.UpdatedAt),
		Document:  view.Document,
		Revision:  view.Revision,
	}
	if p.Slug != nil {
		message.Slug = p.Slug
	}
	if p.Summary != nil {
		message.Summary = p.Summary
	}
	message.LocalizationInfo = localizationInfo(localization)
	if p.PublishedAt != nil {
		message.PublishedAt = timestamppb.New(*p.PublishedAt)
	}
	if p.ScheduledAt != nil {
		message.ScheduledAt = timestamppb.New(*p.ScheduledAt)
	}
	message.ScheduledTimeZone = p.ScheduledTimeZone
	message.OgAsset = mediaasset.ReferenceProto(view.OgAsset)
	if p.MapPlaceID != nil {
		message.MapPlaceId = p.MapPlaceID
		message.LocationPlace = view.MapPlace
	}
	applyPostLocalization(message, localization)
	message.AuthorMembers = memberref.ReferenceMessages(view.Authors)
	for _, category := range p.Categories {
		slug := category.Slug
		message.Categories = append(message.Categories, &openv1.PostCategory{Id: category.ID, Name: category.Name, Slug: &slug})
	}
	for _, tag := range p.Tags {
		slug := tag.Slug
		message.Tags = append(message.Tags, &openv1.PostTag{Id: tag.ID, Name: tag.Name, Slug: &slug})
	}
	if p.Series != nil {
		slug := p.Series.Slug
		message.Series = &openv1.PostSeries{Id: p.Series.ID, Title: view.SeriesTitle, Slug: &slug}
	}
	if p.SeriesOrder != nil {
		message.SeriesOrder = p.SeriesOrder
	}
	message.FeaturedImageDelivery = fileref.DeliveryProto(view.FeaturedImage)
	return &openv1.GetPostResponse{Post: message, BlockMedia: view.BlockMedia}
}

func applyPostLocalization(protoPost *openv1.Post, localization LocalizedContentSelection) {
	if localization.Title != nil {
		protoPost.Title = *localization.Title
	}
	if localization.Summary != nil {
		protoPost.Summary = localization.Summary
	}
}

// summaryMessage renders a listed Post without its content.
func summaryMessage(listed Summary) *openv1.PostSummary {
	p := listed.Post
	localization := listed.Localization
	summary := &openv1.PostSummary{
		Id:    p.ID,
		Title: p.Title,
	}
	if p.Slug != nil {
		summary.Slug = p.Slug
	}
	if p.Summary != nil {
		summary.Summary = p.Summary
	}
	if localization.Title != nil {
		summary.Title = *localization.Title
	}
	if localization.Summary != nil {
		summary.Summary = localization.Summary
	}
	if p.PublishedAt != nil {
		summary.PublishedAt = timestamppb.New(*p.PublishedAt)
	}
	if p.MapPlaceID != nil {
		summary.MapPlaceId = p.MapPlaceID
	}
	if p.SeriesOrder != nil {
		summary.SeriesOrder = p.SeriesOrder
	}
	summary.FeaturedImageDelivery = fileref.DeliveryProto(listed.FeaturedImage)
	summary.AuthorMembers = memberref.ReferenceMessages(listed.Authors)
	for _, category := range p.Categories {
		slug := category.Slug
		summary.Categories = append(summary.Categories, &openv1.PostCategory{Id: category.ID, Name: category.Name, Slug: &slug})
	}
	for _, tag := range p.Tags {
		slug := tag.Slug
		summary.Tags = append(summary.Tags, &openv1.PostTag{Id: tag.ID, Name: tag.Name, Slug: &slug})
	}
	return summary
}

func summaryMessages(summaries []Summary) []*openv1.PostSummary {
	messages := make([]*openv1.PostSummary, 0, len(summaries))
	for _, listed := range summaries {
		messages = append(messages, summaryMessage(listed))
	}
	return messages
}

// mapViewport maps a requested viewport; nil stays nil and a missing bounds
// stays missing, which the use case rejects.
func mapViewport(viewport *openv1.PostMapViewport) *MapViewport {
	if viewport == nil {
		return nil
	}
	mapped := &MapViewport{
		Zoom: viewport.Zoom, WidthPx: viewport.WidthPx, HeightPx: viewport.HeightPx,
		ClusterRadiusPx: viewport.ClusterRadiusPx, MinClusterPoints: viewport.MinClusterPoints,
	}
	if bounds := viewport.Bounds; bounds != nil {
		mapped.Bounds = &MapBounds{West: bounds.West, South: bounds.South, East: bounds.East, North: bounds.North}
	}
	return mapped
}

func mapFeaturesMessage(features MapFeatures) *openv1.ListPostMapFeaturesResponse {
	response := &openv1.ListPostMapFeaturesResponse{}
	if features.Clusters != nil {
		response.Clusters = make([]*openv1.PostMapCluster, len(features.Clusters))
		for i, cluster := range features.Clusters {
			response.Clusters[i] = &openv1.PostMapCluster{
				Id: cluster.ID, Lat: cluster.Lat, Lng: cluster.Lng, PlaceCount: cluster.PlaceCount, PostCount: cluster.PostCount,
				MinBreakoutZoom: cluster.MinBreakoutZoom,
				Bounds: &openv1.MapBounds{
					West: cluster.Bounds.West, South: cluster.Bounds.South, East: cluster.Bounds.East, North: cluster.Bounds.North,
				},
			}
		}
	}
	if features.Items != nil {
		response.Items = make([]*openv1.PostMapItem, len(features.Items))
		for i, item := range features.Items {
			response.Items[i] = &openv1.PostMapItem{
				PlaceId: item.PlaceID, Name: item.Name, Address: item.Address, Lat: item.Lat, Lng: item.Lng,
				PostCount: item.PostCount, PrimaryPostId: item.PrimaryPostID, PrimaryPostSlug: item.PrimaryPostSlug,
				PrimaryPostTitle: item.PrimaryPostTitle,
			}
		}
	}
	return response
}

func commentNodeMessages(nodes []*post.CommentNode) []*openv1.PostCommentNode {
	result := make([]*openv1.PostCommentNode, len(nodes))
	for index, node := range nodes {
		message := &openv1.PostCommentNode{
			Id: node.ID, PostId: node.PostID, ParentId: node.ParentID, Content: node.Content,
			IsDeleted: node.IsDeleted, CreatedAt: timestamppb.New(node.CreatedAt), UpdatedAt: timestamppb.New(node.UpdatedAt),
			Replies: commentNodeMessages(node.Replies), HasMoreReplies: node.HasMoreReplies,
			TotalReplyCount: node.TotalReplyCount,
		}
		if node.MemberID != nil {
			message.MemberId = node.MemberID
			message.Author = memberref.ReferenceMessage(node.Author)
		}
		result[index] = message
	}
	return result
}

func commentsMessage(page post.CommentPage) *openv1.ListPostCommentsResponse {
	return &openv1.ListPostCommentsResponse{
		Comments: commentNodeMessages(page.Comments), NextCursor: page.NextCursor,
		HasMore: page.HasMore, TotalCount: page.TotalCount,
	}
}
