package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	postdomain "github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/work"

	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	pagedomain "github.com/echovisionlab/geul-api/internal/page"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestContentRelatedToolDescriptors(t *testing.T) {
	tools, err := NewContentRelatedTools(&recordingPostRelated{}, &recordingWorkRelated{}, &recordingPageRelated{})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != len(contentRelatedTools) || len(listed) != 17 {
		t.Fatalf("listed %d related content tools", len(listed))
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

func TestContentRelatedToolsRouteFeaturedImageParticipantsAndCredits(t *testing.T) {
	posts := &recordingPostRelated{}
	works := &recordingWorkRelated{}
	pages := &recordingPageRelated{}
	tools, err := NewContentRelatedTools(posts, works, pages)
	if err != nil {
		t.Fatal(err)
	}

	_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentFeaturedImageSet, toolArguments(t, `{"document_type":"work","document_id":"`+managementWorkID+`","file_id":"44444444-4444-4444-8444-444444444444"}`))
	if err != nil {
		t.Fatal(err)
	}
	if works.featured.WorkID != managementWorkID || works.featured.FileID != "44444444-4444-4444-8444-444444444444" {
		t.Fatalf("featured request = %#v", works.featured)
	}

	participant, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolPostCollaboratorAdd, toolArguments(t, `{"document_id":"`+managementPostID+`","member_id":"55555555-5555-4555-8555-555555555555"}`))
	if err != nil {
		t.Fatal(err)
	}
	if posts.collaborator.PostID != managementPostID || participant.StructuredContent["role"] != "collaborator" {
		t.Fatalf("participant = %#v %#v", posts.collaborator, participant.StructuredContent)
	}

	credit, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolWorkCreditAdd, toolArguments(t, `{"document_id":"`+managementWorkID+`","name":"Sound designer","credit_role":"sound"}`))
	if err != nil {
		t.Fatal(err)
	}
	if works.credit.Name == nil || *works.credit.Name != "Sound designer" || credit.StructuredContent["credit_role"] != "sound" || credit.StructuredContent["changed"] != true {
		t.Fatalf("credit = %#v %#v", works.credit, credit.StructuredContent)
	}
}

func TestContentRelatedToolsRouteVersionsAndSlugCheck(t *testing.T) {
	posts := &recordingPostRelated{}
	tools, err := NewContentRelatedTools(posts, &recordingWorkRelated{}, &recordingPageRelated{})
	if err != nil {
		t.Fatal(err)
	}

	versions, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentVersionsList, toolArguments(t, `{"document_type":"post","document_id":"`+managementPostID+`","limit":10,"offset":5}`))
	if err != nil {
		t.Fatal(err)
	}
	if posts.versions.Page.Limit != 10 || posts.versions.Page.Offset != 5 || versions.StructuredContent["total"] != float64(6) {
		t.Fatalf("versions = %#v %#v", posts.versions, versions.StructuredContent)
	}

	slug, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentSlugCheck, toolArguments(t, `{"document_type":"post","slug":"available-slug","exclude_document_id":"`+managementPostID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if posts.slug.ExcludePostID == nil || *posts.slug.ExcludePostID != managementPostID || slug.StructuredContent["available"] != true {
		t.Fatalf("slug = %#v %#v", posts.slug, slug.StructuredContent)
	}
}

func TestWorkCreditUpdatesReturnCurrentFieldsWithoutClaimingChange(t *testing.T) {
	works := &recordingWorkRelated{}
	tools, err := NewContentRelatedTools(&recordingPostRelated{}, works, &recordingPageRelated{})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		tool, input, field, value string
	}{
		{ToolWorkCreditGroupUpdate, `{"group_id":"77777777-7777-4777-8777-777777777777","name":"Sound"}`, "name", "Sound"},
		{ToolWorkCreditUpdate, `{"credit_id":"77777777-7777-4777-8777-777777777777","credit_role":"sound"}`, "credit_role", "sound"},
	} {
		t.Run(test.tool, func(t *testing.T) {
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, test.tool, toolArguments(t, test.input))
			if err != nil {
				t.Fatal(err)
			}
			if _, present := result.StructuredContent["changed"]; present || result.StructuredContent[test.field] != test.value {
				t.Fatalf("update did not return truthful current fields: %+v", result.StructuredContent)
			}
			var text map[string]any
			if err := json.Unmarshal([]byte(result.Content[0]["text"].(string)), &text); err != nil || !reflect.DeepEqual(text, result.StructuredContent) {
				t.Fatalf("text/structured output differ: text=%+v result=%+v err=%v", text, result.StructuredContent, err)
			}
			for _, tool := range listed {
				if tool.Name != test.tool {
					continue
				}
				var schema struct {
					Required   []string                   `json:"required"`
					Properties map[string]json.RawMessage `json:"properties"`
				}
				if err := json.Unmarshal(tool.OutputSchema, &schema); err != nil {
					t.Fatal(err)
				}
				if _, advertised := schema.Properties["changed"]; advertised {
					t.Fatal("update schema advertises unavailable changed status")
				}
				for _, key := range schema.Required {
					if _, present := result.StructuredContent[key]; !present {
						t.Fatalf("required output %q missing", key)
					}
				}
				for key := range result.StructuredContent {
					if _, advertised := schema.Properties[key]; !advertised {
						t.Fatalf("output field %q is absent from schema", key)
					}
				}
			}
		})
	}
	// Create/delete tools continue to advertise the existing change status.
	for _, tool := range listed {
		switch tool.Name {
		case ToolWorkCreditGroupCreate, ToolWorkCreditGroupDelete, ToolWorkCreditAdd, ToolWorkCreditDelete:
			if string(tool.OutputSchema) != contentActionOutputJSONSchema {
				t.Fatalf("%s existing output contract changed", tool.Name)
			}
		}
	}
}

func TestDocumentVersionsListContinuationUsesSafePagination(t *testing.T) {
	for _, documentType := range []string{"post", "work", "page"} {
		for _, test := range []struct {
			name   string
			fields string
			more   bool
		}{
			{name: "maximum offset exhausted", fields: `,"limit":100,"offset":2147483647`},
			{name: "last page", fields: `,"limit":1,"offset":5`},
			{name: "more results", fields: `,"limit":1,"offset":4`, more: true},
		} {
			t.Run(documentType+"/"+test.name, func(t *testing.T) {
				tools, err := NewContentRelatedTools(&recordingPostRelated{}, &recordingWorkRelated{}, &recordingPageRelated{})
				if err != nil {
					t.Fatal(err)
				}
				result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentVersionsList, toolArguments(t, `{"document_type":"`+documentType+`","document_id":"`+managementPostID+`"`+test.fields+`}`))
				if err != nil {
					t.Fatal(err)
				}
				if result.StructuredContent["has_more"] != test.more {
					t.Fatalf("pagination = %+v", result.StructuredContent)
				}
				next, present := result.StructuredContent["next_offset"]
				if present != test.more || (test.more && next != float64(5)) {
					t.Fatalf("continuation = %+v", result.StructuredContent)
				}
			})
		}
	}
}

func TestDocumentVersionsListEnforcesPaginationBeforeCallingApplication(t *testing.T) {
	for _, documentType := range []string{"post", "work", "page"} {
		for _, test := range []struct {
			name    string
			fields  string
			limit   int32
			offset  int32
			invalid bool
		}{
			{name: "omitted", limit: 20},
			{name: "minimum", fields: `,"limit":1,"offset":0`, limit: 1},
			{name: "maximum", fields: `,"limit":100,"offset":2147483647`, limit: 100, offset: 2147483647},
			{name: "zero", fields: `,"limit":0`, invalid: true},
			{name: "negative", fields: `,"limit":-1`, invalid: true},
			{name: "above maximum", fields: `,"limit":101`, invalid: true},
			{name: "null limit", fields: `,"limit":null`, invalid: true},
			{name: "negative offset", fields: `,"offset":-1`, invalid: true},
			{name: "null offset", fields: `,"offset":null`, invalid: true},
			{name: "offset exceeds native integer", fields: `,"offset":2147483648`, invalid: true},
		} {
			t.Run(documentType+"/"+test.name, func(t *testing.T) {
				posts, works, pages := &recordingPostRelated{}, &recordingWorkRelated{}, &recordingPageRelated{}
				tools, err := NewContentRelatedTools(posts, works, pages)
				if err != nil {
					t.Fatal(err)
				}
				_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentVersionsList, toolArguments(t, `{"document_type":"`+documentType+`","document_id":"`+managementPostID+`"`+test.fields+`}`))
				if test.invalid {
					var executionErr *mcpserver.ToolExecutionError
					if !errors.As(err, &executionErr) || posts.versions != nil || works.versions != nil || pages.versions != nil {
						t.Fatalf("invalid pagination reached application: err=%v post=%+v work=%+v page=%+v", err, posts.versions, works.versions, pages.versions)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				var pagination *commonv1.PaginationRequest
				switch documentType {
				case "post":
					pagination = &commonv1.PaginationRequest{Limit: posts.versions.Page.Limit, Offset: posts.versions.Page.Offset}
				case "work":
					pagination = &commonv1.PaginationRequest{Limit: works.versions.Page.Limit, Offset: works.versions.Page.Offset}
				case "page":
					pagination = &commonv1.PaginationRequest{Limit: pages.versions.Page.Limit, Offset: pages.versions.Page.Offset}
				}
				if pagination.Limit != test.limit || pagination.Offset != test.offset {
					t.Fatalf("pagination=%+v, want limit=%d offset=%d", pagination, test.limit, test.offset)
				}
			})
		}
	}
}

type recordingPostRelated struct {
	PostRelatedApplication
	collaborator *postdomain.PostParticipantCommand
	versions     *postdomain.ListPostVersionsQuery
	slug         *postdomain.CheckSlugAvailableQuery
}

func (r *recordingPostRelated) AddPostCollaborator(_ context.Context, command postdomain.PostParticipantCommand) (*managev1.PostParticipant, error) {
	r.collaborator = &command
	return &managev1.PostParticipant{Member: &commonv1.MemberSummary{Id: command.MemberID, Nickname: "Member"}, Role: managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_COLLABORATOR, CreatedAt: timestamppb.Now()}, nil
}

func (r *recordingPostRelated) ListPostVersions(_ context.Context, query postdomain.ListPostVersionsQuery) (*managev1.ListPostVersionsResponse, error) {
	r.versions = &query
	return &managev1.ListPostVersionsResponse{
		Versions:   []*managev1.PostVersion{{Id: "66666666-6666-4666-8666-666666666666", Version: 1, Title: "Version", CanonicalHash: "hash", SourceLocale: "ko", CreatedAt: timestamppb.Now()}},
		Pagination: (queryutil.Pagination{Limit: query.Page.Limit, Offset: query.Page.Offset}).BuildResponse(6),
	}, nil
}

func (r *recordingPostRelated) CheckSlugAvailable(_ context.Context, query postdomain.CheckSlugAvailableQuery) (*managev1.CheckSlugAvailableResponse, error) {
	r.slug = &query
	return &managev1.CheckSlugAvailableResponse{Available: true}, nil
}

func TestWorkCreditSchemasHaveNoRootCombinators(t *testing.T) {
	for name, raw := range map[string]string{"work_credit_add": workCreditAddInputJSONSchema, "work_credit_update": workCreditUpdateInputJSONSchema} {
		var schema map[string]any
		if err := json.Unmarshal([]byte(raw), &schema); err != nil {
			t.Fatal(err)
		}
		for _, keyword := range []string{"allOf", "anyOf", "oneOf", "not", "if", "then", "else", "dependentRequired", "dependentSchemas"} {
			if _, present := schema[keyword]; present {
				t.Fatalf("%s input schema has root %s", name, keyword)
			}
		}
	}
}

func TestWorkCreditMutationsRejectInvalidArgumentsBeforeOwner(t *testing.T) {
	const creditID = "77777777-7777-4777-8777-777777777777"
	artistID, memberID := "88888888-8888-4888-8888-888888888888", "99999999-9999-4999-8999-999999999999"
	for _, test := range []struct{ tool, arguments, message string }{
		{ToolWorkCreditAdd, `{"document_id":"` + managementWorkID + `","credit_role":"sound"}`, "exactly one of artist_id, member_id, or name is required"},
		{ToolWorkCreditAdd, `{"document_id":"` + managementWorkID + `","artist_id":"` + artistID + `","name":"Sound designer"}`, "exactly one of artist_id, member_id, or name is required"},
		{ToolWorkCreditAdd, `{"document_id":"` + managementWorkID + `","artist_id":"` + artistID + `","member_id":"` + memberID + `"}`, "exactly one of artist_id, member_id, or name is required"},
		{ToolWorkCreditUpdate, `{"credit_id":"` + creditID + `"}`, "at least one of group_id or credit_role is required"},
		{ToolWorkCreditAdd, `{"document_id":"` + managementWorkID + `","credit_id":"` + creditID + `","name":"Sound designer"}`, `unexpected argument "credit_id"`},
		{ToolWorkCreditUpdate, `{"credit_id":"` + creditID + `","credit_role":"sound","artist_id":"` + artistID + `"}`, `unexpected argument "artist_id"`},
		{ToolWorkCreditDelete, `{"credit_id":"` + creditID + `","name":"Sound designer"}`, `unexpected argument "name"`},
		{ToolWorkCreditGroupCreate, `{"document_id":"` + managementWorkID + `","name":"Sound","group_id":"` + creditID + `"}`, `unexpected argument "group_id"`},
		{ToolWorkCreditGroupUpdate, `{"group_id":"` + creditID + `","name":"Sound","document_id":"` + managementWorkID + `"}`, `unexpected argument "document_id"`},
		{ToolWorkCreditGroupDelete, `{"group_id":"` + creditID + `","name":"Sound"}`, `unexpected argument "name"`},
	} {
		t.Run(test.tool+"/"+test.arguments, func(t *testing.T) {
			works := &recordingWorkRelated{}
			tools, err := NewContentRelatedTools(&recordingPostRelated{}, works, &recordingPageRelated{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, test.tool, toolArguments(t, test.arguments))
			var executionErr *mcpserver.ToolExecutionError
			if !errors.As(err, &executionErr) || executionErr.Message != test.message || works.credit != nil || works.creditUpdate != nil {
				t.Fatalf("err = %v, owner called = %v", err, works.credit != nil || works.creditUpdate != nil)
			}
		})
	}
}

type recordingWorkRelated struct {
	WorkRelatedApplication
	versions     *work.ListWorkVersionsQuery
	featured     *work.SetWorkFeaturedImageCommand
	credit       *work.AddWorkCreditCommand
	creditUpdate *work.UpdateWorkCreditCommand
}

func (r *recordingWorkRelated) ListWorkVersions(_ context.Context, query work.ListWorkVersionsQuery) (*managev1.ListWorkVersionsResponse, error) {
	r.versions = &query
	return &managev1.ListWorkVersionsResponse{Pagination: (queryutil.Pagination{Limit: query.Page.Limit, Offset: query.Page.Offset}).BuildResponse(6)}, nil
}

func (r *recordingWorkRelated) SetWorkFeaturedImage(_ context.Context, command work.SetWorkFeaturedImageCommand) (*managev1.SetWorkFeaturedImageResponse, error) {
	r.featured = &command
	return &managev1.SetWorkFeaturedImageResponse{}, nil
}

func (r *recordingWorkRelated) AddWorkCredit(_ context.Context, command work.AddWorkCreditCommand) (*managev1.WorkCredit, error) {
	r.credit = &command
	return &managev1.WorkCredit{Id: "77777777-7777-4777-8777-777777777777", Name: command.Name, CreditRole: command.CreditRole}, nil
}

func (r *recordingWorkRelated) UpdateWorkCreditGroup(_ context.Context, command work.UpdateWorkCreditGroupCommand) (*managev1.WorkCreditGroup, error) {
	name := ""
	if command.Name != nil {
		name = *command.Name
	}
	return &managev1.WorkCreditGroup{Id: command.GroupID, Name: name}, nil
}

func (r *recordingWorkRelated) UpdateWorkCredit(_ context.Context, command work.UpdateWorkCreditCommand) (*managev1.WorkCredit, error) {
	r.creditUpdate = &command
	return &managev1.WorkCredit{Id: command.CreditID, CreditRole: command.CreditRole}, nil
}

type recordingPageRelated struct {
	versions *pagedomain.ListPageVersionsQuery
}

func (r *recordingPageRelated) ListPageVersions(_ context.Context, query pagedomain.ListPageVersionsQuery) (*managev1.ListPageVersionsResponse, error) {
	r.versions = &query
	return &managev1.ListPageVersionsResponse{Pagination: (queryutil.Pagination{Limit: query.Page.Limit, Offset: query.Page.Offset}).BuildResponse(6)}, nil
}

func (*recordingPageRelated) SetPageFeaturedImage(context.Context, pagedomain.SetPageFeaturedImageCommand) (*managev1.SetPageFeaturedImageResponse, error) {
	return nil, errors.New("set page featured image is not part of this test")
}

func (*recordingPageRelated) DeletePageFeaturedImage(context.Context, pagedomain.DeletePageFeaturedImageCommand) (*managev1.OgAssetDeleteResponse, error) {
	return nil, errors.New("delete page featured image is not part of this test")
}

func (*recordingPageRelated) RestorePageVersion(context.Context, pagedomain.RestorePageVersionCommand) (*managev1.Page, error) {
	return nil, errors.New("restore page version is not part of this test")
}

func (*recordingPageRelated) CheckPageSlugAvailable(context.Context, pagedomain.CheckPageSlugAvailableQuery) (*managev1.CheckPageSlugAvailableResponse, error) {
	return nil, errors.New("check page slug is not part of this test")
}
