package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
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
	if works.featured.Msg.WorkId != managementWorkID || works.featured.Msg.FileId != "44444444-4444-4444-8444-444444444444" {
		t.Fatalf("featured request = %#v", works.featured.Msg)
	}

	participant, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolPostCollaboratorAdd, toolArguments(t, `{"document_id":"`+managementPostID+`","member_id":"55555555-5555-4555-8555-555555555555"}`))
	if err != nil {
		t.Fatal(err)
	}
	if posts.collaborator.Msg.PostId != managementPostID || participant.StructuredContent["role"] != "collaborator" {
		t.Fatalf("participant = %#v %#v", posts.collaborator.Msg, participant.StructuredContent)
	}

	credit, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolWorkCreditAdd, toolArguments(t, `{"document_id":"`+managementWorkID+`","name":"Sound designer","credit_role":"sound"}`))
	if err != nil {
		t.Fatal(err)
	}
	if works.credit.Msg.Name == nil || *works.credit.Msg.Name != "Sound designer" || credit.StructuredContent["credit_role"] != "sound" {
		t.Fatalf("credit = %#v %#v", works.credit.Msg, credit.StructuredContent)
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
	if posts.versions.Msg.Pagination.Limit != 10 || posts.versions.Msg.Pagination.Offset != 5 || versions.StructuredContent["total"] != float64(6) {
		t.Fatalf("versions = %#v %#v", posts.versions.Msg, versions.StructuredContent)
	}

	slug, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentSlugCheck, toolArguments(t, `{"document_type":"post","slug":"available-slug","exclude_document_id":"`+managementPostID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if posts.slug.Msg.ExcludePostId == nil || *posts.slug.Msg.ExcludePostId != managementPostID || slug.StructuredContent["available"] != true {
		t.Fatalf("slug = %#v %#v", posts.slug.Msg, slug.StructuredContent)
	}
}

type recordingPostRelated struct {
	managev1connect.UnimplementedPostServiceHandler
	collaborator *connect.Request[managev1.AddPostCollaboratorRequest]
	versions     *connect.Request[managev1.ListPostVersionsRequest]
	slug         *connect.Request[managev1.CheckSlugAvailableRequest]
}

func (r *recordingPostRelated) AddPostCollaborator(_ context.Context, request *connect.Request[managev1.AddPostCollaboratorRequest]) (*connect.Response[managev1.PostParticipant], error) {
	r.collaborator = request
	return connect.NewResponse(&managev1.PostParticipant{Member: &commonv1.MemberSummary{Id: request.Msg.MemberId, Nickname: "Member"}, Role: managev1.PostParticipantRole_POST_PARTICIPANT_ROLE_COLLABORATOR, CreatedAt: timestamppb.Now()}), nil
}

func (r *recordingPostRelated) ListPostVersions(_ context.Context, request *connect.Request[managev1.ListPostVersionsRequest]) (*connect.Response[managev1.ListPostVersionsResponse], error) {
	r.versions = request
	return connect.NewResponse(&managev1.ListPostVersionsResponse{
		Versions:   []*managev1.PostVersion{{Id: "66666666-6666-4666-8666-666666666666", Version: 1, Title: "Version", CanonicalHash: "hash", SourceLocale: "ko", CreatedAt: timestamppb.Now()}},
		Pagination: &commonv1.PaginationResponse{Total: 6, Limit: request.Msg.Pagination.Limit, Offset: request.Msg.Pagination.Offset, HasMore: true},
	}), nil
}

func (r *recordingPostRelated) CheckSlugAvailable(_ context.Context, request *connect.Request[managev1.CheckSlugAvailableRequest]) (*connect.Response[managev1.CheckSlugAvailableResponse], error) {
	r.slug = request
	return connect.NewResponse(&managev1.CheckSlugAvailableResponse{Available: true}), nil
}

type recordingWorkRelated struct {
	managev1connect.UnimplementedWorkServiceHandler
	featured *connect.Request[managev1.SetWorkFeaturedImageRequest]
	credit   *connect.Request[managev1.AddWorkCreditRequest]
}

func (r *recordingWorkRelated) SetWorkFeaturedImage(_ context.Context, request *connect.Request[managev1.SetWorkFeaturedImageRequest]) (*connect.Response[managev1.SetWorkFeaturedImageResponse], error) {
	r.featured = request
	return connect.NewResponse(&managev1.SetWorkFeaturedImageResponse{}), nil
}

func (r *recordingWorkRelated) AddWorkCredit(_ context.Context, request *connect.Request[managev1.AddWorkCreditRequest]) (*connect.Response[managev1.WorkCredit], error) {
	r.credit = request
	return connect.NewResponse(&managev1.WorkCredit{Id: "77777777-7777-4777-8777-777777777777", Name: request.Msg.Name, CreditRole: request.Msg.CreditRole}), nil
}

type recordingPageRelated struct {
	managev1connect.UnimplementedPageServiceHandler
}
