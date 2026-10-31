package public

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	artistdomain "github.com/echovisionlab/geul-api/internal/artist"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
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

// Deps are the collaborators of the public Artist reads; all are required.
type Deps struct {
	DB            *gorm.DB
	SpiceDB       *auth.SpiceDBClient
	ContentBlocks *contentblock.Store
	Media         MediaProjection
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"DB": d.DB == nil, "SpiceDB": d.SpiceDB == nil, "ContentBlocks": d.ContentBlocks == nil, "Media": d.Media == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("public artist dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the public Artist reads.
type Service struct {
	db            *gorm.DB
	spiceDB       *auth.SpiceDBClient
	contentBlocks *contentblock.Store
	media         MediaProjection
}

// NewService validates deps and returns the public Artist reads.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	return &Service{db: deps.DB, spiceDB: deps.SpiceDB, contentBlocks: deps.ContentBlocks, media: deps.Media}, nil
}

var errArtistNotFound = domainerr.New(domainerr.NotFound, "", "artist not found")

// ListArtistsQuery pages through published Artists.
type ListArtistsQuery struct {
	Limit   int32
	Offset  int32
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
}

// ArtistPage is one page of published Artist summaries.
type ArtistPage struct {
	Artists    []*openv1.ArtistSummary
	Pagination *commonv1.PaginationResponse
}

// List returns published artists with filtering and sorting
func (s *Service) List(ctx context.Context, query ListArtistsQuery) (ArtistPage, error) {
	// Base query - only published artists
	artists := s.db.WithContext(ctx).Model(&model.Artist{}).Where("status = ?", model.ArtistStatusPublished)

	// 1. 조인 필터 직접 처리
	var remainingFilters []*commonv1.FilterSpec
	for _, f := range query.Filters {
		switch f.GetField() {
		case "label_id":
			switch f.GetOp() {
			case commonv1.FilterOp_FILTER_OP_EQ:
				artists = artists.Where("id IN (SELECT artist_id FROM artist_label WHERE label_id = ?)", f.GetValue())
			case commonv1.FilterOp_FILTER_OP_IN:
				artists = artists.Where("id IN (SELECT artist_id FROM artist_label WHERE label_id IN ?)", f.GetValues())
			default:
				return ArtistPage{}, domainerr.New(domainerr.Invalid, "", fmt.Sprintf("invalid filter operation '%s' for field '%s'", f.GetOp().String(), f.GetField()))
			}
		default:
			remainingFilters = append(remainingFilters, f)
		}
	}

	// 2. 나머지 필터 적용
	filtered, err := ArtistFilterConfig.ApplyFilters(artists, remainingFilters)
	if err != nil {
		return ArtistPage{}, err
	}

	// Get total count before pagination
	var total int64
	if err := filtered.Count(&total).Error; err != nil {
		return ArtistPage{}, err
	}

	// 3. Sort
	sorted, err := ArtistSortConfig.ApplySort(filtered, query.Sorts)
	if err != nil {
		return ArtistPage{}, err
	}

	// 4. Pagination
	pagination := queryutil.GetPaginationParams(query.Limit, query.Offset, 20)

	// Fetch artists
	var rows []model.Artist
	if err := queryutil.ApplyPagination(sorted, pagination).Find(&rows).Error; err != nil {
		return ArtistPage{}, err
	}
	summaries, err := s.artistSummaries(ctx, rows)
	if err != nil {
		return ArtistPage{}, err
	}
	return ArtistPage{Artists: summaries, Pagination: &commonv1.PaginationResponse{
		Total:   int32(total),
		Limit:   pagination.Limit,
		Offset:  pagination.Offset,
		HasMore: pagination.Offset+int32(len(rows)) < int32(total),
	}}, nil
}

// artistSummaries renders artists with their source titles, primary images
// and OG images.
func (s *Service) artistSummaries(ctx context.Context, artists []model.Artist) ([]*openv1.ArtistSummary, error) {
	sourceTitles, err := artistdomain.LoadCreativeSourceTitlesForPublic(ctx, s.db, collectArtistIDs(artists))
	if err != nil {
		return nil, err
	}
	for i := range artists {
		artists[i].Name = sourceTitles[artists[i].ID]
	}
	sourceOgAssetIDs, err := artistdomain.LoadCreativeSourceOgAssetIDsForPublic(ctx, s.db, collectArtistIDs(artists))
	if err != nil {
		return nil, err
	}
	for i := range artists {
		artists[i].OgAssetID = sourceOgAssetIDs[artists[i].ID]
	}
	artistIDs := collectArtistIDs(artists)
	readyImages, err := s.media.LoadArtistImages(ctx, artistIDs)
	if err != nil {
		return nil, err
	}
	ogCandidates := make([]*string, 0, len(artists))
	for index := range artists {
		ogCandidates = append(ogCandidates, artists[index].OgAssetID)
	}
	readyOgAssets, err := s.media.ResolveReadyAssetRefs(ctx, ogCandidates)
	if err != nil {
		return nil, err
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

	return summaries, nil
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

// GetArtistQuery reads one Artist by ID or slug. A draft Artist needs a
// share token (and its password) or the caller's exact view permission.
// AcceptLanguage picks the displayed locale.
type GetArtistQuery struct {
	SlugOrID       string
	ShareToken     string
	SharePassword  string
	AcceptLanguage string
}

// Get retrieves an artist by slug or ID
// - UUID-first: if input is valid UUID, query by ID; otherwise query by slug
// - share_token allows access to draft artists
// - authenticated accounts with the exact SpiceDB view permission can access drafts
func (s *Service) Get(ctx context.Context, query GetArtistQuery) (*openv1.Artist, error) {
	slugOrID := query.SlugOrID
	shareToken := query.ShareToken
	sharePassword := query.SharePassword

	var artist model.Artist
	var err error

	// UUID-first approach
	if artistdomain.IsValidUUID(slugOrID) {
		err = s.db.WithContext(ctx).First(&artist, "id = ?", slugOrID).Error
	} else {
		err = s.db.WithContext(ctx).First(&artist, "slug = ?", slugOrID).Error
	}

	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errArtistNotFound
		}
		return nil, err
	}

	// Check access for draft artists
	if artist.Status != model.ArtistStatusPublished {
		if err := s.requireDraftArtistAccess(ctx, artist.ID, shareToken, sharePassword); err != nil {
			return nil, err
		}
	}

	return s.buildArtistResponse(ctx, query.AcceptLanguage, &artist)
}

func (s *Service) requireDraftArtistAccess(
	ctx context.Context,
	artistID string,
	shareToken string,
	sharePassword string,
) error {
	allowed, err := hasDraftResourceView(ctx, s.spiceDB, artistID)
	if err != nil {
		return fmt.Errorf("check artist draft view permission: %w", err)
	}
	if allowed {
		return nil
	}
	return requireOpaqueDraftShareLinkAccess(
		ctx, s.db, shareToken, sharePassword,
		managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_ARTIST, artistID, "artist",
	)
}

// ArtistWorksQuery pages through the published Releases or Works of an
// Artist. A zero Limit is the default page.
type ArtistWorksQuery struct {
	ArtistID       string
	Limit          int32
	Offset         *int32
	Sorts          []*commonv1.SortSpec
	AcceptLanguage string
}

func (q ArtistWorksQuery) page() (int32, int32) {
	limit := int32(20)
	if q.Limit > 0 {
		limit = q.Limit
	}
	offset := int32(0)
	if q.Offset != nil {
		offset = *q.Offset
	}
	return limit, offset
}

// GetReleases returns releases by an artist
func (s *Service) GetReleases(ctx context.Context, query ArtistWorksQuery) (*openv1.GetArtistReleasesResponse, error) {
	artistID := query.ArtistID
	limit, offset := query.page()

	// Build ORDER BY clause from SortSpec
	orderBy := "release.release_date DESC NULLS LAST"
	if len(query.Sorts) > 0 {
		var orderParts []string
		for _, sortSpec := range query.Sorts {
			column, ok := artistReleasesSortColumns[sortSpec.GetField()]
			if !ok {
				return nil, domainerr.New(domainerr.Invalid, "", "invalid sort field: "+sortSpec.GetField())
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
		return nil, err
	}

	// Get total count
	var total int64
	if err := s.db.WithContext(ctx).
		Table("release_artist").
		Select("COUNT(DISTINCT release.id)").
		Joins("JOIN release ON release.id = release_artist.release_id").
		Where("release_artist.artist_id = ? AND release.status = ?", artistID, managev1.ReleaseStatus_RELEASE_STATUS_PUBLISHED.String()).
		Scan(&total).Error; err != nil {
		slog.ErrorContext(ctx, "Artist release count query failed; reporting a zero total", "domain", "artist", "operation", "count_artist_releases", failurecause.Attr(err), "artist_id", artistID)
	}
	releaseIDs := make([]string, 0, len(releases))
	for _, release := range releases {
		releaseIDs = append(releaseIDs, release.ID)
	}
	artworkAssets, err := s.media.LoadReleaseArtworkAssets(ctx, releaseIDs)
	if err != nil {
		return nil, err
	}
	otherArtists, err := loadArtistReleaseOtherArtists(ctx, s.db, releaseIDs, artistID)
	if err != nil {
		return nil, err
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

	return &openv1.GetArtistReleasesResponse{
		Releases: protoReleases,
		Total:    int32(total),
	}, nil
}

// artistWorksOrderBy is the ORDER BY clause of sorts, newest first by
// default.
func artistWorksOrderBy(sorts []*commonv1.SortSpec) (string, error) {
	if len(sorts) == 0 {
		return "work.published_at DESC NULLS LAST", nil
	}
	orderParts := make([]string, 0, len(sorts))
	for _, sortSpec := range sorts {
		if !artistWorksSortAllowedFields[sortSpec.GetField()] {
			return "", domainerr.New(domainerr.Invalid, "", "invalid sort field: "+sortSpec.GetField())
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
	return strings.Join(orderParts, ", "), nil
}

// GetWorks returns works by an artist
func (s *Service) GetWorks(ctx context.Context, query ArtistWorksQuery) (*openv1.GetArtistWorksResponse, error) {
	artistID := query.ArtistID
	limit, offset := query.page()

	orderBy, err := artistWorksOrderBy(query.Sorts)
	if err != nil {
		return nil, err
	}

	var works []struct {
		ID          string     `gorm:"column:id"`
		Title       string     `gorm:"column:title"`
		Slug        *string    `gorm:"column:slug"`
		Summary     *string    `gorm:"column:summary"`
		Type        string     `gorm:"column:type"`
		ImageFileID *string    `gorm:"column:image_file_id"`
		PublishedAt *time.Time `gorm:"column:published_at"`
	}

	err = s.db.WithContext(ctx).
		Table("work_credit").
		Select("DISTINCT work.id, "+workSourceTitleSQL("work")+" AS title, "+workSourceSummarySQL("work")+" AS summary, work.slug, work.type, work.featured_image_file_id AS image_file_id, work.published_at").
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
		return nil, err
	}

	localizedSelections, err := publiccontent.ResolveBatch(
		ctx,
		s.db,
		artistWorkLocalizationSpec,
		collectArtistWorkIDs(works),
		query.AcceptLanguage,
	)
	if err != nil {
		slog.ErrorContext(ctx, "Artist work localization lookup failed; serving unlocalized Works", "domain", "artist", "operation", "resolve_work_localizations", failurecause.Attr(err), "artist_id", artistID)
	}

	// Get total count
	var total int64
	if err := s.db.WithContext(ctx).
		Table("work_credit").
		Select("COUNT(DISTINCT work.id)").
		Joins("JOIN work ON work.id = work_credit.work_id").
		Where("work_credit.artist_id = ? AND work.status IN ?", artistID, []string{
			managev1.WorkStatus_WORK_STATUS_PUBLISHED.String(),
			managev1.WorkStatus_WORK_STATUS_ARCHIVED.String(),
		}).
		Scan(&total).Error; err != nil {
		slog.ErrorContext(ctx, "Artist work count query failed; reporting a zero total", "domain", "artist", "operation", "count_artist_works", failurecause.Attr(err), "artist_id", artistID)
	}
	workFileIDs := make(map[string]*string, len(works))
	for _, work := range works {
		workFileIDs[work.ID] = work.ImageFileID
	}
	workImageAssets, err := s.media.LoadWorkImageAssets(ctx, workFileIDs)
	if err != nil {
		return nil, err
	}

	protoWorks := make([]*openv1.ArtistWork, 0, len(works))
	for _, w := range works {
		work := &openv1.ArtistWork{
			Id:    w.ID,
			Title: w.Title,
			Type:  artistWorkTypeFromDatabase(w.Type),
		}
		if w.Slug != nil {
			work.Slug = w.Slug
		}
		if w.Summary != nil {
			work.Summary = w.Summary
		}
		if localized, ok := localizedSelections[w.ID]; ok {
			if localized.Title != nil {
				work.Title = *localized.Title
			}
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

	return &openv1.GetArtistWorksResponse{
		Works: protoWorks,
		Total: int32(total),
	}, nil
}

// buildArtistResponse projects the Artist in one snapshot that waits out a
// concurrent writer of its row.
func (s *Service) buildArtistResponse(
	ctx context.Context,
	acceptLanguage string,
	artist *model.Artist,
) (*openv1.Artist, error) {
	wasPublic := artist.Status == model.ArtistStatusPublished
	var response *openv1.Artist
	err := mutation.ReadLocked(ctx, s.db, func(tx *gorm.DB) error {
		projection := *s
		projection.db = tx
		var err error
		response, err = projection.buildArtistResponseInTransaction(
			ctx, acceptLanguage, artist, wasPublic,
		)
		return err
	})
	return response, err
}

func (s *Service) buildArtistResponseInTransaction(
	ctx context.Context,
	acceptLanguage string,
	artist *model.Artist,
	wasPublic bool,
) (*openv1.Artist, error) {
	artistID := artist.ID
	var current model.Artist
	if err := s.db.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
		Where("id = ?", artistID).
		Take(&current).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errArtistNotFound
		}
		return nil, err
	}
	if wasPublic && current.Status != model.ArtistStatusPublished {
		return nil, errArtistNotFound
	}
	*artist = current
	localization, err := publiccontent.Resolve(ctx, s.db, artistLocalizationSpec, artistID, acceptLanguage)
	if err != nil {
		slog.ErrorContext(ctx, "Artist localization lookup failed; serving the unlocalized Artist", "domain", "artist", "operation", "resolve_artist_localization", failurecause.Attr(err), "artist_id", artistID)
	}
	localization, err = publiccontent.ResolveOGConsistency(
		ctx, s.db, artistLocalizationSpec, artistID, localization, s.localizedOGAssetReady,
	)
	if err != nil {
		return nil, err
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
		Status:      publicArtistStatus(artist.Status),
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
		return nil, err
	}
	artistSourceOgAssetID := sourceOgAssetIDs[artist.ID]
	if localization.OmitSourceOgFallback {
		artistSourceOgAssetID = nil
	}
	protoArtist.OgAsset, err = s.media.ResolveArtistOGAsset(ctx, artistSourceOgAssetID, localization.OgAssetID)
	if err != nil {
		return nil, err
	}
	images, err := s.media.LoadArtistImages(ctx, []string{artist.ID})
	if err != nil {
		return nil, err
	}
	applyOpenArtistImages(protoArtist, images[artist.ID])

	// Get counts
	protoArtist.ReleaseCount = s.getArtistReleaseCount(ctx, artist.ID)
	protoArtist.WorkCount = s.getArtistWorkCount(ctx, artist.ID)

	// Get labels
	protoArtist.Labels, err = s.getArtistLabels(ctx, artist.ID)
	if err != nil {
		return nil, err
	}
	return protoArtist, nil
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
		Type        string     `gorm:"column:type"`
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

func (s *Service) isArtistGroup(ctx context.Context, artistID string) bool {
	var count int64
	if err := s.db.WithContext(ctx).
		Model(&model.Artist{}).
		Where("parent_artist_id = ?", artistID).
		Count(&count).Error; err != nil {
		slog.ErrorContext(ctx, "Artist group lookup failed; treating the Artist as solo", "domain", "artist", "operation", "check_artist_group", failurecause.Attr(err), "artist_id", artistID)
		return false
	}

	return count > 0
}

// getArtistReleaseCount gets the count of published releases for an artist
func (s *Service) getArtistReleaseCount(ctx context.Context, artistID string) int32 {
	var count int64
	if err := s.db.WithContext(ctx).Raw(`
		SELECT COUNT(DISTINCT release.id)
		FROM release_artist
		JOIN release ON release.id = release_artist.release_id
		WHERE release_artist.artist_id = ? AND release.status = ?
	`, artistID, managev1.ReleaseStatus_RELEASE_STATUS_PUBLISHED.String()).Scan(&count).Error; err != nil {
		slog.ErrorContext(ctx, "Artist release count query failed; reporting zero", "domain", "artist", "operation", "count_artist_releases", failurecause.Attr(err), "artist_id", artistID)
	}
	return int32(count)
}

// getArtistWorkCount gets the count of publicly visible works for an artist.
func (s *Service) getArtistWorkCount(ctx context.Context, artistID string) int32 {
	var count int64
	if err := s.db.WithContext(ctx).Raw(`
		SELECT COUNT(DISTINCT work.id)
		FROM work_credit
		JOIN work ON work.id = work_credit.work_id
		WHERE work_credit.artist_id = ? AND work.status IN (?, ?)
	`, artistID,
		managev1.WorkStatus_WORK_STATUS_PUBLISHED.String(),
		managev1.WorkStatus_WORK_STATUS_ARCHIVED.String(),
	).Scan(&count).Error; err != nil {
		slog.ErrorContext(ctx, "Artist work count query failed; reporting zero", "domain", "artist", "operation", "count_artist_works", failurecause.Attr(err), "artist_id", artistID)
	}
	return int32(count)
}

// getArtistLabels gets published labels associated with an artist
func (s *Service) getArtistLabels(ctx context.Context, artistID string) ([]*openv1.ArtistLabel, error) {
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
		Where("artist_label.artist_id = ? AND label.status = ?", artistID, model.LabelStatusPublished).
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
