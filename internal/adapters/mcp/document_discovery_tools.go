package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/artist"
	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/label"
	"github.com/echovisionlab/geul-api/internal/legal"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"github.com/echovisionlab/geul-api/internal/menu"
	pagedomain "github.com/echovisionlab/geul-api/internal/page"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/programevent"
	releasedomain "github.com/echovisionlab/geul-api/internal/release"
	"github.com/echovisionlab/geul-api/internal/series"
	workdomain "github.com/echovisionlab/geul-api/internal/work"
)

const ToolDocumentList = "document_list"

const (
	documentListDefaultLimit = 20
	documentListMaxLimit     = 100
	documentListMaxOffset    = 1<<31 - 1
)

var documentDiscoveryTools = toolRegistry[DocumentDiscoveryTools]{
	defineTool(mcpserver.Tool{
		Name:  ToolDocumentList,
		Title: "List accessible documents",
		Description: "List documents of one type through the owning service's authorized management list. " +
			"query matches source titles, or Artist source names and real names; Menus and legal policies have no such search, so for them it filters the returned page. Use this when the document UUID is unknown. " +
			"Pass the returned document_id unchanged, with source_locale as locale, to document_read; a slug or URL is not a document ID.",
		InputSchema:     json.RawMessage(documentListInputJSONSchema),
		OutputSchema:    json.RawMessage(documentListOutputJSONSchema),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(true, false, false),
		Meta:            oauthSecurityMeta(),
	}, (*DocumentDiscoveryTools).list),
}

// PostDocumentDiscovery is the exact Post-owned read capability consumed by
// the MCP discovery adapter. Authorization remains in the Post service.
type PostDocumentDiscovery interface {
	ListAIDocuments(context.Context, postdomain.AIDocumentListInput) (postdomain.AIDocumentListResult, error)
}

type WorkDocumentDiscovery interface {
	ListWorksAdmin(context.Context, workdomain.ListWorksAdminQuery) (workdomain.AdminWorkPage, error)
}

type PageDocumentDiscovery interface {
	ListPagesAdmin(context.Context, pagedomain.ListPagesAdminQuery) (pagedomain.PageList, error)
}

type ProgramEventDocumentDiscovery interface {
	ListProgramEventsAdmin(context.Context, programevent.ListProgramEventsQuery) (programevent.EventList, error)
}

type ReleaseDocumentDiscovery interface {
	ListReleasesAdmin(context.Context, releasedomain.ListReleasesQuery) (releasedomain.ReleasePage, error)
}

type ArtistDocumentDiscovery interface {
	ListArtistsAdmin(context.Context, artist.ListArtistsQuery) (artist.AdminArtistPage, error)
}

// DocumentDiscoveryDeps holds each document type's authorized management list.
type DocumentDiscoveryDeps struct {
	Posts          PostDocumentDiscovery
	Works          WorkDocumentDiscovery
	Pages          PageDocumentDiscovery
	ProgramEvents  ProgramEventDocumentDiscovery
	Releases       ReleaseDocumentDiscovery
	Artists        ArtistDocumentDiscovery
	Labels         LabelDocumentDiscovery
	Menus          MenuDocumentDiscovery
	Forms          FormDocumentDiscovery
	PostSeries     PostSeriesDocumentDiscovery
	Policies       PolicyDocumentDiscovery
	Campaigns      CampaignDocumentDiscovery
	EmailTemplates EmailTemplateDocumentDiscovery
	EmailLayouts   EmailLayoutDocumentDiscovery
}

// DocumentDiscoveryTools owns only document selection metadata. DCDP reads
// and mutations remain in AIDocumentTools.
type DocumentDiscoveryTools struct {
	lists DocumentDiscoveryDeps
}

type LabelDocumentDiscovery interface {
	ListLabelsAdmin(context.Context, label.ListLabelsQuery) (label.AdminLabelPage, error)
}

type FormDocumentDiscovery interface {
	ListFormsAdmin(context.Context, form.ListFormsAdminQuery) (form.FormList, error)
}

type PostSeriesDocumentDiscovery interface {
	ListSeriesAdmin(context.Context, series.ListSeriesAdminQuery) (series.SeriesPage, error)
}

type MenuDocumentDiscovery interface {
	ListMenus(context.Context, menu.ListMenusQuery) (menu.MenuPage, error)
}

// PolicyDocumentDiscovery lists the versions of one legal policy.
type PolicyDocumentDiscovery interface {
	ListPolicyVersions(context.Context, legal.PolicyKind, legal.ListPolicyVersionsQuery) (legal.PolicyPage, error)
}

type CampaignDocumentDiscovery interface {
	ListCampaigns(context.Context, campaign.ListCampaignsQuery) (campaign.CampaignPage, error)
}

type EmailTemplateDocumentDiscovery interface {
	ListEmailTemplates(context.Context, emailauthoring.ListEmailTemplatesQuery) (emailauthoring.EmailTemplatePage, error)
}

type EmailLayoutDocumentDiscovery interface {
	ListEmailLayouts(context.Context, emailauthoring.ListEmailLayoutsQuery) (emailauthoring.EmailLayoutPage, error)
}

func NewDocumentDiscoveryTools(lists DocumentDiscoveryDeps) (*DocumentDiscoveryTools, error) {
	for _, list := range []struct {
		name  string
		value any
	}{
		{"Post", lists.Posts}, {"Work", lists.Works}, {"Page", lists.Pages}, {"Program Event", lists.ProgramEvents},
		{"Release", lists.Releases}, {"Artist", lists.Artists}, {"Label", lists.Labels}, {"Menu", lists.Menus},
		{"Form", lists.Forms}, {"Post Series", lists.PostSeries}, {"Policy", lists.Policies}, {"Campaign", lists.Campaigns},
		{"Email Template", lists.EmailTemplates}, {"Email Layout", lists.EmailLayouts},
	} {
		if interfaceValueIsNil(list.value) {
			return nil, fmt.Errorf("MCP %s document discovery is required", list.name)
		}
	}
	return &DocumentDiscoveryTools{lists: lists}, nil
}

func (*DocumentDiscoveryTools) ToolNames() []string { return documentDiscoveryTools.names() }

func (*DocumentDiscoveryTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return documentDiscoveryTools.list(), nil
}

func (tools *DocumentDiscoveryTools) CallTool(
	ctx context.Context,
	_ mcpserver.Principal,
	name string,
	arguments mcpserver.ToolArguments,
) (mcpserver.ToolResult, error) {
	return documentDiscoveryTools.call(tools, ctx, name, arguments)
}

type documentListArguments struct {
	DocumentType core.Domain `json:"document_type"`
	Query        string      `json:"query,omitempty"`
	Status       string      `json:"status,omitempty"`
	Limit        int         `json:"limit,omitempty"`
	Offset       int         `json:"offset,omitempty"`
}

func (tools *DocumentDiscoveryTools) list(ctx context.Context, _ string, call toolCall[documentListArguments]) (mcpserver.ToolResult, error) {
	if err := rejectNullArguments(call.Arguments, "query", "status", "limit", "offset"); err != nil {
		return executionError(err)
	}
	var input documentListArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if !slices.Contains(listedDocumentTypes, input.DocumentType) {
		return executionError(unknownDocumentTypeError())
	}
	_, limitSupplied := call.Arguments["limit"]
	if input.Limit < 0 || input.Limit > documentListMaxLimit || (limitSupplied && input.Limit == 0) {
		return executionError(fmt.Errorf("limit must be between 1 and %d, or omitted for the default", documentListMaxLimit))
	}
	if input.Status != "" && !slices.Contains(statusFilteredDocumentTypes, input.DocumentType) {
		return executionError(fmt.Errorf("a %s list has no status filter; it works for %s", input.DocumentType, strings.Join(documentTypeNames(statusFilteredDocumentTypes), ", ")))
	}
	if input.Offset < 0 || input.Offset > documentListMaxOffset {
		return executionError(fmt.Errorf("offset must be between 0 and %d", documentListMaxOffset))
	}
	listed, err := tools.listDocuments(ctx, documentListRequest{
		documentType: input.DocumentType, query: input.Query, status: input.Status, limit: input.Limit, offset: input.Offset,
	})
	if err != nil {
		return expectedToolError(err)
	}
	output := struct {
		Documents  []listedDocument `json:"documents"`
		Total      int64            `json:"total"`
		NextOffset *int             `json:"next_offset"`
	}{Documents: listed.documents, Total: listed.total}
	if next := input.Offset + listed.limit; int64(next) < listed.total {
		output.NextOffset = &next
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return mcpserver.ToolResult{}, fmt.Errorf("encode MCP document list: %w", err)
	}
	return structuredResult(encoded, false)
}

var (
	_ mcpserver.ToolRegistry   = (*DocumentDiscoveryTools)(nil)
	_ mcpserver.ToolDispatcher = (*DocumentDiscoveryTools)(nil)
	_ ToolProvider             = (*DocumentDiscoveryTools)(nil)
)
