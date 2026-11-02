package public

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"

	fileref "github.com/echovisionlab/geul-api/internal/fileref"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mapcluster"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/memberref"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/post"
	"github.com/google/uuid"

	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/sliceutil"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// Service holds the public Post reads.
type Service struct {
	units     mutation.UnitOfWork[Store]
	spiceDB   authz.AuthorizationDecisionChecker
	files     FileService
	localizer Localizer
	members   MemberSummaryLoader
}

// Deps are the collaborators of the public Post reads. Every field is
// required; NewService rejects a missing one.
type Deps struct {
	Units     mutation.UnitOfWork[Store] // postpublicpostgres.NewUnits
	SpiceDB   authz.AuthorizationDecisionChecker
	Files     FileService
	Localizer Localizer // postpublicpostgres.NewLocalizer
	Members   MemberSummaryLoader
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"Units": d.Units == nil, "SpiceDB": d.SpiceDB == nil, "Files": d.Files == nil,
		"Localizer": d.Localizer == nil, "Members": d.Members == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("public Post dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// NewService validates deps and returns the public Post reads.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	return &Service{
		units: deps.Units, spiceDB: deps.SpiceDB, files: deps.Files, localizer: deps.Localizer, members: deps.Members,
	}, nil
}

// GetPostQuery reads a public Post by ID or slug. A draft is visible to a
// caller with view permission or through its share link. AcceptLanguage
// picks the displayed locale.
type GetPostQuery struct {
	Slug           string
	ShareToken     string
	SharePassword  string
	AcceptLanguage string
}

// ListPostsQuery lists public Posts.
type ListPostsQuery struct {
	Limit          int32
	Offset         int32
	Filters        []*commonv1.FilterSpec
	Sorts          []*commonv1.SortSpec
	AcceptLanguage string
}

// ListPostMapFeaturesQuery clusters the mapped public Posts in Viewport.
type ListPostMapFeaturesQuery struct {
	Viewport       *MapViewport
	Filters        []*commonv1.FilterSpec
	Sorts          []*commonv1.SortSpec
	AcceptLanguage string
}

// SearchPostsQuery searches public Post titles and text.
type SearchPostsQuery struct {
	Query          string
	Limit          int32
	AcceptLanguage string
}

// MapViewport is the part of the map a reader sees. A nil Bounds is
// invalid.
type MapViewport struct {
	Bounds           *MapBounds
	Zoom             float64
	WidthPx          int32
	HeightPx         int32
	ClusterRadiusPx  int32
	MinClusterPoints int32
}

// MapBounds are a viewport's or a cluster's edges.
type MapBounds struct {
	West, South, East, North float64
}

// MapFeatures are the clusters and single places of a viewport.
type MapFeatures struct {
	Clusters []MapCluster
	Items    []MapItem
}

// MapCluster is several places drawn as one.
type MapCluster struct {
	ID              string
	Lat             float64
	Lng             float64
	PlaceCount      int32
	PostCount       int32
	MinBreakoutZoom *float64
	Bounds          MapBounds
}

// MapItem is one place with its Posts.
type MapItem struct {
	PlaceID          string
	Name             string
	Address          string
	Lat              float64
	Lng              float64
	PostCount        int32
	PrimaryPostID    string
	PrimaryPostSlug  *string
	PrimaryPostTitle string
}

// PostView is a public Post as a reader opens it.
type PostView struct {
	PublicPost
	Localization  LocalizedContentSelection
	Document      *contentv1.LocalizedRichTextDocument
	Revision      string
	OgAsset       *mediaasset.Reference
	Authors       []*memberref.Summary
	SeriesTitle   string
	BlockMedia    []*fileref.BlockMediaItem
	FeaturedImage *fileref.Delivery
}

// Summary is a public Post in a list, without its content.
type Summary struct {
	Post          post.Post
	Localization  LocalizedContentSelection
	Authors       []*memberref.Summary
	FeaturedImage *fileref.Delivery
}

// SummaryPage is one page of public Posts.
type SummaryPage struct {
	Summaries  []Summary
	Pagination *commonv1.PaginationResponse
}

type normalizedPostMapViewport struct {
	West             float64
	South            float64
	East             float64
	North            float64
	Zoom             float64
	WidthPx          float64
	HeightPx         float64
	ClusterRadiusPx  float64
	MinClusterPoints int
	FullLongitude    bool
	FullLatitude     bool
}

type postMapPlaceGroup struct {
	PlaceID          string
	Name             string
	Address          string
	Lat              float64
	Lng              float64
	PostCount        int32
	PrimaryPostID    string
	PrimaryPostSlug  *string
	PrimaryPostTitle string
	Order            int
}

func isPublicPostStatus(status post.Status) bool {
	return status == post.StatusPublished ||
		status == post.StatusArchived
}

// Get retrieves a post by slug or ID
// - UUID-first: if input is valid UUID, query by ID; otherwise query by slug
// - share_token allows access to draft posts
// - authenticated accounts with the exact SpiceDB view permission can access drafts
func (s *Service) Get(ctx context.Context, query GetPostQuery) (PostView, error) {
	projection, err := s.loadPublicPostSnapshot(ctx, query)
	if err != nil {
		return PostView{}, err
	}
	return s.buildPostView(ctx, query.AcceptLanguage, projection)
}

func (s *Service) requireDraftPostAccess(
	ctx context.Context,
	store Store,
	postID string,
	shareToken string,
	sharePassword string,
) (mediaasset.ContentDownloadOwnerAccessMode, *mediaasset.ContentDownloadShareLinkWitness, error) {
	allowed, err := hasDraftResourceView(ctx, s.spiceDB, postID)
	if err != nil {
		return "", nil, fmt.Errorf("check post draft view permission: %w", err)
	}
	if allowed {
		return mediaasset.ContentDownloadOwnerAccessAuthenticatedDraft, nil, nil
	}
	link, err := requireDraftShareLinkAccess(ctx, store, shareToken, sharePassword, postID, "post")
	if err != nil {
		return "", nil, err
	}
	return mediaasset.ContentDownloadOwnerAccessShare, link, nil
}

type publicPostSnapshot struct {
	post               PublicPost
	localization       LocalizedContentSelection
	document           *contentv1.LocalizedRichTextDocument
	revision           string
	mediaAuthorization mediaasset.ContentDownloadOwnerAuthorization
}

// loadPublicPostSnapshot holds the Post root SHARE lock while checking its
// public/draft visibility and reading the localized body from the same RR
// snapshot. Post content writers take UPDATE on this root before the document
// lock, matching the page read/write lock order.
func (s *Service) loadPublicPostSnapshot(ctx context.Context, request GetPostQuery) (*publicPostSnapshot, error) {
	var snapshot publicPostSnapshot
	err := s.units.ReadLocked(ctx, func(store Store) error {
		locked, err := store.ShareLockPost(ctx, request.Slug, isValidUUID(request.Slug))
		if errors.Is(err, post.ErrPostNotFound) {
			return domainerr.New(domainerr.NotFound, "", "post not found")
		}
		if err != nil {
			return err
		}
		snapshot.post = locked
		authorization, err := s.authorizePublicPostMedia(ctx, store, &snapshot.post, request)
		if err != nil {
			return err
		}
		snapshot.mediaAuthorization = authorization
		return s.loadPublicPostBody(ctx, store, &snapshot, request.AcceptLanguage)
	})
	if err != nil {
		return nil, err
	}
	return &snapshot, nil
}

// authorizePublicPostMedia checks a draft Post's view or share-link access
// and returns the witness its media downloads are authorized with.
func (s *Service) authorizePublicPostMedia(
	ctx context.Context,
	store Store,
	locked *PublicPost,
	request GetPostQuery,
) (mediaasset.ContentDownloadOwnerAuthorization, error) {
	authorization := mediaasset.ContentDownloadOwnerAuthorization{
		ResourceType: "post",
		ResourceID:   locked.ID,
		Status:       string(locked.Status),
		Mode:         mediaasset.ContentDownloadOwnerAccessPublic,
	}
	if locked.ContentDocumentID != nil {
		authorization.DocumentID = *locked.ContentDocumentID
	}
	if isPublicPostStatus(locked.Status) {
		return authorization, nil
	}
	mode, link, err := s.requireDraftPostAccess(ctx, store, locked.ID, request.ShareToken, request.SharePassword)
	if err != nil {
		return authorization, err
	}
	authorization.Mode = mode
	authorization.ShareLink = link
	if mode == mediaasset.ContentDownloadOwnerAccessAuthenticatedDraft {
		if user := auth.GetUser(ctx); user != nil {
			authorization.IdentityID = user.IdentityID.String()
			authorization.MemberID = user.MemberID.String()
		}
	}
	return authorization, nil
}

// loadPublicPostBody overlays the source locale and reads the localized body
// the caller's Accept-Language selects.
func (s *Service) loadPublicPostBody(ctx context.Context, store Store, snapshot *publicPostSnapshot, acceptLanguage string) error {
	locked := &snapshot.post
	states, err := store.SourceLocaleStates(ctx, []string{locked.ID})
	if err != nil {
		return err
	}
	state, ok := states[locked.ID]
	if !ok {
		return domainerr.ResourceNotFound("post_translation", locked.ID)
	}
	post.OverlaySourceLocale(&locked.Post, state)
	localization, err := store.ResolveLocalization(ctx, locked.ID, acceptLanguage)
	if err != nil {
		slog.ErrorContext(ctx, "failed to resolve post localization", "postId", locked.ID, "failure_cause", failurecause.Message(err))
	}
	localization, err = store.ResolveOgConsistency(ctx, locked.ID, localization)
	if err != nil {
		return err
	}
	document, revision, err := post.LoadLocalizedContent(ctx, store, locked.ID, localization.DisplayedLocale)
	if err != nil {
		return err
	}
	snapshot.localization = localization
	snapshot.document = document
	snapshot.revision = revision
	return nil
}

// List returns public posts, including archived read-only posts.
func (s *Service) List(ctx context.Context, request ListPostsQuery) (SummaryPage, error) {
	pagination := queryutil.ExtractPaginationRaw(request.Limit, request.Offset, queryutil.DefaultPaginationConfig)
	var posts []post.Post
	var total int64
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		posts, total, err = store.ListPosts(ctx, ListQuery{
			Filters: request.Filters, Sorts: request.Sorts, Limit: pagination.Limit, Offset: pagination.Offset,
		})
		return err
	}); err != nil {
		return SummaryPage{}, err
	}
	summaries, err := s.summaries(ctx, posts, request.AcceptLanguage, "failed to resolve post summary localizations")
	if err != nil {
		return SummaryPage{}, err
	}
	return SummaryPage{Summaries: summaries, Pagination: pagination.BuildResponse(total)}, nil
}

// summaries overlays the source locale of posts and attaches the localized
// title and summary, authors and featured image each shows.
func (s *Service) summaries(ctx context.Context, posts []post.Post, acceptLanguage, localizationFailure string) ([]Summary, error) {
	if err := s.overlayPostSourceLocaleDocuments(ctx, posts); err != nil {
		return nil, err
	}
	localizedSelections, err := s.localizer.ResolveSelections(ctx, "post", collectPostIDs(posts), acceptLanguage, true)
	if err != nil {
		slog.ErrorContext(ctx, localizationFailure, "failure_cause", failurecause.Message(err))
	}
	authorMembers, err := s.loadPostAuthorMembers(ctx, collectPostIDs(posts))
	if err != nil {
		return nil, err
	}
	featuredDeliveries, err := s.loadPostFeaturedImageDeliveries(ctx, posts)
	if err != nil {
		return nil, err
	}
	summaries := make([]Summary, 0, len(posts))
	for _, listed := range posts {
		summaries = append(summaries, Summary{
			Post: listed, Localization: localizedSelections[listed.ID],
			Authors: authorMembers[listed.ID], FeaturedImage: featuredDeliveries[listed.ID],
		})
	}
	return summaries, nil
}

// ListMapFeatures returns server-clustered map data for public posts in the current viewport.
func (s *Service) ListMapFeatures(ctx context.Context, request ListPostMapFeaturesQuery) (MapFeatures, error) {
	viewport, err := normalizePostMapViewport(request.Viewport)
	if err != nil {
		return MapFeatures{}, err
	}
	var rows []MapFeatureRow
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		rows, err = store.ListMapFeatures(ctx, MapQuery{
			South: viewport.South, North: viewport.North, West: viewport.West, East: viewport.East,
			FullLongitude: viewport.FullLongitude, Filters: request.Filters, Sorts: request.Sorts,
		})
		return err
	}); err != nil {
		return MapFeatures{}, err
	}

	sourceStates, err := s.sourceLocaleStates(ctx, collectPostMapIDs(rows))
	if err != nil {
		return MapFeatures{}, err
	}
	for i := range rows {
		sourceState, ok := sourceStates[rows[i].PostID]
		if !ok {
			return MapFeatures{}, domainerr.ResourceNotFound("post_translation", rows[i].PostID)
		}
		if sourceState.Title != nil {
			rows[i].PostTitle = *sourceState.Title
		}
	}

	localizedSelections, err := s.localizer.ResolveSelections(ctx, "post", collectPostMapIDs(rows), request.AcceptLanguage, true)
	if err != nil {
		slog.ErrorContext(ctx, "failed to resolve post map localizations", "failure_cause", failurecause.Message(err))
	}
	placeGroups := buildPostMapPlaceGroups(rows, localizedSelections)
	clusters, items := clusterPostMapPlaceGroups(placeGroups, viewport)
	return MapFeatures{Clusters: clusters, Items: items}, nil
}

func normalizePostMapViewport(viewport *MapViewport) (normalizedPostMapViewport, error) {
	if viewport == nil || viewport.Bounds == nil {
		return normalizedPostMapViewport{}, domainerr.InvalidField("viewport", "viewport bounds are required")
	}

	south := mapcluster.Clamp(viewport.Bounds.South, -85, 85)
	north := mapcluster.Clamp(viewport.Bounds.North, -85, 85)
	if north < south {
		south, north = north, south
	}

	west := mapcluster.NormalizeLongitude(viewport.Bounds.West)
	east := mapcluster.NormalizeLongitude(viewport.Bounds.East)

	zoom := viewport.Zoom
	if math.IsNaN(zoom) || math.IsInf(zoom, 0) {
		return normalizedPostMapViewport{}, domainerr.InvalidField("viewport.zoom", "zoom must be finite")
	}
	// Signed MapLibre zooms down to the renderer's minimum are valid with
	// measured dimensions. Preserve legacy defaults for missing-size inputs
	// at zoom zero or below, and for zooms below the supported minimum.
	if zoom < mapcluster.MinViewportZoom || (zoom <= 0 && (viewport.WidthPx <= 0 || viewport.HeightPx <= 0)) {
		zoom = 1.5
	}

	widthPx := float64(viewport.WidthPx)
	if widthPx <= 0 {
		widthPx = 1280
	}

	heightPx := float64(viewport.HeightPx)
	if heightPx <= 0 {
		heightPx = 720
	}

	clusterRadiusPx := float64(viewport.ClusterRadiusPx)
	if clusterRadiusPx <= 0 {
		clusterRadiusPx = mapcluster.DefaultMapClusterRadiusPxForZoom(zoom, widthPx)
	}

	worldScale := mapcluster.WorldTileSize * math.Pow(2, zoom)
	fullLongitude := widthPx >= worldScale-1
	fullLatitude := heightPx >= worldScale-1

	minClusterPoints := int(viewport.MinClusterPoints)
	if minClusterPoints <= 0 {
		minClusterPoints = mapcluster.MapClusterDefaultMinPoints
	}

	if fullLongitude {
		west = -180
		east = 180
	}

	if fullLatitude {
		south = -85
		north = 85
	}

	return normalizedPostMapViewport{
		West:             west,
		South:            south,
		East:             east,
		North:            north,
		Zoom:             zoom,
		WidthPx:          widthPx,
		HeightPx:         heightPx,
		ClusterRadiusPx:  clusterRadiusPx,
		MinClusterPoints: minClusterPoints,
		FullLongitude:    fullLongitude,
		FullLatitude:     fullLatitude,
	}, nil
}

func buildPostMapPlaceGroups(
	rows []MapFeatureRow,
	localizedSelections map[string]LocalizedContentSelection,
) []*postMapPlaceGroup {
	placeByID := make(map[string]*postMapPlaceGroup, len(rows))
	ordered := make([]*postMapPlaceGroup, 0, len(rows))

	for idx, row := range rows {
		group, ok := placeByID[row.PlaceID]
		if !ok {
			title := row.PostTitle
			if localized, ok := localizedSelections[row.PostID]; ok && localized.Title != nil {
				title = *localized.Title
			}
			group = &postMapPlaceGroup{
				PlaceID:          row.PlaceID,
				Name:             row.PlaceName,
				Address:          row.PlaceAddress,
				Lat:              row.PlaceLat,
				Lng:              row.PlaceLng,
				PrimaryPostID:    row.PostID,
				PrimaryPostSlug:  row.PostSlug,
				PrimaryPostTitle: title,
				Order:            idx,
			}
			placeByID[row.PlaceID] = group
			ordered = append(ordered, group)
		}
		group.PostCount++
	}

	return ordered
}

func collectPostMapIDs(rows []MapFeatureRow) []string {
	ids := make([]string, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if _, ok := seen[row.PostID]; ok {
			continue
		}
		seen[row.PostID] = struct{}{}
		ids = append(ids, row.PostID)
	}
	return ids
}

func clusterPostMapPlaceGroups(
	placeGroups []*postMapPlaceGroup,
	viewport normalizedPostMapViewport,
) ([]MapCluster, []MapItem) {
	if len(placeGroups) == 0 {
		return nil, nil
	}

	parameters := mapcluster.MapClusterParameters{
		Zoom:             viewport.Zoom,
		RadiusPx:         viewport.ClusterRadiusPx,
		MinClusterPoints: viewport.MinClusterPoints,
	}
	components := mapcluster.ClusterMapPlaceGroups(
		placeGroups,
		parameters,
		func(group *postMapPlaceGroup) (float64, float64) { return group.Lat, group.Lng },
		func(group *postMapPlaceGroup) int32 { return group.PostCount },
	)

	clusters := make([]MapCluster, 0, len(components))
	items := make([]MapItem, 0, len(placeGroups))
	for index, component := range components {
		if component.ShouldRenderItems(parameters) {
			for _, group := range component.Groups {
				items = append(items, postMapItem(group))
			}
			continue
		}
		clusters = append(clusters, MapCluster{
			ID:              fmt.Sprintf("cluster-%d", index+1),
			Lat:             component.Lat,
			Lng:             component.Lng,
			PlaceCount:      int32(len(component.Groups)),
			PostCount:       component.Count,
			MinBreakoutZoom: component.MinBreakoutZoom,
			Bounds: MapBounds{
				West: component.West, South: component.South,
				East: component.East, North: component.North,
			},
		})
	}
	return clusters, items
}

func postMapItem(group *postMapPlaceGroup) MapItem {
	return MapItem{
		PlaceID:          group.PlaceID,
		Name:             group.Name,
		Address:          group.Address,
		Lat:              group.Lat,
		Lng:              group.Lng,
		PostCount:        group.PostCount,
		PrimaryPostID:    group.PrimaryPostID,
		PrimaryPostSlug:  group.PrimaryPostSlug,
		PrimaryPostTitle: group.PrimaryPostTitle,
	}
}

// Search searches for posts by query
func (s *Service) Search(ctx context.Context, request SearchPostsQuery) ([]Summary, error) {
	query := request.Query
	if len(query) < 2 {
		return []Summary{}, nil
	}

	pattern := "%" + query + "%"
	limit := int32(10)
	if request.Limit > 0 {
		limit = request.Limit
	}
	var posts []post.Post
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		posts, err = store.SearchPosts(ctx, pattern, int(limit))
		return err
	}); err != nil {
		return nil, err
	}
	return s.summaries(ctx, posts, request.AcceptLanguage, "failed to resolve post search localizations")
}

// buildPostView completes a public Post read after its snapshot: OG image,
// authors, Series title and media.
func (s *Service) buildPostView(
	ctx context.Context,
	acceptLanguage string,
	projection *publicPostSnapshot,
) (PostView, error) {
	locked := projection.post
	localization := projection.localization
	ctx = mediaasset.WithContentDownloadOwnerAuthorization(ctx, projection.mediaAuthorization)
	if err := validateStoredLayout(locked.DocumentLayout); err != nil {
		return PostView{}, err
	}
	view := PostView{
		PublicPost: locked, Localization: localization, Document: projection.document, Revision: projection.revision,
	}
	postSourceOgAssetID := locked.OgAssetID
	if localization.OmitSourceOgFallback {
		postSourceOgAssetID = nil
	}
	var err error
	if err = s.units.Read(ctx, func(store Store) error {
		view.OgAsset, err = store.ReadyOgAsset(ctx, localization.OgAssetID, postSourceOgAssetID)
		return err
	}); err != nil {
		return PostView{}, err
	}

	authorMembers, err := s.loadPostAuthorMembers(ctx, []string{locked.ID})
	if err != nil {
		return PostView{}, err
	}
	view.Authors = authorMembers[locked.ID]

	if locked.Series != nil {
		view.SeriesTitle = locked.Series.Title
		seriesLocalization, localizationErr := s.localizer.ResolveSelection(ctx, "series", locked.Series.ID, acceptLanguage, true)
		if localizationErr != nil {
			return PostView{}, localizationErr
		}
		if seriesLocalization.Title != nil {
			view.SeriesTitle = *seriesLocalization.Title
		}
	}

	if s.files == nil {
		return PostView{}, fmt.Errorf("post media resolver is not configured")
	}
	if locked.ContentDocumentID == nil || *locked.ContentDocumentID == "" {
		return PostView{}, domainerr.Precondition("Post content document is not initialized")
	}
	documentID, err := uuid.Parse(*locked.ContentDocumentID)
	if err != nil {
		return PostView{}, err
	}
	if view.BlockMedia, err = s.files.ResolveAuthorizedContentBlockMedia(ctx, documentID); err != nil {
		return PostView{}, err
	}
	if locked.FeaturedImageFileID != nil {
		featured, featuredErr := s.files.ResolveDisplayMedia(ctx, []string{*locked.FeaturedImageFileID})
		if featuredErr != nil {
			return PostView{}, featuredErr
		}
		view.FeaturedImage = publicDisplayOnlyDelivery(featured[*locked.FeaturedImageFileID])
	}
	return view, nil
}

// validateStoredLayout rejects a stored layout a read cannot render, as the
// layout's API mapping does.
func validateStoredLayout(layout post.DocumentLayout) error {
	switch {
	case layout.ContentHeight != post.ContentHeightContent && layout.ContentHeight != post.ContentHeightViewport:
		return fmt.Errorf("invalid persisted document layout: invalid contentHeight %q", layout.ContentHeight)
	case layout.PageChrome != post.PlacementFlow && layout.PageChrome != post.PlacementPinned:
		return fmt.Errorf("invalid persisted document layout: invalid pageChrome %q", layout.PageChrome)
	case layout.Footer != post.PlacementFlow && layout.Footer != post.PlacementPinned:
		return fmt.Errorf("invalid persisted document layout: invalid footer %q", layout.Footer)
	}
	return nil
}

func collectPostIDs(posts []post.Post) []string {
	ids := make([]string, 0, len(posts))
	for _, listed := range posts {
		ids = append(ids, listed.ID)
	}
	return ids
}

func (s *Service) sourceLocaleStates(ctx context.Context, postIDs []string) (map[string]post.SourceLocaleState, error) {
	var states map[string]post.SourceLocaleState
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		states, err = store.SourceLocaleStates(ctx, postIDs)
		return err
	})
	return states, err
}

func (s *Service) overlayPostSourceLocaleDocuments(ctx context.Context, posts []post.Post) error {
	sourceStates, err := s.sourceLocaleStates(ctx, collectPostIDs(posts))
	if err != nil {
		return err
	}
	for i := range posts {
		sourceState, ok := sourceStates[posts[i].ID]
		if !ok {
			return domainerr.ResourceNotFound("post_translation", posts[i].ID)
		}
		post.OverlaySourceLocale(&posts[i], sourceState)
	}
	return nil
}

// loadPostAuthorMembers resolves durable post authors and Member
// summaries in a fixed number of queries for any page size.
func (s *Service) loadPostAuthorMembers(ctx context.Context, postIDs []string) (map[string][]*memberref.Summary, error) {
	ids := sliceutil.UniqueTrimmed(postIDs)
	result := make(map[string][]*memberref.Summary, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	var links []post.AuthorLink
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		links, err = store.AuthorLinks(ctx, ids)
		return err
	}); err != nil {
		return nil, fmt.Errorf("load durable post authors: %w", err)
	}
	memberIDs := make([]string, 0, len(links))
	for _, link := range links {
		memberIDs = append(memberIDs, link.MemberID)
	}
	summaries, err := s.members.LoadPublicMemberSummaries(ctx, memberIDs)
	if err != nil {
		return nil, err
	}
	for _, link := range links {
		if summary := summaries[link.MemberID]; summary != nil {
			result[link.PostID] = append(result[link.PostID], summary)
		}
	}
	return result, nil
}
