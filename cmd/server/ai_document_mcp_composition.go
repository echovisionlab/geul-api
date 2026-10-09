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

const mcpServerImplementationVersion = "17"

const mcpServerInstructions = "Use document_list with p=post, p=work, p=page, p=program_event, p=release, or p=artist to find UUIDs. Pass d unchanged; never substitute slugs or URLs. " +
	"Use policy_list/get for policy UUIDs; read/edit with p=terms or p=privacy. policy_create creates an empty draft without email; edits send no email. policy_schedule and policy_activate_now start notice email delivery. policy_delete allows every lifecycle and retains delivery history. " +
	"Use menu_list for Menu UUIDs/locales and menu_locations_get for placement. Pass d with p=menu to document tools using current revisions and stable handles. " +
	"Read settings before focused management tools. Post/Page/Release unpublish returns to draft; administrator-managed archived Posts can also return to draft. Program Events archive. Track publication follows Release. Map Places/Themes have no publication state. " +
	"Read release_relations_get or track_list before relation edits; preserve exact observed snapshots and supported order_intent. Resolve references through reference_search, label_list, event type/series lists, music genre/style/format lists, form_list, post_series_list, and map_theme_list/get. Theme snapshot updates require its read revision. " +
	"Use program_event_media_list before media edits. Adding the same event/role/file upserts alt/caption; omitted values clear them. Use member_admin_list/get for audited administrator information and member_tag_list for tag names. " +
	"Use document_catalog for typed fields, block kinds, relations and File ownership. document_metadata_update edits titles, summaries, Post categories/tags and Page layout. Post settings/layout require configuration_revision. " +
	"Use work_settings_get before updating Work metadata or clients; pass read values as observed_metadata or observed_client_ids. Use focused featured-image, participant, credit, version, slug, and File tools. " +
	"Use file_upload for ChatGPT attachments, including independent File library uploads without a document. Retain correlation_id on retry; only the returned DSUB File UUID can be attached. file_list browses folders. Read deletion impacts before file_delete; accepted IDs mean deletion scheduled, and referenced Files are rejected. " +
	"Use document_file_add/replace/remove for document images and attachments; native File Blocks render image MIME. Use document_file_caption_update for localized captions. Removing a Block preserves File bytes. Page placements need a rich-text parent. " +
	"Use document_file_download_policy_get before updates; copy audience and segment IDs into observed_policy.audience and observed_policy.audience_segment_ids. expected_file_id guards the exact attachment. " +
	"For Track audio use file_transfer k=track_audio with track_id and current audio_original_file_id as expected_current_file_id; omit only if absent. Preserve returned handles and browser media bundles. Remote imports retain the same correlation_id on retries. " +
	"Use focused paragraph/block tools for ordinary text. Use document_apply for advanced typed batches. Use document_validate only for explicit dry runs. " +
	"A sync_required result with isError=false and applied=false means no change was applied. Follow document_read recovery. For read_continuation_invalid, discard previous pages and restart without a cursor. " +
	"For document_revision_changed or target_revision_changed, reread targets and compare the previous read, latest values, and intended edit; preserve current values and adjust the intention using fresh revisions. Never just replace an expected revision and resend stale operations. affected_handles are pending targets, not proof that concurrent edits are disjoint. " +
	"The server cannot semantically merge without a stored base. Limit automatic reread/reassess retries to 3 cycles per task edit; report continued changes as pending. Ask only about incompatible intentions or ambiguous deleted targets, not routine version changes."

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

type contentMCPApplications struct {
	terms      mcpadapter.TermsPolicyManagementApplication
	privacy    mcpadapter.PrivacyPolicyManagementApplication
	categories mcpadapter.CategoryReferenceDiscovery
	tags       mcpadapter.TagReferenceDiscovery
	clients    mcpadapter.ClientReferenceDiscovery
	mapPlaces  interface {
		mcpadapter.MapPlaceReferenceDiscovery
		mcpadapter.MapPlaceManagementApplication
	}
	members interface {
		mcpadapter.MemberReferenceDiscovery
		mcpadapter.MemberAdminReader
		mcpadapter.MemberTagReader
	}
	artists      mcpadapter.ArtistReferenceDiscovery
	files        mcpadapter.FileReferenceDiscovery
	eventTypes   mcpadapter.ProgramEventTypeReferenceDiscovery
	eventSeries  mcpadapter.ProgramEventSeriesReferenceDiscovery
	labels       mcpadapter.LabelReferenceDiscovery
	genres       mcpadapter.GenreReferenceDiscovery
	styles       mcpadapter.StyleReferenceDiscovery
	formats      mcpadapter.FormatReferenceDiscovery
	forms        mcpadapter.FormReferenceDiscovery
	postSeries   mcpadapter.PostSeriesReferenceDiscovery
	tracks       mcpadapter.TrackManagementApplication
	mapThemes    mcpadapter.MapThemeManagement
	menus        mcpadapter.MenuDiscovery
	menuSettings mcpadapter.MenuLocationReader
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
	programEvents interface {
		mcpadapter.ProgramEventDocumentDiscovery
		mcpadapter.ProgramEventManagementApplication
		mcpadapter.ProgramEventMediaApplication
	},
	releases interface {
		mcpadapter.ReleaseDocumentDiscovery
		mcpadapter.ReleaseManagementApplication
		mcpadapter.ReleaseRelationApplication
	},
	artists mcpadapter.ArtistDocumentDiscovery,
	references contentMCPApplications,
	translation managev1connect.TranslationServiceHandler,
	files interface {
		filemediaadapter.MCPFileRuntime
		mcpadapter.FileBlockManagement
		mcpadapter.FileManager
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
	catalogTools, err := mcpadapter.NewDocumentCatalogTools(application)
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize MCP document catalog tools: %w", err)
	}
	discoveryTools, err := mcpadapter.NewDocumentDiscoveryTools(posts, works, pages, programEvents, releases, artists)
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize MCP document discovery tools: %w", err)
	}
	menuTools, err := mcpadapter.NewMenuDiscoveryTools(references.menus, references.menuSettings)
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize MCP menu discovery tools: %w", err)
	}
	managementTools, err := mcpadapter.NewContentManagementTools(posts, works, pages)
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize MCP content management tools: %w", err)
	}
	policyTools, err := mcpadapter.NewLegalPolicyTools(references.terms, references.privacy)
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize MCP legal policy tools: %w", err)
	}
	eventManagementTools, err := mcpadapter.NewProgramEventManagementTools(programEvents)
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize MCP Program Event management tools: %w", err)
	}
	releaseManagementTools, err := mcpadapter.NewReleaseManagementTools(releases)
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize MCP Release management tools: %w", err)
	}
	releaseRelationTools, err := mcpadapter.NewReleaseRelationTools(releases)
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize MCP Release relations tools: %w", err)
	}
	trackManagementTools, err := mcpadapter.NewTrackManagementTools(references.tracks)
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize MCP Track management tools: %w", err)
	}
	mapPlaceManagementTools, err := mcpadapter.NewMapPlaceManagementTools(references.mapPlaces)
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize MCP Map Place management tools: %w", err)
	}
	mapThemeManagementTools, err := mcpadapter.NewMapThemeManagementTools(references.mapThemes)
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize MCP Map Theme management tools: %w", err)
	}
	musicReferenceTools, err := mcpadapter.NewMusicReferenceTools(references.genres, references.styles, references.formats)
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize MCP music reference tools: %w", err)
	}
	pageReferenceTools, err := mcpadapter.NewPageReferenceTools(references.forms, references.postSeries)
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize MCP Page reference tools: %w", err)
	}
	eventMediaTools, err := mcpadapter.NewProgramEventMediaTools(programEvents)
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize MCP Program Event media tools: %w", err)
	}
	memberTagTools, err := mcpadapter.NewMemberTagTools(references.members)
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize MCP Member tag tools: %w", err)
	}
	memberAdminTools, err := mcpadapter.NewMemberAdminTools(references.members)
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize MCP Member administrator tools: %w", err)
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
	eventReferenceTools, err := mcpadapter.NewProgramEventReferenceTools(references.eventTypes, references.eventSeries, references.labels)
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize MCP Program Event reference tools: %w", err)
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
	fileManagerTools, err := mcpadapter.NewFileManagerTools(files)
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize MCP File Manager tools: %w", err)
	}
	toolSet, err := mcpadapter.NewToolSet(
		discoveryTools, menuTools, referenceTools, eventReferenceTools, musicReferenceTools, pageReferenceTools,
		managementTools, policyTools, eventManagementTools, eventMediaTools, releaseManagementTools, releaseRelationTools,
		trackManagementTools, mapPlaceManagementTools, mapThemeManagementTools, memberAdminTools, memberTagTools,
		relatedTools, documentTools, catalogTools, translationTools, fileTools, fileBlockTools, fileManagerTools,
	)
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
