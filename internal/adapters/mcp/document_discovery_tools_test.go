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
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const discoveryDocumentID = "44444444-4444-4444-8444-444444444444"

func TestDocumentDiscoveryToolDescriptorAndAuthorizedResult(t *testing.T) {
	slug := "test-post"
	updatedAt := time.Date(2026, time.August, 27, 5, 2, 3, 0, time.UTC)
	discovery := &recordingPostDocumentDiscovery{result: postdomain.AIDocumentListResult{
		Items: []postdomain.AIDocumentListItem{{
			ID: discoveryDocumentID, Title: "Test Post", Slug: &slug,
			SourceLocale: "ko", Status: "POST_STATUS_DRAFT", UpdatedAt: updatedAt,
		}},
		Total: 3, Limit: 1, Offset: 1,
	}}
	tools, err := NewDocumentDiscoveryTools(discovery, &recordingWorkDocumentDiscovery{}, &recordingPageDocumentDiscovery{}, &recordingProgramEventDocumentDiscovery{})
	if err != nil {
		t.Fatalf("NewDocumentDiscoveryTools() error = %v", err)
	}

	listed, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(listed) != 1 || listed[0].Name != ToolDocumentList {
		t.Fatalf("ListTools() = %#v, want document_list", listed)
	}
	assertMCPToolOAuthSecurity(t, listed[0])
	assertMCPToolAnnotations(t, listed[0], toolAnnotations(true, false, false))
	for name, schema := range map[string]json.RawMessage{
		"input": listed[0].InputSchema, "output": listed[0].OutputSchema,
	} {
		var object map[string]any
		if err := json.Unmarshal(schema, &object); err != nil || object["type"] != "object" {
			t.Fatalf("%s schema is not an object: %s (%v)", name, schema, err)
		}
	}

	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentList, toolArguments(t, `{
		"p":"post","q":"Test","limit":1,"offset":1
	}`))
	if err != nil {
		t.Fatalf("document_list error = %v", err)
	}
	wantInput := postdomain.AIDocumentListInput{Query: "Test", Limit: 1, Offset: 1}
	if !reflect.DeepEqual(discovery.input, wantInput) {
		t.Fatalf("ListAIDocuments input = %#v, want %#v", discovery.input, wantInput)
	}
	documents, ok := result.StructuredContent["documents"].([]any)
	if !ok || len(documents) != 1 {
		t.Fatalf("document_list documents = %#v", result.StructuredContent["documents"])
	}
	document, ok := documents[0].(map[string]any)
	if !ok || document["d"] != discoveryDocumentID || document["slug"] != slug || document["title"] != "Test Post" {
		t.Fatalf("document_list document = %#v", documents[0])
	}
	if result.StructuredContent["next_offset"] != float64(2) || result.StructuredContent["total"] != float64(3) {
		t.Fatalf("document_list pagination = %#v", result.StructuredContent)
	}
}

func TestDocumentDiscoveryToolRejectsUnsupportedProfileAndMapsExpectedErrors(t *testing.T) {
	discovery := &recordingPostDocumentDiscovery{}
	tools, err := NewDocumentDiscoveryTools(discovery, &recordingWorkDocumentDiscovery{}, &recordingPageDocumentDiscovery{}, &recordingProgramEventDocumentDiscovery{})
	if err != nil {
		t.Fatalf("NewDocumentDiscoveryTools() error = %v", err)
	}

	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentList, toolArguments(t, `{"p":"artist"}`))
	var executionErr *mcpserver.ToolExecutionError
	if result.Content != nil || !errors.As(err, &executionErr) || executionErr.Message != "p must be post, work, page, or program_event" {
		t.Fatalf("unsupported profile result = %#v, error = %v", result, err)
	}

	discovery.err = connect.NewError(connect.CodePermissionDenied, errors.New("permission denied"))
	result, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentList, toolArguments(t, `{"p":"post"}`))
	if result.Content != nil || !errors.As(err, &executionErr) || executionErr.Message != "permission denied" {
		t.Fatalf("permission result = %#v, error = %v", result, err)
	}

	discovery.err = errors.New("database included a secret")
	if _, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentList, toolArguments(t, `{"p":"post"}`)); err == nil {
		t.Fatal("document_list hid an internal failure as a safe tool error")
	} else if errors.As(err, &executionErr) {
		t.Fatalf("internal failure was exposed as ToolExecutionError: %v", err)
	}
}

func TestNewDocumentDiscoveryToolsRejectsNil(t *testing.T) {
	if _, err := NewDocumentDiscoveryTools(nil, &recordingWorkDocumentDiscovery{}, &recordingPageDocumentDiscovery{}, &recordingProgramEventDocumentDiscovery{}); err == nil {
		t.Fatal("NewDocumentDiscoveryTools(nil) succeeded")
	}
}

func TestDocumentDiscoveryListsProgramEvents(t *testing.T) {
	updatedAt := time.Date(2026, time.August, 28, 1, 2, 3, 0, time.UTC)
	slug := "live-set"
	programEvents := &recordingProgramEventDocumentDiscovery{response: &managev1.ListProgramEventsAdminResponse{
		Events: []*managev1.ProgramEventSummary{{
			Id: discoveryDocumentID, Title: "Live Set", Slug: &slug, SourceLocale: "ko",
			Status:    managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED,
			UpdatedAt: timestamppb.New(updatedAt),
		}},
		Pagination: &commonv1.PaginationResponse{Total: 1, Limit: 10},
	}}
	tools, err := NewDocumentDiscoveryTools(&recordingPostDocumentDiscovery{}, &recordingWorkDocumentDiscovery{}, &recordingPageDocumentDiscovery{}, programEvents)
	if err != nil {
		t.Fatal(err)
	}
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentList, toolArguments(t, `{"p":"program_event","q":"Live","limit":10}`))
	if err != nil {
		t.Fatal(err)
	}
	if programEvents.request == nil || len(programEvents.request.Msg.Filters) != 1 || programEvents.request.Msg.Filters[0].Value != "Live" {
		t.Fatalf("Program Event request = %+v", programEvents.request)
	}
	documents := result.StructuredContent["documents"].([]any)
	document := documents[0].(map[string]any)
	if document["p"] != "program_event" || document["d"] != discoveryDocumentID || document["status"] != "published" {
		t.Fatalf("Program Event document = %+v", document)
	}
}

type recordingPostDocumentDiscovery struct {
	input  postdomain.AIDocumentListInput
	result postdomain.AIDocumentListResult
	err    error
}

func (discovery *recordingPostDocumentDiscovery) ListAIDocuments(
	_ context.Context,
	input postdomain.AIDocumentListInput,
) (postdomain.AIDocumentListResult, error) {
	discovery.input = input
	return discovery.result, discovery.err
}

type recordingWorkDocumentDiscovery struct {
	managev1connect.UnimplementedWorkServiceHandler
}

func (*recordingWorkDocumentDiscovery) ListWorksAdmin(
	context.Context,
	*connect.Request[managev1.ListWorksAdminRequest],
) (*connect.Response[managev1.ListWorksAdminResponse], error) {
	return connect.NewResponse(&managev1.ListWorksAdminResponse{}), nil
}

type recordingPageDocumentDiscovery struct {
	managev1connect.UnimplementedPageServiceHandler
}

type recordingProgramEventDocumentDiscovery struct {
	managev1connect.UnimplementedProgramEventServiceHandler
	request  *connect.Request[managev1.ListProgramEventsAdminRequest]
	response *managev1.ListProgramEventsAdminResponse
}

func (discovery *recordingProgramEventDocumentDiscovery) ListProgramEventsAdmin(
	_ context.Context,
	request *connect.Request[managev1.ListProgramEventsAdminRequest],
) (*connect.Response[managev1.ListProgramEventsAdminResponse], error) {
	discovery.request = request
	if discovery.response == nil {
		discovery.response = &managev1.ListProgramEventsAdminResponse{}
	}
	return connect.NewResponse(discovery.response), nil
}

func (*recordingPageDocumentDiscovery) ListPagesAdmin(
	context.Context,
	*connect.Request[managev1.ListPagesAdminRequest],
) (*connect.Response[managev1.ListPagesAdminResponse], error) {
	return connect.NewResponse(&managev1.ListPagesAdminResponse{}), nil
}
