package public

import (
	"context"
	"fmt"
	"log/slog"
	"math"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mapcluster"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// PostService implements the public PostService
type PostService struct {
	openv1connect.UnimplementedPostServiceHandler
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

type PostServiceOption func(*PostService)

func WithPostContentBlockStore(store *contentblock.Store) PostServiceOption {
	return func(service *PostService) {
		service.blocks = store
	}
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

// NewPostService creates a new public PostService
func NewPostService(
	db *gorm.DB,
	cdnDomain string,
	spiceDB *auth.SpiceDBClient,
	files FileService,
	localizer LocalizationService,
	mapPlaces MapPlaceProjector,
	members MemberSummaryLoader,
	shareLinks postdomain.ShareLinkValidator,
	options ...PostServiceOption,
) *PostService {
	if db == nil || spiceDB == nil || files == nil || localizer == nil || mapPlaces == nil || members == nil || shareLinks == nil {
		panic("public post service dependencies are required")
	}
	service := &PostService{
		db:         db,
		cdnDomain:  cdnDomain,
		spiceDB:    spiceDB,
		files:      files,
		localizer:  localizer,
		mapPlaces:  mapPlaces,
		members:    members,
		shareLinks: shareLinks,
	}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service
}

func isPublicPostStatus(status model.PostStatus) bool {
	return status == model.PostStatus(managev1.PostStatus_POST_STATUS_PUBLISHED.String()) ||
		status == model.PostStatus(managev1.PostStatus_POST_STATUS_ARCHIVED.String())
}

func publicPostStatusValues() []string {
	return []string{
		managev1.PostStatus_POST_STATUS_PUBLISHED.String(),
		managev1.PostStatus_POST_STATUS_ARCHIVED.String(),
	}
}

func withPublicPostStatusFence(query *gorm.DB) *gorm.DB {
	return query.Where("post.status IN ?", publicPostStatusValues())
}

// Get retrieves a post by slug or ID
// - UUID-first: if input is valid UUID, query by ID; otherwise query by slug
// - share_token allows access to draft posts
// - authenticated accounts with the exact SpiceDB view permission can access drafts
func (s *PostService) Get(
	ctx context.Context,
	req *connect.Request[openv1.GetPostRequest],
) (*connect.Response[openv1.GetPostResponse], error) {
	slugOrID := req.Msg.Slug
	shareToken := req.Msg.ShareToken

	var post model.Post
	var err error
	query := s.db.WithContext(ctx).
		Preload("Categories").
		Preload("Tags").
		Preload("Series").
		Preload("MapPlace")

	// UUID-first approach
	if isValidUUID(slugOrID) {
		err = query.First(&post, "id = ?", slugOrID).Error
	} else {
		err = query.First(&post, "slug = ?", slugOrID).Error
	}

	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFoundMsg("post not found")
		}
		return nil, errs.Internal(err)
	}

	if err := s.overlayPostSourceLocaleDocument(ctx, &post); err != nil {
		return nil, err
	}

	mediaAuthorization := mediaasset.ContentDownloadOwnerAuthorization{
		ResourceType: "post",
		ResourceID:   post.ID,
		Status:       string(post.Status),
		Mode:         mediaasset.ContentDownloadOwnerAccessPublic,
	}
	if post.ContentDocumentID != nil {
		mediaAuthorization.DocumentID = *post.ContentDocumentID
	}

	// Private states are hidden unless the current verified account has exact
	// Post authority or a bounded ShareLink/password proof succeeds.
	if !isPublicPostStatus(post.Status) {
		mode, link, accessErr := s.requireDraftPostAccess(
			ctx, post.ID, optionalStringValue(shareToken), req.Msg.GetSharePassword(),
		)
		if accessErr != nil {
			return nil, accessErr
		}
		mediaAuthorization.Mode = mode
		mediaAuthorization.ShareLink = mediaasset.ContentDownloadShareLinkWitnessFromModel(link)
		if mode == mediaasset.ContentDownloadOwnerAccessAuthenticatedDraft {
			if user := auth.GetUser(ctx); user != nil {
				mediaAuthorization.IdentityID = user.IdentityID.String()
				mediaAuthorization.MemberID = user.MemberID.String()
			}
		}
	}

	return s.buildPostResponse(
		mediaasset.WithContentDownloadOwnerAuthorization(ctx, mediaAuthorization),
		req.Header().Get("Accept-Language"),
		&post,
	)
}

func (s *PostService) requireDraftPostAccess(
	ctx context.Context,
	postID string,
	shareToken string,
	sharePassword string,
) (mediaasset.ContentDownloadOwnerAccessMode, *model.ShareLink, error) {
	allowed, err := hasDraftResourceView(ctx, s.spiceDB, postID)
	if err != nil {
		return "", nil, errs.Internal(fmt.Errorf("check post draft view permission: %w", err))
	}
	if allowed {
		return mediaasset.ContentDownloadOwnerAccessAuthenticatedDraft, nil, nil
	}
	link, err := requireDraftShareLinkAccess(
		ctx, s.db, shareToken, sharePassword,
		managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_POST, postID, "post", s.shareLinks,
	)
	if err != nil {
		return "", nil, err
	}
	return mediaasset.ContentDownloadOwnerAccessShare, link, nil
}

// List returns public posts, including archived read-only posts.
func (s *PostService) List(
	ctx context.Context,
	req *connect.Request[openv1.ListPostsRequest],
) (*connect.Response[openv1.ListPostsResponse], error) {
	var posts []model.Post
	var total int64

	query := withPublicPostStatusFence(s.db.WithContext(ctx).Model(&model.Post{}).
		Preload("Categories").
		Preload("Tags").
		Preload("FeaturedImage"))

	query, err := s.applyPublicPostFilters(ctx, query, req.Msg.Filters)
	if err != nil {
		return nil, err
	}

	// Count total before pagination
	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}

	// 4. Apply sort
	query, err = PostSortConfig.ApplySort(query, req.Msg.Sorts)
	if err != nil {
		return nil, err
	}

	// 5. Apply pagination
	pagination := queryutil.ExtractPagination(req.Msg.Pagination, queryutil.DefaultPaginationConfig)
	query = pagination.Apply(query)

	if err := query.Find(&posts).Error; err != nil {
		return nil, errs.Internal(err)
	}

	if err := s.overlayPostSourceLocaleDocuments(ctx, posts); err != nil {
		return nil, err
	}

	localizedSelections, err := s.localizer.ResolveSelectionsWithPolicy(
		ctx,
		s.db,
		"post",
		collectPostIDs(posts),
		req.Header().Get("Accept-Language"),
		true,
	)
	if err != nil {
		slog.Warn("failed to resolve post summary localizations", "error", err)
	}
	authorMembers, err := s.loadPostAuthorMembers(ctx, collectPostIDs(posts))
	if err != nil {
		return nil, errs.Internal(err)
	}
	featuredDeliveries, err := s.loadPostFeaturedImageDeliveries(ctx, posts)
	if err != nil {
		return nil, errs.Internal(err)
	}

	// Convert to proto summaries
	summaries := make([]*openv1.PostSummary, 0, len(posts))
	for _, post := range posts {
		summaries = append(summaries, s.toPostSummary(&post, localizedSelections[post.ID], authorMembers[post.ID], featuredDeliveries[post.ID]))
	}

	return connect.NewResponse(&openv1.ListPostsResponse{
		Posts:      summaries,
		Pagination: pagination.BuildResponse(total),
	}), nil
}

func (s *PostService) applyPublicPostFilters(
	ctx context.Context,
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
				return nil, errs.InvalidFilterOp(f.GetField(), f.GetOp().String())
			}
		case "tag_id":
			if f.GetOp() == commonv1.FilterOp_FILTER_OP_EQ {
				query = query.Where("post.id IN (SELECT post_id FROM post_tag WHERE tag_id = ?)", f.GetValue())
			} else if f.GetOp() == commonv1.FilterOp_FILTER_OP_IN {
				query = query.Where("post.id IN (SELECT post_id FROM post_tag WHERE tag_id IN ?)", f.GetValues())
			} else {
				return nil, errs.InvalidFilterOp(f.GetField(), f.GetOp().String())
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
				return nil, errs.InvalidFilterOp(f.GetField(), f.GetOp().String())
			}
		default:
			remainingFilters = append(remainingFilters, f)
		}
	}

	return PostFilterConfig.ApplyFilters(query, remainingFilters)
}

// ListMapFeatures returns server-clustered map data for public posts in the current viewport.
func (s *PostService) ListMapFeatures(
	ctx context.Context,
	req *connect.Request[openv1.ListPostMapFeaturesRequest],
) (*connect.Response[openv1.ListPostMapFeaturesResponse], error) {
	viewport, err := normalizePostMapViewport(req.Msg.Viewport)
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

	query, err = s.applyPublicPostFilters(ctx, query, req.Msg.Filters)
	if err != nil {
		return nil, err
	}

	query, err = PostSortConfig.ApplySort(query, req.Msg.Sorts)
	if err != nil {
		return nil, err
	}

	if err := query.Find(&rows).Error; err != nil {
		return nil, errs.Internal(err)
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
			return nil, errs.NotFound("post_translation", rows[i].PostID)
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
		req.Header().Get("Accept-Language"),
		true,
	)
	if err != nil {
		slog.Warn("failed to resolve post map localizations", "error", err)
	}
	placeGroups := buildPostMapPlaceGroups(rows, localizedSelections)
	clusters, items := clusterPostMapPlaceGroups(placeGroups, viewport)

	return connect.NewResponse(&openv1.ListPostMapFeaturesResponse{
		Clusters: clusters,
		Items:    items,
	}), nil
}

func normalizePostMapViewport(viewport *openv1.PostMapViewport) (normalizedPostMapViewport, error) {
	if viewport == nil || viewport.Bounds == nil {
		return normalizedPostMapViewport{}, errs.InvalidArgument("viewport", "viewport bounds are required")
	}

	south := mapcluster.Clamp(viewport.Bounds.South, -85, 85)
	north := mapcluster.Clamp(viewport.Bounds.North, -85, 85)
	if north < south {
		south, north = north, south
	}

	west := mapcluster.NormalizeLongitude(viewport.Bounds.West)
	east := mapcluster.NormalizeLongitude(viewport.Bounds.East)

	zoom := viewport.Zoom
	if zoom <= 0 {
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

	worldScale := 256 * math.Pow(2, zoom)
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
func (s *PostService) Search(
	ctx context.Context,
	req *connect.Request[openv1.SearchPostsRequest],
) (*connect.Response[openv1.SearchPostsResponse], error) {
	query := req.Msg.Query
	if len(query) < 2 {
		return connect.NewResponse(&openv1.SearchPostsResponse{
			Posts: []*openv1.PostSummary{},
		}), nil
	}

	var posts []model.Post
	pattern := "%" + query + "%"
	limit := int32(10)
	if req.Msg.Limit > 0 {
		limit = req.Msg.Limit
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
		return nil, errs.Internal(err)
	}

	if err := s.overlayPostSourceLocaleDocuments(ctx, posts); err != nil {
		return nil, err
	}

	localizedSelections, err := s.localizer.ResolveSelectionsWithPolicy(
		ctx,
		s.db,
		"post",
		collectPostIDs(posts),
		req.Header().Get("Accept-Language"),
		true,
	)
	if err != nil {
		slog.Warn("failed to resolve post search localizations", "error", err)
	}
	authorMembers, err := s.loadPostAuthorMembers(ctx, collectPostIDs(posts))
	if err != nil {
		return nil, errs.Internal(err)
	}
	featuredDeliveries, err := s.loadPostFeaturedImageDeliveries(ctx, posts)
	if err != nil {
		return nil, errs.Internal(err)
	}

	summaries := make([]*openv1.PostSummary, 0, len(posts))
	for _, post := range posts {
		summaries = append(summaries, s.toPostSummary(&post, localizedSelections[post.ID], authorMembers[post.ID], featuredDeliveries[post.ID]))
	}

	return connect.NewResponse(&openv1.SearchPostsResponse{
		Posts: summaries,
	}), nil
}

// buildPostResponse builds a GetPostResponse with the post
func (s *PostService) buildPostResponse(
	ctx context.Context,
	acceptLanguage string,
	post *model.Post,
) (*connect.Response[openv1.GetPostResponse], error) {
	localization, err := s.localizer.ResolveSelectionWithPolicy(
		ctx,
		s.db,
		"post",
		post.ID,
		acceptLanguage,
		true,
	)
	if err != nil {
		slog.Warn("failed to resolve post localization", "postId", post.ID, "error", err)
	}
	localization, err = s.localizer.ResolveOgConsistency(
		ctx, s.db, s.cdnDomain, "post", post.ID, localization,
	)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if s.blocks == nil {
		return nil, errs.Internal(fmt.Errorf("post content Block store is not configured"))
	}
	document, revision, err := postdomain.LoadLocalizedPostContentProjectionForPublic(
		ctx,
		s.db,
		s.blocks,
		post.ID,
		localization.DisplayedLocale,
	)
	if err != nil {
		return nil, err
	}

	protoPost := &openv1.Post{
		Id:              post.ID,
		Title:           post.Title,
		Status:          s.toProtoStatus(post.Status),
		CommentsEnabled: post.CommentsEnabled,
		DocumentLayout:  post.DocumentLayout.Proto(),
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
	postSourceOgAssetID := post.OgAssetID
	if localization.OmitSourceOgFallback {
		postSourceOgAssetID = nil
	}
	protoPost.OgAsset, err = resolvedOgAssetRef(ctx, s.db, s.cdnDomain, postSourceOgAssetID, localization.OgAssetID)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if post.MapPlaceID != nil {
		protoPost.MapPlaceId = post.MapPlaceID
		protoPost.LocationPlace = s.mapPlaces.Basic(post.MapPlace)
	}

	applyPostLocalization(protoPost, localization)

	authorMembers, err := s.loadPostAuthorMembers(ctx, []string{post.ID})
	if err != nil {
		return nil, errs.Internal(err)
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
			return nil, errs.Internal(localizationErr)
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
		return nil, errs.Internal(fmt.Errorf("post media resolver is not configured"))
	}
	if post.ContentDocumentID == nil || *post.ContentDocumentID == "" {
		return nil, errs.FailedPrecondition("Post content document is not initialized")
	}
	documentID, err := uuid.Parse(*post.ContentDocumentID)
	if err != nil {
		return nil, errs.Internal(err)
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
	return connect.NewResponse(&openv1.GetPostResponse{
		Post:       protoPost,
		BlockMedia: contentMediaItems,
	}), nil
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
func (s *PostService) toPostSummary(
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

func (s *PostService) overlayPostSourceLocaleDocument(
	ctx context.Context,
	post *model.Post,
) error {
	state, err := postdomain.LoadPostSourceLocaleDocumentStateForPublic(ctx, s.db, post.ID)
	if err != nil {
		return err
	}
	if state == nil {
		return errs.NotFound("post_translation", post.ID)
	}
	postdomain.OverlayPostSourceLocaleDocumentForPublic(post, state)
	return nil
}

func (s *PostService) overlayPostSourceLocaleDocuments(
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
			return errs.NotFound("post_translation", posts[i].ID)
		}
		postdomain.OverlayPostSourceLocaleDocumentForPublic(&posts[i], sourceState)
	}
	return nil
}

// loadPostAuthorMembers resolves durable post authors and Member
// summaries in a fixed number of queries for any page size.
func (s *PostService) loadPostAuthorMembers(ctx context.Context, postIDs []string) (map[string][]*commonv1.MemberSummary, error) {
	ids := uniqueNonEmptyIDs(postIDs)
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

// toProtoStatus converts model status to proto status
func (s *PostService) toProtoStatus(status model.PostStatus) openv1.PostStatus {
	switch status {
	case model.PostStatus(managev1.PostStatus_POST_STATUS_DRAFT.String()):
		return openv1.PostStatus_POST_STATUS_DRAFT
	case model.PostStatus(managev1.PostStatus_POST_STATUS_PUBLISHED.String()):
		return openv1.PostStatus_POST_STATUS_PUBLISHED
	case model.PostStatus(managev1.PostStatus_POST_STATUS_SCHEDULED.String()):
		return openv1.PostStatus_POST_STATUS_SCHEDULED
	case model.PostStatus(managev1.PostStatus_POST_STATUS_ARCHIVED.String()):
		return openv1.PostStatus_POST_STATUS_ARCHIVED
	default:
		return openv1.PostStatus_POST_STATUS_UNSPECIFIED
	}
}
