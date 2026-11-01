package public

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	workdomain "github.com/echovisionlab/geul-api/internal/work"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// ContentBlockMediaHydrator is the public Work media boundary. Public File
// delivery owns its separate share/public policy evaluation.
type ContentBlockMediaHydrator interface {
	HydrateAuthorizedContentBlockMedia(context.Context, []*contentv1.ContentBlockMediaItem) ([]*contentv1.ContentBlockMediaItem, error)
}

// MediaResolver is the Work public-read projection supplied by its runtime
// adapter.
type MediaResolver interface {
	ResolveReadyOGAsset(context.Context, *string, *string) (*commonv1.AssetRef, error)
	ResolveReadyAssetForSourceFile(context.Context, string, ...string) *commonv1.AssetRef
	ResolveArtistImageAsset(context.Context, string) *commonv1.AssetRef
}

type MemberSummaryLoader interface {
	LoadPublicMemberSummaries(context.Context, []string) (map[string]*commonv1.MemberSummary, error)
}

// Deps are the collaborators of the public Work reads; all are required.
type Deps struct {
	Units      mutation.UnitOfWork[Store] // workpublicpostgres.NewUnits
	SpiceDB    authz.AuthorizationDecisionChecker
	BlockMedia ContentBlockMediaHydrator
	Assets     MediaResolver
	Members    MemberSummaryLoader
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"Units": d.Units == nil, "SpiceDB": d.SpiceDB == nil, "BlockMedia": d.BlockMedia == nil, "Assets": d.Assets == nil,
		"Members": d.Members == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("public Work dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the public Work reads.
type Service struct {
	units      mutation.UnitOfWork[Store]
	spiceDB    authz.AuthorizationDecisionChecker
	blockMedia ContentBlockMediaHydrator
	assets     MediaResolver
	members    MemberSummaryLoader
}

// NewService validates deps and returns the public Work reads.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	return &Service{
		units: deps.Units, spiceDB: deps.SpiceDB, blockMedia: deps.BlockMedia, assets: deps.Assets, members: deps.Members,
	}, nil
}

// GetWorkQuery reads a published or archived Work by ID or slug. A draft is
// visible to a caller with view permission or through its share link.
// AcceptLanguage picks the displayed locale.
type GetWorkQuery struct {
	Slug           string
	ShareToken     string
	SharePassword  string
	AcceptLanguage string
}

// ListWorksQuery lists published and archived Works.
type ListWorksQuery struct {
	Limit          int32
	Offset         int32
	Filters        []*commonv1.FilterSpec
	Sorts          []*commonv1.SortSpec
	AcceptLanguage string
}

// ListWorkMapFeaturesQuery clusters the mapped Works in Viewport.
type ListWorkMapFeaturesQuery struct {
	Viewport *Viewport
	Filters  []*commonv1.FilterSpec
	Sorts    []*commonv1.SortSpec
}

// Pagination is where a page sits among every match.
type Pagination struct {
	Total   int32
	Limit   int32
	Offset  int32
	HasMore bool
}

// Summary is a public Work as a list shows it, in its displayed locale.
type Summary struct {
	Work          workdomain.Work
	Title         string
	Summary       *string
	FeaturedImage *commonv1.AssetRef
}

// WorkPage is one page of public Work summaries.
type WorkPage struct {
	Works      []Summary
	Pagination Pagination
}

// MapFeatures are the clustered and individual places of a map read.
type MapFeatures struct {
	Clusters []MapCluster
	Items    []MapItem
}

// Credit is a public credit with its Artist and Member, when they resolve.
type Credit struct {
	Credit workdomain.Credit
	Artist *CreditArtist
	Member *commonv1.MemberSummary
}

// CreditArtist is a credited Artist with its image.
type CreditArtist struct {
	ID    string
	Name  string
	Slug  *string
	Image *commonv1.AssetRef
}

// Client is a client of a public Work with its logos.
type Client struct {
	ID        string
	Name      string
	Website   *string
	LightLogo *commonv1.AssetRef
	DarkLogo  *commonv1.AssetRef
}

// Detail is a public Work with its displayed locale, content and
// relations.
type Detail struct {
	Work          Work
	Title         string
	Summary       *string
	Localization  publiccontent.Selection
	OgAsset       *commonv1.AssetRef
	FeaturedImage *commonv1.AssetRef
	Document      *contentv1.LocalizedRichTextDocument
	Revision      string
	CreditGroups  []workdomain.CreditGroup
	Credits       []Credit
	Clients       []Client
	BlockMedia    []*contentv1.ContentBlockMediaItem
}

func isPublicWorkStatus(status workdomain.Status) bool {
	return status == workdomain.StatusPublished || status == workdomain.StatusArchived
}

// PublicStatuses are the statuses a public read lists.
func PublicStatuses() []string {
	return []string{string(workdomain.StatusPublished), string(workdomain.StatusArchived)}
}

func collectWorkIDs(works []workdomain.Work) []string {
	ids := make([]string, 0, len(works))
	for _, work := range works {
		ids = append(ids, work.ID)
	}
	return ids
}

// List returns published and archived works.
func (s *Service) List(ctx context.Context, request ListWorksQuery) (WorkPage, error) {
	pagination := queryutil.GetPaginationParams(request.Limit, request.Offset, 20)
	var works []workdomain.Work
	var total int64
	var sourceStates map[string]workdomain.SourceDocument
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		if works, total, err = store.ListWorks(ctx, ListPage{
			Filters: request.Filters, Sorts: request.Sorts, Limit: pagination.Limit, Offset: pagination.Offset,
		}); err != nil {
			return err
		}
		if len(works) == 0 {
			return nil
		}
		sourceStates, err = store.SourceDocuments(ctx, collectWorkIDs(works))
		return err
	}); err != nil {
		return WorkPage{}, err
	}
	summaries := make([]Summary, 0, len(works))
	for _, work := range works {
		sourceState, ok := sourceStates[work.ID]
		if !ok {
			return WorkPage{}, fmt.Errorf("missing work source locale row for public list item %s", work.ID)
		}
		summaries = append(summaries, Summary{Work: work, Title: optionalText(sourceState.Title), Summary: sourceState.Summary})
	}

	var localizedSelections map[string]publiccontent.Selection
	if err := s.units.ReadLocked(ctx, func(store Store) error {
		var err error
		localizedSelections, err = store.ResolveLocalizations(ctx, collectWorkIDs(works), request.AcceptLanguage)
		return err
	}); err != nil {
		slog.ErrorContext(ctx, "Work summary localization lookup failed; listing works without localization", "domain", "work", "operation", "resolve_summary_localizations", failurecause.Attr(err))
	}

	for i := range summaries {
		localization := localizedSelections[summaries[i].Work.ID]
		if localization.Summary != nil {
			summaries[i].Summary = localization.Summary
		}
		if localization.Title != nil {
			summaries[i].Title = *localization.Title
		}
		summaries[i].FeaturedImage = s.getWorkFeaturedImageAssetForSourceFile(ctx, summaries[i].Work.FeaturedImageFileID)
	}

	return WorkPage{
		Works: summaries,
		Pagination: Pagination{
			Total:   int32(total),
			Limit:   pagination.Limit,
			Offset:  pagination.Offset,
			HasMore: pagination.Offset+int32(len(works)) < int32(total),
		},
	}, nil
}

func optionalText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// ListMapFeatures returns server-clustered map data for published and archived works in the current viewport.
func (s *Service) ListMapFeatures(ctx context.Context, request ListWorkMapFeaturesQuery) (MapFeatures, error) {
	viewport, err := normalizeWorkMapViewport(request.Viewport)
	if err != nil {
		return MapFeatures{}, err
	}

	var rows []MapFeatureRow
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		rows, err = store.MapFeatures(ctx, MapBounds{
			West: viewport.West, South: viewport.South, East: viewport.East, North: viewport.North,
			FullLongitude: viewport.FullLongitude,
		}, request.Filters, request.Sorts)
		if err != nil || len(rows) == 0 {
			return err
		}
		return requireSourceRowsForPublicWorkMap(ctx, store, rows)
	}); err != nil {
		return MapFeatures{}, err
	}

	placeGroups := buildWorkMapPlaceGroups(rows)
	clusters, items := clusterWorkMapPlaceGroups(placeGroups, viewport)
	return MapFeatures{Clusters: clusters, Items: items}, nil
}

func requireSourceRowsForPublicWorkMap(ctx context.Context, store Store, rows []MapFeatureRow) error {
	workIDs := collectWorkMapIDs(rows)
	sourceStates, err := store.SourceDocuments(ctx, workIDs)
	if err != nil {
		return err
	}
	for _, workID := range workIDs {
		if _, ok := sourceStates[workID]; !ok {
			return fmt.Errorf("missing work source locale row for public map item %s", workID)
		}
	}
	return nil
}

// publicWorkSnapshot is a Work and its relations read in one snapshot.
type publicWorkSnapshot struct {
	work               Work
	title              string
	summary            *string
	ogAssetID          *string
	mediaAuthorization mediaasset.ContentDownloadOwnerAuthorization
	localization       publiccontent.Selection
	document           Document
	creditGroups       []workdomain.CreditGroup
	credits            []workdomain.Credit
	clients            []workdomain.ClientRow
}

// Get retrieves a work by slug or ID
// - UUID-first: if input is valid UUID, query by ID; otherwise query by slug
// - share_token allows access to draft works
// - authenticated accounts with the exact SpiceDB view permission can access drafts
func (s *Service) Get(ctx context.Context, query GetWorkQuery) (Detail, error) {
	var snapshot publicWorkSnapshot
	if err := s.units.ReadLocked(ctx, func(store Store) error {
		var err error
		snapshot, err = s.loadPublicWorkSnapshot(ctx, store, query)
		return err
	}); err != nil {
		return Detail{}, err
	}
	blockMedia, err := s.blockMedia.HydrateAuthorizedContentBlockMedia(
		mediaasset.WithContentDownloadOwnerAuthorization(ctx, snapshot.mediaAuthorization), snapshot.document.BlockMedia,
	)
	if err != nil {
		return Detail{}, err
	}
	detail, err := s.projectPublicWork(ctx, snapshot)
	if err != nil {
		return Detail{}, err
	}
	detail.BlockMedia = blockMedia
	return detail, nil
}

func (s *Service) loadPublicWorkSnapshot(ctx context.Context, store Store, query GetWorkQuery) (publicWorkSnapshot, error) {
	var snapshot publicWorkSnapshot
	work, err := store.FindWork(ctx, query.Slug, workdomain.IsValidUUID(query.Slug))
	if errors.Is(err, workdomain.ErrWorkNotFound) {
		return snapshot, domainerr.New(domainerr.NotFound, "", "work not found")
	}
	if err != nil {
		return snapshot, err
	}
	snapshot.work = work
	if snapshot.mediaAuthorization, err = s.authorizePublicWork(ctx, store, work.Work, query); err != nil {
		return snapshot, err
	}
	documentID, err := store.ContentDocumentID(ctx, work.ID)
	if err != nil {
		return snapshot, err
	}
	snapshot.mediaAuthorization.DocumentID = documentID.String()
	sourceStates, err := store.SourceDocuments(ctx, []string{work.ID})
	if err != nil {
		return snapshot, err
	}
	sourceState, ok := sourceStates[work.ID]
	if !ok {
		return snapshot, errors.New("Work source locale is not initialized")
	}
	snapshot.title, snapshot.summary, snapshot.ogAssetID = optionalText(sourceState.Title), sourceState.Summary, sourceState.OgAssetID
	if snapshot.localization, err = store.ResolveLocalization(ctx, work.ID, query.AcceptLanguage); err != nil {
		return snapshot, err
	}
	if snapshot.localization, err = store.ResolveOGConsistency(ctx, work.ID, snapshot.localization); err != nil {
		return snapshot, err
	}
	if snapshot.document, err = store.Document(ctx, work.ID, documentID, snapshot.localization.DisplayedLocale); err != nil {
		return snapshot, err
	}
	if snapshot.creditGroups, err = store.CreditGroups(ctx, work.ID); err != nil {
		slog.ErrorContext(ctx, "Public Work credit group lookup failed; serving work without credit groups", "domain", "work", "operation", "load_credit_groups", failurecause.Attr(err), "work_id", work.ID)
	}
	if snapshot.credits, err = store.Credits(ctx, work.ID); err != nil {
		return publicWorkSnapshot{}, err
	}
	if snapshot.clients, err = store.Clients(ctx, work.ID); err != nil {
		slog.ErrorContext(ctx, "Public Work client lookup failed; serving work without clients", "domain", "work", "operation", "load_clients", failurecause.Attr(err), "work_id", work.ID)
	}
	return snapshot, nil
}

// authorizePublicWork returns how the reader may download the Work's
// media: as the public, as a viewer of the draft, or through its share link.
func (s *Service) authorizePublicWork(
	ctx context.Context,
	store Store,
	work workdomain.Work,
	query GetWorkQuery,
) (mediaasset.ContentDownloadOwnerAuthorization, error) {
	authorization := mediaasset.ContentDownloadOwnerAuthorization{
		ResourceType: "work",
		ResourceID:   work.ID,
		Status:       string(work.Status),
		Mode:         mediaasset.ContentDownloadOwnerAccessPublic,
	}
	if isPublicWorkStatus(work.Status) {
		return authorization, nil
	}
	allowed, err := hasDraftWorkView(ctx, s.spiceDB, work.ID)
	if err != nil {
		return authorization, fmt.Errorf("check work draft view permission: %w", err)
	}
	if allowed {
		authorization.Mode = mediaasset.ContentDownloadOwnerAccessAuthenticatedDraft
		if user := auth.GetUser(ctx); user != nil {
			authorization.IdentityID = user.IdentityID.String()
			authorization.MemberID = user.MemberID.String()
		}
		return authorization, nil
	}
	link, err := requireDraftShareLinkAccess(ctx, store, query.ShareToken, query.SharePassword, work.ID, "work")
	if err != nil {
		return authorization, err
	}
	authorization.Mode = mediaasset.ContentDownloadOwnerAccessShare
	authorization.ShareLink = link.Witness
	return authorization, nil
}

func hasDraftWorkView(ctx context.Context, spiceDB authz.AuthorizationDecisionChecker, workID string) (bool, error) {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || user.Banned || strings.TrimSpace(user.IdentityID.String()) == "" {
		return false, nil
	}
	can, err := policyv1.Work.View(workID)
	if err != nil {
		return false, err
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return false, err
	}
	return spiceDB.Can(ctx, decision)
}

func requireDraftShareLinkAccess(
	ctx context.Context,
	store Store,
	shareToken string,
	sharePassword string,
	entityID string,
	entityName string,
) (ShareLink, error) {
	if shareToken == "" {
		return ShareLink{}, domainerr.New(domainerr.NotFound, "", entityName+" not found")
	}
	link, found, err := store.ShareLink(ctx, shareToken, entityID, time.Now())
	if err != nil {
		return ShareLink{}, fmt.Errorf("validate %s share link: %w", entityName, err)
	}
	if !found {
		return ShareLink{}, domainerr.New(domainerr.NotFound, "", entityName+" not found")
	}
	if link.PasswordHash == nil {
		return link, nil
	}
	if sharePassword == "" {
		return ShareLink{}, domainerr.New(domainerr.NotFound, "", entityName+" not found")
	}
	match, err := crypto.NewPasswordHasher(nil).Verify(sharePassword, *link.PasswordHash)
	if err != nil {
		return ShareLink{}, fmt.Errorf("validate %s share link: %w", entityName, err)
	}
	if !match {
		return ShareLink{}, domainerr.New(domainerr.NotFound, "", entityName+" not found")
	}
	return link, nil
}

func (s *Service) projectPublicWork(ctx context.Context, snapshot publicWorkSnapshot) (Detail, error) {
	localization := snapshot.localization
	detail := Detail{
		Work: snapshot.work, Title: snapshot.title, Summary: snapshot.summary, Localization: localization,
		Document: snapshot.document.Document, Revision: snapshot.document.Revision, CreditGroups: snapshot.creditGroups,
	}
	if localization.Title != nil {
		detail.Title = *localization.Title
	}
	workSourceOgAssetID := snapshot.ogAssetID
	if localization.OmitSourceOgFallback {
		workSourceOgAssetID = nil
	}
	var err error
	if detail.OgAsset, err = s.assets.ResolveReadyOGAsset(ctx, workSourceOgAssetID, localization.OgAssetID); err != nil {
		return Detail{}, err
	}
	detail.FeaturedImage = s.getWorkFeaturedImageAssetForSourceFile(ctx, snapshot.work.FeaturedImageFileID)
	if localization.Summary != nil {
		detail.Summary = localization.Summary
	}
	if detail.Credits, err = s.getWorkCredits(ctx, snapshot.credits); err != nil {
		return Detail{}, err
	}
	detail.Clients = s.getWorkClients(ctx, snapshot.clients)
	return detail, nil
}

// getWorkCredits gets work credits with artist/Member details.
func (s *Service) getWorkCredits(ctx context.Context, credits []workdomain.Credit) ([]Credit, error) {
	memberIDs := make([]string, 0, len(credits))
	for _, credit := range credits {
		if credit.MemberID != nil {
			memberIDs = append(memberIDs, *credit.MemberID)
		}
	}
	memberSummaries, err := s.members.LoadPublicMemberSummaries(ctx, memberIDs)
	if err != nil {
		return nil, err
	}

	views := make([]Credit, 0, len(credits))
	for _, credit := range credits {
		view := Credit{Credit: credit}
		if credit.ArtistID != nil {
			view.Artist = s.loadPublicCreditArtist(ctx, *credit.ArtistID)
		}
		if credit.MemberID != nil {
			view.Member = memberSummaries[*credit.MemberID]
		}
		views = append(views, view)
	}
	return views, nil
}

func (s *Service) loadPublicCreditArtist(ctx context.Context, artistID string) *CreditArtist {
	var artist CreditArtistRow
	var found bool
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		artist, found, err = store.CreditArtist(ctx, artistID)
		return err
	}); err != nil {
		slog.ErrorContext(ctx, "Public Work credit artist lookup failed; serving credit without artist", "domain", "work", "operation", "load_credit_artist", failurecause.Attr(err), "artist_id", artistID)
		return nil
	}
	if !found {
		return nil
	}
	return &CreditArtist{ID: artist.ID, Name: artist.Name, Slug: artist.Slug, Image: s.assets.ResolveArtistImageAsset(ctx, artist.ID)}
}

func (s *Service) getWorkFeaturedImageAssetForSourceFile(ctx context.Context, sourceFileID *string) *commonv1.AssetRef {
	if sourceFileID == nil {
		return nil
	}
	return s.assets.ResolveReadyAssetForSourceFile(ctx, *sourceFileID, "image")
}

// getWorkClients resolves the logos of a Work's clients.
func (s *Service) getWorkClients(ctx context.Context, rows []workdomain.ClientRow) []Client {
	if len(rows) == 0 {
		return nil
	}

	clients := make([]Client, 0, len(rows))
	for _, row := range rows {
		client := Client{ID: row.ID, Name: row.Name, Website: row.Website}
		if row.LightFileID != nil {
			client.LightLogo = s.assets.ResolveReadyAssetForSourceFile(ctx, *row.LightFileID, "logo")
		}
		if row.DarkFileID != nil {
			client.DarkLogo = s.assets.ResolveReadyAssetForSourceFile(ctx, *row.DarkFileID, "logo")
		}
		clients = append(clients, client)
	}

	return clients
}

// mapPlaceOf is the Work's projected Map Place, nil when it has none.
func mapPlaceOf(work Work) *openv1.MapPlaceBasic {
	if work.MapPlaceID == nil {
		return nil
	}
	return work.MapPlace
}
