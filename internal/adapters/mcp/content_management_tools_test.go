package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"connectrpc.com/connect"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestContentManagementToolDescriptors(t *testing.T) {
	tools, err := NewContentManagementTools(&recordingPostManagement{}, &recordingWorkManagement{}, &recordingPageManagement{})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != len(contentManagementTools) || len(listed) != 22 {
		t.Fatalf("listed %d content tools", len(listed))
	}
	for _, tool := range listed {
		assertMCPToolOAuthSecurity(t, tool)
		for _, schema := range []json.RawMessage{tool.InputSchema, tool.OutputSchema} {
			var object map[string]any
			if err := json.Unmarshal(schema, &object); err != nil || object["type"] != "object" {
				t.Fatalf("%s invalid schema: %v", tool.Name, err)
			}
		}
		if tool.Name == ToolPostSettingsUpdate {
			var inputSchema struct {
				Required   []string                  `json:"required"`
				Properties map[string]map[string]any `json:"properties"`
			}
			if err := json.Unmarshal(tool.InputSchema, &inputSchema); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(inputSchema.Required, []string{"document_id", "expected_configuration_revision"}) {
				t.Fatalf("Post settings required input = %#v", inputSchema.Required)
			}
			if inputSchema.Properties["expected_configuration_revision"]["format"] != "uuid" {
				t.Fatalf("Post settings revision schema = %#v", inputSchema.Properties["expected_configuration_revision"])
			}
			var outputSchema struct {
				Required []string `json:"required"`
			}
			if err := json.Unmarshal(tool.OutputSchema, &outputSchema); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(outputSchema.Required, []string{"document_type", "document_id", "changed", "configuration_revision"}) {
				t.Fatalf("Post settings required output = %#v", outputSchema.Required)
			}
		}
	}
}

func TestWorkCreateSchemaAdvertisesExactDateRangeAlternatives(t *testing.T) {
	var schema struct {
		OneOf []map[string]any `json:"oneOf"`
	}
	if err := json.Unmarshal([]byte(workCreateInputJSONSchema), &schema); err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{
		{
			"required":   []any{"is_present"},
			"properties": map[string]any{"is_present": map[string]any{"const": true}},
			"not": map[string]any{"anyOf": []any{
				map[string]any{"required": []any{"until_year"}},
				map[string]any{"required": []any{"until_month"}},
			}},
		},
		{
			"required":   []any{"until_year", "until_month"},
			"properties": map[string]any{"is_present": map[string]any{"const": false}},
		},
	}
	if !reflect.DeepEqual(schema.OneOf, want) {
		t.Fatalf("Work create date alternatives = %#v, want %#v", schema.OneOf, want)
	}
}

func TestContentManagementPostAndPageSettingsReadReturnSourceState(t *testing.T) {
	layout := &commonv1.DocumentLayout{ContentHeight: commonv1.DocumentContentHeight_DOCUMENT_CONTENT_HEIGHT_VIEWPORT, PageChrome: commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_PINNED, Footer: commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_FLOW}
	slug, summary, zone := "source-slug", "source-summary", "Asia/Seoul"
	mapPlaceID := managementPageID
	stamp := timestamppb.New(time.Date(2026, 10, 5, 1, 2, 3, 0, time.UTC))
	seriesOrder := int32(4)
	posts := &recordingPostManagement{getResponse: &managev1.Post{
		Id: managementPostID, Title: "Source Post", Summary: &summary, Slug: &slug, SourceLocale: "ko", Status: managev1.PostStatus_POST_STATUS_SCHEDULED,
		Revision: "actual-source-post-revision", ConfigurationRevision: managementPostConfigurationRevision, CommentsEnabled: false, MapPlaceId: &mapPlaceID,
		DocumentLayout: layout, Categories: []*managev1.Category{{Id: managementWorkID}}, Tags: []*managev1.Tag{{Id: managementPageID}}, Series: &managev1.Series{Id: managementWorkID}, SeriesOrder: &seriesOrder,
		AllowedActions: []managev1.PostAction{managev1.PostAction_POST_ACTION_EDIT, managev1.PostAction_POST_ACTION_CANCEL_SCHEDULE}, ScheduledAt: stamp, ScheduledTimeZone: &zone, PublishedAt: stamp, CreatedAt: stamp, UpdatedAt: stamp,
		Document: &contentv1.RichTextDocument{}, FeaturedImageDelivery: &commonv1.MediaDelivery{FileId: managementWorkID},
	}}
	pages := &recordingPageManagement{getResponse: &managev1.Page{
		Id: managementPageID, Title: "Source Page", Summary: &summary, Slug: &slug, SourceLocale: "ko", Status: managev1.PageStatus_PAGE_STATUS_PUBLISHED,
		Revision: "actual-source-page-revision", ShowTitle: false, DocumentLayout: layout, PublishedAt: stamp, CreatedAt: stamp, UpdatedAt: stamp,
		Document: &contentv1.PageDocument{}, FeaturedImageDelivery: &commonv1.MediaDelivery{FileId: managementWorkID},
	}}
	tools, err := NewContentManagementTools(posts, &recordingWorkManagement{}, pages)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	if err != nil {
		t.Fatal(err)
	}
	wantLayout := map[string]any{"content_height": "DOCUMENT_CONTENT_HEIGHT_VIEWPORT", "page_chrome": "DOCUMENT_REGION_PLACEMENT_PINNED", "footer": "DOCUMENT_REGION_PLACEMENT_FLOW"}
	for _, test := range []struct {
		name, id string
		want     map[string]any
	}{
		{ToolPostSettingsGet, managementPostID, map[string]any{"document_revision": "actual-source-post-revision", "configuration_revision": managementPostConfigurationRevision, "comments_enabled": false, "map_place_id": managementPageID, "status": "scheduled", "category_ids": []any{managementWorkID}, "tag_ids": []any{managementPageID}, "allowed_actions": []any{"edit", "cancel_schedule"}, "scheduled_time_zone": zone, "scheduled_at": "2026-10-05T01:02:03Z", "series_id": managementWorkID, "series_order": float64(4)}},
		{ToolPageSettingsGet, managementPageID, map[string]any{"document_revision": "actual-source-page-revision", "show_title": false, "status": "published"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, test.name, toolArguments(t, `{"document_id":"`+test.id+`"}`))
			if err != nil {
				t.Fatal(err)
			}
			test.want["summary"], test.want["slug"], test.want["source_locale"], test.want["document_layout"] = summary, slug, "ko", wantLayout
			test.want["published_at"], test.want["featured_image_file_id"] = "2026-10-05T01:02:03Z", managementWorkID
			for key, want := range test.want {
				if !reflect.DeepEqual(result.StructuredContent[key], want) {
					t.Fatalf("%s=%+v, want %+v", key, result.StructuredContent[key], want)
				}
			}
			if _, dumped := result.StructuredContent["document"]; dumped {
				t.Fatal("native document leaked into settings")
			}
			var text map[string]any
			if err := json.Unmarshal([]byte(result.Content[0]["text"].(string)), &text); err != nil || !reflect.DeepEqual(text, result.StructuredContent) {
				t.Fatalf("text/structured parity: %v %+v", err, result)
			}
			for _, tool := range listed {
				if tool.Name == test.name {
					assertMCPToolAnnotations(t, tool, toolAnnotations(true, false, false))
					var schema struct {
						Required   []string                   `json:"required"`
						Properties map[string]json.RawMessage `json:"properties"`
					}
					if err := json.Unmarshal(tool.OutputSchema, &schema); err != nil {
						t.Fatal(err)
					}
					for _, key := range schema.Required {
						if _, present := text[key]; !present {
							t.Fatalf("required output %s absent", key)
						}
					}
					for key := range text {
						if _, advertised := schema.Properties[key]; !advertised {
							t.Fatalf("output %s not advertised", key)
						}
					}
				}
			}
		})
	}
	if posts.get.Msg.Id != managementPostID || pages.get.Msg.Id != managementPageID {
		t.Fatalf("getters changed exact target: %+v %+v", posts.get, pages.get)
	}
}

func TestContentManagementSettingsReadPreservesOwningPermissionErrors(t *testing.T) {
	for _, name := range []string{ToolPostSettingsGet, ToolPageSettingsGet} {
		for _, code := range []connect.Code{connect.CodePermissionDenied, connect.CodeNotFound} {
			t.Run(name+"/"+code.String(), func(t *testing.T) {
				ownerErr := connect.NewError(code, errors.New("settings are not accessible"))
				tools, err := NewContentManagementTools(&recordingPostManagement{getErr: ownerErr}, &recordingWorkManagement{}, &recordingPageManagement{getErr: ownerErr})
				if err != nil {
					t.Fatal(err)
				}
				result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, name, toolArguments(t, `{"document_id":"`+managementPostID+`"}`))
				var executionErr *mcpserver.ToolExecutionError
				if !errors.As(err, &executionErr) || executionErr.Message != "settings are not accessible" || result.StructuredContent != nil {
					t.Fatalf("permission error=%v result=%+v", err, result)
				}
			})
		}
	}
}

func TestContentManagementCreateToolsBuildExactDomainRequests(t *testing.T) {
	posts := &recordingPostManagement{}
	works := &recordingWorkManagement{}
	pages := &recordingPageManagement{}
	tools, err := NewContentManagementTools(posts, works, pages)
	if err != nil {
		t.Fatal(err)
	}

	postResult, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolPostCreate, toolArguments(t, `{"title":"Post","source_locale":"ko"}`))
	if err != nil {
		t.Fatal(err)
	}
	if posts.create.Header().Get("Accept-Language") != "ko" || !posts.create.Msg.CommentsEnabled {
		t.Fatalf("Post create = %#v", posts.create)
	}
	assertEmptyManagementDocument(t, posts.create.Msg.Document, contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST, "ko")
	if postResult.StructuredContent["document_id"] != managementPostID || postResult.StructuredContent["configuration_revision"] != managementPostConfigurationRevision {
		t.Fatalf("Post result = %#v", postResult.StructuredContent)
	}

	workResult, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolWorkCreate, toolArguments(t, `{"title":"Work","source_locale":"en","type":"portfolio","year":2026,"month":8,"until_year":2026,"until_month":8,"metadata":{"key":"value"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if works.create.Header().Get("Accept-Language") != "en" || works.create.Msg.Type != managev1.WorkType_WORK_TYPE_PORTFOLIO || works.create.Msg.Metadata.GetFields()["key"].GetStringValue() != "value" {
		t.Fatalf("Work create = %#v", works.create.Msg)
	}
	if works.create.Msg.UntilYear == nil || *works.create.Msg.UntilYear != 2026 || works.create.Msg.UntilMonth == nil || *works.create.Msg.UntilMonth != 8 {
		t.Fatalf("Work create range = %#v", works.create.Msg)
	}
	assertEmptyManagementDocument(t, works.create.Msg.Document, contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK, "en")
	if workResult.StructuredContent["document_id"] != managementWorkID {
		t.Fatalf("Work result = %#v", workResult.StructuredContent)
	}

	pageResult, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolPageCreate, toolArguments(t, `{"title":"Page","source_locale":"ja","show_title":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if pages.create.Header().Get("Accept-Language") != "ja" || pages.create.Msg.ShowTitle == nil || *pages.create.Msg.ShowTitle {
		t.Fatalf("Page create = %#v", pages.create.Msg)
	}
	if pageResult.StructuredContent["document_id"] != managementPageID {
		t.Fatalf("Page result = %#v", pageResult.StructuredContent)
	}
}

func TestPostSettingsUpdatePassesExactRevisionAndAcknowledgesPersistedRevision(t *testing.T) {
	posts := &recordingPostManagement{}
	tools, err := NewContentManagementTools(posts, &recordingWorkManagement{}, &recordingPageManagement{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolPostSettingsUpdate, toolArguments(t,
		`{"document_id":"`+managementPostID+`","expected_configuration_revision":"`+managementPostConfigurationRevision+`","slug":"updated-slug"}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if posts.updateCalls != 1 || posts.update == nil || posts.update.Msg.ExpectedConfigurationRevision != managementPostConfigurationRevision {
		t.Fatalf("UpdatePost request = %#v (calls %d)", posts.update, posts.updateCalls)
	}
	if posts.update.Msg.Slug == nil || *posts.update.Msg.Slug != "updated-slug" {
		t.Fatalf("UpdatePost settings = %#v", posts.update.Msg)
	}
	if result.StructuredContent["configuration_revision"] != managementPostConfigurationRevisionNext {
		t.Fatalf("Post settings result = %#v", result.StructuredContent)
	}
}

func TestPostSettingsLayoutUsesExactNativeEnumsAndAuthoritativeResponse(t *testing.T) {
	storedSlug, storedPlace := "stored-slug", managementWorkID
	storedLayout := &commonv1.DocumentLayout{ContentHeight: commonv1.DocumentContentHeight_DOCUMENT_CONTENT_HEIGHT_CONTENT, PageChrome: commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_FLOW, Footer: commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_FLOW}
	posts := &recordingPostManagement{updateResponse: &managev1.UpdatePostResponse{
		Id: managementPostID, Changed: false, Slug: &storedSlug, CommentsEnabled: false, MapPlaceId: &storedPlace, DocumentLayout: storedLayout, ConfigurationRevision: managementPostConfigurationRevision,
	}}
	tools, err := NewContentManagementTools(posts, &recordingWorkManagement{}, &recordingPageManagement{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolPostSettingsUpdate, toolArguments(t, `{"document_id":"`+managementPostID+`","expected_configuration_revision":"`+managementPostConfigurationRevision+`","comments_enabled":true,"document_layout":{"content_height":"DOCUMENT_CONTENT_HEIGHT_VIEWPORT","page_chrome":"DOCUMENT_REGION_PLACEMENT_PINNED","footer":"DOCUMENT_REGION_PLACEMENT_PINNED"}}`))
	if err != nil {
		t.Fatal(err)
	}
	request := posts.update.Msg
	if request.ExpectedConfigurationRevision != managementPostConfigurationRevision || request.DocumentLayout.ContentHeight != commonv1.DocumentContentHeight_DOCUMENT_CONTENT_HEIGHT_VIEWPORT || request.DocumentLayout.PageChrome != commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_PINNED || request.DocumentLayout.Footer != commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_PINNED {
		t.Fatalf("layout request=%+v", request)
	}
	if result.StructuredContent["changed"] != false || result.StructuredContent["comments_enabled"] != false || result.StructuredContent["slug"] != storedSlug || result.StructuredContent["map_place_id"] != storedPlace || result.StructuredContent["configuration_revision"] != managementPostConfigurationRevision {
		t.Fatalf("request values replaced owning response: %+v", result.StructuredContent)
	}
	layout := result.StructuredContent["document_layout"].(map[string]any)
	if layout["content_height"] != "DOCUMENT_CONTENT_HEIGHT_CONTENT" || layout["page_chrome"] != "DOCUMENT_REGION_PLACEMENT_FLOW" {
		t.Fatalf("layout response=%+v", layout)
	}
}

func TestPostSettingsLayoutSchemaAdvertisesExactNativeEnums(t *testing.T) {
	var schema struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(managementDocumentLayoutJSONSchema), &schema); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(schema.Required, []string{"content_height", "page_chrome", "footer"}) {
		t.Fatalf("layout required=%+v", schema.Required)
	}
	if !reflect.DeepEqual(schema.Properties["content_height"].Enum, []string{commonv1.DocumentContentHeight_DOCUMENT_CONTENT_HEIGHT_CONTENT.String(), commonv1.DocumentContentHeight_DOCUMENT_CONTENT_HEIGHT_VIEWPORT.String()}) {
		t.Fatalf("height schema=%+v", schema)
	}
	for _, field := range []string{"page_chrome", "footer"} {
		if !reflect.DeepEqual(schema.Properties[field].Enum, []string{commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_FLOW.String(), commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_PINNED.String()}) {
			t.Fatalf("%s schema=%+v", field, schema)
		}
	}
}

func TestPostSettingsLayoutRejectsInvalidOrPartialNativeEnumsBeforeMutation(t *testing.T) {
	for _, layout := range []string{
		`null`,
		`{"content_height":"viewport","page_chrome":"DOCUMENT_REGION_PLACEMENT_FLOW","footer":"DOCUMENT_REGION_PLACEMENT_FLOW"}`,
		`{"content_height":"DOCUMENT_CONTENT_HEIGHT_UNSPECIFIED","page_chrome":"DOCUMENT_REGION_PLACEMENT_FLOW","footer":"DOCUMENT_REGION_PLACEMENT_FLOW"}`,
		`{"content_height":"DOCUMENT_CONTENT_HEIGHT_CONTENT","page_chrome":"DOCUMENT_REGION_PLACEMENT_PINNED","footer":"pinned"}`,
		`{"content_height":"DOCUMENT_CONTENT_HEIGHT_CONTENT","page_chrome":"DOCUMENT_REGION_PLACEMENT_UNSPECIFIED","footer":"DOCUMENT_REGION_PLACEMENT_FLOW"}`,
		`{"content_height":"DOCUMENT_CONTENT_HEIGHT_CONTENT","page_chrome":"DOCUMENT_REGION_PLACEMENT_FLOW"}`,
		`{"content_height":2,"page_chrome":"DOCUMENT_REGION_PLACEMENT_FLOW","footer":"DOCUMENT_REGION_PLACEMENT_FLOW"}`,
	} {
		t.Run(layout, func(t *testing.T) {
			posts := &recordingPostManagement{}
			tools, err := NewContentManagementTools(posts, &recordingWorkManagement{}, &recordingPageManagement{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolPostSettingsUpdate, toolArguments(t, `{"document_id":"`+managementPostID+`","expected_configuration_revision":"`+managementPostConfigurationRevision+`","document_layout":`+layout+`}`))
			var executionErr *mcpserver.ToolExecutionError
			if !errors.As(err, &executionErr) || posts.updateCalls != 0 {
				t.Fatalf("invalid layout reached owner: error=%v calls=%d", err, posts.updateCalls)
			}
		})
	}
}

func TestPageSettingsUpdateReturnsOwningValuesAndNoopStatus(t *testing.T) {
	for _, changed := range []bool{true, false} {
		pages := &recordingPageManagement{updateResponse: &managev1.UpdatePageResponse{Id: managementPageID, Changed: changed, ShowTitle: false}}
		tools, err := NewContentManagementTools(&recordingPostManagement{}, &recordingWorkManagement{}, pages)
		if err != nil {
			t.Fatal(err)
		}
		result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolPageSettingsUpdate, toolArguments(t, `{"document_id":"`+managementPageID+`","show_title":true}`))
		if err != nil {
			t.Fatal(err)
		}
		if pages.update.Msg.ShowTitle == nil || !*pages.update.Msg.ShowTitle || result.StructuredContent["show_title"] != false || result.StructuredContent["changed"] != changed {
			t.Fatalf("request/owning response=%+v %+v", pages.update, result.StructuredContent)
		}
	}
}

func TestPostSettingsUpdateForwardsMissingMalformedAndStaleRevisionsSafely(t *testing.T) {
	cases := []struct {
		name         string
		arguments    string
		errorCode    connect.Code
		apiMessage   string
		wantRevision string
	}{
		{
			name:      "missing revision remains API precondition error",
			arguments: `{"document_id":"` + managementPostID + `","slug":"updated-slug"}`,
			errorCode: connect.CodeFailedPrecondition, apiMessage: "expected_configuration_revision is required",
			wantRevision: "",
		},
		{
			name:      "malformed revision remains API validation error",
			arguments: `{"document_id":"` + managementPostID + `","expected_configuration_revision":"bad-revision","slug":"updated-slug"}`,
			errorCode: connect.CodeInvalidArgument, apiMessage: "expected_configuration_revision must be a canonical UUID",
			wantRevision: "bad-revision",
		},
		{
			name:      "stale revision is returned without retry or rebase",
			arguments: `{"document_id":"` + managementPostID + `","expected_configuration_revision":"` + managementPostConfigurationRevision + `","slug":"updated-slug"}`,
			errorCode: connect.CodeAborted, apiMessage: "Post settings changed; reload before applying this update",
			wantRevision: managementPostConfigurationRevision,
		},
		{
			name:      "owning permission denial is preserved",
			arguments: `{"document_id":"` + managementPostID + `","expected_configuration_revision":"` + managementPostConfigurationRevision + `","comments_enabled":false}`,
			errorCode: connect.CodePermissionDenied, apiMessage: "post settings permission denied",
			wantRevision: managementPostConfigurationRevision,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			posts := &recordingPostManagement{updateErr: connect.NewError(test.errorCode, errors.New(test.apiMessage))}
			tools, err := NewContentManagementTools(posts, &recordingWorkManagement{}, &recordingPageManagement{})
			if err != nil {
				t.Fatal(err)
			}
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolPostSettingsUpdate, toolArguments(t, test.arguments))
			var executionErr *mcpserver.ToolExecutionError
			if result.Content != nil || !errors.As(err, &executionErr) || executionErr.Message != test.apiMessage {
				t.Fatalf("Post settings error result = %#v, error = %v", result, err)
			}
			if posts.updateCalls != 1 || posts.update == nil || posts.update.Msg.ExpectedConfigurationRevision != test.wantRevision {
				t.Fatalf("UpdatePost request = %#v (calls %d), want revision %q", posts.update, posts.updateCalls, test.wantRevision)
			}
		})
	}
}

func TestPostScheduleUsesExactTimestampAndLifecycleResponse(t *testing.T) {
	posts := &recordingPostManagement{}
	tools, err := NewContentManagementTools(posts, &recordingWorkManagement{}, &recordingPageManagement{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolPostSchedule, toolArguments(t, `{"document_id":"`+managementPostID+`","scheduled_at":"2026-09-01T03:00:00+09:00","scheduled_time_zone":"Asia/Seoul"}`))
	if err != nil {
		t.Fatal(err)
	}
	if posts.schedule.Msg.ScheduledAt.AsTime().UTC() != time.Date(2026, 8, 31, 18, 0, 0, 0, time.UTC) || posts.schedule.Msg.ScheduledTimeZone != "Asia/Seoul" {
		t.Fatalf("schedule request = %#v", posts.schedule.Msg)
	}
	if result.StructuredContent["status"] != "scheduled" {
		t.Fatalf("schedule result = %#v", result.StructuredContent)
	}
}

func assertEmptyManagementDocument(t *testing.T, document *contentv1.RichTextDocument, profile contentv1.RichTextProfile, locale string) {
	t.Helper()
	if document == nil || document.Profile != profile || document.SourceLocale != locale || document.BlockCatalogFingerprint != contentv1.ContentBlockCatalogFingerprint || document.Base == nil || len(document.Base.Nodes) != 0 || len(document.LocaleOverlays) != 1 || document.LocaleOverlays[0].Locale != locale {
		t.Fatalf("document = %#v", document)
	}
}

const (
	managementPostID                        = "11111111-1111-4111-8111-111111111111"
	managementPostConfigurationRevision     = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	managementPostConfigurationRevisionNext = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	managementWorkID                        = "22222222-2222-4222-8222-222222222222"
	managementPageID                        = "33333333-3333-4333-8333-333333333333"
)

type recordingPostManagement struct {
	managev1connect.UnimplementedPostServiceHandler
	create         *connect.Request[managev1.CreatePostRequest]
	update         *connect.Request[managev1.UpdatePostRequest]
	schedule       *connect.Request[managev1.SchedulePostRequest]
	updateErr      error
	updateCalls    int
	get            *connect.Request[managev1.GetPostRequest]
	getResponse    *managev1.Post
	getErr         error
	updateResponse *managev1.UpdatePostResponse
}

func (r *recordingPostManagement) GetPost(_ context.Context, req *connect.Request[managev1.GetPostRequest]) (*connect.Response[managev1.Post], error) {
	r.get = req
	if r.getErr != nil {
		return nil, r.getErr
	}
	return connect.NewResponse(r.getResponse), nil
}

func (r *recordingPostManagement) CreatePost(_ context.Context, req *connect.Request[managev1.CreatePostRequest]) (*connect.Response[managev1.Post], error) {
	r.create = req
	return connect.NewResponse(&managev1.Post{Id: managementPostID, Title: req.Msg.Title, SourceLocale: req.Msg.SourceLocale, Status: managev1.PostStatus_POST_STATUS_DRAFT, Revision: "post-revision", ConfigurationRevision: managementPostConfigurationRevision}), nil
}
func (r *recordingPostManagement) UpdatePost(_ context.Context, req *connect.Request[managev1.UpdatePostRequest]) (*connect.Response[managev1.UpdatePostResponse], error) {
	r.updateCalls++
	r.update = req
	if r.updateErr != nil {
		return nil, r.updateErr
	}
	if r.updateResponse != nil {
		return connect.NewResponse(r.updateResponse), nil
	}
	return connect.NewResponse(&managev1.UpdatePostResponse{
		Id: req.Msg.Id, Changed: true, Slug: req.Msg.Slug,
		ConfigurationRevision: managementPostConfigurationRevisionNext,
	}), nil
}
func (r *recordingPostManagement) SchedulePost(_ context.Context, req *connect.Request[managev1.SchedulePostRequest]) (*connect.Response[managev1.PostLifecycleMutationResponse], error) {
	r.schedule = req
	return connect.NewResponse(&managev1.PostLifecycleMutationResponse{Id: req.Msg.Id, Changed: true, Status: managev1.PostStatus_POST_STATUS_SCHEDULED, ScheduledAt: req.Msg.ScheduledAt, ScheduledTimeZone: &req.Msg.ScheduledTimeZone, UpdatedAt: timestamppb.Now()}), nil
}

type recordingWorkManagement struct {
	managev1connect.UnimplementedWorkServiceHandler
	create      *connect.Request[managev1.CreateWorkRequest]
	get         *connect.Request[managev1.GetWorkRequest]
	getResponse *managev1.Work
	getErr      error
	update      *connect.Request[managev1.UpdateWorkRequest]
}

func (r *recordingWorkManagement) GetWork(_ context.Context, req *connect.Request[managev1.GetWorkRequest]) (*connect.Response[managev1.Work], error) {
	r.get = req
	if r.getErr != nil {
		return nil, r.getErr
	}
	return connect.NewResponse(r.getResponse), nil
}

func (r *recordingWorkManagement) UpdateWork(_ context.Context, req *connect.Request[managev1.UpdateWorkRequest]) (*connect.Response[managev1.UpdateWorkResponse], error) {
	r.update = req
	return connect.NewResponse(&managev1.UpdateWorkResponse{Id: req.Msg.Id, Changed: true, UpdatedAt: timestamppb.Now()}), nil
}

func (r *recordingWorkManagement) CreateWork(_ context.Context, req *connect.Request[managev1.CreateWorkRequest]) (*connect.Response[managev1.Work], error) {
	r.create = req
	return connect.NewResponse(&managev1.Work{Id: managementWorkID, Title: req.Msg.Title, Type: req.Msg.Type, SourceLocale: req.Msg.SourceLocale, Status: managev1.WorkStatus_WORK_STATUS_DRAFT, Revision: "work-revision"}), nil
}

type recordingPageManagement struct {
	managev1connect.UnimplementedPageServiceHandler
	create         *connect.Request[managev1.CreatePageRequest]
	get            *connect.Request[managev1.GetPageRequest]
	getResponse    *managev1.Page
	getErr         error
	update         *connect.Request[managev1.UpdatePageRequest]
	updateResponse *managev1.UpdatePageResponse
}

func (r *recordingPageManagement) GetPage(_ context.Context, req *connect.Request[managev1.GetPageRequest]) (*connect.Response[managev1.Page], error) {
	r.get = req
	if r.getErr != nil {
		return nil, r.getErr
	}
	return connect.NewResponse(r.getResponse), nil
}

func (r *recordingPageManagement) UpdatePage(_ context.Context, req *connect.Request[managev1.UpdatePageRequest]) (*connect.Response[managev1.UpdatePageResponse], error) {
	r.update = req
	return connect.NewResponse(r.updateResponse), nil
}

func (r *recordingPageManagement) CreatePage(_ context.Context, req *connect.Request[managev1.CreatePageRequest]) (*connect.Response[managev1.Page], error) {
	r.create = req
	return connect.NewResponse(&managev1.Page{Id: managementPageID, Title: req.Msg.Title, SourceLocale: req.Msg.SourceLocale, Status: managev1.PageStatus_PAGE_STATUS_DRAFT, Revision: "page-revision"}), nil
}

func TestWorkSettingsReadSuppliesExactObservedBaselineForUpdate(t *testing.T) {
	metadata, err := structpb.NewStruct(map[string]any{"nested": map[string]any{"keep": "peer-compatible"}})
	if err != nil {
		t.Fatal(err)
	}
	works := &recordingWorkManagement{getResponse: &managev1.Work{
		Id: managementWorkID, Type: managev1.WorkType_WORK_TYPE_PORTFOLIO,
		Metadata: metadata, Clients: []*managev1.WorkClient{{Id: managementPostID}},
		Year: 2026, Month: 10, IsPresent: true, Revision: "content-revision",
	}}
	tools, err := NewContentManagementTools(&recordingPostManagement{}, works, &recordingPageManagement{})
	if err != nil {
		t.Fatal(err)
	}
	read, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolWorkSettingsGet, toolArguments(t, `{"document_id":"`+managementWorkID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if works.get.Msg.Id != managementWorkID || !reflect.DeepEqual(read.StructuredContent["metadata"], metadata.AsMap()) || !reflect.DeepEqual(read.StructuredContent["client_ids"], []any{managementPostID}) {
		t.Fatalf("settings read = %#v", read.StructuredContent)
	}
	encoded, err := json.Marshal(map[string]any{
		"document_id": managementWorkID, "metadata": map[string]any{}, "client_ids": []string{},
		"observed_metadata": read.StructuredContent["metadata"], "observed_client_ids": read.StructuredContent["client_ids"],
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolWorkSettingsUpdate, toolArguments(t, string(encoded)))
	if err != nil {
		t.Fatal(err)
	}
	request := works.update.Msg
	if request.Metadata == nil || len(request.Metadata.Fields) != 0 || request.Clients == nil || len(request.Clients.ClientIds) != 0 {
		t.Fatalf("empty desired values lost presence: %#v", request)
	}
	if !reflect.DeepEqual(request.ObservedMetadata.AsMap(), metadata.AsMap()) || request.ObservedClients == nil || !reflect.DeepEqual(request.ObservedClients.ClientIds, []string{managementPostID}) {
		t.Fatalf("observed baseline changed: %#v", request)
	}
}

func TestWorkSettingsRejectsMissingBaselineAndPreservesReadAuthorityErrors(t *testing.T) {
	works := &recordingWorkManagement{getErr: connect.NewError(connect.CodePermissionDenied, errors.New("permission denied"))}
	tools, err := NewContentManagementTools(&recordingPostManagement{}, works, &recordingPageManagement{})
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{
		`{"document_id":"` + managementWorkID + `","metadata":{}}`,
		`{"document_id":"` + managementWorkID + `","client_ids":[]}`,
	} {
		_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolWorkSettingsUpdate, toolArguments(t, payload))
		var executionErr *mcpserver.ToolExecutionError
		if !errors.As(err, &executionErr) || works.update != nil {
			t.Fatalf("missing baseline called domain service: %v", err)
		}
	}
	_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolWorkSettingsGet, toolArguments(t, `{"document_id":"`+managementWorkID+`"}`))
	var executionErr *mcpserver.ToolExecutionError
	if !errors.As(err, &executionErr) || executionErr.Message != "permission denied" {
		t.Fatalf("read authority error = %v", err)
	}
}

func TestWorkSettingsGetDoesNotReturnBaselineWhenOwningReadFails(t *testing.T) {
	readErr := connect.NewError(connect.CodeInternal, errors.New("client query failed"))
	works := &recordingWorkManagement{getErr: readErr}
	tools, err := NewContentManagementTools(&recordingPostManagement{}, works, &recordingPageManagement{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolWorkSettingsGet, toolArguments(t, `{"document_id":"`+managementWorkID+`"}`))
	if !errors.Is(err, readErr) || result.StructuredContent != nil {
		t.Fatalf("failed owning read returned a settings baseline: result=%+v err=%v", result, err)
	}
}

func TestWorkSettingsSchemaConditionallyRequiresObservedValues(t *testing.T) {
	var schema struct {
		AllOf []map[string]any `json:"allOf"`
	}
	if err := json.Unmarshal([]byte(workSettingsUpdateInputJSONSchema), &schema); err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{
		{"if": map[string]any{"required": []any{"metadata"}}, "then": map[string]any{"required": []any{"observed_metadata"}}},
		{"if": map[string]any{"required": []any{"client_ids"}}, "then": map[string]any{"required": []any{"observed_client_ids"}}},
	}
	if !reflect.DeepEqual(schema.AllOf, want) {
		t.Fatalf("baseline schema = %#v", schema.AllOf)
	}
}
