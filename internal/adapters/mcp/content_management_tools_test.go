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
	pagedomain "github.com/echovisionlab/geul-api/internal/page"
	"github.com/echovisionlab/geul-api/internal/pageaccess"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/work"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"google.golang.org/protobuf/proto"
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

func TestWorkSchemasHaveNoRootCombinators(t *testing.T) {
	for name, raw := range map[string]string{"work_create": workCreateInputJSONSchema, "work_settings_update": workSettingsUpdateInputJSONSchema} {
		var schema map[string]any
		if err := json.Unmarshal([]byte(raw), &schema); err != nil {
			t.Fatal(err)
		}
		for _, keyword := range []string{"allOf", "anyOf", "oneOf", "not", "if", "then", "else", "dependentRequired", "dependentSchemas"} {
			if _, present := schema[keyword]; present {
				t.Fatalf("%s input schema has root %s", name, keyword)
			}
		}
		if schema["type"] != "object" || schema["additionalProperties"] != false {
			t.Fatalf("%s input schema root = %#v", name, schema)
		}
	}
}

func TestWorkCreateRejectsInvalidDateRangeBeforeOwner(t *testing.T) {
	base := `"title":"Work","source_locale":"en","type":"portfolio","year":2026,"month":8`
	for _, test := range []struct{ arguments, message string }{
		{`{` + base + `,"is_present":true,"until_year":2026}`, "until_year and until_month must be omitted when is_present is true"},
		{`{` + base + `,"is_present":true,"until_month":9}`, "until_year and until_month must be omitted when is_present is true"},
		{`{` + base + `}`, "until_year and until_month are required unless is_present is true"},
		{`{` + base + `,"is_present":false,"until_year":2026}`, "until_year and until_month are required unless is_present is true"},
		{`{` + base + `,"until_month":9}`, "until_year and until_month are required unless is_present is true"},
	} {
		t.Run(test.arguments, func(t *testing.T) {
			works := &recordingWorkManagement{}
			tools, err := NewContentManagementTools(&recordingPostManagement{}, works, &recordingPageManagement{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolWorkCreate, toolArguments(t, test.arguments))
			var executionErr *mcpserver.ToolExecutionError
			if !errors.As(err, &executionErr) || executionErr.Message != test.message || works.create != nil {
				t.Fatalf("err = %v, create called = %v", err, works.create != nil)
			}
		})
	}
	works := &recordingWorkManagement{}
	tools, err := NewContentManagementTools(&recordingPostManagement{}, works, &recordingPageManagement{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolWorkCreate, toolArguments(t, `{`+base+`,"is_present":true}`)); err != nil || works.create == nil {
		t.Fatalf("present Work create err = %v", err)
	}
}

func TestContentManagementPostAndPageSettingsReadReturnSourceState(t *testing.T) {
	slug, summary, zone := "source-slug", "source-summary", "Asia/Seoul"
	mapPlaceID := managementPageID
	moment := time.Date(2026, 10, 5, 1, 2, 3, 0, time.UTC)
	stamp := timestamppb.New(moment)
	seriesOrder := int32(4)
	posts := &recordingPostManagement{getResponse: postdomain.View{
		Post: postdomain.Post{
			ID: managementPostID, Title: "Source Post", Summary: &summary, Slug: &slug, SourceLocale: "ko", Status: postdomain.StatusScheduled,
			ContentRevision: "actual-source-post-revision", ConfigurationRevision: managementPostConfigurationRevision, CommentsEnabled: false, MapPlaceID: &mapPlaceID,
			DocumentLayout: postdomain.DocumentLayout{ContentHeight: postdomain.ContentHeightViewport, PageChrome: postdomain.PlacementPinned, Footer: postdomain.PlacementFlow, HideTableOfContents: true},
			Categories:     []postdomain.Category{{ID: managementWorkID}}, Tags: []postdomain.Tag{{ID: managementPageID}}, Series: &postdomain.Series{ID: managementWorkID}, SeriesOrder: &seriesOrder,
			ScheduledAt: &moment, ScheduledTimeZone: &zone, PublishedAt: &moment, CreatedAt: moment, UpdatedAt: moment,
		},
		Actions:  []postdomain.Action{postdomain.ActionEdit, postdomain.ActionCancelSchedule},
		Document: &contentv1.RichTextDocument{}, FeaturedImage: &commonv1.MediaDelivery{FileId: managementWorkID},
	}}
	published := stamp.AsTime()
	pages := &recordingPageManagement{getResponse: pagedomain.Detail{
		View: pagedomain.View{Page: pagedomain.Page{
			ID: managementPageID, Slug: &slug, SourceLocale: "ko", Status: pagedomain.StatusPublished, ShowTitle: false,
			DocumentLayout: pagedomain.DocumentLayout{
				ContentHeight: pagedomain.ContentHeightViewport, PageChrome: pagedomain.PlacementPinned,
				Footer: pagedomain.PlacementFlow, HideTableOfContents: true,
			},
			AccessPolicy: encodedPageAccessPolicy(t, &commonv1.PageAccessPolicy{
				Mode: commonv1.PageAccessMode_PAGE_ACCESS_MODE_CONDITIONS, Match: commonv1.PageAccessMatch_PAGE_ACCESS_MATCH_ALL,
				AllowedRoles: []policyv1.AuthorizationRole{policyv1.AuthorizationRole_AUTHOR}, UserTagIds: []string{managementWorkID}, NewsletterSubscriber: true,
			}),
			PublishedAt: &published, CreatedAt: published, UpdatedAt: published,
		}, Title: "Source Page", Summary: &summary},
		Revision: "actual-source-page-revision", Document: &contentv1.PageDocument{},
		FeaturedImage: &commonv1.MediaDelivery{FileId: managementWorkID},
	}}
	tools, err := NewContentManagementTools(posts, &recordingWorkManagement{}, pages)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	if err != nil {
		t.Fatal(err)
	}
	wantLayout := map[string]any{"content_height": "DOCUMENT_CONTENT_HEIGHT_VIEWPORT", "page_chrome": "DOCUMENT_REGION_PLACEMENT_PINNED", "footer": "DOCUMENT_REGION_PLACEMENT_FLOW", "hide_table_of_contents": true}
	for _, test := range []struct {
		name, id string
		want     map[string]any
	}{
		{ToolPostSettingsGet, managementPostID, map[string]any{"document_revision": "actual-source-post-revision", "configuration_revision": managementPostConfigurationRevision, "comments_enabled": false, "map_place_id": managementPageID, "status": "scheduled", "category_ids": []any{managementWorkID}, "tag_ids": []any{managementPageID}, "allowed_actions": []any{"edit", "cancel_schedule"}, "scheduled_time_zone": zone, "scheduled_at": "2026-10-05T01:02:03Z", "series_id": managementWorkID, "series_order": float64(4)}},
		{ToolPageSettingsGet, managementPageID, map[string]any{"document_revision": "actual-source-page-revision", "show_title": false, "status": "published", "access_policy": map[string]any{"mode": "conditions", "match": "all", "allowed_roles": []any{"author"}, "user_tag_ids": []any{managementWorkID}, "newsletter_subscriber": true}}},
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
	if posts.get.ID != managementPostID || pages.get.ID != managementPageID {
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
	if posts.create.AcceptLanguage != "ko" || !posts.create.CommentsEnabled {
		t.Fatalf("Post create = %#v", posts.create)
	}
	assertEmptyManagementDocument(t, posts.create.Document, contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST, "ko")
	if postResult.StructuredContent["document_id"] != managementPostID || postResult.StructuredContent["configuration_revision"] != managementPostConfigurationRevision {
		t.Fatalf("Post result = %#v", postResult.StructuredContent)
	}

	workResult, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolWorkCreate, toolArguments(t, `{"title":"Work","source_locale":"en","type":"portfolio","year":2026,"month":8,"until_year":2026,"until_month":8,"metadata":{"key":"value"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if works.create.AcceptLanguage != "en" || works.create.Type != managev1.WorkType_WORK_TYPE_PORTFOLIO.String() || works.create.Metadata["key"] != "value" {
		t.Fatalf("Work create = %#v", works.create)
	}
	if works.create.UntilYear == nil || *works.create.UntilYear != 2026 || works.create.UntilMonth == nil || *works.create.UntilMonth != 8 {
		t.Fatalf("Work create range = %#v", works.create)
	}
	assertEmptyManagementDocument(t, works.create.Document, contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK, "en")
	if workResult.StructuredContent["document_id"] != managementWorkID {
		t.Fatalf("Work result = %#v", workResult.StructuredContent)
	}

	pageResult, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolPageCreate, toolArguments(t, `{"title":"Page","source_locale":"ja","show_title":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if pages.create.AcceptLanguage != "ja" || pages.create.ShowTitle == nil || *pages.create.ShowTitle {
		t.Fatalf("Page create = %#v", pages.create)
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
	if posts.updateCalls != 1 || posts.update == nil || posts.update.ExpectedConfigurationRevision != managementPostConfigurationRevision {
		t.Fatalf("UpdatePost request = %#v (calls %d)", posts.update, posts.updateCalls)
	}
	if posts.update.Slug == nil || *posts.update.Slug != "updated-slug" {
		t.Fatalf("UpdatePost settings = %#v", posts.update)
	}
	if result.StructuredContent["configuration_revision"] != managementPostConfigurationRevisionNext {
		t.Fatalf("Post settings result = %#v", result.StructuredContent)
	}
}

func TestPostSettingsLayoutUsesExactNativeEnumsAndAuthoritativeResponse(t *testing.T) {
	storedSlug, storedPlace := "stored-slug", managementWorkID
	posts := &recordingPostManagement{updateResponse: &postdomain.SettingsUpdate{
		ID: managementPostID, Changed: false, Slug: &storedSlug, CommentsEnabled: false, MapPlaceID: &storedPlace,
		DocumentLayout: postdomain.DocumentLayout{ContentHeight: postdomain.ContentHeightContent, PageChrome: postdomain.PlacementFlow, Footer: postdomain.PlacementFlow}, ConfigurationRevision: managementPostConfigurationRevision,
	}}
	tools, err := NewContentManagementTools(posts, &recordingWorkManagement{}, &recordingPageManagement{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolPostSettingsUpdate, toolArguments(t, `{"document_id":"`+managementPostID+`","expected_configuration_revision":"`+managementPostConfigurationRevision+`","comments_enabled":true,"document_layout":{"content_height":"DOCUMENT_CONTENT_HEIGHT_VIEWPORT","page_chrome":"DOCUMENT_REGION_PLACEMENT_PINNED","footer":"DOCUMENT_REGION_PLACEMENT_PINNED","hide_table_of_contents":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	request := posts.update
	if request.ExpectedConfigurationRevision != managementPostConfigurationRevision || request.DocumentLayout.ContentHeight != postdomain.ContentHeightViewport || request.DocumentLayout.PageChrome != postdomain.PlacementPinned || request.DocumentLayout.Footer != postdomain.PlacementPinned || !request.DocumentLayout.HideTableOfContents {
		t.Fatalf("layout request=%+v", request)
	}
	if result.StructuredContent["changed"] != false || result.StructuredContent["comments_enabled"] != false || result.StructuredContent["slug"] != storedSlug || result.StructuredContent["map_place_id"] != storedPlace || result.StructuredContent["configuration_revision"] != managementPostConfigurationRevision {
		t.Fatalf("request values replaced owning response: %+v", result.StructuredContent)
	}
	layout := result.StructuredContent["document_layout"].(map[string]any)
	if layout["content_height"] != "DOCUMENT_CONTENT_HEIGHT_CONTENT" || layout["page_chrome"] != "DOCUMENT_REGION_PLACEMENT_FLOW" || layout["hide_table_of_contents"] != false {
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
	if !reflect.DeepEqual(schema.Required, []string{"content_height", "page_chrome", "footer", "hide_table_of_contents"}) {
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
		`{"content_height":"DOCUMENT_CONTENT_HEIGHT_CONTENT","page_chrome":"DOCUMENT_REGION_PLACEMENT_FLOW","footer":"DOCUMENT_REGION_PLACEMENT_FLOW","hide_table_of_contents":null}`,
		`{"content_height":"DOCUMENT_CONTENT_HEIGHT_CONTENT","page_chrome":"DOCUMENT_REGION_PLACEMENT_FLOW","footer":"DOCUMENT_REGION_PLACEMENT_FLOW","hide_table_of_contents":"true"}`,
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
		pages := &recordingPageManagement{updateResponse: pagedomain.UpdateResult{Page: pagedomain.Page{ID: managementPageID, ShowTitle: false}, Changed: changed}}
		tools, err := NewContentManagementTools(&recordingPostManagement{}, &recordingWorkManagement{}, pages)
		if err != nil {
			t.Fatal(err)
		}
		result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolPageSettingsUpdate, toolArguments(t, `{"document_id":"`+managementPageID+`","show_title":true}`))
		if err != nil {
			t.Fatal(err)
		}
		if pages.update.ShowTitle == nil || !*pages.update.ShowTitle || result.StructuredContent["show_title"] != false || result.StructuredContent["changed"] != changed {
			t.Fatalf("request/owning response=%+v %+v", pages.update, result.StructuredContent)
		}
	}
}

func TestPageSettingsReadReportsPublicWhenPolicyIsAbsent(t *testing.T) {
	pages := &recordingPageManagement{getResponse: pagedomain.Detail{
		View: pagedomain.View{Page: pagedomain.Page{
			ID: managementPageID, SourceLocale: "en", Status: pagedomain.StatusDraft, DocumentLayout: pagedomain.DefaultDocumentLayout(),
		}, Title: "Page"},
		Revision: "r",
	}}
	tools, err := NewContentManagementTools(&recordingPostManagement{}, &recordingWorkManagement{}, pages)
	if err != nil {
		t.Fatal(err)
	}
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolPageSettingsGet, toolArguments(t, `{"document_id":"`+managementPageID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.StructuredContent["access_policy"], map[string]any{"mode": "public"}) {
		t.Fatalf("access_policy = %#v", result.StructuredContent["access_policy"])
	}
}

func TestPageSettingsUpdateForwardsAccessPolicy(t *testing.T) {
	for _, test := range []struct {
		name, policy string
		want         *commonv1.PageAccessPolicy
	}{
		{"authenticated", `{"mode":"authenticated"}`, &commonv1.PageAccessPolicy{Mode: commonv1.PageAccessMode_PAGE_ACCESS_MODE_AUTHENTICATED, Match: commonv1.PageAccessMatch_PAGE_ACCESS_MATCH_ANY}},
		{"conditions default any", `{"mode":"conditions","user_tag_ids":["` + managementWorkID + `"]}`, &commonv1.PageAccessPolicy{Mode: commonv1.PageAccessMode_PAGE_ACCESS_MODE_CONDITIONS, Match: commonv1.PageAccessMatch_PAGE_ACCESS_MATCH_ANY, UserTagIds: []string{managementWorkID}}},
		{"conditions all", `{"mode":"conditions","match":"all","allowed_roles":["admin","author"],"newsletter_subscriber":true}`, &commonv1.PageAccessPolicy{Mode: commonv1.PageAccessMode_PAGE_ACCESS_MODE_CONDITIONS, Match: commonv1.PageAccessMatch_PAGE_ACCESS_MATCH_ALL, AllowedRoles: []policyv1.AuthorizationRole{policyv1.AuthorizationRole_ADMIN, policyv1.AuthorizationRole_AUTHOR}, NewsletterSubscriber: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			pages := &recordingPageManagement{updateResponse: pagedomain.UpdateResult{
				Page: pagedomain.Page{ID: managementPageID, AccessPolicy: encodedPageAccessPolicy(t, test.want)}, Changed: true,
			}}
			tools, err := NewContentManagementTools(&recordingPostManagement{}, &recordingWorkManagement{}, pages)
			if err != nil {
				t.Fatal(err)
			}
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolPageSettingsUpdate, toolArguments(t, `{"document_id":"`+managementPageID+`","access_policy":`+test.policy+`}`))
			if err != nil {
				t.Fatal(err)
			}
			if pages.update.AccessPolicy == nil || !proto.Equal(pageaccess.Message(*pages.update.AccessPolicy), test.want) || pages.update.Slug != nil || pages.update.ShowTitle != nil {
				t.Fatalf("request = %+v", pages.update)
			}
			stored, err := pageaccess.Decode(pages.updateResponse.Page.AccessPolicy)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(result.StructuredContent["access_policy"], pageAccessPolicyOutput(pageaccess.Message(stored))) {
				t.Fatalf("access_policy = %#v", result.StructuredContent["access_policy"])
			}
		})
	}
}

func TestPageSettingsUpdateRejectsInvalidAccessPolicyBeforeOwner(t *testing.T) {
	for _, test := range []struct{ policy, message string }{
		{`{"mode":"members"}`, "access_policy.mode must be public, authenticated, or conditions"},
		{`{"mode":"public","allowed_roles":["admin"]}`, "access_policy conditions require mode conditions"},
		{`{"mode":"authenticated","match":"all"}`, "access_policy conditions require mode conditions"},
		{`{"mode":"conditions","match":"some","newsletter_subscriber":true}`, "access_policy.match must be any or all"},
		{`{"mode":"conditions","allowed_roles":["user"]}`, "access_policy.allowed_roles accepts author and admin"},
	} {
		t.Run(test.policy, func(t *testing.T) {
			pages := &recordingPageManagement{}
			tools, err := NewContentManagementTools(&recordingPostManagement{}, &recordingWorkManagement{}, pages)
			if err != nil {
				t.Fatal(err)
			}
			_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolPageSettingsUpdate, toolArguments(t, `{"document_id":"`+managementPageID+`","access_policy":`+test.policy+`}`))
			var executionErr *mcpserver.ToolExecutionError
			if !errors.As(err, &executionErr) || executionErr.Message != test.message || pages.update != nil {
				t.Fatalf("err = %v, update called = %v", err, pages.update != nil)
			}
		})
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
			if posts.updateCalls != 1 || posts.update == nil || posts.update.ExpectedConfigurationRevision != test.wantRevision {
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
	if posts.schedule.ScheduledAt.UTC() != time.Date(2026, 8, 31, 18, 0, 0, 0, time.UTC) || posts.schedule.TimeZone != "Asia/Seoul" {
		t.Fatalf("schedule request = %#v", posts.schedule)
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
	PostManagementApplication
	create         *postdomain.CreatePostCommand
	update         *postdomain.UpdatePostCommand
	schedule       *postdomain.SchedulePostCommand
	updateErr      error
	updateCalls    int
	get            *postdomain.GetPostQuery
	getResponse    postdomain.View
	getErr         error
	updateResponse *postdomain.SettingsUpdate
}

func (r *recordingPostManagement) GetPost(_ context.Context, query postdomain.GetPostQuery) (postdomain.View, error) {
	r.get = &query
	if r.getErr != nil {
		return postdomain.View{}, r.getErr
	}
	return r.getResponse, nil
}

func (r *recordingPostManagement) CreatePost(_ context.Context, command postdomain.CreatePostCommand) (postdomain.View, error) {
	r.create = &command
	return postdomain.View{Post: postdomain.Post{
		ID: managementPostID, Title: command.Title, SourceLocale: command.SourceLocale, Status: postdomain.StatusDraft,
		ContentRevision: "post-revision", ConfigurationRevision: managementPostConfigurationRevision,
		DocumentLayout: postdomain.DefaultDocumentLayout(),
	}}, nil
}
func (r *recordingPostManagement) UpdatePost(_ context.Context, command postdomain.UpdatePostCommand) (postdomain.SettingsUpdate, error) {
	r.updateCalls++
	r.update = &command
	if r.updateErr != nil {
		return postdomain.SettingsUpdate{}, r.updateErr
	}
	if r.updateResponse != nil {
		return *r.updateResponse, nil
	}
	return postdomain.SettingsUpdate{
		ID: command.ID, Changed: true, Slug: command.Slug,
		ConfigurationRevision: managementPostConfigurationRevisionNext,
		DocumentLayout:        postdomain.DefaultDocumentLayout(),
	}, nil
}
func (r *recordingPostManagement) SchedulePost(_ context.Context, command postdomain.SchedulePostCommand) (postdomain.Lifecycle, error) {
	r.schedule = &command
	scheduledAt := command.ScheduledAt.UTC()
	return postdomain.Lifecycle{ID: command.ID, Status: postdomain.StatusScheduled, ScheduledAt: &scheduledAt, ScheduledTimeZone: &command.TimeZone, UpdatedAt: time.Now()}, nil
}

type recordingWorkManagement struct {
	WorkManagementApplication
	create      *work.CreateWorkCommand
	get         *work.GetWorkQuery
	getResponse work.View
	getErr      error
	update      *work.UpdateWorkCommand
}

func (r *recordingWorkManagement) GetWork(_ context.Context, query work.GetWorkQuery) (work.View, error) {
	r.get = &query
	if r.getErr != nil {
		return work.View{}, r.getErr
	}
	return r.getResponse, nil
}

func (r *recordingWorkManagement) UpdateWork(_ context.Context, command work.UpdateWorkCommand) (work.Update, error) {
	r.update = &command
	return work.Update{Work: work.Work{ID: command.ID, UpdatedAt: time.Now()}, Changed: true}, nil
}

func (r *recordingWorkManagement) CreateWork(_ context.Context, command work.CreateWorkCommand) (work.View, error) {
	r.create = &command
	return work.View{
		Work:  work.Work{ID: managementWorkID, Type: command.Type, SourceLocale: command.SourceLocale, Status: work.StatusDraft},
		Title: command.Title, Revision: "work-revision",
	}, nil
}

type recordingPageManagement struct {
	create         *pagedomain.CreatePageCommand
	get            *pagedomain.GetPageQuery
	getResponse    pagedomain.Detail
	getErr         error
	update         *pagedomain.UpdatePageCommand
	updateResponse pagedomain.UpdateResult
}

func (r *recordingPageManagement) GetPage(_ context.Context, query pagedomain.GetPageQuery) (pagedomain.Detail, error) {
	r.get = &query
	if r.getErr != nil {
		return pagedomain.Detail{}, r.getErr
	}
	return r.getResponse, nil
}

func (r *recordingPageManagement) UpdatePage(_ context.Context, command pagedomain.UpdatePageCommand) (pagedomain.UpdateResult, error) {
	r.update = &command
	return r.updateResponse, nil
}

func (r *recordingPageManagement) CreatePage(_ context.Context, command pagedomain.CreatePageCommand) (pagedomain.Detail, error) {
	r.create = &command
	return pagedomain.Detail{
		View: pagedomain.View{Page: pagedomain.Page{
			ID: managementPageID, SourceLocale: command.SourceLocale, Status: pagedomain.StatusDraft, DocumentLayout: pagedomain.DefaultDocumentLayout(),
		}, Title: command.Title},
		Revision: "page-revision",
	}, nil
}

func (*recordingPageManagement) PublishPage(context.Context, pagedomain.PublishPageCommand) (pagedomain.LifecycleResult, error) {
	return pagedomain.LifecycleResult{}, errors.New("publish page is not part of this test")
}

func (*recordingPageManagement) UnpublishPage(context.Context, pagedomain.UnpublishPageCommand) (pagedomain.LifecycleResult, error) {
	return pagedomain.LifecycleResult{}, errors.New("unpublish page is not part of this test")
}

// encodedPageAccessPolicy is the stored form of policy.
func encodedPageAccessPolicy(t *testing.T, policy *commonv1.PageAccessPolicy) json.RawMessage {
	t.Helper()
	encoded, err := pageaccess.Encode(pageaccess.FromMessage(policy))
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func (*recordingPageManagement) DeletePage(context.Context, pagedomain.DeletePageCommand) error {
	return errors.New("delete page is not part of this test")
}

func TestWorkSettingsReadSuppliesExactObservedBaselineForUpdate(t *testing.T) {
	metadata, err := structpb.NewStruct(map[string]any{"nested": map[string]any{"keep": "peer-compatible"}})
	if err != nil {
		t.Fatal(err)
	}
	works := &recordingWorkManagement{getResponse: work.View{
		Work: work.Work{
			ID: managementWorkID, Type: managev1.WorkType_WORK_TYPE_PORTFOLIO.String(),
			Metadata: metadata.AsMap(), Year: 2026, Month: 10, IsPresent: true,
		},
		Clients: []work.Client{{ID: managementPostID}}, Revision: "content-revision",
	}}
	tools, err := NewContentManagementTools(&recordingPostManagement{}, works, &recordingPageManagement{})
	if err != nil {
		t.Fatal(err)
	}
	read, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolWorkSettingsGet, toolArguments(t, `{"document_id":"`+managementWorkID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if works.get.ID != managementWorkID || !reflect.DeepEqual(read.StructuredContent["metadata"], metadata.AsMap()) || !reflect.DeepEqual(read.StructuredContent["client_ids"], []any{managementPostID}) {
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
	request := works.update
	if request.Metadata == nil || len(request.Metadata) != 0 || request.Clients == nil || len(*request.Clients) != 0 {
		t.Fatalf("empty desired values lost presence: %#v", request)
	}
	if !reflect.DeepEqual(map[string]any(request.ObservedMetadata), metadata.AsMap()) || request.ObservedClients == nil || !reflect.DeepEqual(*request.ObservedClients, []string{managementPostID}) {
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

func TestWorkSettingsUpdateRequiresObservedValuesBeforeOwner(t *testing.T) {
	for _, test := range []struct{ arguments, message string }{
		{`{"document_id":"` + managementWorkID + `","metadata":{"key":"value"}}`, "observed_metadata is required when updating metadata"},
		{`{"document_id":"` + managementWorkID + `","client_ids":[]}`, "observed_client_ids is required when updating client_ids"},
	} {
		t.Run(test.message, func(t *testing.T) {
			works := &recordingWorkManagement{}
			tools, err := NewContentManagementTools(&recordingPostManagement{}, works, &recordingPageManagement{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolWorkSettingsUpdate, toolArguments(t, test.arguments))
			var executionErr *mcpserver.ToolExecutionError
			if !errors.As(err, &executionErr) || executionErr.Message != test.message || works.update != nil {
				t.Fatalf("err = %v, update called = %v", err, works.update != nil)
			}
		})
	}
}
