package public

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
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
		if errors.Is(err, gorm.ErrRecordNotFound) {
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
		if errors.Is(err, gorm.ErrRecordNotFound) {
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
		slog.ErrorContext(ctx, "failed to resolve release localization", "releaseId", releaseID, failurecause.Attr(err))
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
