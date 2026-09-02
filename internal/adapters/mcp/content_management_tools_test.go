package mcp

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"connectrpc.com/connect"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
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
	if len(listed) != len(contentManagementTools) || len(listed) != 19 {
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
	if postResult.StructuredContent["document_id"] != managementPostID {
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
	managementPostID = "11111111-1111-4111-8111-111111111111"
	managementWorkID = "22222222-2222-4222-8222-222222222222"
	managementPageID = "33333333-3333-4333-8333-333333333333"
)

type recordingPostManagement struct {
	managev1connect.UnimplementedPostServiceHandler
	create   *connect.Request[managev1.CreatePostRequest]
	schedule *connect.Request[managev1.SchedulePostRequest]
}

func (r *recordingPostManagement) CreatePost(_ context.Context, req *connect.Request[managev1.CreatePostRequest]) (*connect.Response[managev1.Post], error) {
	r.create = req
	return connect.NewResponse(&managev1.Post{Id: managementPostID, Title: req.Msg.Title, SourceLocale: req.Msg.SourceLocale, Status: managev1.PostStatus_POST_STATUS_DRAFT, Revision: "post-revision"}), nil
}
func (r *recordingPostManagement) SchedulePost(_ context.Context, req *connect.Request[managev1.SchedulePostRequest]) (*connect.Response[managev1.PostLifecycleMutationResponse], error) {
	r.schedule = req
	return connect.NewResponse(&managev1.PostLifecycleMutationResponse{Id: req.Msg.Id, Changed: true, Status: managev1.PostStatus_POST_STATUS_SCHEDULED, ScheduledAt: req.Msg.ScheduledAt, ScheduledTimeZone: &req.Msg.ScheduledTimeZone, UpdatedAt: timestamppb.Now()}), nil
}

type recordingWorkManagement struct {
	managev1connect.UnimplementedWorkServiceHandler
	create *connect.Request[managev1.CreateWorkRequest]
}

func (r *recordingWorkManagement) CreateWork(_ context.Context, req *connect.Request[managev1.CreateWorkRequest]) (*connect.Response[managev1.Work], error) {
	r.create = req
	return connect.NewResponse(&managev1.Work{Id: managementWorkID, Title: req.Msg.Title, Type: req.Msg.Type, SourceLocale: req.Msg.SourceLocale, Status: managev1.WorkStatus_WORK_STATUS_DRAFT, Revision: "work-revision"}), nil
}

type recordingPageManagement struct {
	managev1connect.UnimplementedPageServiceHandler
	create *connect.Request[managev1.CreatePageRequest]
}

func (r *recordingPageManagement) CreatePage(_ context.Context, req *connect.Request[managev1.CreatePageRequest]) (*connect.Response[managev1.Page], error) {
	r.create = req
	return connect.NewResponse(&managev1.Page{Id: managementPageID, Title: req.Msg.Title, SourceLocale: req.Msg.SourceLocale, Status: managev1.PageStatus_PAGE_STATUS_DRAFT, Revision: "page-revision"}), nil
}
