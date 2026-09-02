package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	aidocumentadapter "github.com/echovisionlab/geul-api/internal/adapters/aidocument"
	filemediaadapter "github.com/echovisionlab/geul-api/internal/adapters/filemedia"
	mcpadapter "github.com/echovisionlab/geul-api/internal/adapters/mcp"
	aidocument "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/auth"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1/intrav1connect"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

const mcpServerImplementationVersion = "7"

const mcpServerInstructions = "Use document_list with p=post, p=work, p=page, or p=program_event when a document UUID is unknown. " +
	"Pass the returned d unchanged to document_open and document_read; never use a slug or URL as d. " +
	"Use the focused Post, Work, and Page creation, settings, lifecycle, scheduling, and deletion tools for root management actions. " +
	"Use the focused featured-image, Post participant, Work credit, version, and slug-check tools for related management actions. " +
	"Use reference_search to resolve Category, Tag, Client, Map Place, or Member UUIDs, and file_list to resolve existing File UUIDs. " +
	"Use document_file_add, document_file_replace, or document_file_remove to reuse existing Files as document File Blocks without uploading or deleting File bytes. Use file_usage_list to inspect every authorized use. " +
	"Use document_file_download_policy_get before document_file_download_policy_update; expected_file_id is only a compare-and-set guard for the exact current File Block attachment. " +
	"Use document_metadata_update for title or summary, and for Post categories or tags, after reading exact current revisions. " +
	"For ordinary plain-text paragraph creation, update, or deletion, read the target and use the focused document_paragraph_create, document_paragraph_update, or document_block_delete tool with the exact current revisions. " +
	"Use document_validate only when the user explicitly requests a dry run. Use document_apply only for advanced typed batches that focused tools cannot represent. " +
	"On a revision conflict, read again before retrying."

// aiDocumentDomainRegistrations makes the complete production DCDP catalog
// explicit at the composition root. The adapter registry independently rejects
// missing, duplicate, nil, or unsupported domains.
type aiDocumentDomainRegistrations struct {
	post          aidocumentadapter.DomainRegistration
	page          aidocumentadapter.DomainRegistration
	work          aidocumentadapter.DomainRegistration
	programEvent  aidocumentadapter.DomainRegistration
	menu          aidocumentadapter.DomainRegistration
	emailTemplate aidocumentadapter.DomainRegistration
	emailLayout   aidocumentadapter.DomainRegistration
	campaign      aidocumentadapter.DomainRegistration
	form          aidocumentadapter.DomainRegistration
	privacy       aidocumentadapter.DomainRegistration
	terms         aidocumentadapter.DomainRegistration
	postSeries    aidocumentadapter.DomainRegistration
}

func (registrations aiDocumentDomainRegistrations) values() []aidocumentadapter.DomainRegistration {
	return []aidocumentadapter.DomainRegistration{
		registrations.post,
		registrations.page,
		registrations.work,
		registrations.programEvent,
		registrations.menu,
		registrations.emailTemplate,
		registrations.emailLayout,
		registrations.campaign,
		registrations.form,
		registrations.privacy,
		registrations.terms,
		registrations.postSeries,
	}
}

type aiDocumentMCPComposition struct {
	editorApplication *aidocumentadapter.InteractiveMutationApplication
	connectService    *aidocumentadapter.Service
	mcpHandler        http.Handler
}

type contentReferenceApplications struct {
	categories mcpadapter.CategoryReferenceDiscovery
	tags       mcpadapter.TagReferenceDiscovery
	clients    mcpadapter.ClientReferenceDiscovery
	mapPlaces  mcpadapter.MapPlaceReferenceDiscovery
	members    mcpadapter.MemberReferenceDiscovery
	files      mcpadapter.FileReferenceDiscovery
}

func newAIDocumentMCPComposition(
	registrations aiDocumentDomainRegistrations,
	posts interface {
		mcpadapter.PostDocumentDiscovery
		mcpadapter.PostManagementApplication
		mcpadapter.PostRelatedApplication
	},
	works interface {
		mcpadapter.WorkDocumentDiscovery
		mcpadapter.WorkManagementApplication
		mcpadapter.WorkRelatedApplication
	},
	pages interface {
		mcpadapter.PageDocumentDiscovery
		mcpadapter.PageManagementApplication
		mcpadapter.PageRelatedApplication
	},
	programEvents mcpadapter.ProgramEventDocumentDiscovery,
	references contentReferenceApplications,
	translation managev1connect.TranslationServiceHandler,
	files interface {
		filemediaadapter.MCPFileRuntime
		mcpadapter.FileBlockManagement
	},
	internalServiceSecret string,
	authHeaderName string,
	internalServiceHeaderName string,
	editorCollabURL string,
	editorCollabHTTPClient connect.HTTPClient,
	fallbackPublisher aidocumentadapter.InteractiveMutationSignalPublisher,
	allowedOrigins []string,
	serverTitleSource mcpserver.ServerTitleSource,
) (aiDocumentMCPComposition, error) {
	registry, err := aidocumentadapter.NewRegistry(registrations.values()...)
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize AI document domain registry: %w", err)
	}
	application, err := aidocument.NewService(registry)
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize AI document application: %w", err)
	}
	connectService, err := aidocumentadapter.NewService(application)
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize AI document Connect service: %w", err)
	}
	relay, err := newInteractiveMutationRelay(
		editorCollabHTTPClient,
		editorCollabURL,
		internalServiceSecret,
		internalServiceHeaderName,
	)
	if err != nil {
		return aiDocumentMCPComposition{}, err
	}
	mcpApplication, err := aidocumentadapter.NewInteractiveMutationApplication(
		application,
		relay,
		fallbackPublisher,
		intrav1.InteractiveAIDocumentMutationOrigin_INTERACTIVE_AI_DOCUMENT_MUTATION_ORIGIN_MCP,
	)
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize MCP AI document completion: %w", err)
	}
	editorApplication, err := aidocumentadapter.NewInteractiveMutationApplication(
		application,
		relay,
		fallbackPublisher,
		intrav1.InteractiveAIDocumentMutationOrigin_INTERACTIVE_AI_DOCUMENT_MUTATION_ORIGIN_IN_EDITOR_AI,
	)
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize editor AI document completion: %w", err)
	}
	documentTools, err := mcpadapter.NewAIDocumentTools(mcpApplication)
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize MCP AI document tools: %w", err)
	}
	discoveryTools, err := mcpadapter.NewDocumentDiscoveryTools(posts, works, pages, programEvents)
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize MCP document discovery tools: %w", err)
	}
	managementTools, err := mcpadapter.NewContentManagementTools(posts, works, pages)
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize MCP content management tools: %w", err)
	}
	relatedTools, err := mcpadapter.NewContentRelatedTools(posts, works, pages)
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize MCP related content tools: %w", err)
	}
	referenceTools, err := mcpadapter.NewReferenceDiscoveryTools(
		references.categories,
		references.tags,
		references.clients,
		references.mapPlaces,
		references.members,
		references.files,
	)
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize MCP content reference discovery tools: %w", err)
	}
	translationTools, err := mcpadapter.NewTranslationTools(translation)
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize MCP Translation tools: %w", err)
	}
	fileFacade, err := filemediaadapter.NewMCPFileFacade(files)
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize MCP File facade: %w", err)
	}
	fileTools, err := mcpadapter.NewFileTools(fileFacade)
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize MCP File tools: %w", err)
	}
	fileBlockTools, err := mcpadapter.NewFileBlockTools(mcpApplication, files)
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize MCP File Block tools: %w", err)
	}
	toolSet, err := mcpadapter.NewToolSet(discoveryTools, referenceTools, managementTools, relatedTools, documentTools, translationTools, fileTools, fileBlockTools)
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize MCP tool set: %w", err)
	}
	mcpHandler, err := mcpadapter.NewHTTPHandler(mcpadapter.HTTPConfig{
		InternalServiceSecret:     internalServiceSecret,
		AuthHeaderName:            authHeaderName,
		InternalServiceHeaderName: internalServiceHeaderName,
		Registry:                  toolSet,
		Dispatcher:                toolSet,
		ServerInfo: mcpserver.Implementation{
			Name: "geul", Version: mcpServerImplementationVersion,
		},
		ServerTitleSource: serverTitleSource,
		Instructions:      mcpServerInstructions,
		AllowedOrigins:    append([]string(nil), allowedOrigins...),
	})
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize MCP HTTP handler: %w", err)
	}
	return aiDocumentMCPComposition{
		editorApplication: editorApplication, connectService: connectService, mcpHandler: mcpHandler,
	}, nil
}

func newInteractiveMutationRelay(
	httpClient connect.HTTPClient,
	editorCollabURL string,
	internalServiceSecret string,
	internalServiceHeaderName string,
) (*aidocumentadapter.InteractiveMutationRelay, error) {
	if httpClient == nil {
		return nil, fmt.Errorf("editor Collab HTTP client is required")
	}
	if editorCollabURL == "" || editorCollabURL != strings.TrimSpace(editorCollabURL) {
		return nil, fmt.Errorf("editor Collab URL must be a canonical absolute origin")
	}
	if internalServiceSecret == "" || internalServiceSecret != strings.TrimSpace(internalServiceSecret) {
		return nil, fmt.Errorf("internal service secret is required")
	}
	if _, err := auth.NormalizeHeaderName(internalServiceHeaderName); err != nil {
		return nil, fmt.Errorf("internal service header name is invalid: %w", err)
	}
	internalTrust := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, request connect.AnyRequest) (connect.AnyResponse, error) {
			request.Header().Set(internalServiceHeaderName, internalServiceSecret)
			return next(ctx, request)
		}
	})
	client := intrav1connect.NewInternalCollaborationRelayServiceClient(
		httpClient,
		editorCollabURL,
		connect.WithInterceptors(internalTrust),
	)
	relay, err := aidocumentadapter.NewInteractiveMutationRelay(client)
	if err != nil {
		return nil, fmt.Errorf("initialize interactive AI document relay: %w", err)
	}
	return relay, nil
}
