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

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	releasepkg "github.com/echovisionlab/geul-api/internal/release"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var releaseLocalizationSpec = publiccontent.Spec{
	EntityType:   "release",
	TableName:    "release_translation",
	SelectClause: "locale, NULL::text AS title, NULL::text AS summary, NULL::jsonb AS content_json, NULL::text AS content_html, NULL::text AS content_text, NULL::uuid AS og_asset_id",
}

// Deps are the collaborators of the public Release reads; all are
// required.
type Deps struct {
	DB            *gorm.DB
	SpiceDB       *auth.SpiceDBClient
	ContentBlocks *contentblock.Store
	Media         MediaRuntime
	Downloads     DownloadAccessResolver
	Members       releasepkg.MemberSummaryLoader
	Artists       releasepkg.ArtistSummaryLoader
	Labels        releasepkg.LabelSummaryLoader
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"DB": d.DB == nil, "SpiceDB": d.SpiceDB == nil, "ContentBlocks": d.ContentBlocks == nil,
		"Media": d.Media == nil, "Downloads": d.Downloads == nil, "Members": d.Members == nil,
		"Artists": d.Artists == nil, "Labels": d.Labels == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("public release dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the public Release reads.
type Service struct {
	db            *gorm.DB
	spiceDB       *auth.SpiceDBClient
	contentBlocks *contentblock.Store
	media         MediaRuntime
	downloads     DownloadAccessResolver
	members       releasepkg.MemberSummaryLoader
	artists       releasepkg.ArtistSummaryLoader
	labels        releasepkg.LabelSummaryLoader
}

// NewService validates deps and returns the public Release reads.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	return &Service{
		db: deps.DB, spiceDB: deps.SpiceDB, contentBlocks: deps.ContentBlocks, media: deps.Media,
		downloads: deps.Downloads, members: deps.Members, artists: deps.Artists, labels: deps.Labels,
	}, nil
}

// GetReleaseQuery names a Release by slug or ID. A draft is visible to a
// caller that can view it or that holds a share link to it.
type GetReleaseQuery struct {
	SlugOrID       string
	ShareToken     string
	SharePassword  string
	AcceptLanguage string
}

// ListReleasesQuery pages through published Releases. A Limit of zero takes
// the default page of 20.
type ListReleasesQuery struct {
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
	Limit   int32
	Offset  int32
}

// ReleasePage is one page of published Release summaries.
type ReleasePage struct {
	Releases []*openv1.ReleaseSummary
	Total    int64
	Limit    int32
	Offset   int32
}

// GetRelease retrieves a release by slug or ID
// - UUID-first: if input is valid UUID, query by ID; otherwise query by slug
// - share_token allows access to draft releases
// - authenticated accounts with the exact SpiceDB view permission can access drafts
func (s *Service) GetRelease(ctx context.Context, query GetReleaseQuery) (*openv1.Release, error) {
	var release model.Release
	var err error
	if releasepkg.IsValidUUID(query.SlugOrID) {
		err = s.db.WithContext(ctx).First(&release, "id = ?", query.SlugOrID).Error
	} else {
		err = s.db.WithContext(ctx).First(&release, "slug = ?", query.SlugOrID).Error
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, releaseNotFound()
	}
	if err != nil {
		return nil, err
	}
	mediaAuthorization, err := s.authorizeReleaseMedia(ctx, &release, query)
	if err != nil {
		return nil, err
	}
	return s.buildRelease(ctx, query.AcceptLanguage, &release, mediaAuthorization)
}

// authorizeReleaseMedia decides how the caller reaches the Release and its
// media: a published Release is public, and a draft needs the view
// permission or a share link.
func (s *Service) authorizeReleaseMedia(
	ctx context.Context,
	release *model.Release,
	query GetReleaseQuery,
) (mediaasset.ContentDownloadOwnerAuthorization, error) {
	authorization := mediaasset.ContentDownloadOwnerAuthorization{
		ResourceType: "release",
		ResourceID:   release.ID,
		Status:       string(release.Status),
		Mode:         mediaasset.ContentDownloadOwnerAccessPublic,
	}
	if release.ContentDocumentID != nil {
		authorization.DocumentID = *release.ContentDocumentID
	}
	if release.Status == model.ReleaseStatusPublished {
		return authorization, nil
	}
	allowed, err := hasDraftReleaseView(ctx, s.spiceDB, release.ID)
	if err != nil {
		return authorization, fmt.Errorf("check release draft view permission: %w", err)
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
		ctx, s.db, query.ShareToken, query.SharePassword,
		managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_RELEASE, release.ID, "release",
	)
	if err != nil {
		return authorization, err
	}
	authorization.Mode = mediaasset.ContentDownloadOwnerAccessShare
	authorization.ShareLink = mediaasset.ContentDownloadShareLinkWitnessFromModel(link)
	return authorization, nil
}

// ListReleases returns a page of published releases.
func (s *Service) ListReleases(ctx context.Context, query ListReleasesQuery) (ReleasePage, error) {
	filtered, remainingFilters, err := applyReleaseRelationFilters(
		s.db.WithContext(ctx).Model(&model.Release{}).Where("status = ?", model.ReleaseStatusPublished),
		query.Filters,
	)
	if err != nil {
		return ReleasePage{}, err
	}
	filtered, err = ReleaseFilterConfig.ApplyFilters(filtered, remainingFilters)
	if err != nil {
		return ReleasePage{}, err
	}
	var page ReleasePage
	if err := filtered.Count(&page.Total).Error; err != nil {
		return ReleasePage{}, err
	}
	sorted, err := ReleaseSortConfig.ApplySort(filtered, query.Sorts)
	if err != nil {
		return ReleasePage{}, err
	}
	pagination := queryutil.GetPaginationParams(query.Limit, query.Offset, 20)
	page.Limit, page.Offset = pagination.Limit, pagination.Offset
	var releases []model.Release
	if err := queryutil.ApplyPagination(sorted, pagination).Find(&releases).Error; err != nil {
		return ReleasePage{}, err
	}
	page.Releases, err = s.releaseSummaries(ctx, releases)
	return page, err
}

// releaseRelationFilters are the list filters on a Release relation, which
// FilterConfig cannot express: the field and its relation table and column.
var releaseRelationFilters = map[string]struct{ table, column string }{
	"label_id":    {"release_label", "label_id"},
	"artist_id":   {"release_artist", "artist_id"},
	"category_id": {"release_category", "category_id"},
}

// applyReleaseRelationFilters narrows query by the relation filters and
// returns the filters FilterConfig applies.
func applyReleaseRelationFilters(query *gorm.DB, filters []*commonv1.FilterSpec) (*gorm.DB, []*commonv1.FilterSpec, error) {
	var remaining []*commonv1.FilterSpec
	for _, filter := range filters {
		relation, ok := releaseRelationFilters[filter.GetField()]
		if !ok {
			remaining = append(remaining, filter)
			continue
		}
		subquery := "id IN (SELECT release_id FROM " + relation.table + " WHERE " + relation.column
		switch filter.GetOp() {
		case commonv1.FilterOp_FILTER_OP_EQ:
			query = query.Where(subquery+" = ?)", filter.GetValue())
		case commonv1.FilterOp_FILTER_OP_IN:
			query = query.Where(subquery+" IN ?)", filter.GetValues())
		default:
			return nil, nil, domainerr.New(domainerr.Invalid, filter.GetField(), fmt.Sprintf(
				"invalid filter operation '%s' for field '%s'", filter.GetOp().String(), filter.GetField(),
			))
		}
	}
	return query, remaining, nil
}

func (s *Service) releaseSummaries(ctx context.Context, releases []model.Release) ([]*openv1.ReleaseSummary, error) {
	releaseIDs := collectReleaseIDs(releases)
	sourceTitles, err := releasepkg.LoadPublicSourceTitles(ctx, s.db, releaseIDs)
	if err != nil {
		return nil, err
	}
	for i := range releases {
		releases[i].Title = sourceTitles[releases[i].ID]
	}
	artworkAssets, err := s.loadReleaseArtworkAssets(ctx, releaseIDs)
	if err != nil {
		return nil, err
	}
	artistsByRelease, err := s.loadReleaseArtists(ctx, releaseIDs)
	if err != nil {
		return nil, err
	}
	summaries := make([]*openv1.ReleaseSummary, 0, len(releases))
	for i := range releases {
		release := &releases[i]
		summaries = append(summaries, toReleaseSummary(release, artworkAssets[release.ID], artistsByRelease[release.ID]))
	}
	return summaries, nil
}

func releaseNotFound() *domainerr.Error {
	return domainerr.New(domainerr.NotFound, "", "release not found")
}

// buildRelease projects the Release in one snapshot. A Release that was
// public when it was found must still be published.
func (s *Service) buildRelease(
	ctx context.Context,
	acceptLanguage string,
	release *model.Release,
	mediaAuthorization mediaasset.ContentDownloadOwnerAuthorization,
) (*openv1.Release, error) {
	wasPublic := release.Status == model.ReleaseStatusPublished
	var projected *openv1.Release
	err := mutation.ReadLocked(ctx, s.db, func(tx *gorm.DB) error {
		projection := *s
		projection.db = tx
		var err error
		projected, err = projection.buildReleaseInTransaction(ctx, acceptLanguage, release, mediaAuthorization, wasPublic)
		return err
	})
	return projected, err
}

func (s *Service) buildReleaseInTransaction(
	ctx context.Context,
	acceptLanguage string,
	release *model.Release,
	mediaAuthorization mediaasset.ContentDownloadOwnerAuthorization,
	wasPublic bool,
) (*openv1.Release, error) {
	releaseID := release.ID
	var current model.Release
	if err := s.db.WithContext(ctx).Where("id = ?", releaseID).Take(&current).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, releaseNotFound()
		}
		return nil, err
	}
	if wasPublic && current.Status != model.ReleaseStatusPublished {
		return nil, releaseNotFound()
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
		Id:               release.ID,
		Title:            release.Title,
		Type:             releaseTypeToOpen(release.Type),
		Document:         content.Document,
		Status:           releaseStatusToOpen(release.Status),
		CreatedAt:        timestamppb.New(release.CreatedAt),
		UpdatedAt:        timestamppb.New(release.UpdatedAt),
		Revision:         content.Revision,
		Slug:             release.Slug,
		LocalizationInfo: publiccontent.ToProtoLocalizationInfo(localization),
		CatalogNumber:    release.CatalogNumber,
		ReleaseDate:      timestampPtr(release.ReleaseDate),
		PublishedAt:      timestampPtr(release.PublishedAt),
		SpotifyUrl:       release.SpotifyURL,
		AppleMusicUrl:    release.AppleMusicURL,
		BandcampUrl:      release.BandcampURL,
		YoutubeMusicUrl:  release.YoutubeMusicURL,
	}
	if err := s.projectReleaseRelations(ctx, protoRelease, release, localization, mediaAuthorization); err != nil {
		return nil, err
	}
	return protoRelease, nil
}

// projectReleaseRelations adds the artwork, relations, credits and tracks
// of release to its projection.
func (s *Service) projectReleaseRelations(
	ctx context.Context,
	protoRelease *openv1.Release,
	release *model.Release,
	localization publiccontent.Selection,
	mediaAuthorization mediaasset.ContentDownloadOwnerAuthorization,
) error {
	artworkAssets, err := s.loadReleaseArtworkAssets(ctx, []string{release.ID})
	if err != nil {
		return err
	}
	protoRelease.ArtworkAsset = artworkAssets[release.ID]
	protoRelease.OgAsset = protoRelease.ArtworkAsset
	artistsByRelease, err := s.loadReleaseArtists(ctx, []string{release.ID})
	if err != nil {
		return err
	}
	protoRelease.Artists = artistsByRelease[release.ID]
	if protoRelease.Labels, err = s.getReleaseLabels(ctx, release.ID); err != nil {
		return err
	}
	if protoRelease.Genres, err = s.getReleaseGenres(ctx, release.ID); err != nil {
		return err
	}
	if protoRelease.Styles, err = s.getReleaseStyles(ctx, release.ID); err != nil {
		return err
	}
	if protoRelease.Formats, err = s.getReleaseFormats(ctx, release.ID); err != nil {
		return err
	}
	protoRelease.Credits, err = s.getReleaseCredits(ctx, release.ID, localization.SourceLocale, localization.DisplayedLocale)
	if err != nil {
		return err
	}
	protoRelease.Tracks, err = s.getReleaseTracks(ctx, release.ID, string(release.Status), mediaAuthorization)
	return err
}

func timestampPtr(value *time.Time) *timestamppb.Timestamp {
	if value == nil {
		return nil
	}
	return timestamppb.New(*value)
}

// toReleaseSummary converts a release to a summary (no tracks)
func toReleaseSummary(
	release *model.Release,
	artworkAsset *commonv1.AssetRef,
	artists []*openv1.ReleaseArtist,
) *openv1.ReleaseSummary {
	return &openv1.ReleaseSummary{
		Id:           release.ID,
		Title:        release.Title,
		Type:         releaseTypeToOpen(release.Type),
		Slug:         release.Slug,
		ReleaseDate:  timestampPtr(release.ReleaseDate),
		PublishedAt:  timestampPtr(release.PublishedAt),
		OgAsset:      artworkAsset,
		ArtworkAsset: artworkAsset,
		Artists:      artists,
	}
}

func collectReleaseIDs(releases []model.Release) []string {
	ids := make([]string, 0, len(releases))
	for _, release := range releases {
		ids = append(ids, release.ID)
	}
	return ids
}

func (s *Service) loadReleaseArtworkAssets(
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
