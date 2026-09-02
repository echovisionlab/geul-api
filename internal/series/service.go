package series

import (
	"context"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// SeriesService implements the SeriesService Connect handler
type SeriesService struct {
	managev1connect.UnimplementedSeriesServiceHandler
	db           *gorm.DB
	spiceDB      *auth.SpiceDBClient
	permissions  seriesPermissionChecker
	kratosClient auth.IdentityManager
	menuTargets  MenuTargets
	postAccess   PostAccess
	members      MemberSummaries
	auditWriter  domainaudit.Appender
	media        MediaRuntime
	ogRefresh    OGRefresh
	reads        SeriesReadProjection
}

func NewAuditedSeriesService(
	db *gorm.DB,
	media MediaRuntime,
	ogRefresh OGRefresh,
	reads SeriesReadProjection,
	spiceDB *auth.SpiceDBClient,
	kratosClient auth.IdentityManager,
	menuTargets MenuTargets,
	postAccess PostAccess,
	members MemberSummaries,
	auditWriter domainaudit.Appender,
) *SeriesService {
	if auditWriter == nil {
		panic("series audit writer is required")
	}
	service := NewSeriesService(db, media, ogRefresh, reads, spiceDB, kratosClient, menuTargets, postAccess, members)
	service.auditWriter = auditWriter
	return service
}

// NewSeriesService creates a new SeriesService
func NewSeriesService(
	db *gorm.DB,
	media MediaRuntime,
	ogRefresh OGRefresh,
	reads SeriesReadProjection,
	spiceDB *auth.SpiceDBClient,
	kratosClient auth.IdentityManager,
	menuTargets MenuTargets,
	postAccess PostAccess,
	members MemberSummaries,
) *SeriesService {
	if db == nil {
		panic("SeriesService: db is required")
	}
	if media == nil || ogRefresh == nil || reads == nil {
		panic("SeriesService: media, OG, and read projection dependencies are required")
	}
	if spiceDB == nil {
		panic("SeriesService: spiceDB is required")
	}
	if kratosClient == nil {
		panic("SeriesService: kratosClient is required")
	}
	if menuTargets == nil || postAccess == nil || members == nil {
		panic("SeriesService: domain dependencies are required")
	}
	return &SeriesService{
		db:           db,
		media:        media,
		ogRefresh:    ogRefresh,
		reads:        reads,
		spiceDB:      spiceDB,
		permissions:  spiceDB,
		kratosClient: kratosClient,
		menuTargets:  menuTargets,
		postAccess:   postAccess,
		members:      members,
	}
}

// ListSeriesPosts returns the full ordered Post set to an exact Manager/Admin.
// Public readers use the open Post list, which exposes only public lifecycle states.
func (s *SeriesService) ListSeriesPosts(
	ctx context.Context,
	req *connect.Request[managev1.ListSeriesPostsRequest],
) (*connect.Response[managev1.ListSeriesPostsResponse], error) {
	type postResult struct {
		ID          string
		Title       string
		Slug        *string
		Status      string
		SeriesOrder *int
		PublishedAt *time.Time
	}

	var posts []postResult
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.requireSeriesViewAndLock(ctx, tx, req.Msg.SeriesId); err != nil {
			return err
		}
		return tx.WithContext(ctx).
			Table("post").
			Select("id, "+s.postAccess.PostSourceTitleSQL()+" AS title, slug, status, series_order, published_at").
			Where("series_id = ?", req.Msg.SeriesId).
			Order("series_order ASC NULLS LAST, created_at ASC").
			Find(&posts).Error
	}); err != nil {
		if _, ok := err.(*connect.Error); ok {
			return nil, err
		}
		return nil, errs.Internal(err)
	}

	// Convert to proto
	protoPosts := make([]*managev1.SeriesPost, len(posts))
	for i, post := range posts {
		seriesOrder := int32(0)
		if post.SeriesOrder != nil {
			seriesOrder = int32(*post.SeriesOrder)
		}
		slug := ""
		if post.Slug != nil {
			slug = *post.Slug
		}
		protoPosts[i] = &managev1.SeriesPost{
			Id:          post.ID,
			Title:       post.Title,
			Slug:        slug,
			Status:      post.Status,
			SeriesOrder: seriesOrder,
		}
		if post.PublishedAt != nil {
			protoPosts[i].PublishedAt = timestamppb.New(*post.PublishedAt)
		}
	}

	return connect.NewResponse(&managev1.ListSeriesPostsResponse{
		Posts: protoPosts,
	}), nil
}

// ListMySeries returns series where current user is a member
func (s *SeriesService) ListMySeries(
	ctx context.Context,
	req *connect.Request[managev1.ListMySeriesRequest],
) (*connect.Response[managev1.ListMySeriesResponse], error) {
	seriesIDs, err := s.lookupSeriesResources(ctx, policyv1.PostSeries.LookupView())
	if err != nil {
		return nil, err
	}

	if len(seriesIDs) == 0 {
		return connect.NewResponse(&managev1.ListMySeriesResponse{
			Series: []*managev1.SeriesWithStats{},
			Pagination: &commonv1.PaginationResponse{
				Total:   0,
				Limit:   20,
				Offset:  0,
				HasMore: false,
			},
		}), nil
	}

	var seriesList []model.Series
	var total int64

	query := s.db.WithContext(ctx).Model(&model.Series{}).
		Where("id IN ?", seriesIDs)

	// Count total
	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}

	// Apply pagination
	limit := int32(20)
	offset := int32(0)
	if req.Msg.Pagination != nil {
		if req.Msg.Pagination.Limit > 0 {
			limit = req.Msg.Pagination.Limit
		}
		offset = req.Msg.Pagination.Offset
	}

	query = query.Order("created_at DESC, id ASC")

	if err := query.Limit(int(limit)).Offset(int(offset)).Find(&seriesList).Error; err != nil {
		return nil, errs.Internal(err)
	}
	if err := s.overlaySeriesSourceLocaleDocuments(ctx, seriesList); err != nil {
		return nil, err
	}
	readyOgAssets, err := s.loadReadySeriesOgAssets(ctx, seriesList)
	if err != nil {
		return nil, err
	}
	listDetails, err := s.loadSeriesListDetails(ctx, seriesList)
	if err != nil {
		return nil, err
	}

	seriesWithStats := make([]*managev1.SeriesWithStats, len(seriesList))
	for i := range seriesList {
		protoSeries := s.toProtoSeries(&seriesList[i], manageOgAssetFromReadyMap(readyOgAssets, seriesList[i].OgAssetID))
		details := listDetails[seriesList[i].ID]
		protoSeries.FeaturedImageAsset = details.FeaturedImageAsset
		seriesWithStats[i] = &managev1.SeriesWithStats{
			Series:       protoSeries,
			PostCount:    details.PostCount,
			ManagerCount: details.ManagerCount,
		}
	}

	return connect.NewResponse(&managev1.ListMySeriesResponse{
		Series: seriesWithStats,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   limit,
			Offset:  offset,
			HasMore: offset+limit < int32(total),
		},
	}), nil
}

// ListSeriesAdmin returns all series with stats (admin only)
func (s *SeriesService) ListSeriesAdmin(
	ctx context.Context,
	req *connect.Request[managev1.ListSeriesAdminRequest],
) (*connect.Response[managev1.ListSeriesAdminResponse], error) {
	listCan, err := policyv1.PostSeries.List()
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := requireSeriesPlatformPermission(ctx, s.permissions, listCan); err != nil {
		return nil, err
	}

	var seriesList []model.Series
	var total int64

	query := s.db.WithContext(ctx).Model(&model.Series{})

	// Apply filters using FilterConfig
	query, err = SeriesFilterConfig.ApplyFilters(query, req.Msg.Filters)
	if err != nil {
		return nil, err
	}

	// Count total
	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}

	// Apply pagination
	limit := int32(20)
	offset := int32(0)
	if req.Msg.Pagination != nil {
		if req.Msg.Pagination.Limit > 0 {
			limit = req.Msg.Pagination.Limit
		}
		offset = req.Msg.Pagination.Offset
	}

	// Apply sorting
	query, err = seriesSortConfig.ApplySort(query, req.Msg.Sorts)
	if err != nil {
		return nil, err
	}
	query = query.Order("id ASC")

	if err := query.Limit(int(limit)).Offset(int(offset)).Find(&seriesList).Error; err != nil {
		return nil, errs.Internal(err)
	}
	if err := s.overlaySeriesSourceLocaleDocuments(ctx, seriesList); err != nil {
		return nil, err
	}
	readyOgAssets, err := s.loadReadySeriesOgAssets(ctx, seriesList)
	if err != nil {
		return nil, err
	}
	listDetails, err := s.loadSeriesListDetails(ctx, seriesList)
	if err != nil {
		return nil, err
	}

	seriesWithStats := make([]*managev1.SeriesWithStats, len(seriesList))
	for i := range seriesList {
		protoSeries := s.toProtoSeries(&seriesList[i], manageOgAssetFromReadyMap(readyOgAssets, seriesList[i].OgAssetID))
		details := listDetails[seriesList[i].ID]
		protoSeries.FeaturedImageAsset = details.FeaturedImageAsset
		seriesWithStats[i] = &managev1.SeriesWithStats{
			Series:       protoSeries,
			PostCount:    details.PostCount,
			ManagerCount: details.ManagerCount,
		}
	}

	return connect.NewResponse(&managev1.ListSeriesAdminResponse{
		Series: seriesWithStats,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   limit,
			Offset:  offset,
			HasMore: offset+limit < int32(total),
		},
	}), nil
}

// ListSeriesSimple returns all series for dropdowns
func (s *SeriesService) ListSeriesSimple(
	ctx context.Context,
	req *connect.Request[managev1.ListSeriesSimpleRequest],
) (*connect.Response[managev1.ListSeriesSimpleResponse], error) {
	seriesIDs, err := s.lookupSeriesResources(ctx, policyv1.PostSeries.LookupManage())
	if err != nil {
		return nil, err
	}
	if len(seriesIDs) == 0 {
		return connect.NewResponse(&managev1.ListSeriesSimpleResponse{Series: []*managev1.SeriesSimple{}}), nil
	}

	var seriesList []model.Series

	query := s.db.WithContext(ctx).Model(&model.Series{}).
		Where("id IN ?", seriesIDs).
		Order("created_at ASC")

	if err := query.Find(&seriesList).Error; err != nil {
		return nil, errs.Internal(err)
	}
	if err := s.overlaySeriesSourceLocaleDocuments(ctx, seriesList); err != nil {
		return nil, err
	}
	sort.SliceStable(seriesList, func(i, j int) bool { return seriesList[i].Title < seriesList[j].Title })

	// Convert to simple
	simpleSeries := make([]*managev1.SeriesSimple, len(seriesList))
	for i := range seriesList {
		simpleSeries[i] = &managev1.SeriesSimple{
			Id:    seriesList[i].ID,
			Title: seriesList[i].Title,
			Slug:  seriesList[i].Slug,
		}
	}

	return connect.NewResponse(&managev1.ListSeriesSimpleResponse{
		Series: simpleSeries,
	}), nil
}

// CreateSeries creates a new series
func (s *SeriesService) CreateSeries(
	ctx context.Context,
	req *connect.Request[managev1.CreateSeriesRequest],
) (*connect.Response[managev1.Series], error) {
	// Validate required fields
	title := strings.TrimSpace(req.Msg.Title)
	if title == "" {
		return nil, errs.Required("title")
	}

	// Creation is always a draft. Publishing is an explicit later transition.
	slug := seriesSlugFromTitle(title)
	if req.Msg.Slug != nil {
		slug = *req.Msg.Slug
	}
	var err error
	slug, err = validateSeriesSlug(slug)
	if err != nil {
		return nil, err
	}

	// Check slug uniqueness
	if err := ensureSlugAvailable(ctx, s.db, &model.Series{}, "series", slug, ""); err != nil {
		return nil, err
	}
	if err := routeregistry.EnsureResourceRouteAvailable(ctx, s.db, "series", "series", slug); err != nil {
		return nil, err
	}

	now := time.Now()
	sourceLocale := resolveInitialSourceLocale(ctx, s.db, s.kratosClient, req.Header().Get("Accept-Language"))
	series := &model.Series{
		Slug: slug, Status: managev1.SeriesStatus_SERIES_STATUS_DRAFT.String(),
		SourceLocale: sourceLocale, CreatedAt: now, UpdatedAt: &now,
	}

	createCan, err := policyv1.PostSeries.Create()
	if err != nil {
		return nil, errs.Internal(err)
	}
	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		if err := requireFreshSeriesPlatformPermission(ctx, tx, s.permissions, createCan); err != nil {
			return err
		}
		if err := ensureSlugAvailable(ctx, tx, &model.Series{}, "series", slug, ""); err != nil {
			return err
		}
		if err := routeregistry.EnsureResourceRouteAvailableInTx(ctx, tx, "series", "series", slug); err != nil {
			return err
		}
		contentDocumentID, err := createSeriesContentDocument(ctx, tx, now.UTC())
		if err != nil {
			return err
		}
		series.ContentDocumentID = contentDocumentID.String()
		if err := tx.Omit("ID").Clauses(clause.Returning{}).Create(series).Error; err != nil {
			return err
		}
		if err := SaveSourceLocaleDocument(
			ctx, tx, series.ID, sourceLocale,
			&title, req.Msg.Description, now.UTC(),
		); err != nil {
			return err
		}
		if _, err := s.ogRefresh.RequestCurrent(
			ctx, tx, series.ID, sourceLocale, false, "series_created",
		); err != nil {
			return err
		}
		series.SourceLocale = sourceLocale
		series.Title = title
		series.Description = req.Msg.Description
		if err := s.appendPostSeriesCreatedAudit(ctx, tx, series.ID); err != nil {
			return err
		}
		policyTouch, err := policyv1.PostSeries.TouchPolicy(series.ID)
		if err != nil {
			return err
		}
		policyDelete, err := policyv1.PostSeries.DeletePolicy(series.ID)
		if err != nil {
			return err
		}
		return write([]policyv1.RelationshipMutation{policyTouch}, []policyv1.RelationshipMutation{policyDelete})
	})
	if err != nil {
		if dberrors.IsUniqueViolation(err) {
			return nil, errs.SlugAlreadyExists("series", slug)
		}
		if _, ok := err.(*connect.Error); ok {
			return nil, err
		}
		return nil, errs.Internal(err)
	}

	ogAsset, err := s.media.ReadyAsset(ctx, series.OgAssetID)
	if err != nil {
		return nil, err
	}
	protoSeries := s.toProtoSeries(series, ogAsset)
	s.setSeriesFeaturedImageAsset(ctx, protoSeries)
	return connect.NewResponse(protoSeries), nil
}

// CheckSeriesSlugAvailable checks if a slug is available.
// If exclude_id is set, caller must have edit access to the series.
func (s *SeriesService) CheckSeriesSlugAvailable(
	ctx context.Context,
	req *connect.Request[managev1.CheckSeriesSlugAvailableRequest],
) (*connect.Response[managev1.CheckSeriesSlugAvailableResponse], error) {
	slug, err := validateSeriesSlug(req.Msg.Slug)
	if err != nil {
		return nil, err
	}
	excludeID := ""
	if req.Msg.ExcludeId != nil {
		excludeID = *req.Msg.ExcludeId
	}

	if excludeID != "" {
		if err := s.requireSeriesPermissionOrNotFound(ctx, excludeID, policyv1.PostSeries.Edit); err != nil {
			return nil, err
		}
	} else {
		createCan, canErr := policyv1.PostSeries.Create()
		if canErr != nil {
			return nil, errs.Internal(canErr)
		}
		if err := requireSeriesPlatformPermission(ctx, s.permissions, createCan); err != nil {
			return nil, err
		}
	}

	available, err := isSlugAvailable(ctx, s.db, &model.Series{}, slug, excludeID)
	if err != nil {
		return nil, err
	}
	if available {
		available, err = routeregistry.IsResourceRouteAvailable(ctx, s.db, "series", slug)
		if err != nil {
			return nil, err
		}
	}

	return connect.NewResponse(&managev1.CheckSeriesSlugAvailableResponse{
		Available: available,
	}), nil
}

// DeleteSeries deletes a series
func (s *SeriesService) DeleteSeries(
	ctx context.Context,
	req *connect.Request[managev1.DeleteSeriesRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	_, err := authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		var series model.Series
		if err := s.requireSeriesPermissionAndLock(ctx, tx, req.Msg.Id, policyv1.PostSeries.Delete); err != nil {
			return err
		}
		if err := tx.First(&series, "id = ?", req.Msg.Id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("series", req.Msg.Id)
			}
			return errs.Internal(err)
		}
		snapshotPlan, err := policyv1.PostSeries.Snapshot(series.ID)
		if err != nil {
			return err
		}
		snapshots, _, err := s.spiceDB.SnapshotResourceRelationshipDescriptors(ctx, snapshotPlan)
		if err != nil {
			return errs.DependencyUnavailable("SpiceDB")
		}
		deleteRelationships, restoreRelationships, err := seriesAuthorizationSnapshotMutations(series.ID, snapshots)
		if err != nil {
			return errs.Internal(err)
		}
		if err := validateResourceDeletionAuthorizationBatchSize("series", deleteRelationships, restoreRelationships); err != nil {
			return err
		}
		if len(deleteRelationships) == 0 {
			return errs.InternalMsg("series authorization relationships are missing")
		}
		var postIDs []string
		if err := tx.Table("post").
			Where("series_id = ?", series.ID).
			Order("id ASC").
			Pluck("id", &postIDs).Error; err != nil {
			return errs.Internal(err)
		}
		if err := lockSeriesOrderPosts(ctx, tx, postIDs); err != nil {
			return err
		}
		if err := tx.Table("post").
			Where("series_id = ?", series.ID).
			Updates(structured.Fields{"series_id": nil, "series_order": nil}).Error; err != nil {
			return errs.Internal(err)
		}
		if err := s.media.ReleaseFeaturedImage(ctx, tx, series.ID); err != nil {
			return err
		}
		if err := s.media.CancelAndReleaseOG(ctx, tx, series.ID); err != nil {
			return err
		}
		// Delete cascades the manager attribution rows. Post relation columns are
		// cleared together above so the paired series/order invariant is preserved.
		if err := tx.Delete(&series).Error; err != nil {
			return errs.Internal(err)
		}
		contentDocumentID, err := parseSeriesContentDocumentUUID(series.ContentDocumentID, "content_document_id")
		if err != nil {
			return err
		}
		if err := deleteSeriesContentDocument(ctx, tx, contentDocumentID); err != nil {
			return err
		}
		if err := s.menuTargets.Remove(ctx, tx, "series", series.ID, series.Slug); err != nil {
			return err
		}
		if err := s.appendPostSeriesDeletedAudit(ctx, tx, series.ID); err != nil {
			return err
		}
		return write(deleteRelationships, restoreRelationships)
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.DeleteResponse{Success: true}), nil
}

// SetSeriesFeaturedImage sets the featured image for a series.
func (s *SeriesService) SetSeriesFeaturedImage(
	ctx context.Context,
	req *connect.Request[managev1.SetSeriesFeaturedImageRequest],
) (*connect.Response[managev1.SetSeriesFeaturedImageResponse], error) {
	var imageAsset *commonv1.AssetRef
	var ogGenerationRunID *string
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var series model.Series
		if err := s.requireSeriesPermissionAndLock(ctx, tx, req.Msg.SeriesId, policyv1.PostSeries.Edit); err != nil {
			return err
		}
		if err := tx.First(&series, "id = ?", req.Msg.SeriesId).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("series", req.Msg.SeriesId)
			}
			return errs.Internal(err)
		}
		if err := s.media.RequireAttachableFile(ctx, tx, req.Msg.FileId); err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("file", req.Msg.FileId)
			}
			return err
		}
		if sameOptionalString(series.FeaturedImageFileID, &req.Msg.FileId) {
			return nil
		}
		if err := tx.Model(&series).Updates(structured.Fields{
			"featured_image_file_id": req.Msg.FileId, "updated_at": time.Now().UTC(),
		}).Error; err != nil {
			return errs.Internal(err)
		}
		ref, err := s.media.BindFeaturedImage(ctx, tx, req.Msg.FileId, series.ID)
		if err != nil {
			return err
		}
		imageAsset = ref
		ogPlan, err := s.ogRefresh.RequestCurrent(
			ctx, tx, series.ID, "", true, "series_featured_image_updated",
		)
		if err != nil {
			return err
		}
		ogGenerationRunID = ogPlan
		return s.appendPostSeriesFeaturedImageAudit(ctx, tx, series.ID, sharedtelemetry.AuditCollectionOperationAdded, req.Msg.FileId)
	}); err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.SetSeriesFeaturedImageResponse{
		ImageAsset: imageAsset, OgGenerationRunId: ogGenerationRunID,
	}), nil
}

// DeleteSeriesFeaturedImage removes the featured image from a series.
func (s *SeriesService) DeleteSeriesFeaturedImage(
	ctx context.Context,
	req *connect.Request[managev1.DeleteSeriesFeaturedImageRequest],
) (*connect.Response[managev1.OgAssetDeleteResponse], error) {
	var ogGenerationRunID *string
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var series model.Series
		if err := s.requireSeriesPermissionAndLock(ctx, tx, req.Msg.SeriesId, policyv1.PostSeries.Edit); err != nil {
			return err
		}
		if err := tx.First(&series, "id = ?", req.Msg.SeriesId).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("series", req.Msg.SeriesId)
			}
			return errs.Internal(err)
		}
		if series.FeaturedImageFileID == nil {
			return nil
		}
		oldFileID := *series.FeaturedImageFileID
		if err := tx.Model(&series).Updates(structured.Fields{
			"featured_image_file_id": nil, "updated_at": time.Now().UTC(),
		}).Error; err != nil {
			return errs.Internal(err)
		}
		if err := s.media.ReleaseFeaturedImage(ctx, tx, series.ID); err != nil {
			return err
		}
		ogPlan, err := s.ogRefresh.RequestCurrent(
			ctx, tx, series.ID, "", true, "series_featured_image_removed",
		)
		if err != nil {
			return err
		}
		ogGenerationRunID = ogPlan
		return s.appendPostSeriesFeaturedImageAudit(ctx, tx, series.ID, sharedtelemetry.AuditCollectionOperationRemoved, oldFileID)
	}); err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.OgAssetDeleteResponse{
		Success: true, OgGenerationRunId: ogGenerationRunID,
	}), nil
}

// GetSeriesWithManagers returns series with its members in one call
