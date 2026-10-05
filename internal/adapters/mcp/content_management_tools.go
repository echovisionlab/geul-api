package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
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

var contentManagementTools = []mcpserver.Tool{
	contentToolWithOutput(ToolPostCreate, "Create Post", "Create a new draft Post with an empty typed document in the requested source locale.", postCreateInputJSONSchema, postConfigurationMutationOutputJSONSchema, false),
	oauthTool(ToolPostSettingsGet, "Get Post settings", "Read current source metadata, settings, publication state, and allowed actions through the authorized Post service. Copy configuration_revision unchanged to post_settings_update. Read the body with document_read.", contentIDInputJSONSchema, postSettingsOutputJSONSchema, true, false),
	contentToolWithOutput(ToolPostSettingsUpdate, "Update Post settings", "Update Post slug, comment setting, Map Place relation, or document layout using the exact configuration_revision returned by post_settings_get, document_list, or post_create. Reload after a stale-revision error before applying pending edits. Use document_metadata_update for title, summary, categories, or tags.", postSettingsUpdateInputJSONSchema, postConfigurationMutationOutputJSONSchema, true),
	contentTool(ToolPostPublish, "Publish Post", "Publish a draft or scheduled Post immediately using the existing Post lifecycle rules.", contentIDInputJSONSchema, true),
	contentTool(ToolPostUnpublish, "Unpublish Post", "Move a published Post, or an archived Post as a site Admin, directly back to draft.", contentIDInputJSONSchema, true),
	contentTool(ToolPostArchive, "Archive Post", "Archive a published Post. Site Admins may use post_unpublish to move an archived Post directly to draft.", contentIDInputJSONSchema, true),
	contentTool(ToolPostSchedule, "Schedule Post", "Schedule a Post for publication at an RFC 3339 instant and IANA time zone.", postScheduleInputJSONSchema, true),
	contentTool(ToolPostScheduleCancel, "Cancel Post schedule", "Cancel the current Post publication schedule.", contentIDInputJSONSchema, true),
	contentTool(ToolPostRepublish, "Republish Post", "Move an archived Post back to published.", contentIDInputJSONSchema, true),
	contentTool(ToolPostDelete, "Delete Post", "Permanently delete a Post allowed by its current lifecycle state.", contentIDInputJSONSchema, true),
	contentTool(ToolWorkCreate, "Create Work", "Create a new draft Work with an empty typed document in the requested source locale.", workCreateInputJSONSchema, false),
	oauthTool(ToolWorkSettingsGet, "Get Work settings", "Read current Work settings through the authorized Work service. Before editing metadata or client_ids, copy the returned metadata to observed_metadata and client_ids to observed_client_ids for work_settings_update.", contentIDInputJSONSchema, workSettingsOutputJSONSchema, true, false),
	contentTool(ToolWorkSettingsUpdate, "Update Work settings", "Update Work slug, type, metadata, featured flag, clients, period, or Map Place relation. Read work_settings_get before editing metadata or client_ids and supply its unchanged values as observed_metadata or observed_client_ids; concurrent peer edits are merged using that observed baseline. Use document_metadata_update for title or summary.", workSettingsUpdateInputJSONSchema, true),
	contentTool(ToolWorkPublish, "Publish Work", "Publish a draft Work or restore a legacy archived Work to published.", contentIDInputJSONSchema, true),
	contentTool(ToolWorkUnpublish, "Unpublish Work", "Move a published Work back to draft.", contentIDInputJSONSchema, true),
	contentTool(ToolWorkDelete, "Delete Work", "Permanently delete a Work allowed by its current lifecycle state.", contentIDInputJSONSchema, true),
	contentTool(ToolPageCreate, "Create Page", "Create a new draft Page with an empty typed Page document in the requested source locale.", pageCreateInputJSONSchema, false),
	oauthTool(ToolPageSettingsGet, "Get Page settings", "Read current source metadata, settings, layout, and publication state through the authorized Page service. Read the body with document_read.", contentIDInputJSONSchema, pageSettingsOutputJSONSchema, true, false),
	contentTool(ToolPageSettingsUpdate, "Update Page settings", "Update Page slug or show-title setting. Use document_metadata_update for title or summary.", pageSettingsUpdateInputJSONSchema, true),
	contentTool(ToolPagePublish, "Publish Page", "Publish a draft Page using the existing Page publication blockers.", contentIDInputJSONSchema, true),
	contentTool(ToolPageUnpublish, "Unpublish Page", "Move a published Page back to draft.", contentIDInputJSONSchema, true),
	contentTool(ToolPageDelete, "Delete Page", "Permanently delete a Page and its owned Page state using the existing Page deletion transaction.", contentIDInputJSONSchema, true),
}

func contentTool(name, title, description, inputSchema string, destructive bool) mcpserver.Tool {
	return contentToolWithOutput(name, title, description, inputSchema, contentMutationOutputJSONSchema, destructive)
}

func contentToolWithOutput(name, title, description, inputSchema, outputSchema string, destructive bool) mcpserver.Tool {
	return mcpserver.Tool{
		Name: name, Title: title, Description: description,
		InputSchema: json.RawMessage(inputSchema), OutputSchema: json.RawMessage(outputSchema),
		SecuritySchemes: oauthSecuritySchemes(), Annotations: toolAnnotations(false, destructive, false), Meta: oauthSecurityMeta(),
	}
}

type PostManagementApplication interface {
	GetPost(context.Context, *connect.Request[managev1.GetPostRequest]) (*connect.Response[managev1.Post], error)
	CreatePost(context.Context, *connect.Request[managev1.CreatePostRequest]) (*connect.Response[managev1.Post], error)
	UpdatePost(context.Context, *connect.Request[managev1.UpdatePostRequest]) (*connect.Response[managev1.UpdatePostResponse], error)
	PublishPost(context.Context, *connect.Request[managev1.PublishPostRequest]) (*connect.Response[managev1.PostLifecycleMutationResponse], error)
	UnpublishPost(context.Context, *connect.Request[managev1.UnpublishPostRequest]) (*connect.Response[managev1.PostLifecycleMutationResponse], error)
	ArchivePost(context.Context, *connect.Request[managev1.ArchivePostRequest]) (*connect.Response[managev1.PostLifecycleMutationResponse], error)
	SchedulePost(context.Context, *connect.Request[managev1.SchedulePostRequest]) (*connect.Response[managev1.PostLifecycleMutationResponse], error)
	CancelPostSchedule(context.Context, *connect.Request[managev1.CancelPostScheduleRequest]) (*connect.Response[managev1.PostLifecycleMutationResponse], error)
	RepublishPost(context.Context, *connect.Request[managev1.RepublishPostRequest]) (*connect.Response[managev1.PostLifecycleMutationResponse], error)
	DeletePost(context.Context, *connect.Request[managev1.DeletePostRequest]) (*connect.Response[managev1.DeleteResponse], error)
}

type WorkManagementApplication interface {
	GetWork(context.Context, *connect.Request[managev1.GetWorkRequest]) (*connect.Response[managev1.Work], error)
	CreateWork(context.Context, *connect.Request[managev1.CreateWorkRequest]) (*connect.Response[managev1.Work], error)
	UpdateWork(context.Context, *connect.Request[managev1.UpdateWorkRequest]) (*connect.Response[managev1.UpdateWorkResponse], error)
	PublishWork(context.Context, *connect.Request[managev1.PublishWorkRequest]) (*connect.Response[managev1.WorkLifecycleMutationResponse], error)
	UnpublishWork(context.Context, *connect.Request[managev1.UnpublishWorkRequest]) (*connect.Response[managev1.WorkLifecycleMutationResponse], error)
	DeleteWork(context.Context, *connect.Request[managev1.DeleteWorkRequest]) (*connect.Response[managev1.DeleteResponse], error)
}

type PageManagementApplication interface {
	GetPage(context.Context, *connect.Request[managev1.GetPageRequest]) (*connect.Response[managev1.Page], error)
	CreatePage(context.Context, *connect.Request[managev1.CreatePageRequest]) (*connect.Response[managev1.Page], error)
	UpdatePage(context.Context, *connect.Request[managev1.UpdatePageRequest]) (*connect.Response[managev1.UpdatePageResponse], error)
	PublishPage(context.Context, *connect.Request[managev1.PublishPageRequest]) (*connect.Response[managev1.PageLifecycleMutationResponse], error)
	UnpublishPage(context.Context, *connect.Request[managev1.UnpublishPageRequest]) (*connect.Response[managev1.PageLifecycleMutationResponse], error)
	DeletePage(context.Context, *connect.Request[managev1.DeletePageRequest]) (*connect.Response[managev1.DeleteResponse], error)
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

func (*ContentManagementTools) ToolNames() []string {
	return toolDefinitionNames(contentManagementTools)
}
func (*ContentManagementTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return cloneToolDefinitions(contentManagementTools), nil
}

func (tools *ContentManagementTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	switch name {
	case ToolPostCreate:
		return tools.createPost(ctx, arguments)
	case ToolPostSettingsGet:
		return tools.getPostSettings(ctx, arguments)
	case ToolPostSettingsUpdate:
		return tools.updatePost(ctx, arguments)
	case ToolPostPublish, ToolPostUnpublish, ToolPostArchive, ToolPostScheduleCancel, ToolPostRepublish, ToolPostDelete:
		return tools.mutatePost(ctx, name, arguments)
	case ToolPostSchedule:
		return tools.schedulePost(ctx, arguments)
	case ToolWorkCreate:
		return tools.createWork(ctx, arguments)
	case ToolWorkSettingsGet:
		return tools.getWorkSettings(ctx, arguments)
	case ToolWorkSettingsUpdate:
		return tools.updateWork(ctx, arguments)
	case ToolWorkPublish, ToolWorkUnpublish, ToolWorkDelete:
		return tools.mutateWork(ctx, name, arguments)
	case ToolPageCreate:
		return tools.createPage(ctx, arguments)
	case ToolPageSettingsGet:
		return tools.getPageSettings(ctx, arguments)
	case ToolPageSettingsUpdate:
		return tools.updatePage(ctx, arguments)
	case ToolPagePublish, ToolPageUnpublish, ToolPageDelete:
		return tools.mutatePage(ctx, name, arguments)
	default:
		return mcpserver.ToolResult{}, mcpserver.ErrUnknownTool
	}
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

func (tools *ContentManagementTools) createPost(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input postCreateArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	commentsEnabled := true
	if input.CommentsEnabled != nil {
		commentsEnabled = *input.CommentsEnabled
	}
	request := connect.NewRequest(&managev1.CreatePostRequest{
		Title: input.Title, Slug: input.Slug, Summary: input.Summary, CommentsEnabled: commentsEnabled,
		CategoryIds: input.CategoryIDs, TagIds: input.TagIDs, MapPlaceId: input.MapPlaceID, SourceLocale: input.SourceLocale,
		Document: emptyRichTextDocument(contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST, input.SourceLocale),
	})
	request.Header().Set("Accept-Language", input.SourceLocale)
	created, err := tools.posts.CreatePost(ctx, request)
	if err != nil {
		return expectedToolError(err)
	}
	return contentResult(map[string]any{
		"document_type": "post", "document_id": created.Msg.Id, "changed": true, "title": created.Msg.Title,
		"slug": optionalStringValue(created.Msg.Slug), "source_locale": created.Msg.SourceLocale,
		"status": contentStatus(created.Msg.Status.String(), "POST_STATUS_"), "document_revision": created.Msg.Revision,
		"configuration_revision": created.Msg.ConfigurationRevision,
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
	ContentHeight string `json:"content_height"`
	PageChrome    string `json:"page_chrome"`
	Footer        string `json:"footer"`
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
	return &commonv1.DocumentLayout{ContentHeight: commonv1.DocumentContentHeight(height), PageChrome: commonv1.DocumentRegionPlacement(chrome), Footer: commonv1.DocumentRegionPlacement(footer)}, nil
}

func documentLayoutOutput(layout *commonv1.DocumentLayout) any {
	if layout == nil {
		return nil
	}
	return documentLayoutArguments{ContentHeight: layout.ContentHeight.String(), PageChrome: layout.PageChrome.String(), Footer: layout.Footer.String()}
}

func (tools *ContentManagementTools) getPostSettings(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input contentIDArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	response, err := tools.posts.GetPost(ctx, connect.NewRequest(&managev1.GetPostRequest{Id: input.DocumentID}))
	if err != nil {
		return expectedToolError(err)
	}
	post := response.Msg
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

func (tools *ContentManagementTools) updatePost(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	if err := rejectNullArguments(arguments, "document_layout"); err != nil {
		return executionError(err)
	}
	var input postSettingsArguments
	if err := decodeArguments(arguments, &input); err != nil {
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
	updated, err := tools.posts.UpdatePost(ctx, connect.NewRequest(&managev1.UpdatePostRequest{
		Id: input.DocumentID, Slug: input.Slug, CommentsEnabled: input.CommentsEnabled,
		MapPlaceId: input.MapPlaceID, DocumentLayout: layout, ExpectedConfigurationRevision: input.ExpectedConfigurationRevision,
	}))
	if err != nil {
		return expectedToolError(err)
	}
	return contentResult(map[string]any{
		"document_type": "post", "document_id": updated.Msg.Id, "changed": updated.Msg.Changed,
		"slug": optionalStringValue(updated.Msg.Slug), "updated_at": timestampString(updated.Msg.UpdatedAt),
		"configuration_revision": updated.Msg.ConfigurationRevision,
		"comments_enabled":       updated.Msg.CommentsEnabled, "map_place_id": optionalStringValue(updated.Msg.MapPlaceId),
		"document_layout": documentLayoutOutput(updated.Msg.DocumentLayout),
	})
}

func (tools *ContentManagementTools) mutatePost(ctx context.Context, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input contentIDArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	if name == ToolPostDelete {
		deleted, err := tools.posts.DeletePost(ctx, connect.NewRequest(&managev1.DeletePostRequest{Id: input.DocumentID}))
		if err != nil {
			return expectedToolError(err)
		}
		return contentResult(map[string]any{"document_type": "post", "document_id": input.DocumentID, "changed": deleted.Msg.Success, "deleted": deleted.Msg.Success})
	}
	var result *connect.Response[managev1.PostLifecycleMutationResponse]
	var err error
	switch name {
	case ToolPostPublish:
		result, err = tools.posts.PublishPost(ctx, connect.NewRequest(&managev1.PublishPostRequest{Id: input.DocumentID}))
	case ToolPostUnpublish:
		result, err = tools.posts.UnpublishPost(ctx, connect.NewRequest(&managev1.UnpublishPostRequest{Id: input.DocumentID}))
	case ToolPostArchive:
		result, err = tools.posts.ArchivePost(ctx, connect.NewRequest(&managev1.ArchivePostRequest{Id: input.DocumentID}))
	case ToolPostScheduleCancel:
		result, err = tools.posts.CancelPostSchedule(ctx, connect.NewRequest(&managev1.CancelPostScheduleRequest{Id: input.DocumentID}))
	case ToolPostRepublish:
		result, err = tools.posts.RepublishPost(ctx, connect.NewRequest(&managev1.RepublishPostRequest{Id: input.DocumentID}))
	}
	if err != nil {
		return expectedToolError(err)
	}
	return postLifecycleResult(result.Msg)
}

type postScheduleArguments struct {
	DocumentID        string `json:"document_id"`
	ScheduledAt       string `json:"scheduled_at"`
	ScheduledTimeZone string `json:"scheduled_time_zone"`
}

func (tools *ContentManagementTools) schedulePost(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input postScheduleArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	scheduledAt, err := time.Parse(time.RFC3339, input.ScheduledAt)
	if err != nil {
		return executionError(fmt.Errorf("scheduled_at must be RFC 3339: %w", err))
	}
	result, err := tools.posts.SchedulePost(ctx, connect.NewRequest(&managev1.SchedulePostRequest{Id: input.DocumentID, ScheduledAt: timestamppb.New(scheduledAt), ScheduledTimeZone: input.ScheduledTimeZone}))
	if err != nil {
		return expectedToolError(err)
	}
	return postLifecycleResult(result.Msg)
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

func (tools *ContentManagementTools) createWork(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input workCreateArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	metadata, err := optionalStruct(input.Metadata)
	if err != nil {
		return executionError(err)
	}
	workType, err := parseWorkType(input.Type)
	if err != nil {
		return executionError(err)
	}
	request := connect.NewRequest(&managev1.CreateWorkRequest{Title: input.Title, Slug: input.Slug, Type: workType, Summary: input.Summary, Metadata: metadata, Featured: input.Featured, Year: input.Year, Month: input.Month, UntilYear: input.UntilYear, UntilMonth: input.UntilMonth, IsPresent: input.IsPresent, SourceLocale: input.SourceLocale, Document: emptyRichTextDocument(contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK, input.SourceLocale)})
	request.Header().Set("Accept-Language", input.SourceLocale)
	created, err := tools.works.CreateWork(ctx, request)
	if err != nil {
		return expectedToolError(err)
	}
	return contentResult(map[string]any{"document_type": "work", "document_id": created.Msg.Id, "changed": true, "title": created.Msg.Title, "slug": optionalStringValue(created.Msg.Slug), "source_locale": created.Msg.SourceLocale, "status": contentStatus(created.Msg.Status.String(), "WORK_STATUS_"), "document_revision": created.Msg.Revision})
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

func (tools *ContentManagementTools) getWorkSettings(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input contentIDArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	response, err := tools.works.GetWork(ctx, connect.NewRequest(&managev1.GetWorkRequest{Id: input.DocumentID}))
	if err != nil {
		return expectedToolError(err)
	}
	work := response.Msg
	metadata := map[string]any{}
	if work.Metadata != nil {
		metadata = work.Metadata.AsMap()
	}
	clientIDs := make([]string, 0, len(work.Clients))
	for _, client := range work.Clients {
		if client != nil {
			clientIDs = append(clientIDs, client.Id)
		}
	}
	output := map[string]any{
		"document_type": "work", "document_id": work.Id,
		"metadata": metadata, "client_ids": clientIDs, "slug": optionalStringValue(work.Slug),
		"type": contentStatus(work.Type.String(), "WORK_TYPE_"), "featured": work.Featured,
		"year": work.Year, "month": work.Month, "is_present": work.IsPresent,
		"map_place_id": optionalStringValue(work.MapPlaceId), "document_revision": work.Revision,
	}
	if work.UntilYear != nil {
		output["until_year"] = *work.UntilYear
	}
	if work.UntilMonth != nil {
		output["until_month"] = *work.UntilMonth
	}
	return contentResult(output)
}

func (tools *ContentManagementTools) updateWork(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input workSettingsArguments
	if err := decodeArguments(arguments, &input); err != nil {
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
	request := &managev1.UpdateWorkRequest{Id: input.DocumentID, Slug: input.Slug, Featured: input.Featured, Year: input.Year, Month: input.Month, MapPlaceId: input.MapPlaceID, UntilYear: input.UntilYear, UntilMonth: input.UntilMonth, IsPresent: input.IsPresent}
	var err error
	request.Metadata, err = optionalStruct(input.Metadata)
	if err != nil {
		return executionError(err)
	}
	request.ObservedMetadata, err = optionalStruct(input.ObservedMetadata)
	if err != nil {
		return executionError(err)
	}
	if input.Type != nil {
		parsed, parseErr := parseWorkType(*input.Type)
		if parseErr != nil {
			return executionError(parseErr)
		}
		request.Type = &parsed
	}
	if input.ClientIDs != nil {
		request.Clients = &managev1.WorkClientsUpdate{ClientIds: *input.ClientIDs}
	}
	if input.ObservedClientIDs != nil {
		request.ObservedClients = &managev1.WorkClientsUpdate{ClientIds: *input.ObservedClientIDs}
	}
	updated, err := tools.works.UpdateWork(ctx, connect.NewRequest(request))
	if err != nil {
		return expectedToolError(err)
	}
	return contentResult(map[string]any{"document_type": "work", "document_id": updated.Msg.Id, "changed": updated.Msg.Changed, "slug": optionalStringValue(updated.Msg.Slug), "updated_at": timestampString(updated.Msg.UpdatedAt)})
}

func (tools *ContentManagementTools) mutateWork(ctx context.Context, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input contentIDArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	if name == ToolWorkDelete {
		deleted, err := tools.works.DeleteWork(ctx, connect.NewRequest(&managev1.DeleteWorkRequest{Id: input.DocumentID}))
		if err != nil {
			return expectedToolError(err)
		}
		return contentResult(map[string]any{"document_type": "work", "document_id": input.DocumentID, "changed": deleted.Msg.Success, "deleted": deleted.Msg.Success})
	}
	var result *connect.Response[managev1.WorkLifecycleMutationResponse]
	var err error
	if name == ToolWorkPublish {
		result, err = tools.works.PublishWork(ctx, connect.NewRequest(&managev1.PublishWorkRequest{Id: input.DocumentID}))
	} else {
		result, err = tools.works.UnpublishWork(ctx, connect.NewRequest(&managev1.UnpublishWorkRequest{Id: input.DocumentID}))
	}
	if err != nil {
		return expectedToolError(err)
	}
	return contentResult(map[string]any{"document_type": "work", "document_id": result.Msg.Id, "changed": result.Msg.Changed, "status": contentStatus(result.Msg.Status.String(), "WORK_STATUS_"), "updated_at": timestampString(result.Msg.UpdatedAt)})
}

type pageCreateArguments struct {
	Title        string  `json:"title"`
	SourceLocale string  `json:"source_locale"`
	Slug         *string `json:"slug,omitempty"`
	Summary      *string `json:"summary,omitempty"`
	ShowTitle    *bool   `json:"show_title,omitempty"`
}

func (tools *ContentManagementTools) createPage(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input pageCreateArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	request := connect.NewRequest(&managev1.CreatePageRequest{Title: input.Title, Slug: input.Slug, Summary: input.Summary, ShowTitle: input.ShowTitle, SourceLocale: input.SourceLocale})
	request.Header().Set("Accept-Language", input.SourceLocale)
	created, err := tools.pages.CreatePage(ctx, request)
	if err != nil {
		return expectedToolError(err)
	}
	return contentResult(map[string]any{"document_type": "page", "document_id": created.Msg.Id, "changed": true, "title": created.Msg.Title, "slug": optionalStringValue(created.Msg.Slug), "source_locale": created.Msg.SourceLocale, "status": contentStatus(created.Msg.Status.String(), "PAGE_STATUS_"), "document_revision": created.Msg.Revision})
}

type pageSettingsArguments struct {
	DocumentID string  `json:"document_id"`
	Slug       *string `json:"slug,omitempty"`
	ShowTitle  *bool   `json:"show_title,omitempty"`
}

func (tools *ContentManagementTools) getPageSettings(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input contentIDArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	response, err := tools.pages.GetPage(ctx, connect.NewRequest(&managev1.GetPageRequest{Id: input.DocumentID}))
	if err != nil {
		return expectedToolError(err)
	}
	page := response.Msg
	output := map[string]any{
		"document_type": "page", "document_id": page.Id, "title": page.Title, "summary": optionalStringValue(page.Summary),
		"slug": optionalStringValue(page.Slug), "source_locale": page.SourceLocale, "status": contentStatus(page.Status.String(), "PAGE_STATUS_"),
		"document_revision": page.Revision, "show_title": page.ShowTitle, "document_layout": documentLayoutOutput(page.DocumentLayout),
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

func (tools *ContentManagementTools) updatePage(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input pageSettingsArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	if input.Slug == nil && input.ShowTitle == nil {
		return executionError(errors.New("at least one Page setting is required"))
	}
	updated, err := tools.pages.UpdatePage(ctx, connect.NewRequest(&managev1.UpdatePageRequest{Id: input.DocumentID, Slug: input.Slug, ShowTitle: input.ShowTitle}))
	if err != nil {
		return expectedToolError(err)
	}
	return contentResult(map[string]any{"document_type": "page", "document_id": updated.Msg.Id, "changed": updated.Msg.Changed, "slug": optionalStringValue(updated.Msg.Slug), "show_title": updated.Msg.ShowTitle, "updated_at": timestampString(updated.Msg.UpdatedAt)})
}

func (tools *ContentManagementTools) mutatePage(ctx context.Context, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input contentIDArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	if name == ToolPageDelete {
		deleted, err := tools.pages.DeletePage(ctx, connect.NewRequest(&managev1.DeletePageRequest{Id: input.DocumentID}))
		if err != nil {
			return expectedToolError(err)
		}
		return contentResult(map[string]any{"document_type": "page", "document_id": input.DocumentID, "changed": deleted.Msg.Success, "deleted": deleted.Msg.Success})
	}
	var result *connect.Response[managev1.PageLifecycleMutationResponse]
	var err error
	if name == ToolPagePublish {
		result, err = tools.pages.PublishPage(ctx, connect.NewRequest(&managev1.PublishPageRequest{Id: input.DocumentID}))
	} else {
		result, err = tools.pages.UnpublishPage(ctx, connect.NewRequest(&managev1.UnpublishPageRequest{Id: input.DocumentID}))
	}
	if err != nil {
		return expectedToolError(err)
	}
	return contentResult(map[string]any{"document_type": "page", "document_id": result.Msg.Id, "changed": result.Msg.Changed, "status": contentStatus(result.Msg.Status.String(), "PAGE_STATUS_"), "updated_at": timestampString(result.Msg.UpdatedAt)})
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

func optionalStruct(value *map[string]any) (*structpb.Struct, error) {
	if value == nil {
		return nil, nil
	}
	result, err := structpb.NewStruct(*value)
	if err != nil {
		return nil, fmt.Errorf("metadata: %w", err)
	}
	return result, nil
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
