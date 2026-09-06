package public

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	releasepkg "github.com/echovisionlab/geul-api/internal/release"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Release status constants (model format)
var (
	ReleaseStatusDraft     = managev1.ReleaseStatus_RELEASE_STATUS_DRAFT.String()
	ReleaseStatusPublished = managev1.ReleaseStatus_RELEASE_STATUS_PUBLISHED.String()
)

var releaseLocalizationSpec = publiccontent.Spec{
	EntityType:   "release",
	TableName:    "release_translation",
	SelectClause: "locale, NULL::text AS title, NULL::text AS summary, NULL::jsonb AS content_json, NULL::text AS content_html, NULL::text AS content_text, NULL::uuid AS og_asset_id",
}

// ReleaseService implements the Release domain's public ReleaseService.
type ReleaseService struct {
	openv1connect.UnimplementedReleaseServiceHandler
	db            *gorm.DB
	spiceDB       *auth.SpiceDBClient
	contentBlocks *contentblock.Store
	media         MediaRuntime
	downloads     DownloadAccessResolver
	members       releasepkg.MemberSummaryLoader
	artists       releasepkg.ArtistSummaryLoader
	labels        releasepkg.LabelSummaryLoader
}

type ReleaseServiceOption func(*ReleaseService)

func WithReleaseContentBlockStore(store *contentblock.Store) ReleaseServiceOption {
	return func(service *ReleaseService) { service.contentBlocks = store }
}

// NewReleaseService creates a new public ReleaseService
func NewReleaseService(
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	media MediaRuntime,
	downloads DownloadAccessResolver,
	members releasepkg.MemberSummaryLoader,
	artists releasepkg.ArtistSummaryLoader,
	labels releasepkg.LabelSummaryLoader,
	options ...ReleaseServiceOption,
) *ReleaseService {
	if db == nil || spiceDB == nil || media == nil || downloads == nil || members == nil || artists == nil || labels == nil {
		panic("public release service dependencies are required")
	}
	service := &ReleaseService{
		db: db, spiceDB: spiceDB, media: media, downloads: downloads,
		members: members, artists: artists, labels: labels,
	}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service
}

// Get retrieves a release by slug or ID
// - UUID-first: if input is valid UUID, query by ID; otherwise query by slug
// - share_token allows access to draft releases
// - authenticated accounts with the exact SpiceDB view permission can access drafts
func (s *ReleaseService) Get(
	ctx context.Context,
	req *connect.Request[openv1.GetReleaseRequest],
) (*connect.Response[openv1.GetReleaseResponse], error) {
	slugOrID := req.Msg.Slug
	shareToken := req.Msg.ShareToken
	sharePassword := req.Msg.GetSharePassword()

	var release model.Release
	var err error

	// UUID-first approach
	if releasepkg.IsValidUUID(slugOrID) {
		err = s.db.WithContext(ctx).First(&release, "id = ?", slugOrID).Error
	} else {
		err = s.db.WithContext(ctx).First(&release, "slug = ?", slugOrID).Error
	}

	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFoundMsg("release not found")
		}
		return nil, errs.Internal(err)
	}
	mediaAuthorization := mediaasset.ContentDownloadOwnerAuthorization{
		ResourceType: "release",
		ResourceID:   release.ID,
		Status:       release.Status,
		Mode:         mediaasset.ContentDownloadOwnerAccessPublic,
	}
	if release.ContentDocumentID != nil {
		mediaAuthorization.DocumentID = *release.ContentDocumentID
	}
	// Check access for draft releases
	if release.Status != ReleaseStatusPublished {
		allowed, permissionErr := hasDraftReleaseView(ctx, s.spiceDB, release.ID)
		if permissionErr != nil {
			return nil, errs.Internal(fmt.Errorf("check release draft view permission: %w", permissionErr))
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
				managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_RELEASE, release.ID, "release",
			)
			if accessErr != nil {
				return nil, accessErr
			}
			mediaAuthorization.Mode = mediaasset.ContentDownloadOwnerAccessShare
			mediaAuthorization.ShareLink = mediaasset.ContentDownloadShareLinkWitnessFromModel(link)
		}
	}

	return s.buildReleaseResponse(ctx, req.Header().Get("Accept-Language"), &release, mediaAuthorization)
}

// List returns published releases
func (s *ReleaseService) List(
	ctx context.Context,
	req *connect.Request[openv1.ListReleasesRequest],
) (*connect.Response[openv1.ListReleasesResponse], error) {
	var releases []model.Release
	var total int64

	query := s.db.WithContext(ctx).Model(&model.Release{}).
		Where("status = ?", ReleaseStatusPublished)

	// 1. 조인 필터 직접 처리
	var remainingFilters []*commonv1.FilterSpec
	for _, f := range req.Msg.Filters {
		switch f.GetField() {
		case "label_id":
			switch f.GetOp() {
			case commonv1.FilterOp_FILTER_OP_EQ:
				query = query.Where("id IN (SELECT release_id FROM release_label WHERE label_id = ?)", f.GetValue())
			case commonv1.FilterOp_FILTER_OP_IN:
				query = query.Where("id IN (SELECT release_id FROM release_label WHERE label_id IN ?)", f.GetValues())
			default:
				return nil, errs.InvalidFilterOp(f.GetField(), f.GetOp().String())
			}
		case "artist_id":
			switch f.GetOp() {
			case commonv1.FilterOp_FILTER_OP_EQ:
				query = query.Where("id IN (SELECT release_id FROM release_artist WHERE artist_id = ?)", f.GetValue())
			case commonv1.FilterOp_FILTER_OP_IN:
				query = query.Where("id IN (SELECT release_id FROM release_artist WHERE artist_id IN ?)", f.GetValues())
			default:
				return nil, errs.InvalidFilterOp(f.GetField(), f.GetOp().String())
			}
		case "category_id":
			switch f.GetOp() {
			case commonv1.FilterOp_FILTER_OP_EQ:
				query = query.Where("id IN (SELECT release_id FROM release_category WHERE category_id = ?)", f.GetValue())
			case commonv1.FilterOp_FILTER_OP_IN:
				query = query.Where("id IN (SELECT release_id FROM release_category WHERE category_id IN ?)", f.GetValues())
			default:
				return nil, errs.InvalidFilterOp(f.GetField(), f.GetOp().String())
			}
		default:
			remainingFilters = append(remainingFilters, f)
		}
	}

	// 2. 나머지 필터 적용
	query, err := ReleaseFilterConfig.ApplyFilters(query, remainingFilters)
	if err != nil {
		return nil, err
	}

	// Count total before pagination
	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}

	// 3. Sort
	query, err = ReleaseSortConfig.ApplySort(query, req.Msg.Sorts)
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

	if err := query.Find(&releases).Error; err != nil {
		return nil, errs.Internal(err)
	}
	sourceTitles, err := releasepkg.LoadPublicSourceTitles(ctx, s.db, collectReleaseIDs(releases))
	if err != nil {
		return nil, err
	}
	for i := range releases {
		releases[i].Title = sourceTitles[releases[i].ID]
	}
	releaseIDs := collectReleaseIDs(releases)
	artworkAssets, err := s.loadReleaseArtworkAssets(ctx, releaseIDs)
	if err != nil {
		return nil, errs.Internal(err)
	}
	artistsByRelease, err := s.loadReleaseArtists(ctx, releaseIDs)
	if err != nil {
		return nil, errs.Internal(err)
	}
	// Convert to proto summaries
	summaries := make([]*openv1.ReleaseSummary, 0, len(releases))
	for _, release := range releases {
		summaries = append(summaries, s.toReleaseSummary(
			&release,
			artworkAssets[release.ID],
			artistsByRelease[release.ID],
		))
	}

	return connect.NewResponse(&openv1.ListReleasesResponse{
		Releases: summaries,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   pagination.Limit,
			Offset:  pagination.Offset,
			HasMore: pagination.Offset+int32(len(releases)) < int32(total),
		},
	}), nil
}

// buildReleaseResponse builds a GetReleaseResponse with the release
func (s *ReleaseService) buildReleaseResponse(
	ctx context.Context,
	acceptLanguage string,
	release *model.Release,
	mediaAuthorization mediaasset.ContentDownloadOwnerAuthorization,
) (*connect.Response[openv1.GetReleaseResponse], error) {
	wasPublic := release.Status == ReleaseStatusPublished
	var response *connect.Response[openv1.GetReleaseResponse]
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		projection := *s
		projection.db = tx
		var err error
		response, err = projection.buildReleaseResponseInTransaction(
			ctx, acceptLanguage, release, mediaAuthorization, wasPublic,
		)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	return response, err
}

func (s *ReleaseService) buildReleaseResponseInTransaction(
	ctx context.Context,
	acceptLanguage string,
	release *model.Release,
	mediaAuthorization mediaasset.ContentDownloadOwnerAuthorization,
	wasPublic bool,
) (*connect.Response[openv1.GetReleaseResponse], error) {
	releaseID := release.ID
	var current model.Release
	if err := s.db.WithContext(ctx).
		Where("id = ?", releaseID).
		Take(&current).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFoundMsg("release not found")
		}
		return nil, errs.Internal(err)
	}
	if wasPublic && current.Status != ReleaseStatusPublished {
		return nil, errs.NotFoundMsg("release not found")
	}
	*release = current
	localization, err := publiccontent.Resolve(ctx, s.db, releaseLocalizationSpec, releaseID, acceptLanguage)
	if err != nil {
		slog.Warn("failed to resolve release localization", "releaseId", releaseID, "error", err)
	}
	content, err := releasepkg.LoadPublicContentDocument(
		ctx, s.db, s.contentBlocks, releaseID, localization.DisplayedLocale,
	)
	if err != nil {
		return nil, err
	}
	release.Title = content.SourceTitle
	if localization.Title != nil {
		release.Title = *localization.Title
	}
	protoRelease := &openv1.Release{
		Id:        release.ID,
		Title:     release.Title,
		Type:      openv1.ReleaseType(openv1.ReleaseType_value[release.Type]),
		Document:  content.Document,
		Status:    openv1.ReleaseStatus(openv1.ReleaseStatus_value[release.Status]),
		CreatedAt: timestamppb.New(release.CreatedAt),
		UpdatedAt: timestamppb.New(release.UpdatedAt),
		Revision:  content.Revision,
	}

	if release.Slug != nil {
		protoRelease.Slug = release.Slug
	}
	protoRelease.LocalizationInfo = publiccontent.ToProtoLocalizationInfo(localization)
	if release.CatalogNumber != nil {
		protoRelease.CatalogNumber = release.CatalogNumber
	}
	if release.ReleaseDate != nil {
		protoRelease.ReleaseDate = timestamppb.New(*release.ReleaseDate)
	}
	if release.PublishedAt != nil {
		protoRelease.PublishedAt = timestamppb.New(*release.PublishedAt)
	}
	// Streaming URLs
	if release.SpotifyURL != nil {
		protoRelease.SpotifyUrl = release.SpotifyURL
	}
	if release.AppleMusicURL != nil {
		protoRelease.AppleMusicUrl = release.AppleMusicURL
	}
	if release.BandcampURL != nil {
		protoRelease.BandcampUrl = release.BandcampURL
	}
	if release.YoutubeMusicURL != nil {
		protoRelease.YoutubeMusicUrl = release.YoutubeMusicURL
	}
	artworkAssets, err := s.loadReleaseArtworkAssets(ctx, []string{release.ID})
	if err != nil {
		return nil, errs.Internal(err)
	}
	protoRelease.ArtworkAsset = artworkAssets[release.ID]
	protoRelease.OgAsset = protoRelease.ArtworkAsset
	artistsByRelease, err := s.loadReleaseArtists(ctx, []string{release.ID})
	if err != nil {
		return nil, errs.Internal(err)
	}
	protoRelease.Artists = artistsByRelease[release.ID]

	// Get release relations
	protoRelease.Labels, err = s.getReleaseLabels(ctx, release.ID)
	if err != nil {
		return nil, errs.Internal(err)
	}
	protoRelease.Genres, err = s.getReleaseGenres(ctx, release.ID)
	if err != nil {
		return nil, errs.Internal(err)
	}
	protoRelease.Styles, err = s.getReleaseStyles(ctx, release.ID)
	if err != nil {
		return nil, errs.Internal(err)
	}
	protoRelease.Formats, err = s.getReleaseFormats(ctx, release.ID)
	if err != nil {
		return nil, errs.Internal(err)
	}
	protoRelease.Credits, err = s.getReleaseCredits(
		ctx,
		release.ID,
		localization.SourceLocale,
		localization.DisplayedLocale,
	)
	if err != nil {
		return nil, errs.Internal(err)
	}

	// Get tracks
	protoRelease.Tracks, err = s.getReleaseTracks(ctx, release.ID, release.Status, mediaAuthorization)
	if err != nil {
		return nil, errs.Internal(err)
	}

	return connect.NewResponse(&openv1.GetReleaseResponse{
		Release: protoRelease,
	}), nil
}

// toReleaseSummary converts a release to a summary (no tracks)
func (s *ReleaseService) toReleaseSummary(
	release *model.Release,
	artworkAsset *commonv1.AssetRef,
	artists []*openv1.ReleaseArtist,
) *openv1.ReleaseSummary {
	summary := &openv1.ReleaseSummary{
		Id:    release.ID,
		Title: release.Title,
		Type:  openv1.ReleaseType(openv1.ReleaseType_value[release.Type]),
	}

	if release.Slug != nil {
		summary.Slug = release.Slug
	}
	if release.ReleaseDate != nil {
		summary.ReleaseDate = timestamppb.New(*release.ReleaseDate)
	}
	if release.PublishedAt != nil {
		summary.PublishedAt = timestamppb.New(*release.PublishedAt)
	}
	summary.OgAsset = artworkAsset
	summary.ArtworkAsset = artworkAsset
	summary.Artists = artists
	return summary
}

func collectReleaseIDs(releases []model.Release) []string {
	ids := make([]string, 0, len(releases))
	for _, release := range releases {
		ids = append(ids, release.ID)
	}
	return ids
}

func (s *ReleaseService) loadReleaseArtworkAssets(
	ctx context.Context,
	releaseIDs []string,
) (map[string]*commonv1.AssetRef, error) {
	type artworkRow struct {
		ReleaseID string `gorm:"column:release_id"`
		FileID    string `gorm:"column:file_id"`
	}
	result := make(map[string]*commonv1.AssetRef, len(releaseIDs))
	if len(releaseIDs) == 0 {
		return result, nil
	}

	var rows []artworkRow
	if err := s.db.WithContext(ctx).Raw(`
		SELECT DISTINCT ON (release_id) release_id, file_id
		FROM release_file
		WHERE release_id IN ?
		ORDER BY release_id, sort_order ASC, created_at ASC
	`, releaseIDs).Scan(&rows).Error; err != nil {
		return nil, err
	}

	fileIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		fileIDs = append(fileIDs, row.FileID)
	}
	assetsByFile, err := s.media.ReadySourceAssets(ctx, s.db, fileIDs, "artwork", "image")
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		asset, ok := assetsByFile[row.FileID]
		if !ok {
			continue
		}
		result[row.ReleaseID] = asset
	}
	return result, nil
}

// loadReleaseArtists gets actual artists for every Release in one relation query.
func (s *ReleaseService) loadReleaseArtists(ctx context.Context, releaseIDs []string) (map[string][]*openv1.ReleaseArtist, error) {
	var artists []struct {
		ReleaseID   string  `gorm:"column:release_id"`
		ArtistID    string  `gorm:"column:artist_id"`
		ArtistName  string  `gorm:"column:artist_name"`
		ArtistSlug  *string `gorm:"column:artist_slug"`
		ImageFileID *string `gorm:"column:image_file_id"`
	}

	err := s.db.WithContext(ctx).
		Table("release_artist").
		Select(`
			release_artist.release_id,
			release_artist.artist_id,
			artist_image.file_id AS image_file_id
		`).
		Joins(`LEFT JOIN LATERAL (
			SELECT af.file_id
			FROM artist_file AS af
			WHERE af.artist_id = release_artist.artist_id
			ORDER BY af.sort_order ASC, af.created_at ASC
			LIMIT 1
		) AS artist_image ON TRUE`).
		Where("release_artist.release_id IN ?", releaseIDs).
		Order("release_artist.release_id ASC, release_artist.sort_order ASC, release_artist.created_at ASC").
		Scan(&artists).Error

	if err != nil {
		return nil, err
	}
	artistIDs := make([]string, 0, len(artists))
	for _, artist := range artists {
		artistIDs = append(artistIDs, artist.ArtistID)
	}
	summaries, err := s.artists.LoadArtistSummariesWithDB(ctx, s.db, artistIDs)
	if err != nil {
		return nil, err
	}

	imageFileIDs := make([]string, 0, len(artists))
	for _, artist := range artists {
		if artist.ImageFileID != nil {
			imageFileIDs = append(imageFileIDs, *artist.ImageFileID)
		}
	}
	imageAssets, err := s.media.ReadySourceAssets(ctx, s.db, imageFileIDs, "image")
	if err != nil {
		return nil, err
	}
	result := make(map[string][]*openv1.ReleaseArtist, len(releaseIDs))
	for _, releaseArtist := range artists {
		summary := summaries[releaseArtist.ArtistID]
		artist := &openv1.ReleaseArtist{
			Id:   releaseArtist.ArtistID,
			Name: summary.Title,
			Role: "primary",
		}
		if summary.Slug != "" {
			artist.Slug = &summary.Slug
		}
		if releaseArtist.ImageFileID != nil {
			if asset, ok := imageAssets[*releaseArtist.ImageFileID]; ok {
				artist.ImageAsset = asset
			}
		}
		result[releaseArtist.ReleaseID] = append(result[releaseArtist.ReleaseID], artist)
	}

	return result, nil
}

// getReleaseLabels gets release labels.
func (s *ReleaseService) getReleaseLabels(ctx context.Context, releaseID string) ([]*openv1.ReleaseLabel, error) {
	var labels []struct {
		ID            string  `gorm:"column:id"`
		Name          string  `gorm:"column:name"`
		Slug          *string `gorm:"column:slug"`
		CatalogNumber *string `gorm:"column:catalog_number"`
	}

	err := s.db.WithContext(ctx).
		Table("release_label").
		Select(`
			release_label.label_id as id,
			release_label.catalog_number as catalog_number
		`).
		Where("release_label.release_id = ?", releaseID).
		Order("release_label.sort_order ASC").
		Scan(&labels).Error

	if err != nil {
		return nil, err
	}
	labelIDs := make([]string, 0, len(labels))
	for _, label := range labels {
		labelIDs = append(labelIDs, label.ID)
	}
	summaries, err := s.labels.LoadLabelSummariesWithDB(ctx, s.db, labelIDs)
	if err != nil {
		return nil, err
	}

	protoLabels := make([]*openv1.ReleaseLabel, 0, len(labels))
	for _, label := range labels {
		summary := summaries[label.ID]
		protoLabel := &openv1.ReleaseLabel{
			Id:   label.ID,
			Name: summary.Title,
		}
		if summary.Slug != "" {
			protoLabel.Slug = &summary.Slug
		}
		if label.CatalogNumber != nil {
			protoLabel.CatalogNumber = label.CatalogNumber
		}
		protoLabels = append(protoLabels, protoLabel)
	}

	return protoLabels, nil
}

func (s *ReleaseService) getReleaseGenres(ctx context.Context, releaseID string) ([]*openv1.ReleaseReference, error) {
	var genres []struct {
		ID   string `gorm:"column:id"`
		Name string `gorm:"column:name"`
		Slug string `gorm:"column:slug"`
	}

	err := s.db.WithContext(ctx).
		Table("release_genre").
		Select("genre.id as id, genre.name as name, genre.slug as slug").
		Joins("JOIN genre ON genre.id = release_genre.genre_id").
		Where("release_genre.release_id = ?", releaseID).
		Order("genre.name ASC").
		Scan(&genres).Error

	if err != nil {
		return nil, err
	}

	protoGenres := make([]*openv1.ReleaseReference, 0, len(genres))
	for _, genre := range genres {
		protoGenre := &openv1.ReleaseReference{
			Id:   genre.ID,
			Name: genre.Name,
		}
		if genre.Slug != "" {
			protoGenre.Slug = &genre.Slug
		}
		protoGenres = append(protoGenres, protoGenre)
	}

	return protoGenres, nil
}

func (s *ReleaseService) getReleaseStyles(ctx context.Context, releaseID string) ([]*openv1.ReleaseReference, error) {
	var styles []struct {
		ID   string `gorm:"column:id"`
		Name string `gorm:"column:name"`
		Slug string `gorm:"column:slug"`
	}

	err := s.db.WithContext(ctx).
		Table("release_style").
		Select("style.id as id, style.name as name, style.slug as slug").
		Joins("JOIN style ON style.id = release_style.style_id").
		Where("release_style.release_id = ?", releaseID).
		Order("style.name ASC").
		Scan(&styles).Error

	if err != nil {
		return nil, err
	}

	protoStyles := make([]*openv1.ReleaseReference, 0, len(styles))
	for _, style := range styles {
		protoStyle := &openv1.ReleaseReference{
			Id:   style.ID,
			Name: style.Name,
		}
		if style.Slug != "" {
			protoStyle.Slug = &style.Slug
		}
		protoStyles = append(protoStyles, protoStyle)
	}

	return protoStyles, nil
}

func (s *ReleaseService) getReleaseFormats(ctx context.Context, releaseID string) ([]*openv1.ReleaseFormat, error) {
	var formats []struct {
		ID          string  `gorm:"column:id"`
		Name        string  `gorm:"column:name"`
		Slug        string  `gorm:"column:slug"`
		Description *string `gorm:"column:description"`
	}

	err := s.db.WithContext(ctx).
		Table("release_format").
		Select(`
			format.id as id,
			format.name as name,
			format.slug as slug,
			release_format.format_description as description
		`).
		Joins("JOIN format ON format.id = release_format.format_id").
		Where("release_format.release_id = ?", releaseID).
		Order("format.name ASC").
		Scan(&formats).Error

	if err != nil {
		return nil, err
	}

	protoFormats := make([]*openv1.ReleaseFormat, 0, len(formats))
	for _, format := range formats {
		protoFormat := &openv1.ReleaseFormat{
			Id:   format.ID,
			Name: format.Name,
		}
		if format.Slug != "" {
			protoFormat.Slug = &format.Slug
		}
		if format.Description != nil {
			protoFormat.Description = format.Description
		}
		protoFormats = append(protoFormats, protoFormat)
	}

	return protoFormats, nil
}

func (s *ReleaseService) getReleaseCredits(
	ctx context.Context,
	releaseID string,
	sourceLocale string,
	displayedLocale string,
) ([]*openv1.ReleaseCredit, error) {
	credits, err := s.loadReleaseCreditRows(ctx, releaseID)
	if err != nil {
		return nil, err
	}
	projection, err := s.loadReleaseCreditProjection(
		ctx, releaseID, credits, sourceLocale, displayedLocale,
	)
	if err != nil {
		return nil, err
	}
	protoCredits := make([]*openv1.ReleaseCredit, 0, len(credits))
	for _, credit := range credits {
		protoCredit, err := s.projectReleaseCredit(credit, projection)
		if err != nil {
			return nil, err
		}
		if protoCredit != nil {
			protoCredits = append(protoCredits, protoCredit)
		}
	}
	return protoCredits, nil
}

type publicReleaseCreditRow struct {
	ID           string  `gorm:"column:id"`
	ArtistName   *string `gorm:"column:artist_name"`
	CreditedName *string `gorm:"column:credited_name"`
	Slug         *string `gorm:"column:slug"`
	CreditRole   *string `gorm:"column:credit_role"`
	ArtistID     *string `gorm:"column:artist_id"`
	MemberID     *string `gorm:"column:member_id"`
	ImageFileID  *string `gorm:"column:image_file_id"`
}

func (s *ReleaseService) loadReleaseCreditRows(ctx context.Context, releaseID string) ([]publicReleaseCreditRow, error) {
	var credits []publicReleaseCreditRow
	err := s.db.WithContext(ctx).Raw(`
			SELECT
			rc.id,
			rc.credited_name,
			rc.credit_role,
			rc.artist_id,
			rc.member_id,
			afi.file_id AS image_file_id
		FROM release_credit rc
		LEFT JOIN (
			SELECT DISTINCT ON (artist_id) artist_id, file_id
			FROM artist_file
			ORDER BY artist_id, sort_order ASC, created_at ASC
		) afi ON afi.artist_id = rc.artist_id
		WHERE rc.release_id = ?
		ORDER BY rc.sort_order ASC, rc.created_at ASC
		`, releaseID).Scan(&credits).Error
	if err != nil {
		return nil, err
	}
	artistIDs := make([]string, 0, len(credits))
	for _, credit := range credits {
		if credit.ArtistID != nil {
			artistIDs = append(artistIDs, *credit.ArtistID)
		}
	}
	summaries, err := s.artists.LoadArtistSummariesWithDB(ctx, s.db, artistIDs)
	if err != nil {
		return nil, err
	}
	for index := range credits {
		if credits[index].ArtistID == nil {
			continue
		}
		summary := summaries[*credits[index].ArtistID]
		if summary.Title != "" {
			credits[index].ArtistName = &summary.Title
		}
		if summary.Slug != "" {
			credits[index].Slug = &summary.Slug
		}
	}
	return credits, nil
}

type releaseCreditProjection struct {
	members        map[string]*commonv1.MemberSummary
	imageAssets    map[string]*commonv1.AssetRef
	noteByCreditID map[string]string
}

func (s *ReleaseService) loadReleaseCreditProjection(
	ctx context.Context,
	releaseID string,
	credits []publicReleaseCreditRow,
	sourceLocale string,
	displayedLocale string,
) (releaseCreditProjection, error) {
	creditIDs := make([]string, 0, len(credits))
	memberIDs := make([]string, 0, len(credits))
	imageFileIDs := make([]string, 0, len(credits))
	for _, credit := range credits {
		creditIDs = append(creditIDs, credit.ID)
		if credit.MemberID != nil {
			memberIDs = append(memberIDs, *credit.MemberID)
		}
		if credit.ImageFileID != nil {
			imageFileIDs = append(imageFileIDs, *credit.ImageFileID)
		}
	}
	members, err := s.members.LoadMemberSummaries(ctx, memberIDs)
	if err != nil {
		return releaseCreditProjection{}, err
	}
	imageAssets, err := s.media.ReadySourceAssets(ctx, s.db, imageFileIDs, "image")
	if err != nil {
		return releaseCreditProjection{}, err
	}
	notes, err := releasepkg.LoadReleaseCreditNotesForDisplay(
		ctx, s.db, releaseID, sourceLocale, displayedLocale, creditIDs,
	)
	if err != nil {
		return releaseCreditProjection{}, err
	}
	return releaseCreditProjection{
		members:        members,
		imageAssets:    imageAssets,
		noteByCreditID: notes,
	}, nil
}

func (s *ReleaseService) projectReleaseCredit(
	credit publicReleaseCreditRow,
	projection releaseCreditProjection,
) (*openv1.ReleaseCredit, error) {
	name := releaseCreditDisplayName(credit, projection.members)
	if name == "" {
		return nil, nil
	}
	protoCredit := &openv1.ReleaseCredit{
		Id:         credit.ID,
		Name:       name,
		Slug:       credit.Slug,
		CreditRole: credit.CreditRole,
		ArtistId:   credit.ArtistID,
		MemberId:   credit.MemberID,
	}
	if credit.MemberID != nil {
		protoCredit.Member = projection.members[*credit.MemberID]
	}
	if note, ok := projection.noteByCreditID[credit.ID]; ok {
		protoCredit.Note = &note
	}
	protoCredit.ImageAsset = projectReleaseCreditImage(credit, protoCredit.Member, projection.imageAssets)
	return protoCredit, nil
}

func projectReleaseCreditImage(
	credit publicReleaseCreditRow,
	member *commonv1.MemberSummary,
	imageAssets map[string]*commonv1.AssetRef,
) *commonv1.AssetRef {
	if credit.ImageFileID == nil {
		if member == nil {
			return nil
		}
		return member.AvatarAsset
	}
	return imageAssets[*credit.ImageFileID]
}

func releaseCreditDisplayName(credit publicReleaseCreditRow, members map[string]*commonv1.MemberSummary) string {
	if credit.ArtistName != nil {
		return *credit.ArtistName
	}
	if credit.MemberID != nil && members[*credit.MemberID] != nil {
		return members[*credit.MemberID].GetNickname()
	}
	if credit.CreditedName != nil {
		return *credit.CreditedName
	}
	return ""
}

// getReleaseTracks gets tracks for a release
func (s *ReleaseService) getReleaseTracks(
	ctx context.Context,
	releaseID string,
	releaseStatus string,
	mediaAuthorization mediaasset.ContentDownloadOwnerAuthorization,
) ([]*openv1.ReleaseTrack, error) {
	var tracks []model.Track

	err := s.db.WithContext(ctx).
		Where("release_id = ?", releaseID).
		Order("track_number ASC").
		Find(&tracks).Error

	if err != nil {
		return nil, err
	}

	audioFileIDs := make([]string, 0, len(tracks))
	downloadRequests := make([]TrackDownloadAccessRequest, 0, len(tracks))
	for _, track := range tracks {
		if track.AudioOriginalFileID != nil && *track.AudioOriginalFileID != "" {
			audioFileIDs = append(audioFileIDs, *track.AudioOriginalFileID)
			downloadRequests = append(downloadRequests, TrackDownloadAccessRequest{
				TrackID: track.ID, FileID: *track.AudioOriginalFileID,
			})
		}
	}
	originalFiles, err := loadTrackOriginalFiles(ctx, s.db, audioFileIDs)
	if err != nil {
		return nil, err
	}
	waveformRefs, err := s.media.TrackWaveforms(ctx, s.db, audioFileIDs)
	if err != nil {
		return nil, err
	}
	spectrogramRefs, err := s.media.TrackSpectrograms(ctx, s.db, audioFileIDs)
	if err != nil {
		return nil, err
	}
	hlsRefs, err := s.media.TrackHLS(ctx, s.db, audioFileIDs)
	if err != nil {
		return nil, err
	}
	downloadByTrackID, err := s.downloads.Resolve(ctx, releaseID, releaseStatus, mediaAuthorization, downloadRequests, func(file MediaFile) (*commonv1.ExpiringMediaRef, error) {
		return s.media.DownloadRef(file)
	})
	if err != nil {
		return nil, err
	}
	trackIDs := make([]string, 0, len(tracks))
	for i := range tracks {
		trackIDs = append(trackIDs, tracks[i].ID)
	}
	creditsByTrack, err := s.loadTrackCredits(ctx, trackIDs)
	if err != nil {
		return nil, err
	}

	protoTracks := make([]*openv1.ReleaseTrack, 0, len(tracks))
	mediaSources := releaseTrackMediaSources{
		originalFiles:     originalFiles,
		playbackRefs:      hlsRefs,
		waveformRefs:      waveformRefs,
		spectrogramRefs:   spectrogramRefs,
		downloadByTrackID: downloadByTrackID,
	}
	for _, track := range tracks {
		mediaRefs, downloadAccess, err := s.resolveReleaseTrackMedia(track, mediaSources)
		if err != nil {
			return nil, err
		}

		protoTrack, projectionErr := projectPublicReleaseTrack(
			track,
			mediaRefs,
			creditsByTrack[track.ID],
			downloadAccess,
		)
		if projectionErr != nil {
			return nil, projectionErr
		}
		protoTracks = append(protoTracks, protoTrack)
	}

	return protoTracks, nil
}

type releaseTrackMediaSources struct {
	originalFiles     map[string]scopedMediaFile
	playbackRefs      map[string]*commonv1.HlsMediaRef
	waveformRefs      map[string]*commonv1.AssetRef
	spectrogramRefs   map[string]*commonv1.AssetRef
	downloadByTrackID map[string]TrackDownloadAuthorization
}

func (s *ReleaseService) resolveReleaseTrackMedia(
	track model.Track,
	sources releaseTrackMediaSources,
) (trackMediaDeliveryRefs, *openv1.FileDownloadAccess, error) {
	mediaRefs := trackMediaDeliveryRefs{}
	downloadAccess := unavailableFileDownloadAccess()
	if track.AudioOriginalFileID == nil || *track.AudioOriginalFileID == "" {
		return mediaRefs, downloadAccess, nil
	}
	fileID := *track.AudioOriginalFileID
	mediaRefs.source = sources.originalFiles[fileID]
	mediaRefs.playback = sources.playbackRefs[fileID]
	mediaRefs.waveform = sources.waveformRefs[fileID]
	mediaRefs.spectrogram = sources.spectrogramRefs[fileID]
	if resolved, exists := sources.downloadByTrackID[track.ID]; exists && resolved.Access != nil {
		downloadAccess = resolved.Access
		mediaRefs.download = resolved.Download
	}
	if downloadAccess.GetAction() != openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_DOWNLOAD {
		return mediaRefs, downloadAccess, nil
	}
	return mediaRefs, downloadAccess, nil
}

func projectPublicReleaseTrack(
	track model.Track,
	mediaRefs trackMediaDeliveryRefs,
	credits []*openv1.TrackCredit,
	downloadAccess *openv1.FileDownloadAccess,
) (*openv1.ReleaseTrack, error) {
	pt := &openv1.ReleaseTrack{
		Id:          track.ID,
		Title:       track.Title,
		TrackNumber: int32(track.TrackNumber),
		Credits:     credits,
	}
	if downloadAccess == nil {
		downloadAccess = unavailableFileDownloadAccess()
	}
	pt.DownloadAvailability = downloadAccess.Availability
	pt.DownloadAction = downloadAccess.Action

	if track.DurationSeconds != nil {
		durationMs := int32(*track.DurationSeconds * 1000)
		pt.DurationMs = &durationMs
	}

	delivery, err := projectTrackMediaDelivery(
		track.DurationSeconds,
		mediaRefs,
	)
	if err != nil {
		return pt, err
	}
	pt.Delivery = delivery

	return pt, nil
}

// loadTrackCredits gets credits for every Track in one relation query.
type publicTrackCreditRow struct {
	TrackID      string  `gorm:"column:track_id"`
	ID           string  `gorm:"column:id"`
	CreditedName *string `gorm:"column:credited_name"`
	RoleName     *string `gorm:"column:role_name"`
	ArtistID     *string `gorm:"column:artist_id"`
	ArtistName   *string `gorm:"column:artist_name"`
	ArtistSlug   *string `gorm:"column:artist_slug"`
	ImageFileID  *string `gorm:"column:image_file_id"`
}

func (s *ReleaseService) loadTrackCredits(ctx context.Context, trackIDs []string) (map[string][]*openv1.TrackCredit, error) {
	var credits []publicTrackCreditRow

	err := s.db.WithContext(ctx).
		Table("track_credit").
		Select(`
			track_credit.track_id,
			track_credit.id,
			track_credit.credited_name,
			track_credit.credit_role as role_name,
			track_credit.artist_id,
			artist_image.file_id AS image_file_id
		`).
		Joins(`LEFT JOIN LATERAL (
			SELECT af.file_id
			FROM artist_file AS af
			WHERE af.artist_id = track_credit.artist_id
			ORDER BY af.sort_order ASC, af.created_at ASC
			LIMIT 1
		) AS artist_image ON TRUE`).
		Where("track_credit.track_id IN ?", trackIDs).
		Order("track_credit.track_id ASC, track_credit.sort_order ASC, track_credit.created_at ASC").
		Scan(&credits).Error

	if err != nil {
		return nil, err
	}
	artistIDs := make([]string, 0, len(credits))
	for _, credit := range credits {
		if credit.ArtistID != nil {
			artistIDs = append(artistIDs, *credit.ArtistID)
		}
	}
	summaries, err := s.artists.LoadArtistSummariesWithDB(ctx, s.db, artistIDs)
	if err != nil {
		return nil, err
	}
	for index := range credits {
		if credits[index].ArtistID == nil {
			continue
		}
		summary := summaries[*credits[index].ArtistID]
		if summary.Title != "" {
			credits[index].ArtistName = &summary.Title
		}
		if summary.Slug != "" {
			credits[index].ArtistSlug = &summary.Slug
		}
	}

	imageFileIDs := make([]string, 0, len(credits))
	for _, credit := range credits {
		if credit.ImageFileID != nil {
			imageFileIDs = append(imageFileIDs, *credit.ImageFileID)
		}
	}
	imageAssets, err := s.media.ReadySourceAssets(ctx, s.db, imageFileIDs, "image")
	if err != nil {
		return nil, err
	}
	result := make(map[string][]*openv1.TrackCredit, len(trackIDs))
	for _, credit := range credits {
		pc, err := s.projectTrackCredit(credit, imageAssets)
		if err != nil {
			return nil, err
		}
		result[credit.TrackID] = append(result[credit.TrackID], pc)
	}

	return result, nil
}

func (s *ReleaseService) projectTrackCredit(
	credit publicTrackCreditRow,
	imageAssets map[string]*commonv1.AssetRef,
) (*openv1.TrackCredit, error) {
	projected := &openv1.TrackCredit{
		Id: credit.ID, Name: credit.CreditedName, CreditRole: credit.RoleName,
	}
	if credit.ArtistID == nil {
		return projected, nil
	}
	artist := &openv1.ReleaseArtist{Id: *credit.ArtistID, Role: "credit", Slug: credit.ArtistSlug}
	if credit.ArtistName != nil {
		artist.Name = *credit.ArtistName
	}
	artist.ImageAsset = projectTrackCreditArtistImage(credit.ImageFileID, imageAssets)
	projected.Artist = artist
	return projected, nil
}

func projectTrackCreditArtistImage(
	fileID *string,
	imageAssets map[string]*commonv1.AssetRef,
) *commonv1.AssetRef {
	if fileID == nil {
		return nil
	}
	return imageAssets[*fileID]
}
