package mcp

import (
	"context"

	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	pagedomain "github.com/echovisionlab/geul-api/internal/page"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/programevent"
	releasedomain "github.com/echovisionlab/geul-api/internal/release"
	"github.com/echovisionlab/geul-api/internal/work"
)

func (tools *DocumentOperationTools) postSettings(ctx context.Context, documentID string) (mcpserver.ToolResult, error) {
	view, err := tools.applications.Posts.GetPost(ctx, postdomain.GetPostQuery{ID: documentID})
	if err != nil {
		return expectedToolError(err)
	}
	post := postdomain.PostMessage(view)
	categories := make([]string, 0, len(post.Categories))
	for _, category := range post.Categories {
		categories = append(categories, category.Id)
	}
	tags := make([]string, 0, len(post.Tags))
	for _, tag := range post.Tags {
		tags = append(tags, tag.Id)
	}
	actions := make([]string, 0, len(post.AllowedActions))
	for _, action := range post.AllowedActions {
		actions = append(actions, contentStatus(action.String(), "POST_ACTION_"))
	}
	output := map[string]any{
		"document_type": "post", "document_id": post.Id, "title": post.Title, "summary": optionalStringValue(post.Summary),
		"slug": optionalStringValue(post.Slug), "source_locale": post.SourceLocale, "status": contentStatus(post.Status.String(), "POST_STATUS_"),
		"document_revision": post.Revision, "configuration_revision": post.ConfigurationRevision, "comments_enabled": post.CommentsEnabled,
		"map_place_id": optionalStringValue(post.MapPlaceId), "document_layout": documentLayoutOutput(post.DocumentLayout),
		"category_ids": categories, "tag_ids": tags, "allowed_actions": actions,
		"scheduled_time_zone": optionalStringValue(post.ScheduledTimeZone),
	}
	if post.Series != nil {
		output["series_id"] = post.Series.Id
	}
	if post.SeriesOrder != nil {
		output["series_order"] = *post.SeriesOrder
	}
	if post.FeaturedImageDelivery != nil {
		output["featured_image_file_id"] = post.FeaturedImageDelivery.FileId
	}
	if post.ScheduledAt != nil {
		output["scheduled_at"] = timestampString(post.ScheduledAt)
	}
	if post.PublishedAt != nil {
		output["published_at"] = timestampString(post.PublishedAt)
	}
	if post.CreatedAt != nil {
		output["created_at"] = timestampString(post.CreatedAt)
	}
	if post.UpdatedAt != nil {
		output["updated_at"] = timestampString(post.UpdatedAt)
	}
	return contentResult(output)
}

func (tools *DocumentOperationTools) workSettings(ctx context.Context, documentID string) (mcpserver.ToolResult, error) {
	view, err := tools.applications.Works.GetWork(ctx, work.GetWorkQuery{ID: documentID})
	if err != nil {
		return expectedToolError(err)
	}
	stored := work.WorkMessage(ctx, view)
	metadata := map[string]any{}
	if stored.Metadata != nil {
		metadata = stored.Metadata.AsMap()
	}
	clientIDs := make([]string, 0, len(view.Clients))
	for _, client := range view.Clients {
		clientIDs = append(clientIDs, client.ID)
	}
	output := map[string]any{
		"document_type": "work", "document_id": stored.Id,
		"metadata": metadata, "client_ids": clientIDs, "slug": optionalStringValue(stored.Slug),
		"type": contentStatus(stored.Type.String(), "WORK_TYPE_"), "featured": stored.Featured,
		"year": stored.Year, "month": stored.Month, "is_present": stored.IsPresent,
		"map_place_id": optionalStringValue(stored.MapPlaceId), "document_revision": stored.Revision,
	}
	if stored.UntilYear != nil {
		output["until_year"] = *stored.UntilYear
	}
	if stored.UntilMonth != nil {
		output["until_month"] = *stored.UntilMonth
	}
	return contentResult(output)
}

func (tools *DocumentOperationTools) pageSettings(ctx context.Context, documentID string) (mcpserver.ToolResult, error) {
	detail, err := tools.applications.Pages.GetPage(ctx, pagedomain.GetPageQuery{ID: documentID})
	page, err := renderPage(detail, err, pagedomain.PageMessage)
	if err != nil {
		return expectedToolError(err)
	}
	output := map[string]any{
		"document_type": "page", "document_id": page.Id, "title": page.Title, "summary": optionalStringValue(page.Summary),
		"slug": optionalStringValue(page.Slug), "source_locale": page.SourceLocale, "status": contentStatus(page.Status.String(), "PAGE_STATUS_"),
		"document_revision": page.Revision, "show_title": page.ShowTitle, "document_layout": documentLayoutOutput(page.DocumentLayout),
		"access_policy": pageAccessPolicyOutput(page.AccessPolicy),
	}
	if page.FeaturedImageDelivery != nil {
		output["featured_image_file_id"] = page.FeaturedImageDelivery.FileId
	}
	if page.PublishedAt != nil {
		output["published_at"] = timestampString(page.PublishedAt)
	}
	if page.CreatedAt != nil {
		output["created_at"] = timestampString(page.CreatedAt)
	}
	if page.UpdatedAt != nil {
		output["updated_at"] = timestampString(page.UpdatedAt)
	}
	return contentResult(output)
}

func (tools *DocumentOperationTools) releaseSettings(ctx context.Context, documentID string) (mcpserver.ToolResult, error) {
	release, err := tools.applications.Releases.GetRelease(ctx, releasedomain.GetReleaseQuery{ID: documentID})
	if err != nil {
		return expectedToolError(err)
	}
	return contentResult(releaseSettingsOutput(releasedomain.ReleaseMessage(release)))
}

func (tools *DocumentOperationTools) programEventSettings(ctx context.Context, documentID string) (mcpserver.ToolResult, error) {
	event, err := tools.applications.Events.GetProgramEvent(ctx, programevent.GetProgramEventQuery{ID: documentID})
	if err != nil {
		return expectedToolError(err)
	}
	return contentResult(programEventSettingsOutput(programevent.EventMessage(event)))
}
