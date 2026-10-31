package public

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"

	"google.golang.org/protobuf/types/known/structpb"
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
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	workdomain "github.com/echovisionlab/geul-api/internal/work"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Service holds the public Work reads.
type Service struct {
	db         *gorm.DB
	spiceDB    *auth.SpiceDBClient
	blockMedia workdomain.ContentBlockMediaHydrator
	assets     MediaResolver
	members    MemberSummaryLoader
	mapPlaces  MapPlaceProjector
	blocks     *contentblock.Store
}

type MemberSummaryLoader interface {
	LoadPublicMemberSummaries(context.Context, []string) (map[string]*commonv1.MemberSummary, error)
}

type MapPlaceProjector interface {
	Basic(*model.MapPlace) *openv1.MapPlaceBasic
}

// Deps are the collaborators of the public Work reads. Every field is
// required; NewService rejects a missing one.
type Deps struct {
	DB            *gorm.DB
	SpiceDB       *auth.SpiceDBClient
	BlockMedia    workdomain.ContentBlockMediaHydrator
	Assets        MediaResolver
	Members       MemberSummaryLoader
	MapPlaces     MapPlaceProjector
	ContentBlocks *contentblock.Store
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"DB": d.DB == nil, "SpiceDB": d.SpiceDB == nil, "BlockMedia": d.BlockMedia == nil, "Assets": d.Assets == nil,
		"Members": d.Members == nil, "MapPlaces": d.MapPlaces == nil, "ContentBlocks": d.ContentBlocks == nil,
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

// NewService validates deps and returns the public Work reads.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	return &Service{
		db: deps.DB, spiceDB: deps.SpiceDB, blockMedia: deps.BlockMedia, assets: deps.Assets,
		members: deps.Members, mapPlaces: deps.MapPlaces, blocks: deps.ContentBlocks,
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
	Viewport *openv1.WorkMapViewport
	Filters  []*commonv1.FilterSpec
	Sorts    []*commonv1.SortSpec
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

type workPublicClientRow struct {
	ID          string
	Name        string
	Website     *string
	LightFileID *string `gorm:"column:light_file_id"`
	DarkFileID  *string `gorm:"column:dark_file_id"`
}

type workPublicRelationsSnapshot struct {
	CreditGroups []model.WorkCreditGroup
	Credits      []model.WorkCredit
	Clients      []workPublicClientRow
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

// List returns published and archived works.
func (s *Service) List(ctx context.Context, request ListWorksQuery) (*openv1.ListWorksResponse, error) {
	var works []model.Work
	var total int64

	query := s.db.WithContext(ctx).Model(&model.Work{})

	// Apply filters using FilterConfig
	query, err := applyPublicWorkFilters(query, request.Filters)
	if err != nil {
		return nil, err
	}

	// Count total before pagination
	if err := query.Count(&total).Error; err != nil {
		return nil, err
	}

	// Apply sorting using SortConfig
	query, err = WorkSortConfig.ApplySort(query, request.Sorts)
	if err != nil {
		return nil, err
	}

	// Apply pagination
	pagination := queryutil.GetPaginationParams(request.Limit, request.Offset, 20)
	query = queryutil.ApplyPagination(query, pagination)

	if err := query.Find(&works).Error; err != nil {
		return nil, err
	}
	if err := s.overlayRequiredSourceStatesForPublicWorks(ctx, works); err != nil {
		return nil, err
	}

	localizedSelections, err := publiccontent.ResolveBatch(
		ctx,
		s.db,
		workLocalizationSpec,
		collectWorkIDs(works),
		request.AcceptLanguage,
	)
	if err != nil {
		slog.ErrorContext(ctx, "Work summary localization lookup failed; listing works without localization", "domain", "work", "operation", "resolve_summary_localizations", failurecause.Attr(err))
	}

	// Convert to proto summaries
	summaries := make([]*openv1.WorkSummary, 0, len(works))
	for _, work := range works {
		summaries = append(summaries, s.toWorkSummary(ctx, &work, localizedSelections[work.ID]))
	}

	return &openv1.ListWorksResponse{
		Works: summaries,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   pagination.Limit,
			Offset:  pagination.Offset,
			HasMore: pagination.Offset+int32(len(works)) < int32(total),
		},
	}, nil
}

// ListMapFeatures returns server-clustered map data for published and archived works in the current viewport.
func (s *Service) ListMapFeatures(ctx context.Context, request ListWorkMapFeaturesQuery) (*openv1.ListWorkMapFeaturesResponse, error) {
	viewport, err := normalizeWorkMapViewport(request.Viewport)
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

	query, err = applyPublicWorkFilters(query, request.Filters)
	if err != nil {
		return nil, err
	}

	query, err = WorkSortConfig.ApplySort(query, request.Sorts)
	if err != nil {
		return nil, err
	}

	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	if err := s.requireSourceRowsForPublicWorkMap(ctx, rows); err != nil {
		return nil, err
	}

	placeGroups := buildWorkMapPlaceGroups(rows)
	clusters, items := clusterWorkMapPlaceGroups(placeGroups, viewport)

	return &openv1.ListWorkMapFeaturesResponse{
		Clusters: clusters,
		Items:    items,
	}, nil
}

// publicWorkSnapshot is a Work and its relations read in one snapshot.
type publicWorkSnapshot struct {
	work               model.Work
	mediaAuthorization mediaasset.ContentDownloadOwnerAuthorization
	localization       publiccontent.Selection
	document           *contentv1.LocalizedRichTextDocument
	revision           string
	blockMedia         []*contentv1.ContentBlockMediaItem
	relations          workPublicRelationsSnapshot
}

// Get retrieves a work by slug or ID
// - UUID-first: if input is valid UUID, query by ID; otherwise query by slug
// - share_token allows access to draft works
// - authenticated accounts with the exact SpiceDB view permission can access drafts
func (s *Service) Get(ctx context.Context, query GetWorkQuery) (*openv1.GetWorkResponse, error) {
	var snapshot publicWorkSnapshot
	if err := mutation.LockedRead(ctx, s.db, func(tx *gorm.DB) error {
		var err error
		snapshot, err = s.loadPublicWorkSnapshot(ctx, tx, query)
		return err
	}); err != nil {
		return nil, err
	}
	blockMedia, err := s.blockMedia.HydrateAuthorizedContentBlockMedia(
		mediaasset.WithContentDownloadOwnerAuthorization(ctx, snapshot.mediaAuthorization), snapshot.blockMedia,
	)
	if err != nil {
		return nil, err
	}
	protoWork, err := s.projectPublicWork(ctx, snapshot)
	if err != nil {
		return nil, err
	}
	return &openv1.GetWorkResponse{Work: protoWork, BlockMedia: blockMedia}, nil
}

func (s *Service) loadPublicWorkSnapshot(ctx context.Context, tx *gorm.DB, query GetWorkQuery) (publicWorkSnapshot, error) {
	var snapshot publicWorkSnapshot
	work := &snapshot.work
	stored := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).Preload("MapPlace")
	var err error
	if workdomain.IsValidUUID(query.Slug) {
		err = stored.First(work, "id = ?", query.Slug).Error
	} else {
		err = stored.First(work, "slug = ?", query.Slug).Error
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return snapshot, domainerr.New(domainerr.NotFound, "", "work not found")
	}
	if err != nil {
		return snapshot, err
	}
	if snapshot.mediaAuthorization, err = s.authorizePublicWork(ctx, tx, *work, query); err != nil {
		return snapshot, err
	}
	documentID, err := workdomain.LoadWorkContentDocumentIDForPublicRead(ctx, tx, work.ID)
	if err != nil {
		return snapshot, err
	}
	snapshot.mediaAuthorization.DocumentID = documentID.String()
	sourceState, err := workdomain.LoadWorkSourceLocaleDocumentStateForPublic(ctx, tx, work.ID)
	if err != nil {
		return snapshot, err
	}
	if sourceState == nil {
		return snapshot, errors.New("Work source locale is not initialized")
	}
	workdomain.OverlayWorkSourceLocaleDocumentForPublic(work, sourceState)
	if snapshot.localization, err = publiccontent.Resolve(ctx, tx, workLocalizationSpec, work.ID, query.AcceptLanguage); err != nil {
		return snapshot, err
	}
	snapshot.localization, err = publiccontent.ResolveOGConsistency(
		ctx, tx, workLocalizationSpec, work.ID, snapshot.localization,
		func(ctx context.Context, assetID string) (bool, error) {
			return s.assets.IsReadyAsset(ctx, tx, assetID)
		},
	)
	if err != nil {
		return snapshot, err
	}
	snapshot.document, snapshot.revision, snapshot.blockMedia, err = workdomain.LoadLocalizedWorkContentProjectionForPublic(
		ctx, tx, s.blocks, work.ID, documentID, snapshot.localization.DisplayedLocale,
	)
	if err != nil {
		return snapshot, err
	}
	snapshot.relations, err = s.loadWorkPublicRelationsSnapshot(ctx, tx, work)
	return snapshot, err
}

// authorizePublicWork returns how the reader may download the Work's
// media: as the public, as a viewer of the draft, or through its share link.
func (s *Service) authorizePublicWork(
	ctx context.Context,
	tx *gorm.DB,
	work model.Work,
	query GetWorkQuery,
) (mediaasset.ContentDownloadOwnerAuthorization, error) {
	authorization := mediaasset.ContentDownloadOwnerAuthorization{
		ResourceType: "work",
		ResourceID:   work.ID,
		Status:       work.Status,
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
	link, err := requireDraftShareLinkAccess(
		ctx, tx, query.ShareToken, query.SharePassword,
		managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_WORK, work.ID, "work",
	)
	if err != nil {
		return authorization, err
	}
	authorization.Mode = mediaasset.ContentDownloadOwnerAccessShare
	authorization.ShareLink = mediaasset.ContentDownloadShareLinkWitnessFromModel(link)
	return authorization, nil
}

func (s *Service) projectPublicWork(ctx context.Context, snapshot publicWorkSnapshot) (*openv1.Work, error) {
	work, localization := &snapshot.work, snapshot.localization
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
		Document:  snapshot.document,
		Revision:  snapshot.revision,
		Slug:      work.Slug,
		Summary:   work.Summary,
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
	protoWork.UntilYear, protoWork.UntilMonth = work.UntilYear, work.UntilMonth
	workSourceOgAssetID := work.OgAssetID
	if localization.OmitSourceOgFallback {
		workSourceOgAssetID = nil
	}
	var err error
	if protoWork.OgAsset, err = s.assets.ResolveReadyOGAsset(ctx, workSourceOgAssetID, localization.OgAssetID); err != nil {
		return nil, err
	}
	protoWork.FeaturedImageAsset = s.getWorkFeaturedImageAssetForSourceFile(ctx, work.FeaturedImageFileID)
	protoWork.Metadata = publicWorkMetadata(ctx, work)
	if localization.Summary != nil {
		protoWork.Summary = localization.Summary
	}
	protoWork.CreditGroups = workCreditGroupsToProto(snapshot.relations.CreditGroups)
	if protoWork.Credits, err = s.getWorkCredits(ctx, snapshot.relations.Credits); err != nil {
		return nil, err
	}
	protoWork.Clients = s.getWorkClients(ctx, snapshot.relations.Clients)
	return protoWork, nil
}

// publicWorkMetadata is the Work's metadata, or none when it cannot be
// converted.
func publicWorkMetadata(ctx context.Context, work *model.Work) *structpb.Struct {
	if work.Metadata == nil {
		return nil
	}
	metadata, err := structpb.NewStruct(work.Metadata)
	if err != nil {
		slog.ErrorContext(ctx, "Public Work metadata conversion failed; serving work without metadata", "domain", "work", "operation", "convert_public_work_metadata", failurecause.Attr(err), "work_id", work.ID)
		return nil
	}
	return metadata
}

// toWorkSummary converts a work to a summary (no content)
func (s *Service) toWorkSummary(
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

	if imageAsset := s.getWorkFeaturedImageAssetForSourceFile(ctx, work.FeaturedImageFileID); imageAsset != nil {
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

func (s *Service) overlayRequiredSourceStatesForPublicWorks(
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
			return fmt.Errorf("missing work source locale row for public list item %s", works[i].ID)
		}
		workdomain.OverlayWorkSourceLocaleDocumentForPublic(&works[i], sourceState)
	}
	return nil
}

func (s *Service) requireSourceRowsForPublicWorkMap(
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
			return fmt.Errorf("missing work source locale row for public map item %s", workID)
		}
	}
	return nil
}

func normalizeWorkMapViewport(viewport *openv1.WorkMapViewport) (normalizedWorkMapViewport, error) {
	if viewport == nil || viewport.Bounds == nil {
		return normalizedWorkMapViewport{}, domainerr.InvalidField("viewport", "viewport bounds are required")
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
		return normalizedWorkMapViewport{}, domainerr.InvalidField("viewport.zoom", "zoom must be finite")
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

func workCreditGroupsToProto(groups []model.WorkCreditGroup) []*openv1.WorkCreditGroup {
	protoGroups := make([]*openv1.WorkCreditGroup, 0, len(groups))
	for _, group := range groups {
		protoGroups = append(protoGroups, &openv1.WorkCreditGroup{
			Id:   group.ID,
			Name: group.Name,
		})
	}

	return protoGroups
}

func (s *Service) loadWorkPublicRelationsSnapshot(
	ctx context.Context,
	tx *gorm.DB,
	work *model.Work,
) (workPublicRelationsSnapshot, error) {
	snapshot := workPublicRelationsSnapshot{}
	// These two legacy projections intentionally suppress read errors. Isolate
	// them with savepoints so a failed optional read does not poison the RR tx.
	if err := optionalRead(tx, "work_credit_groups", func(savepoint *gorm.DB) error {
		return savepoint.WithContext(ctx).
			Where("work_id = ?", work.ID).
			Order("sort_order ASC").
			Order("id ASC").
			Find(&snapshot.CreditGroups).Error
	}); err != nil {
		slog.ErrorContext(ctx, "Public Work credit group lookup failed; serving work without credit groups", "domain", "work", "operation", "load_credit_groups", failurecause.Attr(err), "work_id", work.ID)
	}

	if err := tx.WithContext(ctx).
		Where("work_id = ?", work.ID).
		Order("sort_order ASC").
		Find(&snapshot.Credits).Error; err != nil {
		return workPublicRelationsSnapshot{}, err
	}

	if err := optionalRead(tx, "work_clients", func(savepoint *gorm.DB) error {
		return savepoint.WithContext(ctx).
			Table("work_client wc").
			Select("c.id, c.name, c.website, c.logo_light_file_id AS light_file_id, c.logo_dark_file_id AS dark_file_id").
			Joins("JOIN client c ON c.id = wc.client_id").
			Where("wc.work_id = ?", work.ID).
			Order("wc.sort_order ASC").
			Scan(&snapshot.Clients).Error
	}); err != nil {
		slog.ErrorContext(ctx, "Public Work client lookup failed; serving work without clients", "domain", "work", "operation", "load_clients", failurecause.Attr(err), "work_id", work.ID)
	}

	return snapshot, nil
}

// optionalRead runs read in a savepoint, so a failed optional read leaves
// the snapshot usable.
func optionalRead(tx *gorm.DB, name string, read func(*gorm.DB) error) error {
	if err := tx.SavePoint(name).Error; err != nil {
		return err
	}
	if err := read(tx); err != nil {
		return errors.Join(err, tx.RollbackTo(name).Error)
	}
	return nil
}

// getWorkCredits gets work credits with artist/Member details.
func (s *Service) getWorkCredits(ctx context.Context, credits []model.WorkCredit) ([]*openv1.WorkCredit, error) {
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

func (s *Service) loadPublicCreditArtist(ctx context.Context, artistID string) *openv1.CreditArtist {
	var artist struct {
		ID   string  `gorm:"column:id"`
		Name string  `gorm:"column:name"`
		Slug *string `gorm:"column:slug"`
	}
	if err := s.db.WithContext(ctx).
		Table("artist").
		Select("artist.id, "+workdomain.ArtistSourceTitleSQL("artist")+" AS name, artist.slug").
		Where("artist.id = ?", artistID).
		Scan(&artist).Error; err != nil {
		slog.ErrorContext(ctx, "Public Work credit artist lookup failed; serving credit without artist", "domain", "work", "operation", "load_credit_artist", failurecause.Attr(err), "artist_id", artistID)
		return nil
	}
	if artist.ID == "" {
		return nil
	}
	result := &openv1.CreditArtist{Id: artist.ID, Name: artist.Name, Slug: artist.Slug}
	result.ImageAsset = s.getArtistImageAsset(ctx, artist.ID)
	return result
}

func (s *Service) getWorkFeaturedImageAssetForSourceFile(
	ctx context.Context,
	sourceFileID *string,
) *commonv1.AssetRef {
	if sourceFileID == nil {
		return nil
	}
	return s.assets.ResolveReadyAssetForSourceFile(ctx, *sourceFileID, "image")
}

func (s *Service) getArtistImageAsset(ctx context.Context, artistID string) *commonv1.AssetRef {
	return s.assets.ResolveArtistImageAsset(ctx, artistID)
}

// getWorkClients fetches clients associated with a work
func (s *Service) getWorkClients(ctx context.Context, rows []workPublicClientRow) []*openv1.WorkClient {
	if len(rows) == 0 {
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
