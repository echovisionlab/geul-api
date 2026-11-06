package mcp

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/artist"
	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/documentsearch"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/label"
	"github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/menu"
	pagedomain "github.com/echovisionlab/geul-api/internal/page"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/programevent"
	releasedomain "github.com/echovisionlab/geul-api/internal/release"
	"github.com/echovisionlab/geul-api/internal/series"
	workdomain "github.com/echovisionlab/geul-api/internal/work"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// listedDocumentTypes is every type document_list and document_search reach, in
// the order a search of them all reads: the site's content first, then what the
// site reuses, then mail and legal text.
var listedDocumentTypes = []core.Domain{
	core.DomainPage, core.DomainPost, core.DomainWork, core.DomainProgramEvent, core.DomainRelease,
	core.DomainArtist, core.DomainLabel, core.DomainPostSeries, core.DomainMenu, core.DomainForm,
	core.DomainEmailTemplate, core.DomainEmailLayout, core.DomainCampaign, core.DomainTerms, core.DomainPrivacy,
}

func unknownDocumentTypeError() error {
	return fmt.Errorf("document_type must be one of %s", strings.Join(documentTypeNames(listedDocumentTypes), ", "))
}

// documentTypeNames lists types for a message or a query.
func documentTypeNames(documentTypes []core.Domain) []string {
	names := make([]string, len(documentTypes))
	for index, documentType := range documentTypes {
		names[index] = string(documentType)
	}
	return names
}

// statusFilteredDocumentTypes are the document types whose lists filter by status.
var statusFilteredDocumentTypes = []core.Domain{core.DomainLabel, core.DomainForm, core.DomainPostSeries, core.DomainTerms, core.DomainPrivacy}

// listedDocument is one document in a list. The optional fields are those its
// type's list carries.
type listedDocument struct {
	DocumentType          core.Domain `json:"document_type"`
	DocumentID            string      `json:"document_id"`
	Title                 string      `json:"title"`
	Slug                  *string     `json:"slug,omitempty"`
	SourceLocale          string      `json:"source_locale"`
	Status                string      `json:"status,omitempty"`
	UpdatedAt             string      `json:"updated_at,omitempty"`
	ConfigurationRevision string      `json:"configuration_revision,omitempty"`
	*listedPolicyVersion
}

// listedPolicyVersion is what a list says about a terms or privacy version.
type listedPolicyVersion struct {
	Version          int32      `json:"version"`
	DocumentRevision string     `json:"document_revision"`
	EffectiveFrom    *time.Time `json:"effective_from"`
	EffectiveUntil   *time.Time `json:"effective_until"`
}

func (document listedDocument) withUpdatedAt(at time.Time) listedDocument {
	document.UpdatedAt = at.UTC().Format(time.RFC3339)
	return document
}

// newListedDocument normalizes what the lists disagree on: an empty slug is none,
// and a status reads as its lowercase name without the generated prefix.
func newListedDocument(documentType core.Domain, id, title string, slug *string, sourceLocale, status string) listedDocument {
	if slug != nil && *slug == "" {
		slug = nil
	}
	return listedDocument{
		DocumentType: documentType, DocumentID: id, Title: title, Slug: slug,
		SourceLocale: sourceLocale, Status: strings.ToLower(trimGeneratedStatusPrefix(status)),
	}
}

// trimGeneratedStatusPrefix turns POST_STATUS_DRAFT into DRAFT, and leaves a status
// that is already a plain name.
func trimGeneratedStatusPrefix(status string) string {
	if _, name, found := strings.Cut(status, "_STATUS_"); found {
		return name
	}
	return status
}

// documentListRequest is one page of one document type's list.
type documentListRequest struct {
	documentType core.Domain
	query        string
	status       string
	limit        int
	offset       int
}

func (request documentListRequest) limit32() int32  { return int32(request.limit) }
func (request documentListRequest) offset32() int32 { return int32(request.offset) }

// generatedStatusPrefixes are the stored status names of the lists that filter by
// a generated status; a caller names the state as a listed document reads.
var generatedStatusPrefixes = map[core.Domain]string{
	core.DomainLabel: "LABEL_STATUS_", core.DomainForm: "FORM_STATUS_", core.DomainPostSeries: "SERIES_STATUS_",
}

// searchAndStatusFilters adds the status filter of a list that has one.
func (request documentListRequest) searchAndStatusFilters() []*commonv1.FilterSpec {
	filters := discoverySearchFilters(request.query)
	if request.status == "" {
		return filters
	}
	return append(filters, &commonv1.FilterSpec{
		Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ,
		Value: generatedStatusPrefixes[request.documentType] + strings.ToUpper(request.status),
	})
}

func (request documentListRequest) page(documents []listedDocument, total int64) documentPage {
	return documentPage{documents: documents, total: total, limit: request.limit}
}

// documentPage is one page of a list. limit is the page size the owner actually
// used, which places the next page.
type documentPage struct {
	documents []listedDocument
	total     int64
	limit     int
}

// ListSearchableDocuments is listDocuments as a document search reads it.
func (tools *DocumentDiscoveryTools) ListSearchableDocuments(ctx context.Context, documentType core.Domain, limit, offset int) (documentsearch.DocumentPage, error) {
	listed, err := tools.listDocuments(ctx, documentListRequest{documentType: documentType, limit: limit, offset: offset})
	if err != nil {
		return documentsearch.DocumentPage{}, err
	}
	documents := make([]documentsearch.Document, len(listed.documents))
	for index, document := range listed.documents {
		documents[index] = documentsearch.Document{
			DocumentType: document.DocumentType, DocumentID: core.DocumentReference(document.DocumentID),
			SourceLocale: core.Locale(document.SourceLocale), Title: document.Title, Slug: document.Slug,
		}
	}
	return documentsearch.DocumentPage{Documents: documents, Total: listed.total}, nil
}

func recentlyUpdatedFirst() []*commonv1.SortSpec {
	return []*commonv1.SortSpec{{Field: "updated_at", Order: commonv1.SortOrder_SORT_ORDER_DESC}}
}

// listDocuments lists through the owning service, which decides what the caller
// may see.
func (tools *DocumentDiscoveryTools) listDocuments(ctx context.Context, request documentListRequest) (documentPage, error) {
	if request.limit == 0 {
		request.limit = documentListDefaultLimit
	}
	switch request.documentType {
	case core.DomainPost:
		return tools.listPosts(ctx, request)
	case core.DomainWork:
		return tools.listWorks(ctx, request)
	case core.DomainPage:
		return tools.listPages(ctx, request)
	case core.DomainProgramEvent:
		return tools.listProgramEvents(ctx, request)
	case core.DomainRelease:
		return tools.listReleases(ctx, request)
	case core.DomainArtist:
		return tools.listArtists(ctx, request)
	case core.DomainLabel:
		return tools.listLabels(ctx, request)
	case core.DomainMenu:
		return tools.listMenus(ctx, request)
	case core.DomainForm:
		return tools.listForms(ctx, request)
	case core.DomainPostSeries:
		return tools.listPostSeries(ctx, request)
	case core.DomainTerms, core.DomainPrivacy:
		return tools.listPolicyVersions(ctx, request)
	case core.DomainCampaign:
		return tools.listCampaigns(ctx, request)
	case core.DomainEmailTemplate:
		return tools.listEmailTemplates(ctx, request)
	case core.DomainEmailLayout:
		return tools.listEmailLayouts(ctx, request)
	}
	return documentPage{}, unknownDocumentTypeError()
}

func (tools *DocumentDiscoveryTools) listPosts(ctx context.Context, request documentListRequest) (documentPage, error) {
	listed, err := tools.lists.Posts.ListAIDocuments(ctx, postdomain.AIDocumentListInput{Query: request.query, Limit: request.limit, Offset: request.offset})
	if err != nil {
		return documentPage{}, err
	}
	documents := make([]listedDocument, len(listed.Items))
	for index, item := range listed.Items {
		document := newListedDocument(core.DomainPost, item.ID, item.Title, item.Slug, item.SourceLocale, item.Status).withUpdatedAt(item.UpdatedAt)
		document.ConfigurationRevision = item.ConfigurationRevision
		documents[index] = document
	}
	return documentPage{documents: documents, total: listed.Total, limit: listed.Limit}, nil
}

func (tools *DocumentDiscoveryTools) listWorks(ctx context.Context, request documentListRequest) (documentPage, error) {
	listed, err := tools.lists.Works.ListWorksAdmin(ctx, workdomain.ListWorksAdminQuery{
		Page:    workdomain.Page{Limit: request.limit32(), Offset: request.offset32()},
		Filters: discoverySearchFilters(request.query),
		Sorts:   recentlyUpdatedFirst(),
	})
	if err != nil {
		return documentPage{}, err
	}
	documents := make([]listedDocument, 0, len(listed.Works))
	for _, item := range listed.Works {
		work := item.View
		documents = append(documents, newListedDocument(core.DomainWork, work.ID, work.Title, work.Slug, work.SourceLocale, workdomain.WorkStatusName(work.Status)).withUpdatedAt(work.UpdatedAt))
	}
	return documentPage{documents: documents, total: int64(listed.Pagination.Total), limit: int(listed.Pagination.Limit)}, nil
}

func (tools *DocumentDiscoveryTools) listPages(ctx context.Context, request documentListRequest) (documentPage, error) {
	list, err := tools.lists.Pages.ListPagesAdmin(ctx, pagedomain.ListPagesAdminQuery{
		Page:    pagedomain.Paging{Limit: request.limit32(), Offset: request.offset32()},
		Filters: discoverySearchFilters(request.query),
		Sorts:   recentlyUpdatedFirst(),
	})
	listed, err := renderPage(list, err, pagedomain.PageListMessage)
	if err != nil {
		return documentPage{}, err
	}
	documents := make([]listedDocument, 0, len(listed.Pages))
	for _, page := range listed.Pages {
		if page == nil || page.UpdatedAt == nil {
			continue
		}
		documents = append(documents, newListedDocument(core.DomainPage, page.Id, page.Title, page.Slug, page.SourceLocale, page.Status.String()).withUpdatedAt(page.UpdatedAt.AsTime()))
	}
	return documentPage{documents: documents, total: int64(listed.Pagination.GetTotal()), limit: int(listed.Pagination.GetLimit())}, nil
}

func (tools *DocumentDiscoveryTools) listProgramEvents(ctx context.Context, request documentListRequest) (documentPage, error) {
	listed, err := tools.lists.ProgramEvents.ListProgramEventsAdmin(ctx, programevent.ListProgramEventsQuery{
		Limit: request.limit32(), Offset: request.offset32(), Filters: discoverySearchFilters(request.query), Sorts: recentlyUpdatedFirst(),
	})
	if err != nil {
		return documentPage{}, err
	}
	documents := make([]listedDocument, 0, len(listed.Events))
	for _, summary := range listed.Events {
		event := programevent.EventSummaryMessage(summary)
		documents = append(documents, newListedDocument(core.DomainProgramEvent, event.Id, event.Title, event.Slug, event.SourceLocale, event.Status.String()).withUpdatedAt(event.UpdatedAt.AsTime()))
	}
	pagination := programevent.PaginationMessage(listed.Pagination)
	return documentPage{documents: documents, total: int64(pagination.GetTotal()), limit: int(pagination.GetLimit())}, nil
}

func (tools *DocumentDiscoveryTools) listReleases(ctx context.Context, request documentListRequest) (documentPage, error) {
	listed, err := tools.lists.Releases.ListReleasesAdmin(ctx, releasedomain.ListReleasesQuery{
		Limit: request.limit32(), Offset: request.offset32(), Filters: discoverySearchFilters(request.query), Sorts: recentlyUpdatedFirst(),
	})
	if err != nil {
		return documentPage{}, err
	}
	documents := make([]listedDocument, 0, len(listed.Releases))
	for _, item := range releasedomain.ReleasesWithStatsMessage(listed.Releases) {
		if item == nil || item.Release == nil || item.Release.UpdatedAt == nil {
			continue
		}
		release := item.Release
		documents = append(documents, newListedDocument(core.DomainRelease, release.Id, release.Title, release.Slug, release.SourceLocale, release.Status).withUpdatedAt(release.UpdatedAt.AsTime()))
	}
	return documentPage{documents: documents, total: listed.Total, limit: int(listed.Limit)}, nil
}

func (tools *DocumentDiscoveryTools) listArtists(ctx context.Context, request documentListRequest) (documentPage, error) {
	listed, err := tools.lists.Artists.ListArtistsAdmin(ctx, artist.ListArtistsQuery{
		Limit: request.limit32(), Offset: request.offset32(), Filters: discoverySearchFilters(request.query), Sorts: recentlyUpdatedFirst(),
	})
	if err != nil {
		return documentPage{}, err
	}
	documents := make([]listedDocument, 0, len(listed.Artists))
	for _, item := range listed.Artists {
		listedArtist := item.Artist.Artist
		if listedArtist.UpdatedAt == nil {
			continue
		}
		documents = append(documents, newListedDocument(core.DomainArtist, listedArtist.ID, item.Artist.Name, listedArtist.Slug, listedArtist.SourceLocale, string(listedArtist.Status)).withUpdatedAt(*listedArtist.UpdatedAt))
	}
	return documentPage{documents: documents, total: int64(listed.Pagination.Total), limit: int(listed.Pagination.Limit)}, nil
}

func (tools *DocumentDiscoveryTools) listLabels(ctx context.Context, request documentListRequest) (documentPage, error) {
	listed, err := tools.lists.Labels.ListLabelsAdmin(ctx, label.ListLabelsQuery{Limit: request.limit32(), Offset: request.offset32(), Filters: request.searchAndStatusFilters()})
	if err != nil {
		return documentPage{}, err
	}
	documents := make([]listedDocument, 0, len(listed.Labels))
	for _, item := range listed.Labels {
		listedLabel := item.Label.Label
		documents = append(documents, newListedDocument(core.DomainLabel, listedLabel.ID, item.Label.Name, listedLabel.Slug, listedLabel.SourceLocale, string(listedLabel.Status)))
	}
	return request.page(documents, int64(listed.Pagination.Total)), nil
}

func (tools *DocumentDiscoveryTools) listMenus(ctx context.Context, request documentListRequest) (documentPage, error) {
	listed, err := tools.lists.Menus.ListMenus(ctx, menu.ListMenusQuery{Limit: request.limit32(), Offset: request.offset32()})
	if err != nil {
		return documentPage{}, err
	}
	documents := make([]listedDocument, 0, len(listed.Menus))
	for _, item := range listed.Menus {
		documents = append(documents, newListedDocument(core.DomainMenu, item.ID, item.Name, nil, item.SourceLocale, ""))
	}
	return request.page(keepTitlesContaining(documents, request.query), int64(listed.Total)), nil
}

func (tools *DocumentDiscoveryTools) listForms(ctx context.Context, request documentListRequest) (documentPage, error) {
	listed, err := tools.lists.Forms.ListFormsAdmin(ctx, form.ListFormsAdminQuery{Page: form.Page{Limit: request.limit32(), Offset: request.offset32()}, Filters: request.searchAndStatusFilters()})
	if err != nil {
		return documentPage{}, err
	}
	documents := make([]listedDocument, 0, len(listed.Forms))
	for _, item := range listed.Forms {
		documents = append(documents, newListedDocument(core.DomainForm, item.Form.ID, item.Title, item.Form.Slug, item.Form.SourceLocale, form.StatusMessage(item.Form.Status).String()))
	}
	return request.page(documents, int64(listed.Pagination.Total)), nil
}

func (tools *DocumentDiscoveryTools) listPostSeries(ctx context.Context, request documentListRequest) (documentPage, error) {
	listed, err := tools.lists.PostSeries.ListSeriesAdmin(ctx, series.ListSeriesAdminQuery{Page: series.Page{Limit: request.limit32(), Offset: request.offset32()}, Filters: request.searchAndStatusFilters()})
	if err != nil {
		return documentPage{}, err
	}
	documents := make([]listedDocument, 0, len(listed.Items))
	for _, item := range listed.Items {
		slug := item.Series.Slug
		documents = append(documents, newListedDocument(core.DomainPostSeries, item.Series.ID, item.Series.Title, &slug, item.Series.SourceLocale, string(item.Series.Status)))
	}
	return request.page(documents, int64(listed.Total)), nil
}

func (tools *DocumentDiscoveryTools) listPolicyVersions(ctx context.Context, request documentListRequest) (documentPage, error) {
	query := legal.ListPolicyVersionsQuery{Limit: request.limit32(), Offset: request.offset32()}
	if request.status != "" {
		status, known := legal.PolicyStatus(legal.PolicyKind(request.documentType), request.status)
		if !known {
			return documentPage{}, &core.InputError{Message: "status must be draft, scheduled, active, or archived"}
		}
		query.Status = &status
	}
	listed, err := tools.lists.Policies.ListPolicyVersions(ctx, legal.PolicyKind(request.documentType), query)
	if err != nil {
		return documentPage{}, err
	}
	documents := make([]listedDocument, 0, len(listed.Versions))
	for _, item := range listed.Versions {
		document := newListedDocument(request.documentType, item.ID, item.Title, nil, item.SourceLocale, string(item.Status))
		document.listedPolicyVersion = &listedPolicyVersion{
			Version: item.Version, DocumentRevision: item.Revision, EffectiveFrom: item.EffectiveFrom, EffectiveUntil: item.EffectiveUntil,
		}
		documents = append(documents, document)
	}
	return request.page(keepTitlesContaining(documents, request.query), int64(listed.Total)), nil
}

func (tools *DocumentDiscoveryTools) listCampaigns(ctx context.Context, request documentListRequest) (documentPage, error) {
	listed, err := tools.lists.Campaigns.ListCampaigns(ctx, campaign.ListCampaignsQuery{Limit: request.limit32(), Offset: request.offset32(), Filters: discoverySearchFilters(request.query)})
	if err != nil {
		return documentPage{}, err
	}
	documents := make([]listedDocument, 0, len(listed.Campaigns))
	for _, item := range listed.Campaigns {
		documents = append(documents, newListedDocument(core.DomainCampaign, item.ID, item.Name, nil, item.SourceLocale, string(item.Status)))
	}
	return request.page(documents, int64(listed.Pagination.Total)), nil
}

func (tools *DocumentDiscoveryTools) listEmailTemplates(ctx context.Context, request documentListRequest) (documentPage, error) {
	listed, err := tools.lists.EmailTemplates.ListEmailTemplates(ctx, emailauthoring.ListEmailTemplatesQuery{Limit: request.limit32(), Offset: request.offset32(), Filters: discoverySearchFilters(request.query)})
	if err != nil {
		return documentPage{}, err
	}
	documents := make([]listedDocument, 0, len(listed.Templates))
	for _, item := range listed.Templates {
		documents = append(documents, newListedDocument(core.DomainEmailTemplate, item.ID, item.Name, nil, item.SourceLocale, ""))
	}
	return request.page(documents, listed.Total), nil
}

func (tools *DocumentDiscoveryTools) listEmailLayouts(ctx context.Context, request documentListRequest) (documentPage, error) {
	listed, err := tools.lists.EmailLayouts.ListEmailLayouts(ctx, emailauthoring.ListEmailLayoutsQuery{Limit: request.limit32(), Offset: request.offset32(), Filters: discoverySearchFilters(request.query)})
	if err != nil {
		return documentPage{}, err
	}
	documents := make([]listedDocument, 0, len(listed.Layouts))
	for _, item := range listed.Layouts {
		documents = append(documents, newListedDocument(core.DomainEmailLayout, item.ID, item.Name, nil, item.SourceLocale, ""))
	}
	return request.page(documents, listed.Total), nil
}

func discoverySearchFilters(query string) []*commonv1.FilterSpec {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	return []*commonv1.FilterSpec{{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: query}}
}

// keepTitlesContaining is the query of a list with no search of its own.
func keepTitlesContaining(documents []listedDocument, query string) []listedDocument {
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		return documents
	}
	return slices.DeleteFunc(documents, func(document listedDocument) bool {
		return !strings.Contains(strings.ToLower(document.Title), needle)
	})
}
