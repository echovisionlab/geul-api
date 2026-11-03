package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	postdomain "github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/work"

	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	pagedomain "github.com/echovisionlab/geul-api/internal/page"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	ToolPostCreate         = "post_create"
	ToolPostSettingsGet    = "post_settings_get"
	ToolPostSettingsUpdate = "post_settings_update"
	ToolPostPublish        = "post_publish"
	ToolPostUnpublish      = "post_unpublish"
	ToolPostArchive        = "post_archive"
	ToolPostSchedule       = "post_schedule"
	ToolPostScheduleCancel = "post_schedule_cancel"
	ToolPostRepublish      = "post_republish"
	ToolPostDelete         = "post_delete"
	ToolWorkCreate         = "work_create"
	ToolWorkSettingsGet    = "work_settings_get"
	ToolWorkSettingsUpdate = "work_settings_update"
	ToolWorkPublish        = "work_publish"
	ToolWorkUnpublish      = "work_unpublish"
	ToolWorkDelete         = "work_delete"
	ToolPageCreate         = "page_create"
	ToolPageSettingsGet    = "page_settings_get"
	ToolPageSettingsUpdate = "page_settings_update"
	ToolPagePublish        = "page_publish"
	ToolPageUnpublish      = "page_unpublish"
	ToolPageDelete         = "page_delete"
)

var contentManagementTools = toolRegistry[ContentManagementTools]{
	defineTool(oauthTool(ToolPostCreate, "Create Post", "Create a new draft Post with an empty typed document in the requested source locale.", postCreateInputJSONSchema, postConfigurationMutationOutputJSONSchema, false, false), (*ContentManagementTools).createPost),
	defineTool(oauthTool(ToolPostSettingsGet, "Get Post settings", "Read current source metadata, settings, publication state, and allowed actions through the authorized Post service. Copy configuration_revision unchanged to post_settings_update. Read the body with document_read.", contentIDInputJSONSchema, postSettingsOutputJSONSchema, true, false), (*ContentManagementTools).getPostSettings),
	defineTool(oauthTool(ToolPostSettingsUpdate, "Update Post settings", "Update Post slug, comment setting, Map Place relation, or document layout using the exact configuration_revision returned by post_settings_get, document_list, or post_create. Reload after a stale-revision error before applying pending edits. Use document_metadata_update for title, summary, categories, or tags.", postSettingsUpdateInputJSONSchema, postConfigurationMutationOutputJSONSchema, false, true), (*ContentManagementTools).updatePost),
	defineTool(contentTool(ToolPostPublish, "Publish Post", "Publish a draft or scheduled Post immediately using the existing Post lifecycle rules.", contentIDInputJSONSchema, true), (*ContentManagementTools).mutatePost),
	defineTool(contentTool(ToolPostUnpublish, "Unpublish Post", "Move a published Post, or an archived Post as a site Admin, directly back to draft.", contentIDInputJSONSchema, true), (*ContentManagementTools).mutatePost),
	defineTool(contentTool(ToolPostArchive, "Archive Post", "Archive a published Post. Site Admins may use post_unpublish to move an archived Post directly to draft.", contentIDInputJSONSchema, true), (*ContentManagementTools).mutatePost),
	defineTool(contentTool(ToolPostSchedule, "Schedule Post", "Schedule a Post for publication at an RFC 3339 instant and IANA time zone.", postScheduleInputJSONSchema, true), (*ContentManagementTools).schedulePost),
	defineTool(contentTool(ToolPostScheduleCancel, "Cancel Post schedule", "Cancel the current Post publication schedule.", contentIDInputJSONSchema, true), (*ContentManagementTools).mutatePost),
	defineTool(contentTool(ToolPostRepublish, "Republish Post", "Move an archived Post back to published.", contentIDInputJSONSchema, true), (*ContentManagementTools).mutatePost),
	defineTool(contentTool(ToolPostDelete, "Delete Post", "Permanently delete a Post allowed by its current lifecycle state.", contentIDInputJSONSchema, true), (*ContentManagementTools).mutatePost),
	defineTool(contentTool(ToolWorkCreate, "Create Work", "Create a new draft Work with an empty typed document in the requested source locale. Set is_present true for an ongoing Work, or supply both until_year and until_month.", workCreateInputJSONSchema, false), (*ContentManagementTools).createWork),
	defineTool(oauthTool(ToolWorkSettingsGet, "Get Work settings", "Read current Work settings through the authorized Work service. Before editing metadata or client_ids, copy the returned metadata to observed_metadata and client_ids to observed_client_ids for work_settings_update.", contentIDInputJSONSchema, workSettingsOutputJSONSchema, true, false), (*ContentManagementTools).getWorkSettings),
	defineTool(contentTool(ToolWorkSettingsUpdate, "Update Work settings", "Update Work slug, type, metadata, featured flag, clients, period, or Map Place relation. Read work_settings_get before editing metadata or client_ids and supply its unchanged values as observed_metadata or observed_client_ids; concurrent peer edits are merged using that observed baseline. Use document_metadata_update for title or summary.", workSettingsUpdateInputJSONSchema, true), (*ContentManagementTools).updateWork),
	defineTool(contentTool(ToolWorkPublish, "Publish Work", "Publish a draft Work or restore a legacy archived Work to published.", contentIDInputJSONSchema, true), (*ContentManagementTools).mutateWork),
	defineTool(contentTool(ToolWorkUnpublish, "Unpublish Work", "Move a published Work back to draft.", contentIDInputJSONSchema, true), (*ContentManagementTools).mutateWork),
	defineTool(contentTool(ToolWorkDelete, "Delete Work", "Permanently delete a Work allowed by its current lifecycle state.", contentIDInputJSONSchema, true), (*ContentManagementTools).mutateWork),
	defineTool(contentTool(ToolPageCreate, "Create Page", "Create a new draft Page with an empty typed Page document in the requested source locale.", pageCreateInputJSONSchema, false), (*ContentManagementTools).createPage),
	defineTool(oauthTool(ToolPageSettingsGet, "Get Page settings", "Read current source metadata, settings, layout, and publication state through the authorized Page service. Read the body with document_read.", contentIDInputJSONSchema, pageSettingsOutputJSONSchema, true, false), (*ContentManagementTools).getPageSettings),
	defineTool(contentTool(ToolPageSettingsUpdate, "Update Page settings", "Update Page slug, show-title setting, or reader access_policy. access_policy replaces the whole policy. Use document_metadata_update for title or summary.", pageSettingsUpdateInputJSONSchema, true), (*ContentManagementTools).updatePage),
	defineTool(contentTool(ToolPagePublish, "Publish Page", "Publish a draft Page using the existing Page publication blockers.", contentIDInputJSONSchema, true), (*ContentManagementTools).mutatePage),
	defineTool(contentTool(ToolPageUnpublish, "Unpublish Page", "Move a published Page back to draft.", contentIDInputJSONSchema, true), (*ContentManagementTools).mutatePage),
	defineTool(contentTool(ToolPageDelete, "Delete Page", "Permanently delete a Page and its owned Page state using the existing Page deletion transaction.", contentIDInputJSONSchema, true), (*ContentManagementTools).mutatePage),
}

func contentTool(name, title, description, inputSchema string, destructive bool) mcpserver.Tool {
	return oauthTool(name, title, description, inputSchema, contentMutationOutputJSONSchema, false, destructive)
}

type PostManagementApplication interface {
	GetPost(context.Context, postdomain.GetPostQuery) (postdomain.View, error)
	CreatePost(context.Context, postdomain.CreatePostCommand) (postdomain.View, error)
	UpdatePost(context.Context, postdomain.UpdatePostCommand) (postdomain.SettingsUpdate, error)
	PublishPost(context.Context, postdomain.PostLifecycleCommand) (postdomain.Lifecycle, error)
	UnpublishPost(context.Context, postdomain.PostLifecycleCommand) (postdomain.Lifecycle, error)
	ArchivePost(context.Context, postdomain.PostLifecycleCommand) (postdomain.Lifecycle, error)
	SchedulePost(context.Context, postdomain.SchedulePostCommand) (postdomain.Lifecycle, error)
	CancelPostSchedule(context.Context, postdomain.PostLifecycleCommand) (postdomain.Lifecycle, error)
	RepublishPost(context.Context, postdomain.PostLifecycleCommand) (postdomain.Lifecycle, error)
	DeletePost(context.Context, postdomain.DeletePostCommand) error
}

type WorkManagementApplication interface {
	GetWork(context.Context, work.GetWorkQuery) (work.View, error)
	CreateWork(context.Context, work.CreateWorkCommand) (work.View, error)
	UpdateWork(context.Context, work.UpdateWorkCommand) (work.Update, error)
	PublishWork(context.Context, work.PublishWorkCommand) (work.LifecycleChange, error)
	UnpublishWork(context.Context, work.UnpublishWorkCommand) (work.LifecycleChange, error)
	DeleteWork(context.Context, work.DeleteWorkCommand) error
}

type PageManagementApplication interface {
	GetPage(context.Context, pagedomain.GetPageQuery) (pagedomain.Detail, error)
	CreatePage(context.Context, pagedomain.CreatePageCommand) (pagedomain.Detail, error)
	UpdatePage(context.Context, pagedomain.UpdatePageCommand) (pagedomain.UpdateResult, error)
	PublishPage(context.Context, pagedomain.PublishPageCommand) (pagedomain.LifecycleResult, error)
	UnpublishPage(context.Context, pagedomain.UnpublishPageCommand) (pagedomain.LifecycleResult, error)
	DeletePage(context.Context, pagedomain.DeletePageCommand) error
}

// renderPage renders a Page use case's result as the manage API does.
func renderPage[R, T any](result R, err error, message func(R) (*T, error)) (*T, error) {
	if err != nil {
		return nil, err
	}
	return message(result)
}

type ContentManagementTools struct {
	posts PostManagementApplication
	works WorkManagementApplication
	pages PageManagementApplication
}

func NewContentManagementTools(posts PostManagementApplication, works WorkManagementApplication, pages PageManagementApplication) (*ContentManagementTools, error) {
	if interfaceValueIsNil(posts) || interfaceValueIsNil(works) || interfaceValueIsNil(pages) {
		return nil, errors.New("MCP Post, Work, and Page management applications are required")
	}
	return &ContentManagementTools{posts: posts, works: works, pages: pages}, nil
}

func (*ContentManagementTools) ToolNames() []string { return contentManagementTools.names() }
func (*ContentManagementTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return contentManagementTools.list(), nil
}

func (tools *ContentManagementTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	return contentManagementTools.call(tools, ctx, name, arguments)
}

type contentIDArguments struct {
	DocumentID string `json:"document_id"`
}

type postCreateArguments struct {
	Title           string   `json:"title"`
	SourceLocale    string   `json:"source_locale"`
	Slug            *string  `json:"slug,omitempty"`
	Summary         *string  `json:"summary,omitempty"`
	CommentsEnabled *bool    `json:"comments_enabled,omitempty"`
	CategoryIDs     []string `json:"category_ids,omitempty"`
	TagIDs          []string `json:"tag_ids,omitempty"`
	MapPlaceID      *string  `json:"map_place_id,omitempty"`
}

func (tools *ContentManagementTools) createPost(ctx context.Context, _ string, call toolCall[postCreateArguments]) (mcpserver.ToolResult, error) {
	var input postCreateArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	commentsEnabled := true
	if input.CommentsEnabled != nil {
		commentsEnabled = *input.CommentsEnabled
	}
	createdView, err := tools.posts.CreatePost(ctx, postdomain.CreatePostCommand{
		Title: input.Title, Slug: input.Slug, Summary: input.Summary, CommentsEnabled: commentsEnabled,
		CategoryIDs: input.CategoryIDs, TagIDs: input.TagIDs, MapPlaceID: input.MapPlaceID, SourceLocale: input.SourceLocale,
		Document:       emptyRichTextDocument(contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST, input.SourceLocale),
		AcceptLanguage: input.SourceLocale,
	})
	if err != nil {
		return expectedToolError(err)
	}
	created := postdomain.PostMessage(createdView)
	return contentResult(map[string]any{
		"document_type": "post", "document_id": created.Id, "changed": true, "title": created.Title,
		"slug": optionalStringValue(created.Slug), "source_locale": created.SourceLocale,
		"status": contentStatus(created.Status.String(), "POST_STATUS_"), "document_revision": created.Revision,
		"configuration_revision": created.ConfigurationRevision,
	})
}

type postSettingsArguments struct {
	DocumentID                    string                   `json:"document_id"`
	ExpectedConfigurationRevision string                   `json:"expected_configuration_revision"`
	Slug                          *string                  `json:"slug,omitempty"`
	CommentsEnabled               *bool                    `json:"comments_enabled,omitempty"`
	MapPlaceID                    *string                  `json:"map_place_id,omitempty"`
	DocumentLayout                *documentLayoutArguments `json:"document_layout,omitempty"`
}

type documentLayoutArguments struct {
	ContentHeight       string `json:"content_height"`
	PageChrome          string `json:"page_chrome"`
	Footer              string `json:"footer"`
	HideTableOfContents *bool  `json:"hide_table_of_contents"`
}

func (input documentLayoutArguments) proto() (*commonv1.DocumentLayout, error) {
	height, ok := commonv1.DocumentContentHeight_value[input.ContentHeight]
	if !ok || (height != int32(commonv1.DocumentContentHeight_DOCUMENT_CONTENT_HEIGHT_CONTENT) && height != int32(commonv1.DocumentContentHeight_DOCUMENT_CONTENT_HEIGHT_VIEWPORT)) {
		return nil, errors.New("document_layout.content_height must be DOCUMENT_CONTENT_HEIGHT_CONTENT or DOCUMENT_CONTENT_HEIGHT_VIEWPORT")
	}
	chrome, ok := commonv1.DocumentRegionPlacement_value[input.PageChrome]
	if !ok || (chrome != int32(commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_FLOW) && chrome != int32(commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_PINNED)) {
		return nil, errors.New("document_layout.page_chrome must be DOCUMENT_REGION_PLACEMENT_FLOW or DOCUMENT_REGION_PLACEMENT_PINNED")
	}
	footer, ok := commonv1.DocumentRegionPlacement_value[input.Footer]
	if !ok || (footer != int32(commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_FLOW) && footer != int32(commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_PINNED)) {
		return nil, errors.New("document_layout.footer must be DOCUMENT_REGION_PLACEMENT_FLOW or DOCUMENT_REGION_PLACEMENT_PINNED")
	}
	if input.HideTableOfContents == nil {
		return nil, errors.New("document_layout.hide_table_of_contents is required")
	}
	return &commonv1.DocumentLayout{ContentHeight: commonv1.DocumentContentHeight(height), PageChrome: commonv1.DocumentRegionPlacement(chrome), Footer: commonv1.DocumentRegionPlacement(footer), HideTableOfContents: *input.HideTableOfContents}, nil
}

func documentLayoutOutput(layout *commonv1.DocumentLayout) any {
	if layout == nil {
		return nil
	}
	hideTableOfContents := layout.HideTableOfContents
	return documentLayoutArguments{ContentHeight: layout.ContentHeight.String(), PageChrome: layout.PageChrome.String(), Footer: layout.Footer.String(), HideTableOfContents: &hideTableOfContents}
}

func (tools *ContentManagementTools) getPostSettings(ctx context.Context, _ string, call toolCall[contentIDArguments]) (mcpserver.ToolResult, error) {
	var input contentIDArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	view, err := tools.posts.GetPost(ctx, postdomain.GetPostQuery{ID: input.DocumentID})
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

func (tools *ContentManagementTools) updatePost(ctx context.Context, _ string, call toolCall[postSettingsArguments]) (mcpserver.ToolResult, error) {
	arguments := call.Arguments

	if err := rejectNullArguments(arguments, "document_layout"); err != nil {
		return executionError(err)
	}
	var input postSettingsArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if input.Slug == nil && input.CommentsEnabled == nil && input.MapPlaceID == nil && input.DocumentLayout == nil {
		return executionError(errors.New("at least one Post setting is required"))
	}
	var layout *commonv1.DocumentLayout
	if input.DocumentLayout != nil {
		var err error
		layout, err = input.DocumentLayout.proto()
		if err != nil {
			return executionError(err)
		}
	}
	update, err := tools.posts.UpdatePost(ctx, postdomain.UpdatePostCommand{
		ID: input.DocumentID, Slug: input.Slug, CommentsEnabled: input.CommentsEnabled,
		MapPlaceID: input.MapPlaceID, DocumentLayout: postdomain.DocumentLayoutFromMessage(layout), ExpectedConfigurationRevision: input.ExpectedConfigurationRevision,
	})
	if err != nil {
		return expectedToolError(err)
	}
	updated := postdomain.SettingsUpdateMessage(update)
	return contentResult(map[string]any{
		"document_type": "post", "document_id": updated.Id, "changed": updated.Changed,
		"slug": optionalStringValue(updated.Slug), "updated_at": timestampString(updated.UpdatedAt),
		"configuration_revision": updated.ConfigurationRevision,
		"comments_enabled":       updated.CommentsEnabled, "map_place_id": optionalStringValue(updated.MapPlaceId),
		"document_layout": documentLayoutOutput(updated.DocumentLayout),
	})
}

func (tools *ContentManagementTools) mutatePost(ctx context.Context, name string, call toolCall[contentIDArguments]) (mcpserver.ToolResult, error) {
	var input contentIDArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if name == ToolPostDelete {
		if err := tools.posts.DeletePost(ctx, postdomain.DeletePostCommand{ID: input.DocumentID}); err != nil {
			return expectedToolError(err)
		}
		return contentResult(map[string]any{"document_type": "post", "document_id": input.DocumentID, "changed": true, "deleted": true})
	}
	command := postdomain.PostLifecycleCommand{ID: input.DocumentID}
	var result postdomain.Lifecycle
	var err error
	switch name {
	case ToolPostPublish:
		result, err = tools.posts.PublishPost(ctx, command)
	case ToolPostUnpublish:
		result, err = tools.posts.UnpublishPost(ctx, command)
	case ToolPostArchive:
		result, err = tools.posts.ArchivePost(ctx, command)
	case ToolPostScheduleCancel:
		result, err = tools.posts.CancelPostSchedule(ctx, command)
	case ToolPostRepublish:
		result, err = tools.posts.RepublishPost(ctx, command)
	}
	if err != nil {
		return expectedToolError(err)
	}
	return postLifecycleResult(postdomain.LifecycleMessage(result))
}

type postScheduleArguments struct {
	DocumentID        string `json:"document_id"`
	ScheduledAt       string `json:"scheduled_at"`
	ScheduledTimeZone string `json:"scheduled_time_zone"`
}

func (tools *ContentManagementTools) schedulePost(ctx context.Context, _ string, call toolCall[postScheduleArguments]) (mcpserver.ToolResult, error) {
	var input postScheduleArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	scheduledAt, err := time.Parse(time.RFC3339, input.ScheduledAt)
	if err != nil {
		return executionError(fmt.Errorf("scheduled_at must be RFC 3339: %w", err))
	}
	result, err := tools.posts.SchedulePost(ctx, postdomain.SchedulePostCommand{ID: input.DocumentID, ScheduledAt: &scheduledAt, TimeZone: input.ScheduledTimeZone})
	if err != nil {
		return expectedToolError(err)
	}
	return postLifecycleResult(postdomain.LifecycleMessage(result))
}

func postLifecycleResult(result *managev1.PostLifecycleMutationResponse) (mcpserver.ToolResult, error) {
	output := map[string]any{"document_type": "post", "document_id": result.Id, "changed": result.Changed, "status": contentStatus(result.Status.String(), "POST_STATUS_"), "updated_at": timestampString(result.UpdatedAt)}
	if result.ScheduledAt != nil {
		output["scheduled_at"] = timestampString(result.ScheduledAt)
	}
	if result.ScheduledTimeZone != nil {
		output["scheduled_time_zone"] = *result.ScheduledTimeZone
	}
	return contentResult(output)
}

type workCreateArguments struct {
	Title        string          `json:"title"`
	SourceLocale string          `json:"source_locale"`
	Slug         *string         `json:"slug,omitempty"`
	Summary      *string         `json:"summary,omitempty"`
	Type         string          `json:"type"`
	Metadata     *map[string]any `json:"metadata,omitempty"`
	Featured     *bool           `json:"featured,omitempty"`
	Year         int32           `json:"year"`
	Month        int32           `json:"month"`
	UntilYear    *int32          `json:"until_year,omitempty"`
	UntilMonth   *int32          `json:"until_month,omitempty"`
	IsPresent    *bool           `json:"is_present,omitempty"`
}

func (tools *ContentManagementTools) createWork(ctx context.Context, _ string, call toolCall[workCreateArguments]) (mcpserver.ToolResult, error) {
	var input workCreateArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	metadata, err := optionalFields(input.Metadata)
	if err != nil {
		return executionError(err)
	}
	workType, err := parseWorkType(input.Type)
	if err != nil {
		return executionError(err)
	}
	if input.IsPresent != nil && *input.IsPresent {
		if input.UntilYear != nil || input.UntilMonth != nil {
			return executionError(errors.New("until_year and until_month must be omitted when is_present is true"))
		}
	} else if input.UntilYear == nil || input.UntilMonth == nil {
		return executionError(errors.New("until_year and until_month are required unless is_present is true"))
	}
	view, err := tools.works.CreateWork(ctx, work.CreateWorkCommand{
		Title: input.Title, Slug: input.Slug, Type: workType.String(), Summary: input.Summary, Metadata: metadata, Featured: input.Featured,
		Year: input.Year, Month: input.Month, UntilYear: input.UntilYear, UntilMonth: input.UntilMonth, IsPresent: input.IsPresent,
		SourceLocale: input.SourceLocale, Document: emptyRichTextDocument(contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK, input.SourceLocale),
		AcceptLanguage: input.SourceLocale,
	})
	if err != nil {
		return expectedToolError(err)
	}
	created := work.WorkMessage(ctx, view)
	return contentResult(map[string]any{"document_type": "work", "document_id": created.Id, "changed": true, "title": created.Title, "slug": optionalStringValue(created.Slug), "source_locale": created.SourceLocale, "status": contentStatus(created.Status.String(), "WORK_STATUS_"), "document_revision": created.Revision})
}

type workSettingsArguments struct {
	DocumentID        string          `json:"document_id"`
	Slug              *string         `json:"slug,omitempty"`
	Type              *string         `json:"type,omitempty"`
	Metadata          *map[string]any `json:"metadata,omitempty"`
	ObservedMetadata  *map[string]any `json:"observed_metadata,omitempty"`
	Featured          *bool           `json:"featured,omitempty"`
	ClientIDs         *[]string       `json:"client_ids,omitempty"`
	ObservedClientIDs *[]string       `json:"observed_client_ids,omitempty"`
	Year              *int32          `json:"year,omitempty"`
	Month             *int32          `json:"month,omitempty"`
	MapPlaceID        *string         `json:"map_place_id,omitempty"`
	UntilYear         *int32          `json:"until_year,omitempty"`
	UntilMonth        *int32          `json:"until_month,omitempty"`
	IsPresent         *bool           `json:"is_present,omitempty"`
}

func (tools *ContentManagementTools) getWorkSettings(ctx context.Context, _ string, call toolCall[contentIDArguments]) (mcpserver.ToolResult, error) {
	var input contentIDArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	view, err := tools.works.GetWork(ctx, work.GetWorkQuery{ID: input.DocumentID})
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

func (tools *ContentManagementTools) updateWork(ctx context.Context, _ string, call toolCall[workSettingsArguments]) (mcpserver.ToolResult, error) {
	var input workSettingsArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if input.Slug == nil && input.Type == nil && input.Metadata == nil && input.Featured == nil && input.ClientIDs == nil && input.Year == nil && input.Month == nil && input.MapPlaceID == nil && input.UntilYear == nil && input.UntilMonth == nil && input.IsPresent == nil {
		return executionError(errors.New("at least one Work setting is required"))
	}
	if input.Metadata != nil && input.ObservedMetadata == nil {
		return executionError(errors.New("observed_metadata is required when updating metadata"))
	}
	if input.ClientIDs != nil && input.ObservedClientIDs == nil {
		return executionError(errors.New("observed_client_ids is required when updating client_ids"))
	}
	command := work.UpdateWorkCommand{
		ID: input.DocumentID, Slug: input.Slug, Featured: input.Featured, Year: input.Year, Month: input.Month, MapPlaceID: input.MapPlaceID,
		UntilYear: input.UntilYear, UntilMonth: input.UntilMonth, IsPresent: input.IsPresent,
		Clients: input.ClientIDs, ObservedClients: input.ObservedClientIDs,
	}
	var err error
	if command.Metadata, err = optionalFields(input.Metadata); err != nil {
		return executionError(err)
	}
	if command.ObservedMetadata, err = optionalFields(input.ObservedMetadata); err != nil {
		return executionError(err)
	}
	if input.Type != nil {
		parsed, parseErr := parseWorkType(*input.Type)
		if parseErr != nil {
			return executionError(parseErr)
		}
		workType := parsed.String()
		command.Type = &workType
	}
	updated, err := tools.works.UpdateWork(ctx, command)
	if err != nil {
		return expectedToolError(err)
	}
	return contentResult(map[string]any{"document_type": "work", "document_id": updated.Work.ID, "changed": updated.Changed, "slug": optionalStringValue(updated.Work.Slug), "updated_at": timestampString(timestamppb.New(updated.Work.UpdatedAt))})
}

func (tools *ContentManagementTools) mutateWork(ctx context.Context, name string, call toolCall[contentIDArguments]) (mcpserver.ToolResult, error) {
	var input contentIDArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if name == ToolWorkDelete {
		if err := tools.works.DeleteWork(ctx, work.DeleteWorkCommand{ID: input.DocumentID}); err != nil {
			return expectedToolError(err)
		}
		return contentResult(map[string]any{"document_type": "work", "document_id": input.DocumentID, "changed": true, "deleted": true})
	}
	var result work.LifecycleChange
	var err error
	if name == ToolWorkPublish {
		result, err = tools.works.PublishWork(ctx, work.PublishWorkCommand{ID: input.DocumentID})
	} else {
		result, err = tools.works.UnpublishWork(ctx, work.UnpublishWorkCommand{ID: input.DocumentID})
	}
	if err != nil {
		return expectedToolError(err)
	}
	return contentResult(map[string]any{"document_type": "work", "document_id": result.ID, "changed": result.Changed, "status": contentStatus(work.WorkStatusName(result.Status), "WORK_STATUS_"), "updated_at": timestampString(timestamppb.New(result.UpdatedAt))})
}

type pageCreateArguments struct {
	Title        string  `json:"title"`
	SourceLocale string  `json:"source_locale"`
	Slug         *string `json:"slug,omitempty"`
	Summary      *string `json:"summary,omitempty"`
	ShowTitle    *bool   `json:"show_title,omitempty"`
}

func (tools *ContentManagementTools) createPage(ctx context.Context, _ string, call toolCall[pageCreateArguments]) (mcpserver.ToolResult, error) {
	var input pageCreateArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	detail, err := tools.pages.CreatePage(ctx, pagedomain.CreatePageCommand{
		Title: input.Title, Slug: input.Slug, Summary: input.Summary, ShowTitle: input.ShowTitle,
		SourceLocale: input.SourceLocale, AcceptLanguage: input.SourceLocale,
	})
	created, err := renderPage(detail, err, pagedomain.PageMessage)
	if err != nil {
		return expectedToolError(err)
	}
	return contentResult(map[string]any{"document_type": "page", "document_id": created.Id, "changed": true, "title": created.Title, "slug": optionalStringValue(created.Slug), "source_locale": created.SourceLocale, "status": contentStatus(created.Status.String(), "PAGE_STATUS_"), "document_revision": created.Revision})
}

type pageSettingsArguments struct {
	DocumentID   string                     `json:"document_id"`
	Slug         *string                    `json:"slug,omitempty"`
	ShowTitle    *bool                      `json:"show_title,omitempty"`
	AccessPolicy *pageAccessPolicyArguments `json:"access_policy,omitempty"`
}

// pageAccessPolicyArguments is the MCP shape of commonv1.PageAccessPolicy with lowercase enum names.
type pageAccessPolicyArguments struct {
	Mode                 string   `json:"mode"`
	Match                string   `json:"match,omitempty"`
	AllowedRoles         []string `json:"allowed_roles,omitempty"`
	UserTagIDs           []string `json:"user_tag_ids,omitempty"`
	NewsletterSubscriber bool     `json:"newsletter_subscriber,omitempty"`
}

var pageAccessModes = map[string]commonv1.PageAccessMode{
	"public":        commonv1.PageAccessMode_PAGE_ACCESS_MODE_PUBLIC,
	"authenticated": commonv1.PageAccessMode_PAGE_ACCESS_MODE_AUTHENTICATED,
	"conditions":    commonv1.PageAccessMode_PAGE_ACCESS_MODE_CONDITIONS,
}

var pageAccessMatches = map[string]commonv1.PageAccessMatch{
	"any": commonv1.PageAccessMatch_PAGE_ACCESS_MATCH_ANY,
	"all": commonv1.PageAccessMatch_PAGE_ACCESS_MATCH_ALL,
}

var pageAccessRoles = map[string]policyv1.AuthorizationRole{
	"author": policyv1.AuthorizationRole_AUTHOR,
	"admin":  policyv1.AuthorizationRole_ADMIN,
}

// pageAccessPolicy converts MCP arguments; the Page service still normalizes and validates tag IDs.
func (input *pageAccessPolicyArguments) pageAccessPolicy() (*commonv1.PageAccessPolicy, error) {
	mode, ok := pageAccessModes[input.Mode]
	if !ok {
		return nil, errors.New("access_policy.mode must be public, authenticated, or conditions")
	}
	policy := &commonv1.PageAccessPolicy{Mode: mode, Match: commonv1.PageAccessMatch_PAGE_ACCESS_MATCH_ANY}
	hasConditions := input.Match != "" || len(input.AllowedRoles) > 0 || len(input.UserTagIDs) > 0 || input.NewsletterSubscriber
	if mode != commonv1.PageAccessMode_PAGE_ACCESS_MODE_CONDITIONS {
		// The service would silently drop conditions outside conditions mode; reject them so the caller sees the mistake.
		if hasConditions {
			return nil, errors.New("access_policy conditions require mode conditions")
		}
		return policy, nil
	}
	if input.Match != "" {
		if policy.Match, ok = pageAccessMatches[input.Match]; !ok {
			return nil, errors.New("access_policy.match must be any or all")
		}
	}
	for _, name := range input.AllowedRoles {
		role, ok := pageAccessRoles[name]
		if !ok {
			return nil, errors.New("access_policy.allowed_roles accepts author and admin")
		}
		policy.AllowedRoles = append(policy.AllowedRoles, role)
	}
	policy.UserTagIds = input.UserTagIDs
	policy.NewsletterSubscriber = input.NewsletterSubscriber
	return policy, nil
}

func pageAccessPolicyOutput(policy *commonv1.PageAccessPolicy) map[string]any {
	output := map[string]any{"mode": "public"}
	if policy == nil {
		return output
	}
	for name, mode := range pageAccessModes {
		if policy.Mode == mode {
			output["mode"] = name
		}
	}
	if policy.Mode != commonv1.PageAccessMode_PAGE_ACCESS_MODE_CONDITIONS {
		return output
	}
	output["match"] = "any"
	if policy.Match == commonv1.PageAccessMatch_PAGE_ACCESS_MATCH_ALL {
		output["match"] = "all"
	}
	roles := []any{}
	for _, role := range policy.AllowedRoles {
		for name, value := range pageAccessRoles {
			if role == value {
				roles = append(roles, name)
			}
		}
	}
	tags := []any{}
	for _, id := range policy.UserTagIds {
		tags = append(tags, id)
	}
	output["allowed_roles"], output["user_tag_ids"], output["newsletter_subscriber"] = roles, tags, policy.NewsletterSubscriber
	return output
}

func (tools *ContentManagementTools) getPageSettings(ctx context.Context, _ string, call toolCall[contentIDArguments]) (mcpserver.ToolResult, error) {
	var input contentIDArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	detail, err := tools.pages.GetPage(ctx, pagedomain.GetPageQuery{ID: input.DocumentID})
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

func (tools *ContentManagementTools) updatePage(ctx context.Context, _ string, call toolCall[pageSettingsArguments]) (mcpserver.ToolResult, error) {
	var input pageSettingsArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if input.Slug == nil && input.ShowTitle == nil && input.AccessPolicy == nil {
		return executionError(errors.New("at least one Page setting is required"))
	}
	command := pagedomain.UpdatePageCommand{ID: input.DocumentID, Slug: input.Slug, ShowTitle: input.ShowTitle}
	if input.AccessPolicy != nil {
		policy, err := input.AccessPolicy.pageAccessPolicy()
		if err != nil {
			return executionError(err)
		}
		command.AccessPolicy = pagedomain.AccessPolicyInput(policy)
	}
	result, err := tools.pages.UpdatePage(ctx, command)
	updated, err := renderPage(result, err, pagedomain.UpdateResponseMessage)
	if err != nil {
		return expectedToolError(err)
	}
	output := map[string]any{"document_type": "page", "document_id": updated.Id, "changed": updated.Changed, "slug": optionalStringValue(updated.Slug), "show_title": updated.ShowTitle, "updated_at": timestampString(updated.UpdatedAt)}
	if updated.AccessPolicy != nil {
		output["access_policy"] = pageAccessPolicyOutput(updated.AccessPolicy)
	}
	return contentResult(output)
}

func (tools *ContentManagementTools) mutatePage(ctx context.Context, name string, call toolCall[contentIDArguments]) (mcpserver.ToolResult, error) {
	var input contentIDArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if name == ToolPageDelete {
		if err := tools.pages.DeletePage(ctx, pagedomain.DeletePageCommand{ID: input.DocumentID}); err != nil {
			return expectedToolError(err)
		}
		return contentResult(map[string]any{"document_type": "page", "document_id": input.DocumentID, "changed": true, "deleted": true})
	}
	var lifecycle pagedomain.LifecycleResult
	var err error
	if name == ToolPagePublish {
		lifecycle, err = tools.pages.PublishPage(ctx, pagedomain.PublishPageCommand{ID: input.DocumentID})
	} else {
		lifecycle, err = tools.pages.UnpublishPage(ctx, pagedomain.UnpublishPageCommand{ID: input.DocumentID})
	}
	result, err := renderPage(lifecycle, err, pagedomain.LifecycleMessage)
	if err != nil {
		return expectedToolError(err)
	}
	return contentResult(map[string]any{"document_type": "page", "document_id": result.Id, "changed": result.Changed, "status": contentStatus(result.Status.String(), "PAGE_STATUS_"), "updated_at": timestampString(result.UpdatedAt)})
}

func emptyRichTextDocument(profile contentv1.RichTextProfile, locale string) *contentv1.RichTextDocument {
	return &contentv1.RichTextDocument{BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint, Profile: profile, SourceLocale: locale, Base: &contentv1.RichTextBlockGraph{}, LocaleOverlays: []*contentv1.RichTextLocaleOverlay{{Locale: locale}}}
}

func parseWorkType(value string) (managev1.WorkType, error) {
	switch value {
	case "music_project":
		return managev1.WorkType_WORK_TYPE_MUSIC_PROJECT, nil
	case "portfolio":
		return managev1.WorkType_WORK_TYPE_PORTFOLIO, nil
	case "article":
		return managev1.WorkType_WORK_TYPE_ARTICLE, nil
	case "contribution":
		return managev1.WorkType_WORK_TYPE_CONTRIBUTION, nil
	default:
		return managev1.WorkType_WORK_TYPE_UNSPECIFIED, fmt.Errorf("unsupported Work type %q", value)
	}
}

// optionalFields is the stored form of optional metadata, normalized like a
// Struct on the wire.
func optionalFields(value *map[string]any) (structured.Fields, error) {
	if value == nil {
		return nil, nil
	}
	result, err := structpb.NewStruct(*value)
	if err != nil {
		return nil, fmt.Errorf("metadata: %w", err)
	}
	return result.AsMap(), nil
}

func optionalStringValue(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
func contentStatus(value, prefix string) string {
	return strings.ToLower(strings.TrimPrefix(value, prefix))
}
func timestampString(value *timestamppb.Timestamp) string {
	if value == nil {
		return ""
	}
	return value.AsTime().UTC().Format(time.RFC3339)
}
func contentResult(output map[string]any) (mcpserver.ToolResult, error) {
	for key, value := range output {
		if value == nil {
			delete(output, key)
		}
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return mcpserver.ToolResult{}, fmt.Errorf("encode MCP content mutation: %w", err)
	}
	return structuredResult(encoded, false)
}

var _ ToolProvider = (*ContentManagementTools)(nil)
