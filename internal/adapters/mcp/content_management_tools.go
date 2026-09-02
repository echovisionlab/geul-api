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
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	ToolPostCreate         = "post_create"
	ToolPostSettingsUpdate = "post_settings_update"
	ToolPostPublish        = "post_publish"
	ToolPostUnpublish      = "post_unpublish"
	ToolPostArchive        = "post_archive"
	ToolPostSchedule       = "post_schedule"
	ToolPostScheduleCancel = "post_schedule_cancel"
	ToolPostRepublish      = "post_republish"
	ToolPostDelete         = "post_delete"
	ToolWorkCreate         = "work_create"
	ToolWorkSettingsUpdate = "work_settings_update"
	ToolWorkPublish        = "work_publish"
	ToolWorkUnpublish      = "work_unpublish"
	ToolWorkDelete         = "work_delete"
	ToolPageCreate         = "page_create"
	ToolPageSettingsUpdate = "page_settings_update"
	ToolPagePublish        = "page_publish"
	ToolPageUnpublish      = "page_unpublish"
	ToolPageDelete         = "page_delete"
)

var contentManagementTools = []mcpserver.Tool{
	contentTool(ToolPostCreate, "Create Post", "Create a new draft Post with an empty typed document in the requested source locale.", postCreateInputJSONSchema, false),
	contentTool(ToolPostSettingsUpdate, "Update Post settings", "Update Post slug, comment setting, or Map Place relation. Use document_metadata_update for title, summary, categories, or tags.", postSettingsUpdateInputJSONSchema, true),
	contentTool(ToolPostPublish, "Publish Post", "Publish a draft Post immediately using the existing Post lifecycle rules.", contentIDInputJSONSchema, true),
	contentTool(ToolPostUnpublish, "Unpublish Post", "Move a published Post back to draft.", contentIDInputJSONSchema, true),
	contentTool(ToolPostArchive, "Archive Post", "Archive a published Post. An archived Post must be republished before deletion.", contentIDInputJSONSchema, true),
	contentTool(ToolPostSchedule, "Schedule Post", "Schedule a Post for publication at an RFC 3339 instant and IANA time zone.", postScheduleInputJSONSchema, true),
	contentTool(ToolPostScheduleCancel, "Cancel Post schedule", "Cancel the current Post publication schedule.", contentIDInputJSONSchema, true),
	contentTool(ToolPostRepublish, "Republish Post", "Move an archived Post back to published.", contentIDInputJSONSchema, true),
	contentTool(ToolPostDelete, "Delete Post", "Permanently delete a Post allowed by its current lifecycle state.", contentIDInputJSONSchema, true),
	contentTool(ToolWorkCreate, "Create Work", "Create a new draft Work with an empty typed document in the requested source locale.", workCreateInputJSONSchema, false),
	contentTool(ToolWorkSettingsUpdate, "Update Work settings", "Update Work slug, type, metadata, featured flag, clients, period, or Map Place relation. Use document_metadata_update for title or summary.", workSettingsUpdateInputJSONSchema, true),
	contentTool(ToolWorkPublish, "Publish Work", "Publish a draft Work or restore a legacy archived Work to published.", contentIDInputJSONSchema, true),
	contentTool(ToolWorkUnpublish, "Unpublish Work", "Move a published Work back to draft.", contentIDInputJSONSchema, true),
	contentTool(ToolWorkDelete, "Delete Work", "Permanently delete a Work allowed by its current lifecycle state.", contentIDInputJSONSchema, true),
	contentTool(ToolPageCreate, "Create Page", "Create a new draft Page with an empty typed Page document in the requested source locale.", pageCreateInputJSONSchema, false),
	contentTool(ToolPageSettingsUpdate, "Update Page settings", "Update Page slug or show-title setting. Use document_metadata_update for title or summary.", pageSettingsUpdateInputJSONSchema, true),
	contentTool(ToolPagePublish, "Publish Page", "Publish a draft Page using the existing Page publication blockers.", contentIDInputJSONSchema, true),
	contentTool(ToolPageUnpublish, "Unpublish Page", "Move a published Page back to draft.", contentIDInputJSONSchema, true),
	contentTool(ToolPageDelete, "Delete Page", "Permanently delete a Page and its owned Page state using the existing Page deletion transaction.", contentIDInputJSONSchema, true),
}

func contentTool(name, title, description, inputSchema string, destructive bool) mcpserver.Tool {
	return mcpserver.Tool{
		Name: name, Title: title, Description: description,
		InputSchema: json.RawMessage(inputSchema), OutputSchema: json.RawMessage(contentMutationOutputJSONSchema),
		SecuritySchemes: oauthSecuritySchemes(), Annotations: toolAnnotations(false, destructive, false), Meta: oauthSecurityMeta(),
	}
}

type PostManagementApplication interface {
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
	CreateWork(context.Context, *connect.Request[managev1.CreateWorkRequest]) (*connect.Response[managev1.Work], error)
	UpdateWork(context.Context, *connect.Request[managev1.UpdateWorkRequest]) (*connect.Response[managev1.UpdateWorkResponse], error)
	PublishWork(context.Context, *connect.Request[managev1.PublishWorkRequest]) (*connect.Response[managev1.WorkLifecycleMutationResponse], error)
	UnpublishWork(context.Context, *connect.Request[managev1.UnpublishWorkRequest]) (*connect.Response[managev1.WorkLifecycleMutationResponse], error)
	DeleteWork(context.Context, *connect.Request[managev1.DeleteWorkRequest]) (*connect.Response[managev1.DeleteResponse], error)
}

type PageManagementApplication interface {
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
	case ToolPostSettingsUpdate:
		return tools.updatePost(ctx, arguments)
	case ToolPostPublish, ToolPostUnpublish, ToolPostArchive, ToolPostScheduleCancel, ToolPostRepublish, ToolPostDelete:
		return tools.mutatePost(ctx, name, arguments)
	case ToolPostSchedule:
		return tools.schedulePost(ctx, arguments)
	case ToolWorkCreate:
		return tools.createWork(ctx, arguments)
	case ToolWorkSettingsUpdate:
		return tools.updateWork(ctx, arguments)
	case ToolWorkPublish, ToolWorkUnpublish, ToolWorkDelete:
		return tools.mutateWork(ctx, name, arguments)
	case ToolPageCreate:
		return tools.createPage(ctx, arguments)
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
	})
}

type postSettingsArguments struct {
	DocumentID      string  `json:"document_id"`
	Slug            *string `json:"slug,omitempty"`
	CommentsEnabled *bool   `json:"comments_enabled,omitempty"`
	MapPlaceID      *string `json:"map_place_id,omitempty"`
}

func (tools *ContentManagementTools) updatePost(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input postSettingsArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	if input.Slug == nil && input.CommentsEnabled == nil && input.MapPlaceID == nil {
		return executionError(errors.New("at least one Post setting is required"))
	}
	updated, err := tools.posts.UpdatePost(ctx, connect.NewRequest(&managev1.UpdatePostRequest{Id: input.DocumentID, Slug: input.Slug, CommentsEnabled: input.CommentsEnabled, MapPlaceId: input.MapPlaceID}))
	if err != nil {
		return expectedToolError(err)
	}
	return contentResult(map[string]any{"document_type": "post", "document_id": updated.Msg.Id, "changed": updated.Msg.Changed, "slug": optionalStringValue(updated.Msg.Slug), "updated_at": timestampString(updated.Msg.UpdatedAt)})
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
	DocumentID string          `json:"document_id"`
	Slug       *string         `json:"slug,omitempty"`
	Type       *string         `json:"type,omitempty"`
	Metadata   *map[string]any `json:"metadata,omitempty"`
	Featured   *bool           `json:"featured,omitempty"`
	ClientIDs  *[]string       `json:"client_ids,omitempty"`
	Year       *int32          `json:"year,omitempty"`
	Month      *int32          `json:"month,omitempty"`
	MapPlaceID *string         `json:"map_place_id,omitempty"`
	UntilYear  *int32          `json:"until_year,omitempty"`
	UntilMonth *int32          `json:"until_month,omitempty"`
	IsPresent  *bool           `json:"is_present,omitempty"`
}

func (tools *ContentManagementTools) updateWork(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input workSettingsArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	if input.Slug == nil && input.Type == nil && input.Metadata == nil && input.Featured == nil && input.ClientIDs == nil && input.Year == nil && input.Month == nil && input.MapPlaceID == nil && input.UntilYear == nil && input.UntilMonth == nil && input.IsPresent == nil {
		return executionError(errors.New("at least one Work setting is required"))
	}
	request := &managev1.UpdateWorkRequest{Id: input.DocumentID, Slug: input.Slug, Featured: input.Featured, Year: input.Year, Month: input.Month, MapPlaceId: input.MapPlaceID, UntilYear: input.UntilYear, UntilMonth: input.UntilMonth, IsPresent: input.IsPresent}
	var err error
	request.Metadata, err = optionalStruct(input.Metadata)
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
	return contentResult(map[string]any{"document_type": "page", "document_id": updated.Msg.Id, "changed": updated.Msg.Changed, "slug": optionalStringValue(updated.Msg.Slug), "updated_at": timestampString(updated.Msg.UpdatedAt)})
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
