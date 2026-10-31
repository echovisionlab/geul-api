package public

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mapcluster"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/sliceutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Service holds the public Post reads.
type Service struct {
	db         *gorm.DB
	cdnDomain  string
	spiceDB    *auth.SpiceDBClient
	files      FileService
	blocks     *contentblock.Store
	localizer  LocalizationService
	mapPlaces  MapPlaceProjector
	members    MemberSummaryLoader
	shareLinks postdomain.ShareLinkValidator
}

// Deps are the collaborators of the public Post reads. Every field is
// required unless marked; NewService rejects a missing one.
type Deps struct {
	DB *gorm.DB
	// CDNDomain builds OG asset URLs; empty serves relative paths.
	CDNDomain     string
	SpiceDB       *auth.SpiceDBClient
	Files         FileService
	ContentBlocks *contentblock.Store
	Localizer     LocalizationService
	MapPlaces     MapPlaceProjector
	Members       MemberSummaryLoader
	ShareLinks    postdomain.ShareLinkValidator
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"DB": d.DB == nil, "SpiceDB": d.SpiceDB == nil, "Files": d.Files == nil, "ContentBlocks": d.ContentBlocks == nil,
		"Localizer": d.Localizer == nil, "MapPlaces": d.MapPlaces == nil, "Members": d.Members == nil, "ShareLinks": d.ShareLinks == nil,
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
		db: deps.DB, cdnDomain: deps.CDNDomain, spiceDB: deps.SpiceDB, files: deps.Files, blocks: deps.ContentBlocks,
		localizer: deps.Localizer, mapPlaces: deps.MapPlaces, members: deps.Members, shareLinks: deps.ShareLinks,
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
	Viewport       *openv1.PostMapViewport
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

type postMapFeatureRow struct {
	PostID       string  `gorm:"column:post_id"`
	PostTitle    string  `gorm:"column:post_title"`
	PostSlug     *string `gorm:"column:post_slug"`
	PlaceID      string  `gorm:"column:place_id"`
	PlaceName    string  `gorm:"column:place_name"`
	PlaceAddress string  `gorm:"column:place_address"`
	PlaceLat     float64 `gorm:"column:place_lat"`
	PlaceLng     float64 `gorm:"column:place_lng"`
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

func isPublicPostStatus(status model.PostStatus) bool {
	return status == model.PostStatusPublished ||
		status == model.PostStatusArchived
}

func publicPostStatusValues() []string {
	return []string{
		string(model.PostStatusPublished),
		string(model.PostStatusArchived),
	}
}

func withPublicPostStatusFence(query *gorm.DB) *gorm.DB {
	return query.Where("post.status IN ?", publicPostStatusValues())
}

// Get retrieves a post by slug or ID
// - UUID-first: if input is valid UUID, query by ID; otherwise query by slug
// - share_token allows access to draft posts
// - authenticated accounts with the exact SpiceDB view permission can access drafts
func (s *Service) Get(ctx context.Context, query GetPostQuery) (*openv1.GetPostResponse, error) {
	projection, err := s.loadPublicPostSnapshot(ctx, query)
	if err != nil {
		return nil, err
	}
	return s.buildPostResponse(ctx, query.AcceptLanguage, projection)
}

func (s *Service) requireDraftPostAccess(
	ctx context.Context,
	db *gorm.DB,
	postID string,
	shareToken string,
	sharePassword string,
) (mediaasset.ContentDownloadOwnerAccessMode, *model.ShareLink, error) {
	allowed, err := hasDraftResourceView(ctx, s.spiceDB, postID)
	if err != nil {
		return "", nil, fmt.Errorf("check post draft view permission: %w", err)
	}
	if allowed {
		return mediaasset.ContentDownloadOwnerAccessAuthenticatedDraft, nil, nil
	}
	link, err := requireDraftShareLinkAccess(
		ctx, db, shareToken, sharePassword,
		managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_POST, postID, "post", s.shareLinks,
	)
	if err != nil {
		return "", nil, err
	}
	return mediaasset.ContentDownloadOwnerAccessShare, link, nil
}

type publicPostSnapshot struct {
	post               model.Post
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
	err := mutation.ReadLocked(ctx, s.db, func(tx *gorm.DB) error {
		if err := lockPublicPost(ctx, tx, request.Slug, &snapshot.post); err != nil {
			return err
		}
		authorization, err := s.authorizePublicPostMedia(ctx, tx, &snapshot.post, request)
		if err != nil {
			return err
		}
		snapshot.mediaAuthorization = authorization
		return s.loadPublicPostBody(ctx, tx, &snapshot, request.AcceptLanguage)
	})
	if err != nil {
		return nil, err
	}
	return &snapshot, nil
}

// lockPublicPost SHARE-locks the Post named by an ID or slug and loads its
// relations.
func lockPublicPost(ctx context.Context, tx *gorm.DB, slugOrID string, post *model.Post) error {
	query := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
		Preload("Categories").
		Preload("Tags").
		Preload("Series").
		Preload("MapPlace")
	var err error
	if isValidUUID(slugOrID) {
		err = query.First(post, "id = ?", slugOrID).Error
	} else {
		err = query.First(post, "slug = ?", slugOrID).Error
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domainerr.New(domainerr.NotFound, "", "post not found")
	}
	return err
}

// authorizePublicPostMedia checks a draft Post's view or share-link access
// and returns the witness its media downloads are authorized with.
func (s *Service) authorizePublicPostMedia(
	ctx context.Context,
	tx *gorm.DB,
	post *model.Post,
	request GetPostQuery,
) (mediaasset.ContentDownloadOwnerAuthorization, error) {
	authorization := mediaasset.ContentDownloadOwnerAuthorization{
		ResourceType: "post",
		ResourceID:   post.ID,
		Status:       string(post.Status),
		Mode:         mediaasset.ContentDownloadOwnerAccessPublic,
	}
	if post.ContentDocumentID != nil {
		authorization.DocumentID = *post.ContentDocumentID
	}
	if isPublicPostStatus(post.Status) {
		return authorization, nil
	}
	mode, link, err := s.requireDraftPostAccess(ctx, tx, post.ID, request.ShareToken, request.SharePassword)
	if err != nil {
		return authorization, err
	}
	authorization.Mode = mode
	authorization.ShareLink = mediaasset.ContentDownloadShareLinkWitnessFromModel(link)
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
func (s *Service) loadPublicPostBody(ctx context.Context, tx *gorm.DB, snapshot *publicPostSnapshot, acceptLanguage string) error {
	post := &snapshot.post
	state, err := postdomain.LoadPostSourceLocaleDocumentStateForPublic(ctx, tx, post.ID)
	if err != nil {
		return err
	}
	if state == nil {
		return domainerr.ResourceNotFound("post_translation", post.ID)
	}
	postdomain.OverlayPostSourceLocaleDocumentForPublic(post, state)
	localization, err := s.localizer.ResolveSelectionWithPolicy(ctx, tx, "post", post.ID, acceptLanguage, true)
	if err != nil {
		slog.ErrorContext(ctx, "failed to resolve post localization", "postId", post.ID, "failure_cause", failurecause.Message(err))
	}
	localization, err = s.localizer.ResolveOgConsistency(ctx, tx, s.cdnDomain, "post", post.ID, localization)
	if err != nil {
		return err
	}
	document, revision, err := postdomain.LoadLocalizedPostContentProjectionForPublic(
		ctx, tx, s.blocks, post.ID, localization.DisplayedLocale,
	)
	if err != nil {
		return err
	}
	snapshot.localization = localization
	snapshot.document = document
	snapshot.revision = revision
	return nil
}

// List returns public posts, including archived read-only posts.
func (s *Service) List(ctx context.Context, request ListPostsQuery) (*openv1.ListPostsResponse, error) {
	var posts []model.Post
	var total int64

	query := withPublicPostStatusFence(s.db.WithContext(ctx).Model(&model.Post{}).
		Preload("Categories").
		Preload("Tags").
		Preload("FeaturedImage"))

	query, err := s.applyPublicPostFilters(query, request.Filters)
	if err != nil {
		return nil, err
	}

	// Count total before pagination
	if err := query.Count(&total).Error; err != nil {
		return nil, err
	}

	// 4. Apply sort
	query, err = PostSortConfig.ApplySort(query, request.Sorts)
	if err != nil {
		return nil, err
	}

	// 5. Apply pagination
	pagination := queryutil.ExtractPaginationRaw(request.Limit, request.Offset, queryutil.DefaultPaginationConfig)
	query = pagination.Apply(query)

	if err := query.Find(&posts).Error; err != nil {
		return nil, err
	}

	if err := s.overlayPostSourceLocaleDocuments(ctx, posts); err != nil {
		return nil, err
	}

	localizedSelections, err := s.localizer.ResolveSelectionsWithPolicy(
		ctx,
		s.db,
		"post",
		collectPostIDs(posts),
		request.AcceptLanguage,
		true,
	)
	if err != nil {
		slog.ErrorContext(ctx, "failed to resolve post summary localizations", "failure_cause", failurecause.Message(err))
	}
	authorMembers, err := s.loadPostAuthorMembers(ctx, collectPostIDs(posts))
	if err != nil {
		return nil, err
	}
	featuredDeliveries, err := s.loadPostFeaturedImageDeliveries(ctx, posts)
	if err != nil {
		return nil, err
	}

	// Convert to proto summaries
	summaries := make([]*openv1.PostSummary, 0, len(posts))
	for _, post := range posts {
		summaries = append(summaries, s.toPostSummary(&post, localizedSelections[post.ID], authorMembers[post.ID], featuredDeliveries[post.ID]))
	}

	return &openv1.ListPostsResponse{
		Posts:      summaries,
		Pagination: pagination.BuildResponse(total),
	}, nil
}

func invalidPostFilterOp(filter *commonv1.FilterSpec) error {
	return domainerr.New(domainerr.Invalid, filter.GetField(),
		fmt.Sprintf("invalid filter operation '%s' for field '%s'", filter.GetOp().String(), filter.GetField()))
}

func (s *Service) applyPublicPostFilters(
	query *gorm.DB,
	filters []*commonv1.FilterSpec,
) (*gorm.DB, error) {
	var remainingFilters []*commonv1.FilterSpec

	for _, f := range filters {
		switch f.GetField() {
		case "category_id":
			if f.GetOp() == commonv1.FilterOp_FILTER_OP_EQ {
				query = query.Where("post.id IN (SELECT post_id FROM post_category WHERE category_id = ?)", f.GetValue())
			} else if f.GetOp() == commonv1.FilterOp_FILTER_OP_IN {
				query = query.Where("post.id IN (SELECT post_id FROM post_category WHERE category_id IN ?)", f.GetValues())
			} else {
				return nil, invalidPostFilterOp(f)
			}
		case "tag_id":
			if f.GetOp() == commonv1.FilterOp_FILTER_OP_EQ {
				query = query.Where("post.id IN (SELECT post_id FROM post_tag WHERE tag_id = ?)", f.GetValue())
			} else if f.GetOp() == commonv1.FilterOp_FILTER_OP_IN {
				query = query.Where("post.id IN (SELECT post_id FROM post_tag WHERE tag_id IN ?)", f.GetValues())
			} else {
				return nil, invalidPostFilterOp(f)
			}
		case "author_id":
			if f.GetOp() == commonv1.FilterOp_FILTER_OP_EQ {
				query = query.Where(
					"post.id IN (SELECT pa.post_id FROM post_author AS pa WHERE pa.member_id = ?)",
					f.GetValue(),
				)
			} else if f.GetOp() == commonv1.FilterOp_FILTER_OP_IN {
				authorIDs := f.GetValues()
				if len(authorIDs) == 0 {
					query = query.Where("1 = 0")
					continue
				}
				query = query.Where(
					"post.id IN (SELECT DISTINCT pa.post_id FROM post_author AS pa WHERE pa.member_id IN ?)",
					authorIDs,
				)
			} else {
				return nil, invalidPostFilterOp(f)
			}
		default:
			remainingFilters = append(remainingFilters, f)
		}
	}

	return PostFilterConfig.ApplyFilters(query, remainingFilters)
}

// ListMapFeatures returns server-clustered map data for public posts in the current viewport.
func (s *Service) ListMapFeatures(ctx context.Context, request ListPostMapFeaturesQuery) (*openv1.ListPostMapFeaturesResponse, error) {
	viewport, err := normalizePostMapViewport(request.Viewport)
	if err != nil {
		return nil, err
	}

	var rows []postMapFeatureRow
	query := withPublicPostStatusFence(s.db.WithContext(ctx).
		Model(&model.Post{}).
		Select(`
			post.id AS post_id,
			`+postdomain.PostSourceTitleSQL+` AS post_title,
			post.slug AS post_slug,
			map_place.id AS place_id,
			map_place.name AS place_name,
			map_place.address AS place_address,
			map_place.lat AS place_lat,
			map_place.lng AS place_lng
		`).
		Joins("JOIN map_place ON map_place.id = post.map_place_id").
		Where("map_place.lat BETWEEN ? AND ?", viewport.South, viewport.North))

	if viewport.FullLongitude {
		// World-wrapping viewport: longitude filter would incorrectly exclude visible places.
	} else if viewport.West <= viewport.East {
		query = query.Where("map_place.lng BETWEEN ? AND ?", viewport.West, viewport.East)
	} else {
		query = query.Where("(map_place.lng >= ? OR map_place.lng <= ?)", viewport.West, viewport.East)
	}

	query, err = s.applyPublicPostFilters(query, request.Filters)
	if err != nil {
		return nil, err
	}

	query, err = PostSortConfig.ApplySort(query, request.Sorts)
	if err != nil {
		return nil, err
	}

	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}

	sourceStates, err := postdomain.LoadPostSourceLocaleDocumentStatesForPublic(
		ctx,
		s.db,
		collectPostMapIDs(rows),
	)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		sourceState := sourceStates[rows[i].PostID]
		if sourceState == nil {
			return nil, domainerr.ResourceNotFound("post_translation", rows[i].PostID)
		}
		if sourceState.Title != nil {
			rows[i].PostTitle = *sourceState.Title
		}
	}

	localizedSelections, err := s.localizer.ResolveSelectionsWithPolicy(
		ctx,
		s.db,
		"post",
		collectPostMapIDs(rows),
		request.AcceptLanguage,
		true,
	)
	if err != nil {
		slog.ErrorContext(ctx, "failed to resolve post map localizations", "failure_cause", failurecause.Message(err))
	}
	placeGroups := buildPostMapPlaceGroups(rows, localizedSelections)
	clusters, items := clusterPostMapPlaceGroups(placeGroups, viewport)

	return &openv1.ListPostMapFeaturesResponse{
		Clusters: clusters,
		Items:    items,
	}, nil
}

func normalizePostMapViewport(viewport *openv1.PostMapViewport) (normalizedPostMapViewport, error) {
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
	rows []postMapFeatureRow,
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

func collectPostMapIDs(rows []postMapFeatureRow) []string {
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
) ([]*openv1.PostMapCluster, []*openv1.PostMapItem) {
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

	clusters := make([]*openv1.PostMapCluster, 0, len(components))
	items := make([]*openv1.PostMapItem, 0, len(placeGroups))
	for index, component := range components {
		if component.ShouldRenderItems(parameters) {
			for _, group := range component.Groups {
				items = append(items, postMapItem(group))
			}
			continue
		}
		clusters = append(clusters, &openv1.PostMapCluster{
			Id:              fmt.Sprintf("cluster-%d", index+1),
			Lat:             component.Lat,
			Lng:             component.Lng,
			PlaceCount:      int32(len(component.Groups)),
			PostCount:       component.Count,
			MinBreakoutZoom: component.MinBreakoutZoom,
			Bounds: &openv1.MapBounds{
				West: component.West, South: component.South,
				East: component.East, North: component.North,
			},
		})
	}
	return clusters, items
}

func postMapItem(group *postMapPlaceGroup) *openv1.PostMapItem {
	return &openv1.PostMapItem{
		PlaceId:          group.PlaceID,
		Name:             group.Name,
		Address:          group.Address,
		Lat:              group.Lat,
		Lng:              group.Lng,
		PostCount:        group.PostCount,
		PrimaryPostId:    group.PrimaryPostID,
		PrimaryPostSlug:  group.PrimaryPostSlug,
		PrimaryPostTitle: group.PrimaryPostTitle,
	}
}

// Search searches for posts by query
func (s *Service) Search(ctx context.Context, request SearchPostsQuery) (*openv1.SearchPostsResponse, error) {
	query := request.Query
	if len(query) < 2 {
		return &openv1.SearchPostsResponse{Posts: []*openv1.PostSummary{}}, nil
	}

	var posts []model.Post
	pattern := "%" + query + "%"
	limit := int32(10)
	if request.Limit > 0 {
		limit = request.Limit
	}

	if err := withPublicPostStatusFence(s.db.WithContext(ctx).
		Model(&model.Post{}).
		Where(postdomain.PostSourceTitleSQL+" ILIKE ? OR "+postdomain.PostSourceContentTextSQL+" ILIKE ?", pattern, pattern).
		Preload("Categories").
		Preload("Tags").
		Preload("FeaturedImage").
		Order("published_at DESC NULLS LAST").
		Limit(int(limit))).
		Find(&posts).Error; err != nil {
		return nil, err
	}

	if err := s.overlayPostSourceLocaleDocuments(ctx, posts); err != nil {
		return nil, err
	}

	localizedSelections, err := s.localizer.ResolveSelectionsWithPolicy(
		ctx,
		s.db,
		"post",
		collectPostIDs(posts),
		request.AcceptLanguage,
		true,
	)
	if err != nil {
		slog.ErrorContext(ctx, "failed to resolve post search localizations", "failure_cause", failurecause.Message(err))
	}
	authorMembers, err := s.loadPostAuthorMembers(ctx, collectPostIDs(posts))
	if err != nil {
		return nil, err
	}
	featuredDeliveries, err := s.loadPostFeaturedImageDeliveries(ctx, posts)
	if err != nil {
		return nil, err
	}

	summaries := make([]*openv1.PostSummary, 0, len(posts))
	for _, post := range posts {
		summaries = append(summaries, s.toPostSummary(&post, localizedSelections[post.ID], authorMembers[post.ID], featuredDeliveries[post.ID]))
	}

	return &openv1.SearchPostsResponse{Posts: summaries}, nil
}

// buildPostResponse builds a GetPostResponse with the post
func (s *Service) buildPostResponse(
	ctx context.Context,
	acceptLanguage string,
	projection *publicPostSnapshot,
) (*openv1.GetPostResponse, error) {
	post := &projection.post
	localization := projection.localization
	document := projection.document
	revision := projection.revision
	ctx = mediaasset.WithContentDownloadOwnerAuthorization(ctx, projection.mediaAuthorization)
	documentLayout, err := post.DocumentLayout.Proto()
	if err != nil {
		return nil, err
	}

	protoPost := &openv1.Post{
		Id:              post.ID,
		Title:           post.Title,
		Status:          postStatusToProto(post.Status),
		CommentsEnabled: post.CommentsEnabled,
		DocumentLayout:  documentLayout,
		CreatedAt:       timestamppb.New(post.CreatedAt),
		UpdatedAt:       timestamppb.New(post.UpdatedAt),
		Document:        document,
		Revision:        revision,
	}

	if post.Slug != nil {
		protoPost.Slug = post.Slug
	}
	if post.Summary != nil {
		protoPost.Summary = post.Summary
	}
	protoPost.LocalizationInfo = localization.ProtoInfo()
	if post.PublishedAt != nil {
		protoPost.PublishedAt = timestamppb.New(*post.PublishedAt)
	}
	if post.ScheduledAt != nil {
		protoPost.ScheduledAt = timestamppb.New(*post.ScheduledAt)
	}
	protoPost.ScheduledTimeZone = post.ScheduledTimeZone
	postSourceOgAssetID := post.OgAssetID
	if localization.OmitSourceOgFallback {
		postSourceOgAssetID = nil
	}
	protoPost.OgAsset, err = resolvedOgAssetRef(ctx, s.db, s.cdnDomain, postSourceOgAssetID, localization.OgAssetID)
	if err != nil {
		return nil, err
	}
	if post.MapPlaceID != nil {
		protoPost.MapPlaceId = post.MapPlaceID
		protoPost.LocationPlace = s.mapPlaces.Basic(post.MapPlace)
	}

	applyPostLocalization(protoPost, localization)

	authorMembers, err := s.loadPostAuthorMembers(ctx, []string{post.ID})
	if err != nil {
		return nil, err
	}
	protoPost.AuthorMembers = authorMembers[post.ID]

	// Categories
	for _, cat := range post.Categories {
		slug := cat.Slug
		protoPost.Categories = append(protoPost.Categories, &openv1.PostCategory{
			Id:   cat.ID,
			Name: cat.Name,
			Slug: &slug,
		})
	}

	// Tags
	for _, tag := range post.Tags {
		slug := tag.Slug
		protoPost.Tags = append(protoPost.Tags, &openv1.PostTag{
			Id:   tag.ID,
			Name: tag.Name,
			Slug: &slug,
		})
	}

	// Series
	if post.Series != nil {
		seriesTitle := post.Series.Title
		seriesLocalization, localizationErr := s.localizer.ResolveSelectionWithPolicy(
			ctx,
			s.db,
			"series",
			post.Series.ID,
			acceptLanguage,
			true,
		)
		if localizationErr != nil {
			return nil, localizationErr
		}
		if seriesLocalization.Title != nil {
			seriesTitle = *seriesLocalization.Title
		}
		slug := post.Series.Slug
		protoPost.Series = &openv1.PostSeries{
			Id:    post.Series.ID,
			Title: seriesTitle,
			Slug:  &slug,
		}
	}

	// Series order
	if post.SeriesOrder != nil {
		protoPost.SeriesOrder = post.SeriesOrder
	}

	if s.files == nil {
		return nil, fmt.Errorf("post media resolver is not configured")
	}
	if post.ContentDocumentID == nil || *post.ContentDocumentID == "" {
		return nil, domainerr.Precondition("Post content document is not initialized")
	}
	documentID, err := uuid.Parse(*post.ContentDocumentID)
	if err != nil {
		return nil, err
	}
	contentMediaItems, err := s.files.ResolveAuthorizedContentBlockMedia(ctx, documentID)
	if err != nil {
		return nil, err
	}
	if post.FeaturedImageFileID != nil {
		featured, featuredErr := s.files.ResolvePublicDisplayMedia(ctx, []string{*post.FeaturedImageFileID})
		if featuredErr != nil {
			return nil, featuredErr
		}
		protoPost.FeaturedImageDelivery = publicDisplayOnlyDelivery(featured[*post.FeaturedImageFileID])
	}
	return &openv1.GetPostResponse{
		Post:       protoPost,
		BlockMedia: contentMediaItems,
	}, nil
}

func applyPostLocalization(protoPost *openv1.Post, localization LocalizedContentSelection) {
	if localization.Title != nil {
		protoPost.Title = *localization.Title
	}
	if localization.Summary != nil {
		protoPost.Summary = localization.Summary
	}
}

// toPostSummary converts a post to a summary (no content)
func (s *Service) toPostSummary(
	post *model.Post,
	localization LocalizedContentSelection,
	authorMembers []*commonv1.MemberSummary,
	featuredImage *commonv1.MediaDelivery,
) *openv1.PostSummary {
	summary := &openv1.PostSummary{
		Id:    post.ID,
		Title: post.Title,
	}

	if post.Slug != nil {
		summary.Slug = post.Slug
	}
	if post.Summary != nil {
		summary.Summary = post.Summary
	}
	if localization.Title != nil {
		summary.Title = *localization.Title
	}
	if localization.Summary != nil {
		summary.Summary = localization.Summary
	}
	if post.PublishedAt != nil {
		summary.PublishedAt = timestamppb.New(*post.PublishedAt)
	}
	if post.MapPlaceID != nil {
		summary.MapPlaceId = post.MapPlaceID
	}
	if post.SeriesOrder != nil {
		summary.SeriesOrder = post.SeriesOrder
	}

	summary.FeaturedImageDelivery = featuredImage
	summary.AuthorMembers = authorMembers

	// Categories
	for _, cat := range post.Categories {
		slug := cat.Slug
		summary.Categories = append(summary.Categories, &openv1.PostCategory{
			Id:   cat.ID,
			Name: cat.Name,
			Slug: &slug,
		})
	}

	// Tags
	for _, tag := range post.Tags {
		slug := tag.Slug
		summary.Tags = append(summary.Tags, &openv1.PostTag{
			Id:   tag.ID,
			Name: tag.Name,
			Slug: &slug,
		})
	}

	return summary
}

func collectPostIDs(posts []model.Post) []string {
	ids := make([]string, 0, len(posts))
	for _, post := range posts {
		ids = append(ids, post.ID)
	}
	return ids
}

func (s *Service) overlayPostSourceLocaleDocuments(
	ctx context.Context,
	posts []model.Post,
) error {
	sourceStates, err := postdomain.LoadPostSourceLocaleDocumentStatesForPublic(ctx, s.db, collectPostIDs(posts))
	if err != nil {
		return err
	}
	for i := range posts {
		sourceState := sourceStates[posts[i].ID]
		if sourceState == nil {
			return domainerr.ResourceNotFound("post_translation", posts[i].ID)
		}
		postdomain.OverlayPostSourceLocaleDocumentForPublic(&posts[i], sourceState)
	}
	return nil
}

// loadPostAuthorMembers resolves durable post authors and Member
// summaries in a fixed number of queries for any page size.
func (s *Service) loadPostAuthorMembers(ctx context.Context, postIDs []string) (map[string][]*commonv1.MemberSummary, error) {
	ids := sliceutil.UniqueTrimmed(postIDs)
	result := make(map[string][]*commonv1.MemberSummary, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	type authorRow struct {
		PostID   string `gorm:"column:post_id"`
		MemberID string `gorm:"column:member_id"`
	}
	var rows []authorRow
	if err := s.db.WithContext(ctx).
		Table("post_author").
		Select("post_id, member_id").
		Where("post_id IN ?", ids).
		Order("post_id ASC, created_at ASC, member_id ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load durable post authors: %w", err)
	}
	memberIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		memberIDs = append(memberIDs, row.MemberID)
	}
	summaries, err := s.members.LoadPublicMemberSummaries(ctx, memberIDs)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if summary := summaries[row.MemberID]; summary != nil {
			result[row.PostID] = append(result[row.PostID], summary)
		}
	}
	return result, nil
}
