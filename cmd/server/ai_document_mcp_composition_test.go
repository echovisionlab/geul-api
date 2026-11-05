package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	membercriteria "github.com/echovisionlab/geul-api/internal/member/criteria"
	"github.com/echovisionlab/geul-api/internal/oauthapp"
	releasepkg "github.com/echovisionlab/geul-api/internal/release"
	"github.com/echovisionlab/geul-api/internal/sitesettings"

	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"

	"connectrpc.com/connect"
	aidocumentadapter "github.com/echovisionlab/geul-api/internal/adapters/aidocument"
	filemediaadapter "github.com/echovisionlab/geul-api/internal/adapters/filemedia"
	mcpadapter "github.com/echovisionlab/geul-api/internal/adapters/mcp"
	aidocument "github.com/echovisionlab/geul-api/internal/aidocument"
	artistdomain "github.com/echovisionlab/geul-api/internal/artist"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	formdomain "github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/label"
	"github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/maptheme"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/menu"
	"github.com/echovisionlab/geul-api/internal/page"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/programevent"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	"github.com/echovisionlab/geul-api/internal/series"
	translationapplication "github.com/echovisionlab/geul-api/internal/translation/application"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1/intrav1connect"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

const (
	compositionInternalSecret            = "composition-internal-secret"
	compositionAuthHeaderName            = "X-Authenticated-Context-B64"
	compositionInternalServiceHeaderName = "X-Internal-Service"
	compositionIdentityID                = "11111111-1111-4111-8111-111111111111"
	compositionMemberID                  = "22222222-2222-4222-8222-222222222222"
	compositionFileID                    = "33333333-3333-4333-8333-333333333333"
)

func TestAIDocumentCompositionContainsEveryDocumentedDomain(t *testing.T) {
	port := &compositionDomainPort{}
	registrations := completeTestAIDocumentRegistrations(port)
	registeredDomains := make([]aidocument.Domain, 0, len(registrations.values()))
	for _, registration := range registrations.values() {
		registeredDomains = append(registeredDomains, registration.Domain)
	}
	require.Equal(t, []aidocument.Domain{
		aidocument.DomainPost,
		aidocument.DomainPage,
		aidocument.DomainWork,
		aidocument.DomainProgramEvent,
		aidocument.DomainRelease,
		aidocument.DomainArtist,
		aidocument.DomainLabel,
		aidocument.DomainMenu,
		aidocument.DomainEmailTemplate,
		aidocument.DomainEmailLayout,
		aidocument.DomainCampaign,
		aidocument.DomainForm,
		aidocument.DomainPrivacy,
		aidocument.DomainTerms,
		aidocument.DomainPostSeries,
	}, registeredDomains)

	policyOwner := &compositionLegalPolicyApplication{}
	references := compositionContentApplications()
	references.legalPolicies = policyOwner
	composition, err := newAIDocumentMCPComposition(
		registrations,
		&compositionPostApplication{},
		&compositionWorkApplication{},
		&compositionPageApplication{},
		&programevent.Service{},
		&compositionReleaseApplication{},
		&compositionArtistApplication{},
		references,
		&translationapplication.Service{},
		&compositionFileRuntime{},
		aiDocumentMCPConfig{
			internalServiceSecret:     compositionInternalSecret,
			authHeaderName:            compositionAuthHeaderName,
			internalServiceHeaderName: compositionInternalServiceHeaderName,
			editorCollabURL:           "http://collab.invalid",
			editorCollabHTTPClient:    http.DefaultClient,
		},
		&compositionSignalPublisher{},
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, composition.connectService)
	require.NotNil(t, composition.mcpHandler)

	t.Run("initialize exposes sync recovery guidance", func(t *testing.T) {
		response := httptest.NewRecorder()
		request := compositionMCPJSONRequest("", `{
			"jsonrpc":"2.0","id":1,"method":"initialize",
			"params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}
		}`)
		request.Header.Del("MCP-Protocol-Version")
		composition.mcpHandler.ServeHTTP(response, request)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		var envelope struct {
			Result struct {
				ServerInfo   mcpserver.Implementation `json:"serverInfo"`
				Instructions string                   `json:"instructions"`
			} `json:"result"`
		}
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
		require.Equal(t, "21", envelope.Result.ServerInfo.Version)
		require.NotContains(t, envelope.Result.Instructions, "DSUB")
		for _, guardrail := range []string{
			"sync_required result with isError=false and applied=false",
			"discard previous pages and restart without a cursor",
			"compare the latest values with the intended edit",
			"Never just replace an expected revision and resend stale operations",
			"not proof that concurrent edits are disjoint",
			"3 cycles per task edit",
			"not routine version changes",
			"document_read m=outline",
			"consecutive edits need no reread",
			"refuse a block with marks, links, colors or breaks",
			"p=release",
			"p=artist",
			"menu_list",
			"menu_locations_get",
			"Use work_settings_get before updating Work metadata or clients",
			"observed_policy.member_criteria_ids",
			"policy_create creates an empty draft without email",
			"policy_schedule and policy_activate_now start notice email delivery",
			"policy_delete allows every lifecycle and retains delivery history",
			"Read site_settings_get/mail_adapter_list before site_settings_update/mail_adapter_sender_update",
			"sender changes keep credentials",
			"Pass oauth_app_get's revision to oauth_app_update/delete/secret_rotate",
		} {
			require.Contains(t, envelope.Result.Instructions, guardrail)
		}
	})

	t.Run("menu discovery is registered on the production MCP surface", func(t *testing.T) {
		response := httptest.NewRecorder()
		composition.mcpHandler.ServeHTTP(response, compositionMCPJSONRequest("", `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`))
		require.Equal(t, http.StatusOK, response.Code)
		var envelope struct {
			Result struct {
				Tools []mcpserver.Tool `json:"tools"`
			} `json:"result"`
		}
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
		names := make([]string, 0, len(envelope.Result.Tools))
		for _, tool := range envelope.Result.Tools {
			names = append(names, tool.Name)
		}
		require.Contains(t, names, "menu_list")
		require.Contains(t, names, "menu_locations_get")
		for _, name := range []string{"policy_list", "policy_get", "policy_create", "policy_schedule", "policy_schedule_cancel", "policy_activate_now", "policy_delete"} {
			require.Contains(t, names, name)
		}
	})

	t.Run("legal policy creation dispatches with authenticated context", func(t *testing.T) {
		for _, kind := range []string{"terms", "privacy"} {
			response := httptest.NewRecorder()
			composition.mcpHandler.ServeHTTP(response, compositionMCPJSONRequest("", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"policy_create","arguments":{"document_type":"`+kind+`","title":"New policy"}}}`))
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			var envelope struct {
				Result mcpserver.ToolResult `json:"result"`
			}
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
			require.False(t, envelope.Result.IsError, response.Body.String())
			require.Equal(t, kind, envelope.Result.StructuredContent["document_type"])
			require.Equal(t, "draft", envelope.Result.StructuredContent["status"])
			require.Equal(t, "New policy", policyOwner.title)
			require.Equal(t, kind, policyOwner.kind)
			require.NotNil(t, policyOwner.principal)
			require.Equal(t, compositionIdentityID, policyOwner.principal.IdentityID.String())
			require.Equal(t, compositionMemberID, policyOwner.principal.MemberID.String())
		}
	})

	registrations.emailLayout = aidocumentadapter.DomainRegistration{}
	_, err = newAIDocumentMCPComposition(
		registrations,
		&compositionPostApplication{},
		&compositionWorkApplication{},
		&compositionPageApplication{},
		&programevent.Service{},
		&compositionReleaseApplication{},
		&compositionArtistApplication{},
		compositionContentApplications(),
		&translationapplication.Service{},
		&compositionFileRuntime{},
		aiDocumentMCPConfig{
			internalServiceSecret:     compositionInternalSecret,
			authHeaderName:            compositionAuthHeaderName,
			internalServiceHeaderName: compositionInternalServiceHeaderName,
			editorCollabURL:           "http://collab.invalid",
			editorCollabHTTPClient:    http.DefaultClient,
		},
		&compositionSignalPublisher{},
		nil,
	)
	require.Error(t, err)
}

func TestAIDocumentRPCAndMCPUseOneApplicationWithoutRepeatedCredentialLookup(t *testing.T) {
	port := &compositionDomainPort{}
	composition, err := newAIDocumentMCPComposition(
		completeTestAIDocumentRegistrations(port),
		&compositionPostApplication{},
		&compositionWorkApplication{},
		&compositionPageApplication{},
		&programevent.Service{},
		&compositionReleaseApplication{},
		&compositionArtistApplication{},
		compositionContentApplications(),
		&translationapplication.Service{},
		&compositionFileRuntime{},
		aiDocumentMCPConfig{
			internalServiceSecret:     compositionInternalSecret,
			authHeaderName:            compositionAuthHeaderName,
			internalServiceHeaderName: compositionInternalServiceHeaderName,
			editorCollabURL:           "http://collab.invalid",
			editorCollabHTTPClient:    http.DefaultClient,
		},
		&compositionSignalPublisher{},
		nil,
	)
	require.NoError(t, err)

	_, err = composition.connectService.OpenAIDocument(t.Context(), connect.NewRequest(&managev1.OpenAIDocumentRequest{
		Document: &managev1.AIDocumentReference{
			Domain: managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_POST, Reference: "post-a",
		},
		Locale: &managev1.AIDocumentLocale{Code: "en"},
	}))
	require.NoError(t, err)
	require.Equal(t, 1, port.loadCount())

	response := httptest.NewRecorder()
	composition.mcpHandler.ServeHTTP(response, compositionMCPRequest(""))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, 2, port.loadCount())
	principal := port.authenticatedPrincipal()
	require.NotNil(t, principal)
	require.Equal(t, compositionIdentityID, principal.IdentityID.String())
	require.Equal(t, compositionMemberID, principal.MemberID.String())
	require.Empty(t, principal.SessionID)

	response = httptest.NewRecorder()
	composition.mcpHandler.ServeHTTP(response, compositionMCPRequest("Bearer must-not-be-reverified"))
	require.Equal(t, http.StatusUnauthorized, response.Code)
	require.Equal(t, 2, port.loadCount(), "main MCP must not dispatch or repeat credential/Member authentication")
}

func TestAIDocumentCompositionListsAndDispatchesFileToolsWithOneAuthenticatedContext(t *testing.T) {
	files := &compositionFileRuntime{}
	composition, err := newAIDocumentMCPComposition(
		completeTestAIDocumentRegistrations(&compositionDomainPort{}),
		&compositionPostApplication{},
		&compositionWorkApplication{},
		&compositionPageApplication{},
		&programevent.Service{},
		&compositionReleaseApplication{},
		&compositionArtistApplication{},
		compositionContentApplications(),
		&translationapplication.Service{},
		files,
		aiDocumentMCPConfig{
			internalServiceSecret:     compositionInternalSecret,
			authHeaderName:            compositionAuthHeaderName,
			internalServiceHeaderName: compositionInternalServiceHeaderName,
			editorCollabURL:           "http://collab.invalid",
			editorCollabHTTPClient:    http.DefaultClient,
		},
		&compositionSignalPublisher{},
		nil,
	)
	require.NoError(t, err)

	response := httptest.NewRecorder()
	composition.mcpHandler.ServeHTTP(response, compositionMCPJSONRequest("", `{
		"jsonrpc":"2.0","id":1,"method":"tools/list"
	}`))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), `"name":"document_list"`)
	require.Contains(t, response.Body.String(), `"name":"reference_search"`)
	for _, tool := range []string{
		"post_settings_get", "page_settings_get", "document_catalog",
		"program_event_create", "program_event_settings_get", "program_event_settings_update",
		"program_event_publish", "program_event_archive", "program_event_delete",
		"program_event_type_list", "program_event_series_list", "label_list",
		"member_admin_list", "member_admin_get",
		"site_settings_get", "site_settings_update", "mail_adapter_list", "mail_adapter_sender_update",
		"release_create", "release_settings_get", "release_settings_update", "release_publish", "release_unpublish", "release_delete",
		"release_artwork_set", "release_artwork_remove", "release_slug_check",
		"release_relations_get", "release_artists_set", "release_labels_set", "release_categories_set", "release_genres_set", "release_styles_set", "release_formats_set", "release_credits_set",
		"track_list", "track_create", "track_settings_update", "track_delete", "track_credits_set", "track_reorder",
		"map_place_get", "map_place_get_many", "map_place_list", "map_place_create", "map_place_settings_update", "map_place_delete",
		"map_theme_list", "map_theme_resolve", "map_theme_get", "map_theme_create", "map_theme_copy", "map_theme_delete", "map_theme_set_default", "map_theme_settings_update",
		"genre_list", "style_list", "format_list", "form_list", "post_series_list", "member_tag_list",
		"oauth_app_list", "oauth_app_get", "oauth_app_create", "oauth_app_update", "oauth_app_delete", "oauth_app_secret_rotate",
		"oauth_connected_tool_list", "oauth_connected_tool_disconnect", "member_criteria_list",
		"program_event_media_list", "program_event_media_add", "program_event_media_remove", "program_event_media_reorder",
	} {
		require.Contains(t, response.Body.String(), `"name":"`+tool+`"`)
	}
	require.Contains(t, response.Body.String(), `"name":"file_list"`)
	require.Contains(t, response.Body.String(), `"name":"document_featured_image_set"`)
	require.Contains(t, response.Body.String(), `"name":"work_credit_add"`)
	require.Contains(t, response.Body.String(), `"name":"file_transfer"`)
	require.Contains(t, response.Body.String(), `"name":"file_read"`)
	for _, tool := range []string{
		"file_upload", "file_deletion_impact_get", "file_delete", "file_rename", "file_move",
		"file_folder_create", "file_folder_rename", "file_folder_move", "file_folder_delete",
		"document_file_caption_update",
	} {
		require.Contains(t, response.Body.String(), `"name":"`+tool+`"`)
	}
	require.Contains(t, response.Body.String(), `"openai/fileParams":["file"]`)
	require.Contains(t, response.Body.String(), `"name":"document_file_add"`)
	require.Contains(t, response.Body.String(), `"name":"document_file_replace"`)
	require.Contains(t, response.Body.String(), `"name":"document_file_remove"`)
	require.Contains(t, response.Body.String(), `"name":"document_file_download_policy_get"`)
	require.Contains(t, response.Body.String(), `"name":"document_file_download_policy_update"`)
	require.Contains(t, response.Body.String(), `"name":"file_usage_list"`)

	response = httptest.NewRecorder()
	composition.mcpHandler.ServeHTTP(response, compositionMCPJSONRequest("", `{
		"jsonrpc":"2.0","id":2,"method":"tools/call",
		"params":{"name":"file_read","arguments":{"f":"`+compositionFileID+`"}}
	}`))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())

	principal, fileID, calls := files.snapshot()
	require.Equal(t, 1, calls)
	require.Equal(t, compositionFileID, fileID)
	require.NotNil(t, principal)
	require.Equal(t, compositionIdentityID, principal.IdentityID.String())
	require.Equal(t, compositionMemberID, principal.MemberID.String())
	require.Empty(t, principal.SessionID)
}

func TestAIDocumentCompositionDispatchesStandaloneUploadAndFileDeletion(t *testing.T) {
	files := &compositionFileRuntime{}
	composition, err := newAIDocumentMCPComposition(
		completeTestAIDocumentRegistrations(&compositionDomainPort{}),
		&compositionPostApplication{}, &compositionWorkApplication{}, &compositionPageApplication{},
		&programevent.Service{}, &compositionReleaseApplication{}, &compositionArtistApplication{},
		compositionContentApplications(), &translationapplication.Service{}, files,
		aiDocumentMCPConfig{
			internalServiceSecret: compositionInternalSecret, authHeaderName: compositionAuthHeaderName,
			internalServiceHeaderName: compositionInternalServiceHeaderName,
			editorCollabURL:           "http://collab.invalid", editorCollabHTTPClient: http.DefaultClient,
		}, &compositionSignalPublisher{}, nil,
	)
	require.NoError(t, err)

	response := httptest.NewRecorder()
	composition.mcpHandler.ServeHTTP(response, compositionMCPJSONRequest("", `{
		"jsonrpc":"2.0","id":1,"method":"tools/call",
		"params":{"name":"file_upload","arguments":{
			"file":{"download_url":"https://files.example.test/opaque?signature=secret","file_id":"chatgpt-opaque-id","file_name":"diagram.png","mime_type":"image/png"},
			"kind":"image","correlation_id":"b2011513-d89a-4c34-90e5-b59b3cb874f2"
		}}
	}`))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.NotContains(t, response.Body.String(), `"isError":true`)
	require.Contains(t, response.Body.String(), compositionFileID)
	require.NotContains(t, response.Body.String(), "signature=secret")
	require.NotNil(t, files.importInput)
	require.Equal(t, filemedia.UploadEditorImage, files.importInput.UploadType)
	require.Equal(t, "diagram.png", files.importInput.FileName)
	require.Equal(t, "b2011513-d89a-4c34-90e5-b59b3cb874f2", files.importInput.CorrelationID)
	require.NotNil(t, files.principal)
	require.Equal(t, compositionMemberID, files.principal.MemberID.String())

	response = httptest.NewRecorder()
	composition.mcpHandler.ServeHTTP(response, compositionMCPJSONRequest("", `{
		"jsonrpc":"2.0","id":2,"method":"tools/call",
		"params":{"name":"file_delete","arguments":{"file_ids":["`+compositionFileID+`"]}}
	}`))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.NotContains(t, response.Body.String(), `"isError":true`)
	require.Contains(t, response.Body.String(), `"accepted_file_ids":["`+compositionFileID+`"]`)
	require.Equal(t, []string{compositionFileID}, files.deleteRequest.FileIds)
	require.Equal(t, compositionMemberID, files.principal.MemberID.String())
}

func TestInteractiveMutationRelayClientUsesConfiguredURLAndInternalTrust(t *testing.T) {
	t.Parallel()
	receiver := &compositionRelayReceiver{}
	path, handler := intrav1connect.NewInternalCollaborationRelayServiceHandler(receiver)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	relay, err := newInteractiveMutationRelay(server.Client(), server.URL, compositionInternalSecret, compositionInternalServiceHeaderName)
	require.NoError(t, err)
	operation := aidocument.DeleteBlockOperation("block-1")

	err = relay.RelayCommitted(t.Context(), aidocumentadapter.AcceptedInteractiveMutation{
		Origin: intrav1.InteractiveAIDocumentMutationOrigin_INTERACTIVE_AI_DOCUMENT_MUTATION_ORIGIN_MCP,
		Request: aidocument.ApplyRequest{
			Protocol:                 aidocument.ProtocolVersion,
			Profile:                  aidocument.DomainPost,
			Document:                 "11111111-1111-4111-8111-111111111111",
			Locale:                   "ko",
			ExpectedDocumentRevision: "document-before",
			Operations:               []aidocument.Operation{operation},
		},
		Result: aidocument.ApplyResult{
			DocumentRevision: "document-after",
			Changed:          true,
			Changes:          []aidocument.Change{{Operation: 0, Kind: aidocument.OperationDeleteBlock}},
			Normalized:       []aidocument.Operation{operation},
		},
		NormalizedOperations: []aidocument.Operation{operation},
		ActorMemberID:        auth.MemberID(compositionMemberID),
	})
	require.NoError(t, err)
	require.Equal(t, compositionInternalSecret, receiver.internalServiceSecret)
	require.Equal(t, compositionMemberID, receiver.request.GetActorMemberId())
}

type compositionDomainPort struct {
	mu        sync.Mutex
	loads     int
	principal *auth.UserInfo
}

// compositionFileRuntime leaves the File library use cases it does not
// record unimplemented; no composition test reaches one.
type compositionFileRuntime struct {
	mcpadapter.FileManager
	mu             sync.Mutex
	principal      *auth.UserInfo
	deliveryFileID string
	deliveryCalls  int
	importInput    *filemedia.RemoteFileImportInput
	deleteRequest  *managev1.DeleteFilesRequest
}

type compositionSignalPublisher struct{}

// compositionPostApplication lists no AI documents and leaves the Post use
// cases unimplemented; no composition test reaches one.
type compositionPostApplication struct {
	mcpadapter.PostManagementApplication
	mcpadapter.PostRelatedApplication
}

func (*compositionPostApplication) ListAIDocuments(
	context.Context,
	postdomain.AIDocumentListInput,
) (postdomain.AIDocumentListResult, error) {
	return postdomain.AIDocumentListResult{Items: []postdomain.AIDocumentListItem{}, Limit: 20}, nil
}

// compositionWorkApplication lists the Work use cases; no composition test
// reaches one.
type compositionWorkApplication struct {
	mcpadapter.WorkDocumentDiscovery
	mcpadapter.WorkManagementApplication
	mcpadapter.WorkRelatedApplication
}

// compositionPageApplication carries the Page use cases the composition
// binds; no test calls them.
type compositionPageApplication struct {
	*page.Service
}

type compositionFileReferences struct {
	mcpadapter.FileReferenceDiscovery
}

func compositionContentApplications() contentMCPApplications {
	return contentMCPApplications{
		legalPolicies: &legal.Service{},
		categories:    &referencecatalog.Service{},
		tags:          &referencecatalog.Service{},
		clients:       &referencecatalog.Service{},
		mapPlaces:     &referencecatalog.Service{},
		members:       &member.Service{},
		artists:       &artistdomain.Service{},
		files:         &compositionFileReferences{},
		eventTypes:    &programevent.Service{},
		eventSeries:   &programevent.Service{},
		labels:        &label.Service{},
		genres:        &referencecatalog.Service{},
		styles:        &referencecatalog.Service{},
		formats:       &referencecatalog.Service{},
		forms:         &formdomain.Service{},
		postSeries:    &series.Service{},
		tracks:        &releasepkg.Service{},
		mapThemes:     &maptheme.Service{},
		menus:         &menu.Service{},
		menuSettings:  &sitesettings.Service{},
		siteSettings:  &sitesettings.Service{},
		mailAdapters:  &emaildelivery.Service{},
		emailLayouts:  (&emailauthoring.InternalService{}).LayoutAIDocuments(),
		oauthApps:     &oauthapp.Service{},
		criteria:      &membercriteria.Service{},
	}
}

func (*compositionSignalPublisher) NotifyProtobuf(context.Context, string, proto.Message) error {
	return nil
}

type compositionRelayReceiver struct {
	intrav1connect.UnimplementedInternalCollaborationRelayServiceHandler
	internalServiceSecret string
	request               *intrav1.RelayInteractiveAIDocumentMutationRequest
}

func (receiver *compositionRelayReceiver) RelayInteractiveAIDocumentMutation(
	_ context.Context,
	request *connect.Request[intrav1.RelayInteractiveAIDocumentMutationRequest],
) (*connect.Response[intrav1.RelayInteractiveAIDocumentMutationResponse], error) {
	receiver.internalServiceSecret = request.Header().Get(compositionInternalServiceHeaderName)
	receiver.request = request.Msg
	return connect.NewResponse(&intrav1.RelayInteractiveAIDocumentMutationResponse{}), nil
}

func (*compositionFileRuntime) InitiateMultipartUpload(context.Context, filemedia.InitiateMultipartUploadCommand) (*filemedia.MultipartUpload, error) {
	return &filemedia.MultipartUpload{}, nil
}

func (*compositionFileRuntime) FindMultipartUploadCandidate(context.Context, filemedia.FindMultipartUploadCandidateQuery) (*filemedia.MultipartCandidate, error) {
	return &filemedia.MultipartCandidate{}, nil
}

func (*compositionFileRuntime) CompleteMultipartUpload(context.Context, filemedia.CompleteMultipartUploadCommand) (*filemedia.CompletedUpload, error) {
	return &filemedia.CompletedUpload{}, nil
}

func (*compositionFileRuntime) DownloadFromURL(context.Context, filemedia.DownloadFromURLCommand) (*filemedia.ImportedFile, error) {
	return &filemedia.ImportedFile{}, nil
}

func (runtime *compositionFileRuntime) ImportRemoteFile(
	ctx context.Context,
	input filemedia.RemoteFileImportInput,
) (*filemedia.ImportedFile, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.importInput = &input
	if principal := auth.GetUser(ctx); principal != nil {
		copy := *principal
		runtime.principal = &copy
	}
	return &filemedia.ImportedFile{
		FileID: compositionFileID,
		Delivery: &filemedia.Delivery{
			FileID: compositionFileID, Extension: "png", MimeType: "image/png", FileSize: 68,
			FileName: &input.FileName,
		},
	}, nil
}

func (runtime *compositionFileRuntime) DeleteFiles(ctx context.Context, command filemedia.DeleteFilesCommand) (filemedia.FileDeletionOutcome, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.deleteRequest = &managev1.DeleteFilesRequest{FileIds: append([]string(nil), command.FileIDs...)}
	if principal := auth.GetUser(ctx); principal != nil {
		copy := *principal
		runtime.principal = &copy
	}
	return filemedia.FileDeletionOutcome{AcceptedFileIDs: command.FileIDs}, nil
}

func (runtime *compositionFileRuntime) GetMediaDelivery(ctx context.Context, query filemedia.GetMediaDeliveryQuery) (*filemedia.MediaDeliveryResult, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.deliveryCalls++
	runtime.deliveryFileID = query.FileID
	if principal := auth.GetUser(ctx); principal != nil {
		copy := *principal
		runtime.principal = &copy
	}
	return &filemedia.MediaDeliveryResult{Delivery: &filemedia.Delivery{
		FileID: query.FileID, Extension: "bin", MimeType: "application/octet-stream", FileSize: 1,
	}}, nil
}

func (*compositionFileRuntime) GetFileDownloadPolicy(context.Context, filemedia.GetFileDownloadPolicyQuery) (*filemedia.DownloadPolicy, error) {
	return nil, nil
}

func (*compositionFileRuntime) UpdateFileDownloadPolicy(context.Context, filemedia.UpdateFileDownloadPolicyCommand) (*filemedia.DownloadPolicy, error) {
	return nil, nil
}

func (*compositionFileRuntime) ListFileUsages(context.Context, filemedia.ListFileUsagesQuery) (filemedia.FileUsagePage, error) {
	return filemedia.FileUsagePage{}, nil
}

func (runtime *compositionFileRuntime) snapshot() (*auth.UserInfo, string, int) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	var principal *auth.UserInfo
	if runtime.principal != nil {
		copy := *runtime.principal
		principal = &copy
	}
	return principal, runtime.deliveryFileID, runtime.deliveryCalls
}

func (port *compositionDomainPort) Load(
	ctx context.Context,
	identity aidocument.DocumentIdentity,
	locale aidocument.Locale,
) (aidocument.Document, error) {
	port.mu.Lock()
	defer port.mu.Unlock()
	port.loads++
	if principal := auth.GetUser(ctx); principal != nil {
		copy := *principal
		port.principal = &copy
	}
	return aidocument.Document{
		Identity: identity, DocumentRevision: "revision-a", SourceLocale: "en", Locale: locale,
		LocaleExists: true, Catalog: aidocument.Catalog{Fingerprint: "catalog-a"},
	}, nil
}

func (*compositionDomainPort) ValidateMutation(context.Context, aidocument.ApplyRequest) (aidocument.ValidationResult, error) {
	return aidocument.ValidationResult{}, nil
}

func (*compositionDomainPort) ExecuteMutation(context.Context, aidocument.ApplyRequest) (aidocument.ApplyResult, error) {
	return aidocument.ApplyResult{DocumentRevision: "revision-b"}, nil
}

func (port *compositionDomainPort) loadCount() int {
	port.mu.Lock()
	defer port.mu.Unlock()
	return port.loads
}

func (port *compositionDomainPort) authenticatedPrincipal() *auth.UserInfo {
	port.mu.Lock()
	defer port.mu.Unlock()
	if port.principal == nil {
		return nil
	}
	copy := *port.principal
	return &copy
}

func completeTestAIDocumentRegistrations(port aidocument.DomainPort) aiDocumentDomainRegistrations {
	registration := func(domain aidocument.Domain) aidocumentadapter.DomainRegistration {
		return aidocumentadapter.DomainRegistration{Domain: domain, Port: port}
	}
	return aiDocumentDomainRegistrations{
		post: registration(aidocument.DomainPost), page: registration(aidocument.DomainPage),
		work: registration(aidocument.DomainWork), programEvent: registration(aidocument.DomainProgramEvent),
		release: registration(aidocument.DomainRelease), artist: registration(aidocument.DomainArtist),
		label: registration(aidocument.DomainLabel), menu: registration(aidocument.DomainMenu),
		emailTemplate: registration(aidocument.DomainEmailTemplate), emailLayout: registration(aidocument.DomainEmailLayout),
		campaign: registration(aidocument.DomainCampaign), form: registration(aidocument.DomainForm),
		privacy: registration(aidocument.DomainPrivacy), terms: registration(aidocument.DomainTerms),
		postSeries: registration(aidocument.DomainPostSeries),
	}
}

func compositionMCPRequest(authorization string) *http.Request {
	return compositionMCPJSONRequest(authorization, `{
		"jsonrpc":"2.0","id":1,"method":"tools/call",
		"params":{"name":"document_open","arguments":{"p":"post","d":"44444444-4444-4444-8444-444444444444","l":"en"}}
	}`)
}

func compositionMCPJSONRequest(authorization, body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", mcpserver.ProtocolVersion)
	request.Header.Set(compositionInternalServiceHeaderName, compositionInternalSecret)
	assertion, err := proto.Marshal(&intrav1.MCPAuthenticatedContext{
		IdentityId: compositionIdentityID, MemberId: compositionMemberID,
		DelegationId: "AAAAAAAAAAAAAAAAAAAAAA", DelegationName: "Example Member · Example Client",
		DelegationMethod: intrav1.MCPDelegationMethod_MCP_DELEGATION_METHOD_OAUTH,
	})
	if err != nil {
		panic(err)
	}
	request.Header.Set(compositionAuthHeaderName, base64.RawURLEncoding.EncodeToString(assertion))
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	return request
}

var _ aidocument.DomainPort = (*compositionDomainPort)(nil)
var _ filemediaadapter.MCPFileRuntime = (*compositionFileRuntime)(nil)

type compositionReleaseApplication struct {
	*releasepkg.Service
	principal *auth.UserInfo
}

func (application *compositionReleaseApplication) ListReleasesAdmin(ctx context.Context, _ releasepkg.ListReleasesQuery) (releasepkg.ReleasePage, error) {
	application.principal = auth.GetUser(ctx)
	return releasepkg.ReleasePage{Releases: []releasepkg.ReleaseWithStats{{View: releasepkg.View{Title: "Release A", Release: releasepkg.Release{
		ID: "44444444-4444-4444-8444-444444444444", Status: "draft", SourceLocale: "ko", UpdatedAt: time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC),
	}}}}, Total: 1, Limit: 20}, nil
}

type compositionArtistApplication struct {
	principal *auth.UserInfo
}

func (application *compositionArtistApplication) ListArtistsAdmin(ctx context.Context, _ artistdomain.ListArtistsQuery) (artistdomain.AdminArtistPage, error) {
	application.principal = auth.GetUser(ctx)
	updatedAt := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	return artistdomain.AdminArtistPage{Artists: []artistdomain.AdminView{{Artist: artistdomain.View{Name: "Artist A", Artist: artistdomain.Artist{
		ID: "55555555-5555-4555-8555-555555555555", Status: "published", SourceLocale: "en", UpdatedAt: &updatedAt,
	}}}}, Pagination: artistdomain.Pagination{Total: 1, Limit: 20}}, nil
}

func TestAIDocumentCompositionDiscoversReleasesAndArtistsWithAuthenticatedContext(t *testing.T) {
	releases, artists := &compositionReleaseApplication{}, &compositionArtistApplication{}
	composition, err := newAIDocumentMCPComposition(
		completeTestAIDocumentRegistrations(&compositionDomainPort{}),
		&compositionPostApplication{}, &compositionWorkApplication{}, &compositionPageApplication{}, &programevent.Service{},
		releases, artists, compositionContentApplications(), &translationapplication.Service{}, &compositionFileRuntime{},
		aiDocumentMCPConfig{
			internalServiceSecret:     compositionInternalSecret,
			authHeaderName:            compositionAuthHeaderName,
			internalServiceHeaderName: compositionInternalServiceHeaderName,
			editorCollabURL:           "http://collab.invalid",
			editorCollabHTTPClient:    http.DefaultClient,
		},
		&compositionSignalPublisher{}, nil,
	)
	require.NoError(t, err)
	for _, test := range []struct{ profile, id, title string }{
		{"release", "44444444-4444-4444-8444-444444444444", "Release A"},
		{"artist", "55555555-5555-4555-8555-555555555555", "Artist A"},
	} {
		t.Run(test.profile, func(t *testing.T) {
			response := httptest.NewRecorder()
			composition.mcpHandler.ServeHTTP(response, compositionMCPJSONRequest("", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"document_list","arguments":{"p":"`+test.profile+`"}}}`))
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			var envelope struct {
				Result struct {
					IsError           bool `json:"isError"`
					StructuredContent struct {
						Documents  []struct{ P, D, Title string }
						NextOffset *int `json:"next_offset"`
					} `json:"structuredContent"`
				} `json:"result"`
			}
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
			require.False(t, envelope.Result.IsError)
			require.Len(t, envelope.Result.StructuredContent.Documents, 1)
			document := envelope.Result.StructuredContent.Documents[0]
			require.Equal(t, test.profile, document.P)
			require.Equal(t, test.id, document.D)
			require.Equal(t, test.title, document.Title)
			require.Nil(t, envelope.Result.StructuredContent.NextOffset)
			principal := releases.principal
			if test.profile == "artist" {
				principal = artists.principal
			}
			require.NotNil(t, principal)
			require.Equal(t, compositionIdentityID, principal.IdentityID.String())
			require.Equal(t, compositionMemberID, principal.MemberID.String())
		})
	}
}

// The HTTP composition must preserve the authenticated caller when dispatching
// lifecycle commands to the owning Legal services.
type compositionLegalPolicyApplication struct {
	mcpadapter.LegalPolicyManagementApplication
	principal *auth.UserInfo
	title     string
	kind      string
}

func (application *compositionLegalPolicyApplication) CreatePolicyVersion(ctx context.Context, kind legal.PolicyKind, command legal.CreatePolicyVersionCommand) (legal.Policy, error) {
	application.principal = auth.GetUser(ctx)
	application.title = *command.Title
	application.kind = string(kind)
	status := legal.TermsDraft
	if kind == legal.Privacy {
		status = legal.PrivacyDraft
	}
	return legal.Policy{ID: compositionFileID, Version: 2, Title: application.title, SourceLocale: "ko", Revision: compositionIdentityID, Status: status, CreatedAt: time.Now(), UpdatedAt: time.Now()}, nil
}

func (publisher *compositionSignalPublisher) NotifyProtobufWithExecutor(
	ctx context.Context,
	_ eventpkg.DBTX,
	signal string,
	message proto.Message,
) error {
	return publisher.NotifyProtobuf(ctx, signal, message)
}
