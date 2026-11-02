package post

import (
	fileref "github.com/echovisionlab/geul-api/internal/fileref"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/memberref"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"

	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// PostMessage renders a Post as its editors read it, for the manage API and
// its MCP tools.
func PostMessage(view View) *managev1.Post {
	p := view.Post
	message := &managev1.Post{
		Id:                    p.ID,
		Title:                 p.Title,
		SourceLocale:          p.SourceLocale,
		Document:              view.Document,
		Revision:              p.ContentRevision,
		ConfigurationRevision: p.ConfigurationRevision,
		BlockMedia:            fileref.BlockMediaItemsProto(view.BlockMedia),
		Status:                postStatusToProto(p.Status),
		CommentsEnabled:       p.CommentsEnabled,
		DocumentLayout:        documentLayoutMessage(p.DocumentLayout),
		CreatedAt:             timestamppb.New(p.CreatedAt),
		OgAsset:               mediaasset.ReferenceProto(view.OgAsset),
	}
	if p.Slug != nil {
		message.Slug = p.Slug
	}
	if p.Summary != nil {
		message.Summary = p.Summary
	}
	if p.PublishedAt != nil {
		message.PublishedAt = timestamppb.New(*p.PublishedAt)
	}
	if p.ScheduledAt != nil {
		message.ScheduledAt = timestamppb.New(*p.ScheduledAt)
	}
	if p.ScheduledTimeZone != nil {
		message.ScheduledTimeZone = p.ScheduledTimeZone
	}
	if !p.UpdatedAt.IsZero() {
		message.UpdatedAt = timestamppb.New(p.UpdatedAt)
	}
	if p.SeriesOrder != nil {
		message.SeriesOrder = p.SeriesOrder
	}
	if p.MapPlaceID != nil {
		message.MapPlaceId = p.MapPlaceID
	}
	if len(p.Categories) > 0 {
		message.Categories = make([]*managev1.Category, len(p.Categories))
		for i, category := range p.Categories {
			message.Categories[i] = &managev1.Category{
				Id:        category.ID,
				Name:      category.Name,
				CreatedAt: timestamppb.New(category.CreatedAt),
				Slug:      &category.Slug,
			}
			if category.Description != nil {
				message.Categories[i].Description = category.Description
			}
		}
	}
	if len(p.Tags) > 0 {
		message.Tags = make([]*managev1.Tag, len(p.Tags))
		for i, tag := range p.Tags {
			message.Tags[i] = &managev1.Tag{
				Id:        tag.ID,
				Name:      tag.Name,
				CreatedAt: timestamppb.New(tag.CreatedAt),
				Slug:      &tag.Slug,
			}
		}
	}
	if p.Series != nil {
		message.Series = &managev1.Series{
			Id:           p.Series.ID,
			SourceLocale: p.Series.SourceLocale,
			Title:        p.Series.Title,
			Status:       p.Series.Status,
			CreatedAt:    timestamppb.New(p.Series.CreatedAt),
			Slug:         p.Series.Slug,
		}
		if p.Series.Description != nil {
			message.Series.Description = p.Series.Description
		}
	}
	message.AuthorMembers = memberref.ReferenceMessages(view.Authors)
	message.FeaturedImageDelivery = fileref.DeliveryProto(view.FeaturedImage)
	message.AllowedActions = postActionMessages(view.Actions)
	return message
}

// postStatusToProto maps a stored Post status to the API enum. A value the
// API does not know is UNSPECIFIED.
func postStatusToProto(status Status) managev1.PostStatus {
	switch status {
	case StatusDraft:
		return managev1.PostStatus_POST_STATUS_DRAFT
	case StatusScheduled:
		return managev1.PostStatus_POST_STATUS_SCHEDULED
	case StatusPublished:
		return managev1.PostStatus_POST_STATUS_PUBLISHED
	case StatusArchived:
		return managev1.PostStatus_POST_STATUS_ARCHIVED
	default:
		return managev1.PostStatus_POST_STATUS_UNSPECIFIED
	}
}

var postActions = map[Action]managev1.PostAction{
	ActionViewVersions:        managev1.PostAction_POST_ACTION_VIEW_VERSIONS,
	ActionEdit:                managev1.PostAction_POST_ACTION_EDIT,
	ActionDelete:              managev1.PostAction_POST_ACTION_DELETE,
	ActionAddAuthor:           managev1.PostAction_POST_ACTION_ADD_AUTHOR,
	ActionRemoveAuthor:        managev1.PostAction_POST_ACTION_REMOVE_AUTHOR,
	ActionManageCollaborators: managev1.PostAction_POST_ACTION_MANAGE_COLLABORATORS,
	ActionManageShareLinks:    managev1.PostAction_POST_ACTION_MANAGE_SHARE_LINKS,
	ActionModerateComments:    managev1.PostAction_POST_ACTION_MODERATE_COMMENTS,
	ActionRestoreVersion:      managev1.PostAction_POST_ACTION_RESTORE_VERSION,
	ActionPublishNow:          managev1.PostAction_POST_ACTION_PUBLISH_NOW,
	ActionSchedule:            managev1.PostAction_POST_ACTION_SCHEDULE,
	ActionCancelSchedule:      managev1.PostAction_POST_ACTION_CANCEL_SCHEDULE,
	ActionUnpublish:           managev1.PostAction_POST_ACTION_UNPUBLISH,
	ActionArchive:             managev1.PostAction_POST_ACTION_ARCHIVE,
	ActionRepublish:           managev1.PostAction_POST_ACTION_REPUBLISH,
}

func postActionMessages(actions []Action) []managev1.PostAction {
	messages := make([]managev1.PostAction, len(actions))
	for i, action := range actions {
		messages[i] = postActions[action]
	}
	return messages
}

var contentHeights = map[string]commonv1.DocumentContentHeight{
	ContentHeightContent:  commonv1.DocumentContentHeight_DOCUMENT_CONTENT_HEIGHT_CONTENT,
	ContentHeightViewport: commonv1.DocumentContentHeight_DOCUMENT_CONTENT_HEIGHT_VIEWPORT,
}

var placements = map[string]commonv1.DocumentRegionPlacement{
	PlacementFlow:   commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_FLOW,
	PlacementPinned: commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_PINNED,
}

func documentLayoutMessage(layout DocumentLayout) *commonv1.DocumentLayout {
	return &commonv1.DocumentLayout{
		ContentHeight: contentHeights[layout.ContentHeight], PageChrome: placements[layout.PageChrome],
		Footer: placements[layout.Footer], HideTableOfContents: layout.HideTableOfContents,
	}
}

// DocumentLayoutFromMessage maps a requested layout; a value the request
// leaves unspecified is empty, which UpdatePost rejects. nil stays nil.
func DocumentLayoutFromMessage(layout *commonv1.DocumentLayout) *DocumentLayout {
	if layout == nil {
		return nil
	}
	mapped := DocumentLayout{HideTableOfContents: layout.HideTableOfContents}
	for value, height := range contentHeights {
		if layout.ContentHeight == height {
			mapped.ContentHeight = value
		}
	}
	for value, placement := range placements {
		if layout.PageChrome == placement {
			mapped.PageChrome = value
		}
		if layout.Footer == placement {
			mapped.Footer = value
		}
	}
	return &mapped
}

// SettingsUpdateMessage renders the outcome of UpdatePost.
func SettingsUpdateMessage(update SettingsUpdate) *managev1.UpdatePostResponse {
	return &managev1.UpdatePostResponse{
		Id:                    update.ID,
		Changed:               update.Changed,
		Slug:                  update.Slug,
		CommentsEnabled:       update.CommentsEnabled,
		MapPlaceId:            update.MapPlaceID,
		DocumentLayout:        documentLayoutMessage(update.DocumentLayout),
		ConfigurationRevision: update.ConfigurationRevision,
		UpdatedAt:             timestamppb.New(update.UpdatedAt),
	}
}

// LifecycleMessage renders a Post's lifecycle after a lifecycle mutation.
func LifecycleMessage(lifecycle Lifecycle) *managev1.PostLifecycleMutationResponse {
	message := &managev1.PostLifecycleMutationResponse{
		Id:                lifecycle.ID,
		Changed:           true,
		Status:            postStatusToProto(lifecycle.Status),
		ScheduledTimeZone: lifecycle.ScheduledTimeZone,
		UpdatedAt:         timestamppb.New(lifecycle.UpdatedAt),
		AllowedActions:    postActionMessages(lifecycle.Actions),
	}
	if lifecycle.PublishedAt != nil {
		message.PublishedAt = timestamppb.New(*lifecycle.PublishedAt)
	}
	if lifecycle.ScheduledAt != nil {
		message.ScheduledAt = timestamppb.New(*lifecycle.ScheduledAt)
	}
	return message
}

func paginationMessage(pagination Pagination) *commonv1.PaginationResponse {
	return &commonv1.PaginationResponse{
		Total: pagination.Total, Limit: pagination.Limit, Offset: pagination.Offset, HasMore: pagination.HasMore,
	}
}

func adminPageMessage(page AdminPage) *managev1.ListPostsAdminResponse {
	posts := make([]*managev1.PostWithStats, len(page.Posts))
	for i, listed := range page.Posts {
		posts[i] = &managev1.PostWithStats{Post: PostMessage(listed.View), ViewCount: 0, CommentCount: listed.CommentCount}
	}
	return &managev1.ListPostsAdminResponse{Posts: posts, Pagination: paginationMessage(page.Pagination)}
}

func myPostsMessage(page PostPage) *managev1.ListMyPostsResponse {
	posts := make([]*managev1.Post, len(page.Posts))
	for i, view := range page.Posts {
		posts[i] = PostMessage(view)
	}
	return &managev1.ListMyPostsResponse{Posts: posts, Pagination: paginationMessage(page.Pagination)}
}

// FeaturedImageMessage renders the outcome of SetPostFeaturedImage.
func FeaturedImageMessage(featured FeaturedImage) *managev1.SetPostFeaturedImageResponse {
	return &managev1.SetPostFeaturedImageResponse{ImageDelivery: fileref.DeliveryProto(featured.Delivery), OgGenerationRunId: featured.OgRunID}
}

// FeaturedImageDeletedMessage renders the outcome of DeletePostFeaturedImage:
// the OG generation the removal started, if any.
func FeaturedImageDeletedMessage(ogRunID *string) *managev1.OgAssetDeleteResponse {
	return &managev1.OgAssetDeleteResponse{Success: true, OgGenerationRunId: ogRunID}
}

var participantRoles = map[ParticipantRole]managev1.PostParticipantRole{
	RoleAuthor:       managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_AUTHOR,
	RoleCollaborator: managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_COLLABORATOR,
}

// ParticipantMessage renders one Post participant.
func ParticipantMessage(participant Participant) *managev1.PostParticipant {
	return &managev1.PostParticipant{
		Member:                memberref.ReferenceMessage(participant.Member),
		Role:                  participantRoles[participant.Role],
		CreatedAt:             timestamppb.New(participant.CreatedAt),
		HasEffectiveAuthority: participant.HasEffectiveAuthority,
	}
}

// ParticipantsMessage renders a Post's participants.
func ParticipantsMessage(participants []Participant) *managev1.ListPostParticipantsResponse {
	messages := make([]*managev1.PostParticipant, len(participants))
	for i, participant := range participants {
		messages[i] = ParticipantMessage(participant)
	}
	return &managev1.ListPostParticipantsResponse{Participants: messages}
}

// VersionsMessage renders one page of a Post's versions.
func VersionsMessage(page VersionPage) *managev1.ListPostVersionsResponse {
	versions := make([]*managev1.PostVersion, len(page.Versions))
	for i, version := range page.Versions {
		contributors := make([]*managev1.VersionContributor, len(version.Contributors))
		for j, contributor := range version.Contributors {
			contributors[j] = &managev1.VersionContributor{MemberId: contributor.MemberID, Nickname: contributor.Nickname}
		}
		versions[i] = &managev1.PostVersion{
			Id: version.ID, Version: version.Version,
			SourceLocale:  version.SourceLocale,
			CanonicalHash: version.CanonicalHash,
			CreatedAt:     timestamppb.New(version.CreatedAt), Contributors: contributors,
			Title: version.Title, Summary: version.Summary,
		}
	}
	return &managev1.ListPostVersionsResponse{Versions: versions, Pagination: paginationMessage(page.Pagination)}
}

// SlugAvailabilityMessage renders whether a slug is free.
func SlugAvailabilityMessage(available bool) *managev1.CheckSlugAvailableResponse {
	return &managev1.CheckSlugAvailableResponse{Available: available}
}

func editorDocumentMessage(document EditorDocument) *managev1.GetPostEditorDocumentResponse {
	return &managev1.GetPostEditorDocumentResponse{
		Document: document.Document, DocumentRevision: document.DocumentRevision, Locale: document.Locale,
		LocaleExists: document.LocaleExists, TargetRevision: document.TargetRevision, SourceLocale: document.SourceLocale,
		BlockMedia: fileref.BlockMediaItemsProto(document.BlockMedia), Editable: document.Editable,
		Title: document.Title, Summary: document.Summary,
	}
}

func roomDocumentMessage(room RoomDocument) *intrav1.LoadPostBlockDocumentResponse {
	var localeMetadata *intrav1.PostLocaleMetadata
	if room.LocaleMetadata != nil {
		localeMetadata = &intrav1.PostLocaleMetadata{
			Locale: room.LocaleMetadata.Locale, Title: room.LocaleMetadata.Title, Summary: room.LocaleMetadata.Summary,
		}
	}
	return &intrav1.LoadPostBlockDocumentResponse{
		Document:         room.Document,
		DocumentRevision: room.DocumentRevision,
		SourceMetadata: &intrav1.PostLocaleMetadata{
			Locale: room.SourceMetadata.Locale, Title: room.SourceMetadata.Title, Summary: room.SourceMetadata.Summary,
		},
		CategoryIds:         room.CategoryIDs,
		TagIds:              room.TagIDs,
		BlockMedia:          fileref.BlockMediaItemsProto(room.BlockMedia),
		Locale:              room.Locale,
		LocaleExists:        room.LocaleExists,
		LocaleMetadata:      localeMetadata,
		TargetRevision:      room.TargetRevision,
		PresentLocaleValues: room.PresentLocaleValues,
	}
}

func blockBatchMessage(saved RoomSave) *intrav1.ApplyPostBlockBatchResponse {
	return &intrav1.ApplyPostBlockBatchResponse{
		DocumentRevision: saved.DocumentRevision, Changed: saved.Changed,
		SourceChanged: saved.SourceChanged, ChangedLocales: saved.ChangedLocales,
		Locale: saved.Locale, TargetRevision: saved.TargetRevision,
	}
}

func localeMetadataMessage(saved RoomSave) *intrav1.UpdatePostLocaleMetadataResponse {
	return &intrav1.UpdatePostLocaleMetadataResponse{
		DocumentRevision: saved.DocumentRevision, Changed: saved.Changed,
		SourceChanged: saved.SourceChanged, ChangedLocales: saved.ChangedLocales,
		Locale: saved.Locale, TargetRevision: saved.TargetRevision,
	}
}

func documentMetadataMessage(saved RoomSave) *intrav1.UpdatePostDocumentMetadataResponse {
	return &intrav1.UpdatePostDocumentMetadataResponse{
		DocumentRevision: saved.DocumentRevision, Changed: saved.Changed,
		SourceChanged: saved.SourceChanged, Locale: saved.Locale,
	}
}

func checkpointMessage(checkpoint Checkpoint) *intrav1.CreatePostVersionCheckpointResponse {
	return &intrav1.CreatePostVersionCheckpointResponse{
		Created: checkpoint.Created, Revision: checkpoint.Revision, Locale: checkpoint.Locale, VersionId: checkpoint.VersionID,
	}
}

// stringChange maps a room's nullable string change; nil stays nil, and a
// change that names neither operation is empty, which the use case rejects.
func stringChange(change *intrav1.PostNullableStringChange) *StringChange {
	if change == nil {
		return nil
	}
	switch value := change.Value.(type) {
	case *intrav1.PostNullableStringChange_SetValue:
		return &StringChange{Set: &value.SetValue}
	case *intrav1.PostNullableStringChange_Clear:
		return &StringChange{Clear: &value.Clear}
	default:
		return &StringChange{}
	}
}

func commentNodeMessages(nodes []*CommentNode) []*managev1.CommentNode {
	messages := make([]*managev1.CommentNode, len(nodes))
	for i, node := range nodes {
		message := &managev1.CommentNode{
			Id:              node.ID,
			PostId:          node.PostID,
			Content:         node.Content,
			IsDeleted:       node.IsDeleted,
			CreatedAt:       timestamppb.New(node.CreatedAt),
			UpdatedAt:       timestamppb.New(node.UpdatedAt),
			Replies:         commentNodeMessages(node.Replies),
			HasMoreReplies:  node.HasMoreReplies,
			TotalReplyCount: node.TotalReplyCount,
		}
		if node.MemberID != nil {
			message.MemberId = node.MemberID
			message.Author = memberref.ReferenceMessage(node.Author)
		}
		if node.ParentID != nil {
			message.ParentId = node.ParentID
		}
		messages[i] = message
	}
	return messages
}

func commentsMessage(page CommentPage) *managev1.ListCommentsByPostResponse {
	return &managev1.ListCommentsByPostResponse{
		Comments: commentNodeMessages(page.Comments), NextCursor: page.NextCursor,
		HasMore: page.HasMore, TotalCount: page.TotalCount,
	}
}

func repliesMessage(page CommentPage) *managev1.LoadMoreRepliesResponse {
	return &managev1.LoadMoreRepliesResponse{
		Replies: commentNodeMessages(page.Comments), NextCursor: page.NextCursor,
		HasMore: page.HasMore, TotalCount: page.TotalCount,
	}
}

func commentWithAuthorMessage(comment AuthoredComment) *managev1.CommentWithAuthor {
	message := &managev1.CommentWithAuthor{
		Id:        comment.ID,
		PostId:    comment.PostID,
		Content:   comment.Content,
		IsDeleted: comment.IsDeleted,
		CreatedAt: timestamppb.New(comment.CreatedAt),
		UpdatedAt: timestamppb.New(comment.UpdatedAt),
	}
	if comment.MemberID != nil {
		message.MemberId = comment.MemberID
		message.Author = memberref.ReferenceMessage(comment.Author)
	}
	if comment.ParentID != nil {
		message.ParentId = comment.ParentID
	}
	return message
}
