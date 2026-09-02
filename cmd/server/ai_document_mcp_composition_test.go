package main

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	aidocumentadapter "github.com/echovisionlab/geul-api/internal/adapters/aidocument"
	filemediaadapter "github.com/echovisionlab/geul-api/internal/adapters/filemedia"
	aidocument "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/auth"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1/intrav1connect"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
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
	actual := make([]aidocument.Domain, 0, len(registrations.values()))
	for _, registration := range registrations.values() {
		actual = append(actual, registration.Domain)
	}
	require.Equal(t, []aidocument.Domain{
		aidocument.DomainPost,
		aidocument.DomainPage,
		aidocument.DomainWork,
		aidocument.DomainProgramEvent,
		aidocument.DomainMenu,
		aidocument.DomainEmailTemplate,
		aidocument.DomainEmailLayout,
		aidocument.DomainCampaign,
		aidocument.DomainForm,
		aidocument.DomainPrivacy,
		aidocument.DomainTerms,
		aidocument.DomainPostSeries,
	}, actual)

	composition, err := newAIDocumentMCPComposition(
		registrations,
		&compositionPostApplication{},
		&compositionWorkApplication{},
		&compositionPageApplication{},
		&compositionProgramEventApplication{},
		compositionReferenceApplications(),
		managev1connect.UnimplementedTranslationServiceHandler{},
		&compositionFileRuntime{},
		compositionInternalSecret,
		compositionAuthHeaderName,
		compositionInternalServiceHeaderName,
		"http://collab.invalid",
		http.DefaultClient,
		&compositionSignalPublisher{},
		nil,
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, composition.editorApplication)
	require.NotNil(t, composition.connectService)
	require.NotNil(t, composition.mcpHandler)

	registrations.emailLayout = aidocumentadapter.DomainRegistration{}
	_, err = newAIDocumentMCPComposition(
		registrations,
		&compositionPostApplication{},
		&compositionWorkApplication{},
		&compositionPageApplication{},
		&compositionProgramEventApplication{},
		compositionReferenceApplications(),
		managev1connect.UnimplementedTranslationServiceHandler{},
		&compositionFileRuntime{},
		compositionInternalSecret,
		compositionAuthHeaderName,
		compositionInternalServiceHeaderName,
		"http://collab.invalid",
		http.DefaultClient,
		&compositionSignalPublisher{},
		nil,
		nil,
	)
	require.Error(t, err)
}

func TestAIDocumentRPCAndMCPUseOneApplicationWithoutRepeatedPATLookup(t *testing.T) {
	port := &compositionDomainPort{}
	composition, err := newAIDocumentMCPComposition(
		completeTestAIDocumentRegistrations(port),
		&compositionPostApplication{},
		&compositionWorkApplication{},
		&compositionPageApplication{},
		&compositionProgramEventApplication{},
		compositionReferenceApplications(),
		managev1connect.UnimplementedTranslationServiceHandler{},
		&compositionFileRuntime{},
		compositionInternalSecret,
		compositionAuthHeaderName,
		compositionInternalServiceHeaderName,
		"http://collab.invalid",
		http.DefaultClient,
		&compositionSignalPublisher{},
		nil,
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
	require.Equal(t, 2, port.loadCount(), "main MCP must not dispatch or repeat PAT/Member authentication")
}

func TestAIDocumentCompositionListsAndDispatchesFileToolsWithOneAuthenticatedContext(t *testing.T) {
	files := &compositionFileRuntime{}
	composition, err := newAIDocumentMCPComposition(
		completeTestAIDocumentRegistrations(&compositionDomainPort{}),
		&compositionPostApplication{},
		&compositionWorkApplication{},
		&compositionPageApplication{},
		&compositionProgramEventApplication{},
		compositionReferenceApplications(),
		managev1connect.UnimplementedTranslationServiceHandler{},
		files,
		compositionInternalSecret,
		compositionAuthHeaderName,
		compositionInternalServiceHeaderName,
		"http://collab.invalid",
		http.DefaultClient,
		&compositionSignalPublisher{},
		nil,
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
	require.Contains(t, response.Body.String(), `"name":"file_list"`)
	require.Contains(t, response.Body.String(), `"name":"document_featured_image_set"`)
	require.Contains(t, response.Body.String(), `"name":"work_credit_add"`)
	require.Contains(t, response.Body.String(), `"name":"file_transfer"`)
	require.Contains(t, response.Body.String(), `"name":"file_read"`)
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

type compositionFileRuntime struct {
	mu             sync.Mutex
	principal      *auth.UserInfo
	deliveryFileID string
	deliveryCalls  int
}

type compositionSignalPublisher struct{}

type compositionPostApplication struct {
	managev1connect.UnimplementedPostServiceHandler
}

func (*compositionPostApplication) ListAIDocuments(
	context.Context,
	postdomain.AIDocumentListInput,
) (postdomain.AIDocumentListResult, error) {
	return postdomain.AIDocumentListResult{Items: []postdomain.AIDocumentListItem{}, Limit: 20}, nil
}

type compositionWorkApplication struct {
	managev1connect.UnimplementedWorkServiceHandler
}
type compositionPageApplication struct {
	managev1connect.UnimplementedPageServiceHandler
}
type compositionProgramEventApplication struct {
	managev1connect.UnimplementedProgramEventServiceHandler
}

type compositionCategoryReferences struct {
	managev1connect.UnimplementedCategoryServiceHandler
}
type compositionTagReferences struct {
	managev1connect.UnimplementedTagServiceHandler
}
type compositionClientReferences struct {
	managev1connect.UnimplementedClientServiceHandler
}
type compositionMapPlaceReferences struct {
	managev1connect.UnimplementedMapPlaceServiceHandler
}
type compositionMemberReferences struct {
	managev1connect.UnimplementedMemberServiceHandler
}
type compositionFileReferences struct {
	managev1connect.UnimplementedFileServiceHandler
}

func compositionReferenceApplications() contentReferenceApplications {
	return contentReferenceApplications{
		categories: &compositionCategoryReferences{},
		tags:       &compositionTagReferences{},
		clients:    &compositionClientReferences{},
		mapPlaces:  &compositionMapPlaceReferences{},
		members:    &compositionMemberReferences{},
		files:      &compositionFileReferences{},
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

func (*compositionFileRuntime) InitiateMultipartUpload(
	context.Context,
	*connect.Request[managev1.InitiateMultipartUploadRequest],
) (*connect.Response[managev1.InitiateMultipartUploadResponse], error) {
	return connect.NewResponse(&managev1.InitiateMultipartUploadResponse{}), nil
}

func (*compositionFileRuntime) FindMultipartUploadCandidate(
	context.Context,
	*connect.Request[managev1.FindMultipartUploadCandidateRequest],
) (*connect.Response[managev1.FindMultipartUploadCandidateResponse], error) {
	return connect.NewResponse(&managev1.FindMultipartUploadCandidateResponse{}), nil
}

func (*compositionFileRuntime) CompleteMultipartUpload(
	context.Context,
	*connect.Request[managev1.CompleteMultipartUploadRequest],
) (*connect.Response[managev1.CompleteMultipartUploadResponse], error) {
	return connect.NewResponse(&managev1.CompleteMultipartUploadResponse{}), nil
}

func (*compositionFileRuntime) DownloadFromUrl(
	context.Context,
	*connect.Request[managev1.DownloadFromUrlRequest],
) (*connect.Response[managev1.DownloadFromUrlResponse], error) {
	return connect.NewResponse(&managev1.DownloadFromUrlResponse{}), nil
}

func (runtime *compositionFileRuntime) GetMediaDelivery(
	ctx context.Context,
	request *connect.Request[managev1.GetMediaDeliveryRequest],
) (*connect.Response[managev1.GetMediaDeliveryResponse], error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.deliveryCalls++
	runtime.deliveryFileID = request.Msg.GetFileId()
	if principal := auth.GetUser(ctx); principal != nil {
		copy := *principal
		runtime.principal = &copy
	}
	return connect.NewResponse(&managev1.GetMediaDeliveryResponse{Delivery: &commonv1.MediaDelivery{
		FileId: request.Msg.GetFileId(), Extension: "bin", MimeType: "application/octet-stream", FileSize: 1,
	}}), nil
}

func (*compositionFileRuntime) GetFileDownloadPolicy(
	context.Context,
	*connect.Request[managev1.GetFileDownloadPolicyRequest],
) (*connect.Response[managev1.GetFileDownloadPolicyResponse], error) {
	return connect.NewResponse(&managev1.GetFileDownloadPolicyResponse{}), nil
}

func (*compositionFileRuntime) UpdateFileDownloadPolicy(
	context.Context,
	*connect.Request[managev1.UpdateFileDownloadPolicyRequest],
) (*connect.Response[managev1.UpdateFileDownloadPolicyResponse], error) {
	return connect.NewResponse(&managev1.UpdateFileDownloadPolicyResponse{}), nil
}

func (*compositionFileRuntime) ListFileUsages(
	context.Context,
	*connect.Request[managev1.ListFileUsagesRequest],
) (*connect.Response[managev1.ListFileUsagesResponse], error) {
	return connect.NewResponse(&managev1.ListFileUsagesResponse{}), nil
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
		menu:          registration(aidocument.DomainMenu),
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
