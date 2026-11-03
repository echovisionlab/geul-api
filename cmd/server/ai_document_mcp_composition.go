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
)

const mcpServerImplementationVersion = "19"

const mcpServerInstructions = "Find UUIDs with document_list (p=post, p=work, p=page, p=program_event, p=release or p=artist), policy_list (p=terms or p=privacy) " +
	"and menu_list (p=menu; menu_locations_get for placement); pass d unchanged, never a slug or URL. " +
	"Edit a document in two steps. 1) Locate with document_read m=outline: q=<text> lists only blocks containing it; " +
	"without q every block comes with a text preview. 2) Change a word or sentence with document_text_replace, which keeps formatting. " +
	"For other edits read only the target handles with m=blocks, then use focused tools for plain paragraphs, headings and tables, or document_apply. " +
	"document_open is optional. Pass dr (and tr for a translation) " +
	"from your latest read or the previous edit's result; consecutive edits need no reread. " +
	"Plain-text paragraph/heading tools refuse a block with marks, links, colors or breaks: " +
	"use document_text_replace or document_apply to keep them. " +
	"Editing the source locale changes every translation's tr, so reread a translation before editing it afterwards. " +
	"A sync_required result with isError=false and applied=false means no change was applied: " +
	"run the returned read recipe, compare the latest values with the intended edit, keep concurrent values, and retry with the new revisions. " +
	"Never just replace an expected revision and resend stale operations; " +
	"affected_handles are pending targets, not proof that concurrent edits are disjoint. " +
	"Stop after 3 cycles per task edit and report it as pending; " +
	"ask only about incompatible intentions or deleted targets, not routine version changes. " +
	"For read_continuation_invalid, discard previous pages and restart without a cursor. " +
	"Use document_validate only for requested dry runs and document_catalog for field shapes, block kinds, relations and File ownership. " +
	"Email Layout wrapper markup is edited with email_layout_source_get/replace; " +
	"its text with document tools (p=email_layout). policy_create creates an empty draft without email; " +
	"edits send no email. policy_schedule and policy_activate_now start notice email delivery. " +
	"policy_delete allows every lifecycle and retains delivery history. " +
	"Read settings before focused management tools: post_settings_update needs configuration_revision. " +
	"Use work_settings_get before updating Work metadata or clients; " +
	"pass read values as observed_metadata or observed_client_ids. Theme snapshot updates need their read revision. " +
	"Post/Page/Release unpublish returns to draft; archived Posts can return to draft; Program Events archive; " +
	"Track publication follows Release; Map Places/Themes have no publication state. " +
	"Read release_relations_get or track_list before relation edits and keep observed snapshots and order_intent. " +
	"Resolve references with reference_search and the label, event type/series, genre/style/format, form, post_series, map_theme and member_tag lists. " +
	"Use member_admin_list/get for audited member data. " +
	"Read site_settings_get/mail_adapter_list before site_settings_update/mail_adapter_sender_update; " +
	"sender changes keep credentials and may send verification email. File work: " +
	"file_upload for chat attachments, keeping correlation_id on retries; " +
	"only the returned File UUID can be attached. file_list browses folders. " +
	"Read deletion impacts before file_delete; referenced Files are rejected. " +
	"expected_file_id guards the exact attachment. Preserve returned handles and browser media bundles. " +
	"document_file_add/replace/remove handle document images and attachments, document_file_caption_update localized captions; " +
	"removing a Block keeps File bytes; Page placements need a rich-text parent. " +
	"Read document_file_download_policy_get before updating and copy its IDs into observed_policy.audience and observed_policy.audience_segment_ids. " +
	"For Track audio use file_transfer k=track_audio with the current audio_original_file_id as expected_current_file_id. " +
	"Program Event media: read program_event_media_list first; " +
	"adding the same event/role/file upserts alt/caption and omitted values clear them."

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
	legalPolicies mcpadapter.LegalPolicyManagementApplication
	categories    mcpadapter.CategoryReferenceDiscovery
	tags          mcpadapter.TagReferenceDiscovery
	clients       mcpadapter.ClientReferenceDiscovery
	mapPlaces     interface {
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
	siteSettings mcpadapter.SiteSettingsAdminApplication
	mailAdapters mcpadapter.MailAdapterAdminApplication
	emailLayouts mcpadapter.EmailLayoutSourceApplication
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
	translation mcpadapter.TranslationApplication,
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
	policyTools, err := mcpadapter.NewLegalPolicyTools(references.legalPolicies)
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize MCP legal policy tools: %w", err)
	}
	emailLayoutSourceTools, err := mcpadapter.NewEmailLayoutSourceTools(references.emailLayouts)
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize MCP Email Layout source tools: %w", err)
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
	adminSettingsTools, err := mcpadapter.NewAdminSettingsTools(references.siteSettings, references.mailAdapters)
	if err != nil {
		return aiDocumentMCPComposition{}, fmt.Errorf("initialize MCP administrator settings tools: %w", err)
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
		managementTools, policyTools, emailLayoutSourceTools, eventManagementTools, eventMediaTools, releaseManagementTools, releaseRelationTools,
		trackManagementTools, mapPlaceManagementTools, mapThemeManagementTools, memberAdminTools, memberTagTools, adminSettingsTools,
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
