package public

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"math"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mapcluster"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	workdomain "github.com/echovisionlab/geul-api/internal/work"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// WorkService implements the public WorkService.
type WorkService struct {
	openv1connect.UnimplementedWorkServiceHandler
	db         *gorm.DB
	spiceDB    *auth.SpiceDBClient
	blockMedia workdomain.ContentBlockMediaHydrator
	assets     MediaResolver
	members    MemberSummaryLoader
	mapPlaces  MapPlaceProjector
	blocks     *contentblock.Store
}

type WorkServiceOption func(*WorkService)

type MemberSummaryLoader interface {
	LoadPublicMemberSummaries(context.Context, []string) (map[string]*commonv1.MemberSummary, error)
}

type MapPlaceProjector interface {
	Basic(*model.MapPlace) *openv1.MapPlaceBasic
}

func WithWorkContentBlockStore(store *contentblock.Store) WorkServiceOption {
	return func(s *WorkService) {
		s.blocks = store
	}
}

type normalizedWorkMapViewport struct {
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

type workMapFeatureRow struct {
	WorkID       string  `gorm:"column:work_id"`
	WorkTitle    string  `gorm:"column:work_title"`
	WorkSlug     *string `gorm:"column:work_slug"`
	PlaceID      string  `gorm:"column:place_id"`
	PlaceName    string  `gorm:"column:place_name"`
	PlaceAddress string  `gorm:"column:place_address"`
	PlaceLat     float64 `gorm:"column:place_lat"`
	PlaceLng     float64 `gorm:"column:place_lng"`
}

type workMapPlaceGroup struct {
	PlaceID          string
	Name             string
	Address          string
	Lat              float64
	Lng              float64
	WorkCount        int32
	PrimaryWorkID    string
	PrimaryWorkSlug  *string
	PrimaryWorkTitle string
	Order            int
}

// NewWorkService creates a new public WorkService
func NewWorkService(
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	blockMedia workdomain.ContentBlockMediaHydrator,
	assets MediaResolver,
	members MemberSummaryLoader,
	mapPlaces MapPlaceProjector,
	options ...WorkServiceOption,
) *WorkService {
	if db == nil || spiceDB == nil || blockMedia == nil || assets == nil || members == nil || mapPlaces == nil {
		panic("public Work service dependencies are required")
	}
	service := &WorkService{
		db:         db,
		spiceDB:    spiceDB,
		blockMedia: blockMedia,
		assets:     assets,
		members:    members,
		mapPlaces:  mapPlaces,
	}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service
}

func isPublicWorkStatus(status string) bool {
	return status == managev1.WorkStatus_WORK_STATUS_PUBLISHED.String() ||
		status == managev1.WorkStatus_WORK_STATUS_ARCHIVED.String()
}

func publicWorkStatusValues() []string {
	return []string{
		managev1.WorkStatus_WORK_STATUS_PUBLISHED.String(),
		managev1.WorkStatus_WORK_STATUS_ARCHIVED.String(),
	}
}

func applyPublicWorkFilters(query *gorm.DB, filters []*commonv1.FilterSpec) (*gorm.DB, error) {
	query = query.Where("work.status IN ?", publicWorkStatusValues())
	return workFilterConfig.ApplyFilters(query, filters)
}

// Get retrieves a work by slug or ID
// - UUID-first: if input is valid UUID, query by ID; otherwise query by slug
// - share_token allows access to draft works
// - authenticated accounts with the exact SpiceDB view permission can access drafts
func (s *WorkService) Get(
	ctx context.Context,
	req *connect.Request[openv1.GetWorkRequest],
) (*connect.Response[openv1.GetWorkResponse], error) {
	slugOrID := req.Msg.Slug
	shareToken := req.Msg.ShareToken
	sharePassword := req.Msg.GetSharePassword()

	var work model.Work
	var err error

	// UUID-first approach
	if workdomain.IsValidUUID(slugOrID) {
		err = s.db.WithContext(ctx).Preload("MapPlace").First(&work, "id = ?", slugOrID).Error
	} else {
		err = s.db.WithContext(ctx).Preload("MapPlace").First(&work, "slug = ?", slugOrID).Error
	}

	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFoundMsg("work not found")
		}
		return nil, errs.Internal(err)
	}
	mediaAuthorization := mediaasset.ContentDownloadOwnerAuthorization{
		ResourceType: "work",
		ResourceID:   work.ID,
		Status:       work.Status,
		Mode:         mediaasset.ContentDownloadOwnerAccessPublic,
	}
	// Check access for draft works
	if !isPublicWorkStatus(work.Status) {
		allowed, permissionErr := hasDraftWorkView(ctx, s.spiceDB, work.ID)
		if permissionErr != nil {
			return nil, errs.Internal(fmt.Errorf("check work draft view permission: %w", permissionErr))
		}
		if allowed {
			mediaAuthorization.Mode = mediaasset.ContentDownloadOwnerAccessAuthenticatedDraft
			if user := auth.GetUser(ctx); user != nil {
				mediaAuthorization.IdentityID = user.IdentityID.String()
				mediaAuthorization.MemberID = user.MemberID.String()
			}
		} else {
			link, accessErr := requireDraftShareLinkAccess(
				ctx, s.db, optionalStringValue(shareToken), sharePassword,
				managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_WORK, work.ID, "work",
			)
			if accessErr != nil {
				return nil, accessErr
			}
			mediaAuthorization.Mode = mediaasset.ContentDownloadOwnerAccessShare
			mediaAuthorization.ShareLink = mediaasset.ContentDownloadShareLinkWitnessFromModel(link)
		}
	}

	return s.buildWorkResponse(ctx, req.Header().Get("Accept-Language"), &work, mediaAuthorization)
}

// List returns published and archived works.
func (s *WorkService) List(
	ctx context.Context,
	req *connect.Request[openv1.ListWorksRequest],
) (*connect.Response[openv1.ListWorksResponse], error) {
	var works []model.Work
	var total int64

	query := s.db.WithContext(ctx).Model(&model.Work{})

	// Apply filters using FilterConfig
	query, err := applyPublicWorkFilters(query, req.Msg.Filters)
	if err != nil {
		return nil, err
	}

	// Count total before pagination
	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}

	// Apply sorting using SortConfig
	query, err = WorkSortConfig.ApplySort(query, req.Msg.Sorts)
	if err != nil {
		return nil, err
	}

	// Apply pagination
	pagination := queryutil.GetPaginationParams(
		req.Msg.Pagination.GetLimit(),
		req.Msg.Pagination.GetOffset(),
		20,
	)
	query = queryutil.ApplyPagination(query, pagination)

	if err := query.Find(&works).Error; err != nil {
		return nil, errs.Internal(err)
	}
	if err := s.overlayRequiredSourceStatesForPublicWorks(ctx, works); err != nil {
		return nil, err
	}

	localizedSelections, err := publiccontent.ResolveBatch(
		ctx,
		s.db,
		workLocalizationSpec,
		collectWorkIDs(works),
		req.Header().Get("Accept-Language"),
	)
	if err != nil {
		slog.Warn("failed to resolve work summary localizations", "error", err)
	}

	// Convert to proto summaries
	summaries := make([]*openv1.WorkSummary, 0, len(works))
	for _, work := range works {
		summaries = append(summaries, s.toWorkSummary(ctx, &work, localizedSelections[work.ID]))
	}

	return connect.NewResponse(&openv1.ListWorksResponse{
		Works: summaries,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   pagination.Limit,
			Offset:  pagination.Offset,
			HasMore: pagination.Offset+int32(len(works)) < int32(total),
		},
	}), nil
}

// ListMapFeatures returns server-clustered map data for published and archived works in the current viewport.
func (s *WorkService) ListMapFeatures(
	ctx context.Context,
	req *connect.Request[openv1.ListWorkMapFeaturesRequest],
) (*connect.Response[openv1.ListWorkMapFeaturesResponse], error) {
	viewport, err := normalizeWorkMapViewport(req.Msg.Viewport)
	if err != nil {
		return nil, err
	}

	var rows []workMapFeatureRow
	query := s.db.WithContext(ctx).
		Model(&model.Work{}).
		Select(`
			work.id AS work_id,
			`+workdomain.WorkSourceTitleSQL("work")+` AS work_title,
			work.slug AS work_slug,
			map_place.id AS place_id,
			map_place.name AS place_name,
			map_place.address AS place_address,
			map_place.lat AS place_lat,
			map_place.lng AS place_lng
		`).
		Joins("JOIN map_place ON map_place.id = work.map_place_id").
		Where("map_place.lat BETWEEN ? AND ?", viewport.South, viewport.North)

	if viewport.FullLongitude {
		// World-wrapping viewport: longitude filter would incorrectly exclude visible places.
	} else if viewport.West <= viewport.East {
		query = query.Where("map_place.lng BETWEEN ? AND ?", viewport.West, viewport.East)
	} else {
		query = query.Where("(map_place.lng >= ? OR map_place.lng <= ?)", viewport.West, viewport.East)
	}

	query, err = applyPublicWorkFilters(query, req.Msg.Filters)
	if err != nil {
		return nil, err
	}

	query, err = WorkSortConfig.ApplySort(query, req.Msg.Sorts)
	if err != nil {
		return nil, err
	}

	if err := query.Find(&rows).Error; err != nil {
		return nil, errs.Internal(err)
	}
	if err := s.requireSourceRowsForPublicWorkMap(ctx, rows); err != nil {
		return nil, err
	}

	placeGroups := buildWorkMapPlaceGroups(rows)
	clusters, items := clusterWorkMapPlaceGroups(placeGroups, viewport)

	return connect.NewResponse(&openv1.ListWorkMapFeaturesResponse{
		Clusters: clusters,
		Items:    items,
	}), nil
}

// buildWorkResponse builds a GetWorkResponse with the work
func (s *WorkService) buildWorkResponse(
	ctx context.Context,
	acceptLanguage string,
	work *model.Work,
	mediaAuthorization mediaasset.ContentDownloadOwnerAuthorization,
) (*connect.Response[openv1.GetWorkResponse], error) {
	if s.blocks == nil {
		return nil, errs.InternalMsg("Work content Block store is not configured")
	}
	var localization publiccontent.Selection
	var document *contentv1.LocalizedRichTextDocument
	var revision string
	var blockMedia []*contentv1.ContentBlockMediaItem
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		documentID, loadErr := workdomain.LoadWorkContentDocumentIDForPublicRead(ctx, tx, work.ID)
		if loadErr != nil {
			return loadErr
		}
		mediaAuthorization.DocumentID = documentID.String()
		sourceState, loadErr := workdomain.LoadWorkSourceLocaleDocumentStateForPublic(ctx, tx, work.ID)
		if loadErr != nil {
			return loadErr
		}
		if sourceState == nil {
			return errs.InternalMsg("Work source locale is not initialized")
		}
		workdomain.OverlayWorkSourceLocaleDocumentForPublic(work, sourceState)
		localization, loadErr = publiccontent.Resolve(ctx, tx, workLocalizationSpec, work.ID, acceptLanguage)
		if loadErr != nil {
			return errs.Internal(loadErr)
		}
		localization, loadErr = publiccontent.ResolveOGConsistency(
			ctx, tx, workLocalizationSpec, work.ID, localization,
			func(ctx context.Context, assetID string) (bool, error) {
				return s.assets.IsReadyAsset(ctx, tx, assetID)
			},
		)
		if loadErr != nil {
			return errs.Internal(loadErr)
		}
		document, revision, blockMedia, loadErr =
			workdomain.LoadLocalizedWorkContentProjectionForPublic(
				ctx,
				tx,
				s.blocks,
				work.ID,
				documentID,
				localization.DisplayedLocale,
			)
		return loadErr
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	blockMedia, err = s.blockMedia.HydrateAuthorizedContentBlockMedia(
		mediaasset.WithContentDownloadOwnerAuthorization(ctx, mediaAuthorization), blockMedia,
	)
	if err != nil {
		return nil, err
	}

	protoWork := &openv1.Work{
		Id:        work.ID,
		Title:     work.Title,
		Type:      openv1.WorkType(openv1.WorkType_value[work.Type]),
		Year:      work.Year,
		Month:     work.Month,
		IsPresent: work.IsPresent,
		Featured:  work.Featured,
		Status:    openv1.WorkStatus(openv1.WorkStatus_value[work.Status]),
		CreatedAt: timestamppb.New(work.CreatedAt),
		UpdatedAt: timestamppb.New(work.UpdatedAt),
		Document:  document,
		Revision:  revision,
	}

	if work.Slug != nil {
		protoWork.Slug = work.Slug
	}
	if work.Summary != nil {
		protoWork.Summary = work.Summary
	}
	if localization.Title != nil {
		protoWork.Title = *localization.Title
	}
	protoWork.LocalizationInfo = publiccontent.ToProtoLocalizationInfo(localization)
	if work.PublishedAt != nil {
		protoWork.PublishedAt = timestamppb.New(*work.PublishedAt)
	}
	if work.MapPlaceID != nil {
		protoWork.MapPlaceId = work.MapPlaceID
		protoWork.LocationPlace = s.mapPlaces.Basic(work.MapPlace)
	}
	if work.UntilYear != nil {
		protoWork.UntilYear = work.UntilYear
	}
	if work.UntilMonth != nil {
		protoWork.UntilMonth = work.UntilMonth
	}
	workSourceOgAssetID := work.OgAssetID
	if localization.OmitSourceOgFallback {
		workSourceOgAssetID = nil
	}
	protoWork.OgAsset, err = s.assets.ResolveReadyOGAsset(ctx, workSourceOgAssetID, localization.OgAssetID)
	if err != nil {
		return nil, errs.Internal(err)
	}

	if imageAsset := s.getWorkFeaturedImageAsset(ctx, work.ID); imageAsset != nil {
		protoWork.FeaturedImageAsset = imageAsset
	}

	// Metadata
	if work.Metadata != nil {
		metadata, err := structpb.NewStruct(work.Metadata)
		if err == nil {
			protoWork.Metadata = metadata
		}
	}

	if localization.Summary != nil {
		protoWork.Summary = localization.Summary
	}

	// OG image key
	// Get credit groups and credits with artist/user details
	protoWork.CreditGroups = s.getWorkCreditGroups(ctx, work.ID)
	// Get credits with artist/Member details.
	protoWork.Credits, err = s.getWorkCredits(ctx, work.ID)
	if err != nil {
		return nil, errs.Internal(err)
	}

	// Get clients
	protoWork.Clients = s.getWorkClients(ctx, work.ID)

	return connect.NewResponse(&openv1.GetWorkResponse{
		Work:       protoWork,
		BlockMedia: blockMedia,
	}), nil
}

// toWorkSummary converts a work to a summary (no content)
func (s *WorkService) toWorkSummary(
	ctx context.Context,
	work *model.Work,
	localization publiccontent.Selection,
) *openv1.WorkSummary {
	summary := &openv1.WorkSummary{
		Id:        work.ID,
		Title:     work.Title,
		Type:      openv1.WorkType(openv1.WorkType_value[work.Type]),
		Year:      work.Year,
		Month:     work.Month,
		IsPresent: work.IsPresent,
		Featured:  work.Featured,
		Status:    openv1.WorkStatus(openv1.WorkStatus_value[work.Status]),
	}

	if work.Slug != nil {
		summary.Slug = work.Slug
	}
	if work.Summary != nil {
		summary.Summary = work.Summary
	}
	if localization.Summary != nil {
		summary.Summary = localization.Summary
	}
	if localization.Title != nil {
		summary.Title = *localization.Title
	}
	if work.PublishedAt != nil {
		summary.PublishedAt = timestamppb.New(*work.PublishedAt)
	}
	if work.MapPlaceID != nil {
		summary.MapPlaceId = work.MapPlaceID
	}
	if work.UntilYear != nil {
		summary.UntilYear = work.UntilYear
	}
	if work.UntilMonth != nil {
		summary.UntilMonth = work.UntilMonth
	}

	if imageAsset := s.getWorkFeaturedImageAsset(ctx, work.ID); imageAsset != nil {
		summary.FeaturedImageAsset = imageAsset
	}
	return summary
}

func collectWorkIDs(works []model.Work) []string {
	ids := make([]string, 0, len(works))
	for _, work := range works {
		ids = append(ids, work.ID)
	}
	return ids
}

func (s *WorkService) overlayRequiredSourceStatesForPublicWorks(
	ctx context.Context,
	works []model.Work,
) error {
	if len(works) == 0 {
		return nil
	}

	sourceStates, err := workdomain.LoadWorkSourceLocaleDocumentStatesForPublic(ctx, s.db, collectWorkIDs(works))
	if err != nil {
		return err
	}
	for i := range works {
		sourceState := sourceStates[works[i].ID]
		if sourceState == nil {
			slog.Error("missing work source locale row for public list item", "workId", works[i].ID)
			return errs.InternalMsg("internal server error")
		}
		workdomain.OverlayWorkSourceLocaleDocumentForPublic(&works[i], sourceState)
	}
	return nil
}

func (s *WorkService) requireSourceRowsForPublicWorkMap(
	ctx context.Context,
	rows []workMapFeatureRow,
) error {
	if len(rows) == 0 {
		return nil
	}

	workIDs := collectWorkMapIDs(rows)
	sourceStates, err := workdomain.LoadWorkSourceLocaleDocumentStatesForPublic(ctx, s.db, workIDs)
	if err != nil {
		return err
	}
	for _, workID := range workIDs {
		if sourceStates[workID] == nil {
			slog.Error("missing work source locale row for public map item", "workId", workID)
			return errs.InternalMsg("internal server error")
		}
	}
	return nil
}

func normalizeWorkMapViewport(viewport *openv1.WorkMapViewport) (normalizedWorkMapViewport, error) {
	if viewport == nil || viewport.Bounds == nil {
		return normalizedWorkMapViewport{}, errs.InvalidArgument("viewport", "viewport bounds are required")
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

	return normalizedWorkMapViewport{
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

func buildWorkMapPlaceGroups(
	rows []workMapFeatureRow,
) []*workMapPlaceGroup {
	placeByID := make(map[string]*workMapPlaceGroup, len(rows))
	ordered := make([]*workMapPlaceGroup, 0, len(rows))

	for idx, row := range rows {
		group, ok := placeByID[row.PlaceID]
		if !ok {
			group = &workMapPlaceGroup{
				PlaceID:          row.PlaceID,
				Name:             row.PlaceName,
				Address:          row.PlaceAddress,
				Lat:              row.PlaceLat,
				Lng:              row.PlaceLng,
				PrimaryWorkID:    row.WorkID,
				PrimaryWorkSlug:  row.WorkSlug,
				PrimaryWorkTitle: row.WorkTitle,
				Order:            idx,
			}
			placeByID[row.PlaceID] = group
			ordered = append(ordered, group)
		}
		group.WorkCount++
	}

	return ordered
}

func collectWorkMapIDs(rows []workMapFeatureRow) []string {
	ids := make([]string, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if _, ok := seen[row.WorkID]; ok {
			continue
		}
		seen[row.WorkID] = struct{}{}
		ids = append(ids, row.WorkID)
	}
	return ids
}

func clusterWorkMapPlaceGroups(
	placeGroups []*workMapPlaceGroup,
	viewport normalizedWorkMapViewport,
) ([]*openv1.WorkMapCluster, []*openv1.WorkMapItem) {
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
		func(group *workMapPlaceGroup) (float64, float64) { return group.Lat, group.Lng },
		func(group *workMapPlaceGroup) int32 { return group.WorkCount },
	)

	clusters := make([]*openv1.WorkMapCluster, 0, len(components))
	items := make([]*openv1.WorkMapItem, 0, len(placeGroups))
	for index, component := range components {
		if component.ShouldRenderItems(parameters) {
			for _, group := range component.Groups {
				items = append(items, workMapItem(group))
			}
			continue
		}
		clusters = append(clusters, &openv1.WorkMapCluster{
			Id:              fmt.Sprintf("cluster-%d", index+1),
			Lat:             component.Lat,
			Lng:             component.Lng,
			PlaceCount:      int32(len(component.Groups)),
			WorkCount:       component.Count,
			MinBreakoutZoom: component.MinBreakoutZoom,
			Bounds: &openv1.WorkMapBounds{
				West: component.West, South: component.South,
				East: component.East, North: component.North,
			},
		})
	}
	return clusters, items
}

func workMapItem(group *workMapPlaceGroup) *openv1.WorkMapItem {
	return &openv1.WorkMapItem{
		PlaceId:          group.PlaceID,
		Name:             group.Name,
		Address:          group.Address,
		Lat:              group.Lat,
		Lng:              group.Lng,
		WorkCount:        group.WorkCount,
		PrimaryWorkId:    group.PrimaryWorkID,
		PrimaryWorkSlug:  group.PrimaryWorkSlug,
		PrimaryWorkTitle: group.PrimaryWorkTitle,
	}
}

func (s *WorkService) getWorkCreditGroups(ctx context.Context, workID string) []*openv1.WorkCreditGroup {
	var groups []model.WorkCreditGroup
	if err := s.db.WithContext(ctx).
		Where("work_id = ?", workID).
		Order("sort_order ASC").
		Order("id ASC").
		Find(&groups).Error; err != nil {
		return nil
	}

	protoGroups := make([]*openv1.WorkCreditGroup, 0, len(groups))
	for _, group := range groups {
		protoGroups = append(protoGroups, &openv1.WorkCreditGroup{
			Id:   group.ID,
			Name: group.Name,
		})
	}

	return protoGroups
}

// getWorkCredits gets work credits with artist/Member details.
func (s *WorkService) getWorkCredits(ctx context.Context, workID string) ([]*openv1.WorkCredit, error) {
	var credits []model.WorkCredit
	if err := s.db.WithContext(ctx).
		Where("work_id = ?", workID).
		Order("sort_order ASC").
		Find(&credits).Error; err != nil {
		return nil, err
	}
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

	protoCredits := make([]*openv1.WorkCredit, 0, len(credits))
	for _, credit := range credits {
		pc := &openv1.WorkCredit{
			Id: credit.ID,
		}

		if credit.GroupID != nil {
			pc.GroupId = credit.GroupID
		}
		if credit.Name != nil {
			pc.Name = credit.Name
		}
		if credit.CreditRole != nil {
			pc.CreditRole = credit.CreditRole
		}

		if credit.ArtistID != nil {
			pc.Artist = s.loadPublicCreditArtist(ctx, *credit.ArtistID)
		}

		if credit.MemberID != nil {
			pc.Member = memberSummaries[*credit.MemberID]
		}

		protoCredits = append(protoCredits, pc)
	}

	return protoCredits, nil
}

func (s *WorkService) loadPublicCreditArtist(ctx context.Context, artistID string) *openv1.CreditArtist {
	var artist struct {
		ID   string  `gorm:"column:id"`
		Name string  `gorm:"column:name"`
		Slug *string `gorm:"column:slug"`
	}
	if err := s.db.WithContext(ctx).
		Table("artist").
		Select("artist.id, "+workdomain.ArtistSourceTitleSQL("artist")+" AS name, artist.slug").
		Where("artist.id = ?", artistID).
		Scan(&artist).Error; err != nil || artist.ID == "" {
		return nil
	}
	result := &openv1.CreditArtist{Id: artist.ID, Name: artist.Name, Slug: artist.Slug}
	result.ImageAsset = s.getArtistImageAsset(ctx, artist.ID)
	return result
}

func (s *WorkService) getWorkFeaturedImageAsset(ctx context.Context, workID string) *commonv1.AssetRef {
	var result struct {
		FileID *string `gorm:"column:file_id"`
	}

	err := s.db.WithContext(ctx).
		Table("work").
		Select("work.featured_image_file_id AS file_id").
		Where("work.id = ?", workID).
		Scan(&result).Error

	if err != nil || result.FileID == nil {
		return nil
	}
	return s.assets.ResolveReadyAssetForSourceFile(ctx, *result.FileID, "image")
}

func (s *WorkService) getArtistImageAsset(ctx context.Context, artistID string) *commonv1.AssetRef {
	return s.assets.ResolveArtistImageAsset(ctx, artistID)
}

// getWorkClients fetches clients associated with a work
func (s *WorkService) getWorkClients(ctx context.Context, workID string) []*openv1.WorkClient {
	type clientRow struct {
		ID          string
		Name        string
		Website     *string
		LightFileID *string `gorm:"column:light_file_id"`
		DarkFileID  *string `gorm:"column:dark_file_id"`
	}

	var rows []clientRow
	err := s.db.WithContext(ctx).
		Table("work_client wc").
		Select("c.id, c.name, c.website, c.logo_light_file_id AS light_file_id, c.logo_dark_file_id AS dark_file_id").
		Joins("JOIN client c ON c.id = wc.client_id").
		Where("wc.work_id = ?", workID).
		Order("wc.sort_order ASC").
		Scan(&rows).Error

	if err != nil || len(rows) == 0 {
		return nil
	}

	clients := make([]*openv1.WorkClient, 0, len(rows))
	for _, row := range rows {
		client := &openv1.WorkClient{
			Id:   row.ID,
			Name: row.Name,
		}
		if row.Website != nil {
			client.Website = row.Website
		}
		if row.LightFileID != nil {
			client.LogoLightAsset = s.assets.ResolveReadyAssetForSourceFile(ctx, *row.LightFileID, "logo")
		}
		if row.DarkFileID != nil {
			client.LogoDarkAsset = s.assets.ResolveReadyAssetForSourceFile(ctx, *row.DarkFileID, "logo")
		}
		clients = append(clients, client)
	}

	return clients
}
