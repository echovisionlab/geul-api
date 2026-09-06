package public

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	artistdomain "github.com/echovisionlab/geul-api/internal/artist"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// artistReleasesSortColumns defines allowed sort fields for artist releases
var artistReleasesSortColumns = map[string]string{
	"release_date": "release.release_date",
	"published_at": "release.published_at",
	"title":        releaseSourceTitleSQL("release"),
}

// artistWorksSortAllowedFields defines allowed sort fields for artist works
var artistWorksSortAllowedFields = map[string]bool{
	"title":        true,
	"published_at": true,
}

// ArtistService implements the public ArtistService
type ArtistService struct {
	openv1connect.UnimplementedArtistServiceHandler
	db            *gorm.DB
	spiceDB       *auth.SpiceDBClient
	contentBlocks *contentblock.Store
	media         MediaProjection
}

type ArtistServiceOption func(*ArtistService)

func WithArtistContentBlockStore(store *contentblock.Store) ArtistServiceOption {
	return func(service *ArtistService) { service.contentBlocks = store }
}

// NewArtistService creates a new public ArtistService
func NewArtistService(db *gorm.DB, spiceDB *auth.SpiceDBClient, media MediaProjection, options ...ArtistServiceOption) *ArtistService {
	if db == nil || spiceDB == nil || media == nil {
		panic("public artist service dependencies are required")
	}
	service := &ArtistService{
		db: db, spiceDB: spiceDB, media: media,
	}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service
}

// List returns published artists with filtering and sorting
func (s *ArtistService) List(
	ctx context.Context,
	req *connect.Request[openv1.ListArtistsRequest],
) (*connect.Response[openv1.ListArtistsResponse], error) {
	// Base query - only published artists
	query := s.db.WithContext(ctx).Model(&model.Artist{}).Where("status = ?", openv1.ArtistStatus_ARTIST_STATUS_PUBLISHED.String())

	// 1. 조인 필터 직접 처리
	var remainingFilters []*commonv1.FilterSpec
	for _, f := range req.Msg.Filters {
		switch f.GetField() {
		case "label_id":
			switch f.GetOp() {
			case commonv1.FilterOp_FILTER_OP_EQ:
				query = query.Where("id IN (SELECT artist_id FROM artist_label WHERE label_id = ?)", f.GetValue())
			case commonv1.FilterOp_FILTER_OP_IN:
				query = query.Where("id IN (SELECT artist_id FROM artist_label WHERE label_id IN ?)", f.GetValues())
			default:
				return nil, errs.InvalidFilterOp(f.GetField(), f.GetOp().String())
			}
		default:
			remainingFilters = append(remainingFilters, f)
		}
	}

	// 2. 나머지 필터 적용
	query, err := ArtistFilterConfig.ApplyFilters(query, remainingFilters)
	if err != nil {
		return nil, err
	}

	// Get total count before pagination
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}

	// 3. Sort
	query, err = ArtistSortConfig.ApplySort(query, req.Msg.Sorts)
	if err != nil {
		return nil, err
	}

	// 4. Pagination
	pagination := queryutil.GetPaginationParams(
		req.Msg.Pagination.GetLimit(),
		req.Msg.Pagination.GetOffset(),
		20,
	)
	query = queryutil.ApplyPagination(query, pagination)

	// Fetch artists
	var artists []model.Artist
	if err := query.Find(&artists).Error; err != nil {
		return nil, errs.Internal(err)
	}
	sourceTitles, err := artistdomain.LoadCreativeSourceTitlesForPublic(ctx, s.db, collectArtistIDs(artists))
	if err != nil {
		return nil, errs.Internal(err)
	}
	for i := range artists {
		artists[i].Name = sourceTitles[artists[i].ID]
	}
	sourceOgAssetIDs, err := artistdomain.LoadCreativeSourceOgAssetIDsForPublic(ctx, s.db, collectArtistIDs(artists))
	if err != nil {
		return nil, errs.Internal(err)
	}
	for i := range artists {
		artists[i].OgAssetID = sourceOgAssetIDs[artists[i].ID]
	}
	artistIDs := collectArtistIDs(artists)
	readyImages, err := s.media.LoadArtistImages(ctx, artistIDs)
	if err != nil {
		return nil, errs.Internal(err)
	}
	ogCandidates := make([]*string, 0, len(artists))
	for index := range artists {
		ogCandidates = append(ogCandidates, artists[index].OgAssetID)
	}
	readyOgAssets, err := s.media.ResolveReadyAssetRefs(ctx, ogCandidates)
	if err != nil {
		return nil, errs.Internal(err)
	}

	// Convert to proto summaries
	summaries := make([]*openv1.ArtistSummary, 0, len(artists))
	for _, artist := range artists {
		summary := buildArtistSummary(&artist)
		if images := readyImages[artist.ID]; len(images) > 0 {
			summary.ImageAsset = images[0].Asset
		}
		if artist.OgAssetID != nil {
			summary.OgAsset = readyOgAssets[*artist.OgAssetID]
		}
		summaries = append(summaries, summary)
	}

	return connect.NewResponse(&openv1.ListArtistsResponse{
		Artists: summaries,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   pagination.Limit,
			Offset:  pagination.Offset,
			HasMore: pagination.Offset+int32(len(artists)) < int32(total),
		},
	}), nil
}

func buildArtistSummary(artist *model.Artist) *openv1.ArtistSummary {
	if artist == nil {
		return &openv1.ArtistSummary{}
	}

	summary := &openv1.ArtistSummary{
		Id:   artist.ID,
		Name: artist.Name,
	}
	if artist.Slug != nil {
		summary.Slug = artist.Slug
	}
	if artist.CountryCode != nil {
		summary.CountryCode = artist.CountryCode
	}
	if artist.PublishedAt != nil {
		summary.PublishedAt = timestamppb.New(*artist.PublishedAt)
	}
	if len(artist.SocialLinks) > 0 {
		summary.SocialLinks = artist.SocialLinks
	}

	return summary
}

// Get retrieves an artist by slug or ID
// - UUID-first: if input is valid UUID, query by ID; otherwise query by slug
// - share_token allows access to draft artists
// - authenticated accounts with the exact SpiceDB view permission can access drafts
func (s *ArtistService) Get(
	ctx context.Context,
	req *connect.Request[openv1.GetArtistRequest],
) (*connect.Response[openv1.GetArtistResponse], error) {
	slugOrID := req.Msg.Slug
	shareToken := req.Msg.GetShareToken()
	sharePassword := req.Msg.GetSharePassword()

	var artist model.Artist
	var err error

	// UUID-first approach
	if artistdomain.IsValidUUID(slugOrID) {
		err = s.db.WithContext(ctx).First(&artist, "id = ?", slugOrID).Error
	} else {
		err = s.db.WithContext(ctx).First(&artist, "slug = ?", slugOrID).Error
	}

	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFoundMsg("artist not found")
		}
		return nil, errs.Internal(err)
	}

	// Check access for draft artists
	if artist.Status != openv1.ArtistStatus_ARTIST_STATUS_PUBLISHED.String() {
		if err := s.requireDraftArtistAccess(ctx, artist.ID, shareToken, sharePassword); err != nil {
			return nil, err
		}
	}

	return s.buildArtistResponse(ctx, req.Header().Get("Accept-Language"), &artist)
}

func (s *ArtistService) requireDraftArtistAccess(
	ctx context.Context,
	artistID string,
	shareToken string,
	sharePassword string,
) error {
	allowed, err := hasDraftResourceView(ctx, s.spiceDB, artistID)
	if err != nil {
		return errs.Internal(fmt.Errorf("check artist draft view permission: %w", err))
	}
	if allowed {
		return nil
	}
	return requireOpaqueDraftShareLinkAccess(
		ctx, s.db, shareToken, sharePassword,
		managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_ARTIST, artistID, "artist",
	)
}

// GetReleases returns releases by an artist
func (s *ArtistService) GetReleases(
	ctx context.Context,
	req *connect.Request[openv1.GetArtistReleasesRequest],
) (*connect.Response[openv1.GetArtistReleasesResponse], error) {
	artistID := req.Msg.ArtistId
	limit := int32(20)
	if req.Msg.Limit > 0 {
		limit = req.Msg.Limit
	}
	offset := int32(0)
	if req.Msg.Offset != nil {
		offset = *req.Msg.Offset
	}

	// Build ORDER BY clause from SortSpec
	orderBy := "release.release_date DESC NULLS LAST"
	if len(req.Msg.Sorts) > 0 {
		var orderParts []string
		for _, sortSpec := range req.Msg.Sorts {
			column, ok := artistReleasesSortColumns[sortSpec.GetField()]
			if !ok {
				return nil, errs.InvalidSortField(sortSpec.GetField())
			}
			order := "ASC"
			if sortSpec.GetOrder() == commonv1.SortOrder_SORT_ORDER_DESC {
				order = "DESC"
			}
			orderParts = append(orderParts, fmt.Sprintf("%s %s NULLS LAST", column, order))
		}
		orderBy = strings.Join(orderParts, ", ")
	}

	var releases []struct {
		ID          string     `gorm:"column:id"`
		Title       string     `gorm:"column:title"`
		Slug        *string    `gorm:"column:slug"`
		Type        string     `gorm:"column:type"`
		ReleaseDate *time.Time `gorm:"column:release_date"`
		PublishedAt *time.Time `gorm:"column:published_at"`
	}

	err := s.db.WithContext(ctx).
		Table("release_artist").
		Select("DISTINCT release.id, "+releaseSourceTitleSQL("release")+" AS title, release.slug, release.type, release.release_date, release.published_at").
		Joins("JOIN release ON release.id = release_artist.release_id").
		Where("release_artist.artist_id = ? AND release.status = ?", artistID, managev1.ReleaseStatus_RELEASE_STATUS_PUBLISHED.String()).
		Order(orderBy).
		Limit(int(limit)).
		Offset(int(offset)).
		Scan(&releases).Error
	if err != nil {
		return nil, errs.Internal(err)
	}

	// Get total count
	var total int64
	s.db.WithContext(ctx).
		Table("release_artist").
		Select("COUNT(DISTINCT release.id)").
		Joins("JOIN release ON release.id = release_artist.release_id").
		Where("release_artist.artist_id = ? AND release.status = ?", artistID, managev1.ReleaseStatus_RELEASE_STATUS_PUBLISHED.String()).
		Scan(&total)
	releaseIDs := make([]string, 0, len(releases))
	for _, release := range releases {
		releaseIDs = append(releaseIDs, release.ID)
	}
	artworkAssets, err := s.media.LoadReleaseArtworkAssets(ctx, releaseIDs)
	if err != nil {
		return nil, errs.Internal(err)
	}
	otherArtists, err := loadArtistReleaseOtherArtists(ctx, s.db, releaseIDs, artistID)
	if err != nil {
		return nil, errs.Internal(err)
	}

	protoReleases := make([]*openv1.ArtistRelease, 0, len(releases))
	for _, r := range releases {
		release := &openv1.ArtistRelease{
			Id:    r.ID,
			Title: r.Title,
			Type:  r.Type,
		}
		if r.Slug != nil {
			release.Slug = r.Slug
		}
		if r.ReleaseDate != nil {
			release.ReleaseDate = timestamppb.New(*r.ReleaseDate)
		}
		if r.PublishedAt != nil {
			release.PublishedAt = timestamppb.New(*r.PublishedAt)
		}

		release.ArtworkAsset = artworkAssets[r.ID]
		release.Artists = otherArtists[r.ID]

		protoReleases = append(protoReleases, release)
	}

	return connect.NewResponse(&openv1.GetArtistReleasesResponse{
		Releases: protoReleases,
		Total:    int32(total),
	}), nil
}

// GetWorks returns works by an artist
func (s *ArtistService) GetWorks(
	ctx context.Context,
	req *connect.Request[openv1.GetArtistWorksRequest],
) (*connect.Response[openv1.GetArtistWorksResponse], error) {
	artistID := req.Msg.ArtistId
	limit := int32(20)
	if req.Msg.Limit > 0 {
		limit = req.Msg.Limit
	}
	offset := int32(0)
	if req.Msg.Offset != nil {
		offset = *req.Msg.Offset
	}

	// Build ORDER BY clause from SortSpec
	orderBy := "work.published_at DESC NULLS LAST"
	if len(req.Msg.Sorts) > 0 {
		var orderParts []string
		for _, sortSpec := range req.Msg.Sorts {
			if !artistWorksSortAllowedFields[sortSpec.GetField()] {
				return nil, errs.InvalidSortField(sortSpec.GetField())
			}
			order := "ASC"
			if sortSpec.GetOrder() == commonv1.SortOrder_SORT_ORDER_DESC {
				order = "DESC"
			}
			if sortSpec.GetField() == "title" {
				orderParts = append(orderParts, fmt.Sprintf("%s %s NULLS LAST", workSourceTitleSQL("work"), order))
				continue
			}
			orderParts = append(orderParts, fmt.Sprintf("work.%s %s NULLS LAST", sortSpec.GetField(), order))
		}
		orderBy = strings.Join(orderParts, ", ")
	}

	var works []struct {
		ID          string     `gorm:"column:id"`
		Title       string     `gorm:"column:title"`
		Slug        *string    `gorm:"column:slug"`
		Summary     *string    `gorm:"column:summary"`
		ImageFileID *string    `gorm:"column:image_file_id"`
		PublishedAt *time.Time `gorm:"column:published_at"`
	}

	err := s.db.WithContext(ctx).
		Table("work_credit").
		Select("DISTINCT work.id, "+workSourceTitleSQL("work")+" AS title, "+workSourceSummarySQL("work")+" AS summary, work.slug, work.featured_image_file_id AS image_file_id, work.published_at").
		Joins("JOIN work ON work.id = work_credit.work_id").
		Where("work_credit.artist_id = ? AND work.status IN ?", artistID, []string{
			managev1.WorkStatus_WORK_STATUS_PUBLISHED.String(),
			managev1.WorkStatus_WORK_STATUS_ARCHIVED.String(),
		}).
		Order(orderBy).
		Limit(int(limit)).
		Offset(int(offset)).
		Scan(&works).Error
	if err != nil {
		return nil, errs.Internal(err)
	}

	localizedSelections, err := publiccontent.ResolveBatch(
		ctx,
		s.db,
		artistWorkLocalizationSpec,
		collectArtistWorkIDs(works),
		req.Header().Get("Accept-Language"),
	)
	if err != nil {
		slog.Warn("failed to resolve artist work localizations", "artistId", artistID, "error", err)
	}

	// Get total count
	var total int64
	s.db.WithContext(ctx).
		Table("work_credit").
		Select("COUNT(DISTINCT work.id)").
		Joins("JOIN work ON work.id = work_credit.work_id").
		Where("work_credit.artist_id = ? AND work.status IN ?", artistID, []string{
			managev1.WorkStatus_WORK_STATUS_PUBLISHED.String(),
			managev1.WorkStatus_WORK_STATUS_ARCHIVED.String(),
		}).
		Scan(&total)
	workFileIDs := make(map[string]*string, len(works))
	for _, work := range works {
		workFileIDs[work.ID] = work.ImageFileID
	}
	workImageAssets, err := s.media.LoadWorkImageAssets(ctx, workFileIDs)
	if err != nil {
		return nil, errs.Internal(err)
	}

	protoWorks := make([]*openv1.ArtistWork, 0, len(works))
	for _, w := range works {
		work := &openv1.ArtistWork{
			Id:    w.ID,
			Title: w.Title,
		}
		if w.Slug != nil {
			work.Slug = w.Slug
		}
		if w.Summary != nil {
			work.Summary = w.Summary
		}
		if localized, ok := localizedSelections[w.ID]; ok {
			if localized.Summary != nil {
				work.Summary = localized.Summary
			}
		}
		if w.PublishedAt != nil {
			work.PublishedAt = timestamppb.New(*w.PublishedAt)
		}

		work.ImageAsset = workImageAssets[w.ID]

		protoWorks = append(protoWorks, work)
	}

	return connect.NewResponse(&openv1.GetArtistWorksResponse{
		Works: protoWorks,
		Total: int32(total),
	}), nil
}

// buildArtistResponse builds a GetArtistResponse with the artist
func (s *ArtistService) buildArtistResponse(
	ctx context.Context,
	acceptLanguage string,
	artist *model.Artist,
) (*connect.Response[openv1.GetArtistResponse], error) {
	wasPublic := artist.Status == openv1.ArtistStatus_ARTIST_STATUS_PUBLISHED.String()
	var response *connect.Response[openv1.GetArtistResponse]
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		projection := *s
		projection.db = tx
		var err error
		response, err = projection.buildArtistResponseInTransaction(
			ctx, acceptLanguage, artist, wasPublic,
		)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	return response, err
}

func (s *ArtistService) buildArtistResponseInTransaction(
	ctx context.Context,
	acceptLanguage string,
	artist *model.Artist,
	wasPublic bool,
) (*connect.Response[openv1.GetArtistResponse], error) {
	artistID := artist.ID
	var current model.Artist
	if err := s.db.WithContext(ctx).
		Clauses(clause.Locking{Strength: "SHARE"}).
		Where("id = ?", artistID).
		Take(&current).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFoundMsg("artist not found")
		}
		return nil, errs.Internal(err)
	}
	if wasPublic && current.Status != openv1.ArtistStatus_ARTIST_STATUS_PUBLISHED.String() {
		return nil, errs.NotFoundMsg("artist not found")
	}
	*artist = current
	localization, err := publiccontent.Resolve(ctx, s.db, artistLocalizationSpec, artistID, acceptLanguage)
	if err != nil {
		slog.Warn("failed to resolve artist localization", "artistId", artistID, "error", err)
	}
	localization, err = publiccontent.ResolveOGConsistency(
		ctx, s.db, artistLocalizationSpec, artistID, localization, s.localizedOGAssetReady,
	)
	if err != nil {
		return nil, errs.Internal(err)
	}
	content, err := artistdomain.LoadCreativeContentDocumentForPublicInTransaction(
		ctx, s.db, s.contentBlocks, artistID, localization.DisplayedLocale,
	)
	if err != nil {
		return nil, err
	}
	artist.Name = content.SourceTitle
	if localization.Title != nil {
		artist.Name = *localization.Title
	}

	protoArtist := &openv1.Artist{
		Id:          artist.ID,
		Name:        artist.Name,
		Document:    content.Document,
		SocialLinks: artist.SocialLinks,
		Status:      openv1.ArtistStatus(openv1.ArtistStatus_value[artist.Status]),
		CreatedAt:   timestamppb.New(artist.CreatedAt),
		IsGroup:     s.isArtistGroup(ctx, artist.ID),
		Revision:    content.Revision,
	}

	if artist.UpdatedAt != nil {
		protoArtist.UpdatedAt = timestamppb.New(*artist.UpdatedAt)
	}

	if artist.Slug != nil {
		protoArtist.Slug = artist.Slug
	}
	if artist.RealName != nil {
		protoArtist.RealName = artist.RealName
	}
	protoArtist.LocalizationInfo = publiccontent.ToProtoLocalizationInfo(localization)
	if artist.CountryCode != nil {
		protoArtist.CountryCode = artist.CountryCode
	}
	if artist.Website != nil {
		protoArtist.Website = artist.Website
	}
	if artist.PublishedAt != nil {
		protoArtist.PublishedAt = timestamppb.New(*artist.PublishedAt)
	}
	sourceOgAssetIDs, err := artistdomain.LoadCreativeSourceOgAssetIDsForPublic(ctx, s.db, []string{artist.ID})
	if err != nil {
		return nil, errs.Internal(err)
	}
	artistSourceOgAssetID := sourceOgAssetIDs[artist.ID]
	if localization.OmitSourceOgFallback {
		artistSourceOgAssetID = nil
	}
	protoArtist.OgAsset, err = s.media.ResolveArtistOGAsset(ctx, artistSourceOgAssetID, localization.OgAssetID)
	if err != nil {
		return nil, errs.Internal(err)
	}
	images, err := s.media.LoadArtistImages(ctx, []string{artist.ID})
	if err != nil {
		return nil, errs.Internal(err)
	}
	applyOpenArtistImages(protoArtist, images[artist.ID])

	// Get counts
	protoArtist.ReleaseCount = s.getArtistReleaseCount(ctx, artist.ID)
	protoArtist.WorkCount = s.getArtistWorkCount(ctx, artist.ID)

	// Get labels
	protoArtist.Labels, err = s.getArtistLabels(ctx, artist.ID)
	if err != nil {
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&openv1.GetArtistResponse{
		Artist: protoArtist,
	}), nil
}

func applyOpenArtistImages(artist *openv1.Artist, images []artistdomain.ArtistImageProjection) {
	if artist == nil {
		return
	}
	artist.Images = make([]*openv1.ArtistImage, 0, len(images))
	for index, image := range images {
		primary := index == 0
		artist.Images = append(artist.Images, &openv1.ArtistImage{
			FileId:    image.FileID,
			Asset:     image.Asset,
			SortOrder: int32(index),
			Primary:   primary,
		})
		if primary {
			artist.ImageAsset = image.Asset
		}
	}
}

func collectArtistWorkIDs(
	works []struct {
		ID          string     `gorm:"column:id"`
		Title       string     `gorm:"column:title"`
		Slug        *string    `gorm:"column:slug"`
		Summary     *string    `gorm:"column:summary"`
		ImageFileID *string    `gorm:"column:image_file_id"`
		PublishedAt *time.Time `gorm:"column:published_at"`
	},
) []string {
	ids := make([]string, 0, len(works))
	for _, work := range works {
		ids = append(ids, work.ID)
	}
	return ids
}

func collectArtistIDs(artists []model.Artist) []string {
	ids := make([]string, 0, len(artists))
	for _, artist := range artists {
		ids = append(ids, artist.ID)
	}
	return ids
}

func (s *ArtistService) isArtistGroup(ctx context.Context, artistID string) bool {
	var count int64
	if err := s.db.WithContext(ctx).
		Model(&model.Artist{}).
		Where("parent_artist_id = ?", artistID).
		Count(&count).Error; err != nil {
		return false
	}

	return count > 0
}

// getArtistReleaseCount gets the count of published releases for an artist
func (s *ArtistService) getArtistReleaseCount(ctx context.Context, artistID string) int32 {
	var count int64
	s.db.WithContext(ctx).Raw(`
		SELECT COUNT(DISTINCT release.id)
		FROM release_artist
		JOIN release ON release.id = release_artist.release_id
		WHERE release_artist.artist_id = ? AND release.status = ?
	`, artistID, managev1.ReleaseStatus_RELEASE_STATUS_PUBLISHED.String()).Scan(&count)
	return int32(count)
}

// getArtistWorkCount gets the count of publicly visible works for an artist.
func (s *ArtistService) getArtistWorkCount(ctx context.Context, artistID string) int32 {
	var count int64
	s.db.WithContext(ctx).Raw(`
		SELECT COUNT(DISTINCT work.id)
		FROM work_credit
		JOIN work ON work.id = work_credit.work_id
		WHERE work_credit.artist_id = ? AND work.status IN (?, ?)
	`, artistID,
		managev1.WorkStatus_WORK_STATUS_PUBLISHED.String(),
		managev1.WorkStatus_WORK_STATUS_ARCHIVED.String(),
	).Scan(&count)
	return int32(count)
}

// getArtistLabels gets published labels associated with an artist
func (s *ArtistService) getArtistLabels(ctx context.Context, artistID string) ([]*openv1.ArtistLabel, error) {
	var labels []struct {
		ID          string  `gorm:"column:id"`
		Name        string  `gorm:"column:name"`
		Slug        *string `gorm:"column:slug"`
		ImageFileID *string `gorm:"column:image_file_id"`
	}

	if err := s.db.WithContext(ctx).
		Table("artist_label").
		Select("label.id, "+labelSourceTitleSQL("label")+" AS name, label.slug, label.logo_light_file_id AS image_file_id").
		Joins("JOIN label ON label.id = artist_label.label_id").
		Where("artist_label.artist_id = ? AND label.status = ?", artistID, managev1.LabelStatus_LABEL_STATUS_PUBLISHED.String()).
		Order(labelSourceTitleSQL("label") + " ASC").
		Limit(12).
		Scan(&labels).Error; err != nil {
		return nil, err
	}
	fileIDs := make([]string, 0, len(labels))
	for _, label := range labels {
		if label.ImageFileID != nil {
			fileIDs = append(fileIDs, *label.ImageFileID)
		}
	}
	imageAssets, err := s.media.LoadReadySourceFileAssets(ctx, "logo", fileIDs)
	if err != nil {
		return nil, err
	}

	protoLabels := make([]*openv1.ArtistLabel, 0, len(labels))
	for _, l := range labels {
		label := &openv1.ArtistLabel{
			Id:   l.ID,
			Name: l.Name,
		}
		if l.Slug != nil {
			label.Slug = l.Slug
		}
		if l.ImageFileID != nil {
			label.ImageAsset = imageAssets[*l.ImageFileID]
		}
		protoLabels = append(protoLabels, label)
	}

	return protoLabels, nil
}
