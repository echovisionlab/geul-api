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

const mcpServerImplementationVersion = "12"

const mcpServerInstructions = "Use document_list with p=post, p=work, p=page, p=program_event, p=release, or p=artist when a document UUID is unknown. " +
	"Pass the returned d unchanged to document_open and document_read; never use a slug or URL as d. " +
	"Use the focused Post, Work, and Page creation, settings, lifecycle, scheduling, and deletion tools for root management actions. " +
	"Use work_settings_get before updating Work metadata or clients, and pass the read values unchanged as observed_metadata or observed_client_ids with the intended new values. " +
	"Use the focused featured-image, Post participant, Work credit, version, and slug-check tools for related management actions. " +
	"Use reference_search to resolve Category, Tag, Client, Map Place, Member, or Artist UUIDs, and file_list to resolve existing File UUIDs. " +
	"Use document_file_add, document_file_replace, or document_file_remove to reuse existing Files as document File Blocks without uploading or deleting File bytes. Use file_usage_list to inspect every authorized use. " +
	"Use document_file_download_policy_get before document_file_download_policy_update; copy its audience and every audience_segments ID into observed_policy.audience and observed_policy.audience_segment_ids. expected_file_id is only a compare-and-set guard for the exact current File Block attachment. " +
	"Use document_metadata_update for title or summary, and for Post categories or tags, after reading exact current revisions. " +
	"For ordinary plain-text paragraph creation, update, or deletion, read the target and use the focused document_paragraph_create, document_paragraph_update, or document_block_delete tool with the exact current revisions. " +
	"Use document_validate only when the user explicitly requests a dry run. Use document_apply only for advanced typed batches that focused tools cannot represent. " +
	"A sync_required result with isError=false and applied=false is a routine refresh; no requested change was applied. " +
	"Follow its document_read recipe. For read_continuation_invalid, discard previous pages and restart without a cursor. " +
	"For document_revision_changed or target_revision_changed on a mutation or validation, reread the latest actual targets, compare the previous read, latest values, and intended edit, preserve current values, and retry the adjusted intention with revisions from the fresh read. " +
	"Never just replace an expected revision and resend stale operations. affected_handles are pending operation targets, not proof that concurrent edits are disjoint. " +
	"The server cannot perform a semantic merge without a stored base. Limit automatic reread/reassess retries to 3 cycles per task edit; if changes continue, briefly report the edit as pending. " +
	"Ask the user only for incompatible semantic intentions or an ambiguous deleted target, not routine version changes."

// aiDocumentDomainRegistrations makes the complete production DCDP catalog
// explicit at the composition root. The adapter registry independently rejects
// missing, duplicate, nil, or unsupported domains.
type aiDocumentDomainRegistrations struct {
	post          aidocumentadapter.DomainRegistration
	page          aidocumentadapter.DomainRegistration
	work          aidocumentadapter.DomainRegistration
	programEvent  aidocumentadapter.DomainRegistration
	release       aidocumentadapter.DomainRegistration
	artist        aidocumentadapter.DomainRegistration
	label         aidocumentadapter.DomainRegistration
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
		registrations.release,
		registrations.artist,
		registrations.label,
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

// aiDocumentMCPConfig names the transport and relay settings shared by the MCP
// HTTP handler and the interactive mutation relay.
type aiDocumentMCPConfig struct {
	internalServiceSecret     string
	authHeaderName            string
	internalServiceHeaderName string
	editorCollabURL           string
	editorCollabHTTPClient    connect.HTTPClient
	allowedOrigins            []string
}

type contentReferenceApplications struct {
	categories mcpadapter.CategoryReferenceDiscovery
	tags       mcpadapter.TagReferenceDiscovery
	clients    mcpadapter.ClientReferenceDiscovery
	mapPlaces  mcpadapter.MapPlaceReferenceDiscovery
	members    mcpadapter.MemberReferenceDiscovery
	artists    mcpadapter.ArtistReferenceDiscovery
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
	releases mcpadapter.ReleaseDocumentDiscovery,
	artists mcpadapter.ArtistDocumentDiscovery,
	references contentReferenceApplications,
	translation managev1connect.TranslationServiceHandler,
	files interface {
		filemediaadapter.MCPFileRuntime
		mcpadapter.FileBlockManagement
	},
	cfg aiDocumentMCPConfig,
	fallbackPublisher aidocumentadapter.InteractiveMutationSignalPublisher,
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
		cfg.editorCollabHTTPClient,
		cfg.editorCollabURL,
		cfg.internalServiceSecret,
		cfg.internalServiceHeaderName,
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
	discoveryTools, err := mcpadapter.NewDocumentDiscoveryTools(posts, works, pages, programEvents, releases, artists)
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
		references.artists,
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
		InternalServiceSecret:     cfg.internalServiceSecret,
		AuthHeaderName:            cfg.authHeaderName,
		InternalServiceHeaderName: cfg.internalServiceHeaderName,
		Registry:                  toolSet,
		Dispatcher:                toolSet,
		ServerInfo: mcpserver.Implementation{
			Name: "geul", Version: mcpServerImplementationVersion,
		},
		ServerTitleSource: serverTitleSource,
		Instructions:      mcpServerInstructions,
		AllowedOrigins:    append([]string(nil), cfg.allowedOrigins...),
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
