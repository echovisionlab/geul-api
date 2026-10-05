package public

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type ContentDocument struct {
	Document    *contentv1.LocalizedRichTextDocument
	Revision    string
	SourceTitle string
}

type ContentProjection interface {
	LoadSourceTitles(context.Context, *gorm.DB, []string) (map[string]string, error)
	LoadDocument(context.Context, *gorm.DB, string, string) (ContentDocument, error)
}

type DraftAccess interface {
	Require(context.Context, string, string, string) error
}

type MediaHydrator interface {
	ReadyForSourceFiles(context.Context, *gorm.DB, []string, ...string) (map[string]*commonv1.AssetRef, error)
	ReadyForAssetIDs(context.Context, *gorm.DB, []string) (map[string]*commonv1.AssetRef, error)
	ResolveOg(context.Context, *gorm.DB, *string) (*commonv1.AssetRef, error)
}

type Dependencies struct {
	Content ContentProjection
	Draft   DraftAccess
	Media   MediaHydrator
}

// LabelService implements the public LabelService
type LabelService struct {
	openv1connect.UnimplementedLabelServiceHandler
	db      *gorm.DB
	content ContentProjection
	draft   DraftAccess
	media   MediaHydrator
}

// NewLabelService creates a new public LabelService
func NewLabelService(db *gorm.DB, dependencies Dependencies) *LabelService {
	if db == nil {
		panic("public label service dependencies are required")
	}
	if dependencies.Content == nil || dependencies.Draft == nil || dependencies.Media == nil {
		panic("public label service content, draft, and media dependencies are required")
	}
	return &LabelService{db: db, content: dependencies.Content, draft: dependencies.Draft, media: dependencies.Media}
}

// List returns published labels with filtering and sorting
func (s *LabelService) List(
	ctx context.Context,
	req *connect.Request[openv1.ListLabelsRequest],
) (*connect.Response[openv1.ListLabelsResponse], error) {
	// Base query - only published labels
	query := s.db.WithContext(ctx).Model(&model.Label{}).Where("status = ?", managev1.LabelStatus_LABEL_STATUS_PUBLISHED.String())

	// 1. Apply filters using LabelFilterConfig
	query, err := LabelFilterConfig.ApplyFilters(query, req.Msg.Filters)
	if err != nil {
		return nil, err
	}

	// Get total count before pagination
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}

	// 2. Sort
	query, err = LabelSortConfig.ApplySort(query, req.Msg.Sorts)
	if err != nil {
		return nil, err
	}

	// 3. Pagination
	pagination := queryutil.GetPaginationParams(
		req.Msg.Pagination.GetLimit(),
		req.Msg.Pagination.GetOffset(),
		20,
	)
	query = queryutil.ApplyPagination(query, pagination)

	// Fetch labels
	var labels []model.Label
	if err := query.Find(&labels).Error; err != nil {
		return nil, errs.Internal(err)
	}
	sourceTitles, err := s.content.LoadSourceTitles(ctx, s.db, collectLabelIDs(labels))
	if err != nil {
		return nil, errs.Internal(err)
	}
	for i := range labels {
		labels[i].Name = sourceTitles[labels[i].ID]
	}
	logoFileIDs := make([]string, 0, len(labels)*2)
	ogAssetIDs := make([]string, 0, len(labels))
	for _, label := range labels {
		for _, candidate := range []*string{label.LogoLightFileID, label.LogoDarkFileID} {
			if candidate != nil {
				logoFileIDs = append(logoFileIDs, *candidate)
			}
		}
		if labelHasEffectiveLogo(label.LogoLightFileID, label.LogoDarkFileID) && label.OgAssetID != nil {
			ogAssetIDs = append(ogAssetIDs, *label.OgAssetID)
		}
	}
	logoRows, err := s.media.ReadyForSourceFiles(ctx, s.db, logoFileIDs, "logo")
	if err != nil {
		return nil, errs.Internal(err)
	}
	ogRows, err := s.media.ReadyForAssetIDs(ctx, s.db, ogAssetIDs)
	if err != nil {
		return nil, errs.Internal(err)
	}
	// Convert to proto summaries
	summaries := make([]*openv1.LabelSummary, 0, len(labels))
	for _, label := range labels {
		summary := &openv1.LabelSummary{
			Id:   label.ID,
			Name: label.Name,
		}
		if label.Slug != nil {
			summary.Slug = label.Slug
		}
		if label.CountryCode != nil {
			summary.CountryCode = label.CountryCode
		}
		if label.Website != nil {
			summary.Website = label.Website
		}
		if label.PublishedAt != nil {
			summary.PublishedAt = timestamppb.New(*label.PublishedAt)
		}
		if labelHasEffectiveLogo(label.LogoLightFileID, label.LogoDarkFileID) && label.OgAssetID != nil {
			summary.OgAsset = ogRows[*label.OgAssetID]
		}
		if label.LogoLightFileID != nil {
			summary.ImageLightAsset = logoRows[*label.LogoLightFileID]
		}
		if label.LogoDarkFileID != nil {
			summary.ImageDarkAsset = logoRows[*label.LogoDarkFileID]
		}
		summaries = append(summaries, summary)
	}

	return connect.NewResponse(&openv1.ListLabelsResponse{
		Labels: summaries,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   pagination.Limit,
			Offset:  pagination.Offset,
			HasMore: pagination.Offset+int32(len(labels)) < int32(total),
		},
	}), nil
}

// Get retrieves a label by slug or ID
// - UUID-first: if input is valid UUID, query by ID; otherwise query by slug
// - share_token allows access to draft labels
// - authenticated accounts with the exact SpiceDB view permission can access drafts
func (s *LabelService) Get(
	ctx context.Context,
	req *connect.Request[openv1.GetLabelRequest],
) (*connect.Response[openv1.GetLabelResponse], error) {
	slugOrID := req.Msg.Slug
	shareToken := req.Msg.GetShareToken()
	sharePassword := req.Msg.GetSharePassword()

	var label model.Label
	var err error

	// UUID-first approach
	if isValidUUID(slugOrID) {
		err = s.db.WithContext(ctx).First(&label, "id = ?", slugOrID).Error
	} else {
		err = s.db.WithContext(ctx).First(&label, "slug = ?", slugOrID).Error
	}

	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFoundMsg("label not found")
		}
		return nil, errs.Internal(err)
	}

	// Check access for draft labels
	if label.Status != managev1.LabelStatus_LABEL_STATUS_PUBLISHED.String() {
		if err := s.requireDraftLabelAccess(ctx, label.ID, shareToken, sharePassword); err != nil {
			return nil, err
		}
	}

	return s.buildLabelResponse(ctx, req.Header().Get("Accept-Language"), &label)
}

func (s *LabelService) requireDraftLabelAccess(
	ctx context.Context,
	labelID string,
	shareToken string,
	sharePassword string,
) error {
	return s.draft.Require(ctx, labelID, shareToken, sharePassword)
}

// buildLabelResponse builds a GetLabelResponse with the label
func (s *LabelService) buildLabelResponse(
	ctx context.Context,
	acceptLanguage string,
	label *model.Label,
) (*connect.Response[openv1.GetLabelResponse], error) {
	wasPublic := label.Status == managev1.LabelStatus_LABEL_STATUS_PUBLISHED.String()
	var response *connect.Response[openv1.GetLabelResponse]
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		projection := *s
		projection.db = tx
		var err error
		response, err = projection.buildLabelResponseInTransaction(
			ctx, acceptLanguage, label, wasPublic,
		)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	return response, err
}

func (s *LabelService) buildLabelResponseInTransaction(
	ctx context.Context,
	acceptLanguage string,
	label *model.Label,
	wasPublic bool,
) (*connect.Response[openv1.GetLabelResponse], error) {
	labelID := label.ID
	var current model.Label
	if err := s.db.WithContext(ctx).
		Clauses(clause.Locking{Strength: "SHARE"}).
		Where("id = ?", labelID).
		Take(&current).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFoundMsg("label not found")
		}
		return nil, errs.Internal(err)
	}
	if wasPublic && current.Status != managev1.LabelStatus_LABEL_STATUS_PUBLISHED.String() {
		return nil, errs.NotFoundMsg("label not found")
	}
	*label = current
	localization, err := publiccontent.Resolve(ctx, s.db, labelLocalizationSpec, labelID, acceptLanguage)
	if err != nil {
		slog.Warn("failed to resolve label localization", "labelId", labelID, "error", err)
	}
	content, err := s.content.LoadDocument(ctx, s.db, labelID, localization.DisplayedLocale)
	if err != nil {
		return nil, err
	}
	label.Name = localizedLabelName(content.SourceTitle, localization.Title)

	protoLabel := &openv1.Label{
		Id:          label.ID,
		Name:        label.Name,
		Document:    content.Document,
		SocialLinks: label.SocialLinks,
		Status:      openv1.LabelStatus(openv1.LabelStatus_value[label.Status]),
		CreatedAt:   timestamppb.New(label.CreatedAt),
		UpdatedAt:   timestamppb.New(label.UpdatedAt),
		Revision:    content.Revision,
	}

	if label.Slug != nil {
		protoLabel.Slug = label.Slug
	}
	if label.CountryCode != nil {
		protoLabel.CountryCode = label.CountryCode
	}
	if label.Website != nil {
		protoLabel.Website = label.Website
	}
	if label.PublishedAt != nil {
		protoLabel.PublishedAt = timestamppb.New(*label.PublishedAt)
	}
	if labelHasEffectiveLogo(label.LogoLightFileID, label.LogoDarkFileID) {
		// Label OG is one locale-independent, logo-only derivative. Localized
		// description selection must never participate in image resolution.
		protoLabel.OgAsset, err = s.media.ResolveOg(ctx, s.db, label.OgAssetID)
		if err != nil {
			return nil, errs.Internal(err)
		}
	}
	s.applyLabelImageAssets(ctx, protoLabel, label.LogoLightFileID, label.LogoDarkFileID)

	protoLabel.ReleaseCount, protoLabel.ArtistCount, err = s.getLabelRelationCounts(ctx, label.ID)
	if err != nil {
		return nil, errs.Internal(err)
	}

	// Get parent label if exists
	if label.ParentLabelID != nil {
		if parentLabel := s.getParentLabel(ctx, *label.ParentLabelID); parentLabel != nil {
			protoLabel.ParentLabel = parentLabel
		}
	}

	// Get first 12 artists and releases for preview
	protoLabel.Artists, err = s.getLabelArtistsPreview(ctx, label.ID)
	if err != nil {
		return nil, errs.Internal(err)
	}
	protoLabel.Releases, err = s.getLabelReleasesPreview(ctx, label.ID)
	if err != nil {
		return nil, errs.Internal(err)
	}
	protoLabel.LocalizationInfo = publiccontent.ToProtoLocalizationInfo(localization)
	return connect.NewResponse(&openv1.GetLabelResponse{
		Label: protoLabel,
	}), nil
}

// getLabelArtistsPreview gets first 12 published artists for a label
func (s *LabelService) getLabelArtistsPreview(ctx context.Context, labelID string) ([]*openv1.LabelArtist, error) {
	var artists []struct {
		ID          string  `gorm:"column:id"`
		Name        string  `gorm:"column:name"`
		Slug        *string `gorm:"column:slug"`
		ImageFileID *string `gorm:"column:image_file_id"`
	}

	if err := s.db.WithContext(ctx).
		Table("artist_label").
		Select("artist.id, "+artistSourceTitleSQL("artist")+" AS name, artist.slug, artist_image.file_id AS image_file_id").
		Joins("JOIN artist ON artist.id = artist_label.artist_id").
		Joins(`
			LEFT JOIN LATERAL (
				SELECT artist_file.file_id
				FROM artist_file
				WHERE artist_file.artist_id = artist.id
				ORDER BY artist_file.sort_order ASC, artist_file.created_at ASC
				LIMIT 1
			) artist_image ON TRUE
		`).
		Where("artist_label.label_id = ? AND artist.status = ?", labelID, managev1.ArtistStatus_ARTIST_STATUS_PUBLISHED.String()).
		Order(artistSourceTitleSQL("artist") + " ASC").
		Limit(12).
		Scan(&artists).Error; err != nil {
		return nil, err
	}
	imageFileIDs := make([]string, 0, len(artists))
	for _, artist := range artists {
		if artist.ImageFileID != nil {
			imageFileIDs = append(imageFileIDs, *artist.ImageFileID)
		}
	}
	imageRows, err := s.media.ReadyForSourceFiles(ctx, s.db, imageFileIDs, "image")
	if err != nil {
		return nil, err
	}

	protoArtists := make([]*openv1.LabelArtist, 0, len(artists))
	for _, a := range artists {
		artist := &openv1.LabelArtist{
			Id:   a.ID,
			Name: a.Name,
		}
		if a.Slug != nil {
			artist.Slug = a.Slug
		}
		if a.ImageFileID != nil {
			artist.ImageAsset = imageRows[*a.ImageFileID]
		}
		protoArtists = append(protoArtists, artist)
	}

	return protoArtists, nil
}

// getLabelReleasesPreview gets first 12 published releases for a label
func (s *LabelService) getLabelReleasesPreview(ctx context.Context, labelID string) ([]*openv1.LabelRelease, error) {
	var releases []struct {
		ID            string     `gorm:"column:id"`
		Title         string     `gorm:"column:title"`
		Slug          *string    `gorm:"column:slug"`
		Type          string     `gorm:"column:type"`
		ReleaseDate   *time.Time `gorm:"column:release_date"`
		PublishedAt   *time.Time `gorm:"column:published_at"`
		ArtworkFileID *string    `gorm:"column:artwork_file_id"`
	}

	if err := s.db.WithContext(ctx).
		Table("release_label").
		Select("release.id, "+releaseSourceTitleSQL("release")+" AS title, release.slug, release.type, release.release_date, release.published_at, artwork.file_id AS artwork_file_id").
		Joins("JOIN release ON release.id = release_label.release_id").
		Joins(`LEFT JOIN LATERAL (
			SELECT release_file.file_id
			FROM release_file
			WHERE release_file.release_id = release.id
			ORDER BY release_file.sort_order ASC
			LIMIT 1
		) artwork ON TRUE`).
		Where("release_label.label_id = ? AND release.status = ?", labelID, managev1.ReleaseStatus_RELEASE_STATUS_PUBLISHED.String()).
		Order("release.release_date DESC NULLS LAST").
		Order("release.published_at DESC").
		Limit(12).
		Scan(&releases).Error; err != nil {
		return nil, err
	}
	releaseIDs := make([]string, 0, len(releases))
	artworkFileIDs := make([]string, 0, len(releases))
	for _, release := range releases {
		releaseIDs = append(releaseIDs, release.ID)
		if release.ArtworkFileID != nil {
			artworkFileIDs = append(artworkFileIDs, *release.ArtworkFileID)
		}
	}
	artworkRows, err := s.media.ReadyForSourceFiles(ctx, s.db, artworkFileIDs, "artwork", "image")
	if err != nil {
		return nil, err
	}
	type releaseArtistRow struct {
		ReleaseID  string  `gorm:"column:release_id"`
		ArtistID   string  `gorm:"column:artist_id"`
		ArtistName string  `gorm:"column:artist_name"`
		ArtistSlug *string `gorm:"column:artist_slug"`
	}
	var artistRows []releaseArtistRow
	if len(releaseIDs) > 0 {
		if err := s.db.WithContext(ctx).
			Table("release_artist").
			Select("release_artist.release_id, artist.id AS artist_id, "+artistSourceTitleSQL("artist")+" AS artist_name, artist.slug AS artist_slug").
			Joins("JOIN artist ON artist.id = release_artist.artist_id").
			Where("release_artist.release_id IN ?", releaseIDs).
			Order("release_artist.release_id, release_artist.sort_order ASC, release_artist.created_at ASC").
			Scan(&artistRows).Error; err != nil {
			return nil, err
		}
	}
	artistsByRelease := make(map[string][]*openv1.LabelReleaseArtist, len(releaseIDs))
	for _, row := range artistRows {
		artist := &openv1.LabelReleaseArtist{Id: row.ArtistID, Name: row.ArtistName, Slug: row.ArtistSlug}
		artistsByRelease[row.ReleaseID] = append(artistsByRelease[row.ReleaseID], artist)
	}

	protoReleases := make([]*openv1.LabelRelease, 0, len(releases))
	for _, r := range releases {
		release := &openv1.LabelRelease{
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
		if r.ArtworkFileID != nil {
			release.ArtworkAsset = artworkRows[*r.ArtworkFileID]
		}
		release.Artists = artistsByRelease[r.ID]
		protoReleases = append(protoReleases, release)
	}

	return protoReleases, nil
}

func (s *LabelService) applyLabelImageAssets(ctx context.Context, label *openv1.Label, lightFileID, darkFileID *string) {
	if label == nil {
		return
	}
	label.ImageLightAsset = s.getOptionalLabelImageAsset(ctx, lightFileID)
	label.ImageDarkAsset = s.getOptionalLabelImageAsset(ctx, darkFileID)
}

func (s *LabelService) applyLabelSummaryImageAssets(ctx context.Context, label *openv1.LabelSummary, lightFileID, darkFileID *string) {
	if label == nil {
		return
	}
	label.ImageLightAsset = s.getOptionalLabelImageAsset(ctx, lightFileID)
	label.ImageDarkAsset = s.getOptionalLabelImageAsset(ctx, darkFileID)
}

func labelHasEffectiveLogo(lightFileID, darkFileID *string) bool {
	return (lightFileID != nil && strings.TrimSpace(*lightFileID) != "") ||
		(darkFileID != nil && strings.TrimSpace(*darkFileID) != "")
}

func (s *LabelService) getOptionalLabelImageAsset(ctx context.Context, fileID *string) *commonv1.AssetRef {
	if fileID == nil {
		return nil
	}
	assets, err := s.media.ReadyForSourceFiles(ctx, s.db, []string{*fileID}, "logo")
	if err != nil {
		return nil
	}
	return assets[*fileID]
}

func (s *LabelService) getLabelRelationCounts(ctx context.Context, labelID string) (int32, int32, error) {
	var counts struct {
		ReleaseCount int32 `gorm:"column:release_count"`
		ArtistCount  int32 `gorm:"column:artist_count"`
	}
	if err := s.db.WithContext(ctx).Raw(`
		SELECT
			(SELECT COUNT(*) FROM release_label JOIN release ON release.id = release_label.release_id
			 WHERE release_label.label_id = ?::uuid AND release.status = ?)::integer AS release_count,
			(SELECT COUNT(*) FROM artist_label JOIN artist ON artist.id = artist_label.artist_id
			 WHERE artist_label.label_id = ?::uuid AND artist.status = ?)::integer AS artist_count
	`, labelID, managev1.ReleaseStatus_RELEASE_STATUS_PUBLISHED.String(), labelID, managev1.ArtistStatus_ARTIST_STATUS_PUBLISHED.String()).Scan(&counts).Error; err != nil {
		return 0, 0, err
	}
	return counts.ReleaseCount, counts.ArtistCount, nil
}

// getParentLabel gets parent label summary
func (s *LabelService) getParentLabel(ctx context.Context, parentLabelID string) *openv1.LabelSummary {
	var parent struct {
		ID              string  `gorm:"column:id"`
		Name            string  `gorm:"column:name"`
		Slug            *string `gorm:"column:slug"`
		LogoLightFileID *string `gorm:"column:logo_light_file_id"`
		LogoDarkFileID  *string `gorm:"column:logo_dark_file_id"`
		OgAssetID       *string `gorm:"column:og_asset_id"`
	}

	err := s.db.WithContext(ctx).
		Table("label").
		Select("id, "+labelSourceTitleSQL("label")+" AS name, slug, logo_light_file_id, logo_dark_file_id, og_asset_id").
		Where("id = ? AND status = ?", parentLabelID, managev1.LabelStatus_LABEL_STATUS_PUBLISHED.String()).
		Scan(&parent).Error

	if err != nil || parent.ID == "" {
		return nil
	}

	summary := &openv1.LabelSummary{
		Id:   parent.ID,
		Name: parent.Name,
	}
	if parent.Slug != nil {
		summary.Slug = parent.Slug
	}
	s.applyLabelSummaryImageAssets(ctx, summary, parent.LogoLightFileID, parent.LogoDarkFileID)
	if labelHasEffectiveLogo(parent.LogoLightFileID, parent.LogoDarkFileID) {
		summary.OgAsset, _ = s.media.ResolveOg(ctx, s.db, parent.OgAssetID)
	}

	return summary
}

func collectLabelIDs(labels []model.Label) []string {
	ids := make([]string, 0, len(labels))
	for _, label := range labels {
		ids = append(ids, label.ID)
	}
	return ids
}

func localizedLabelName(sourceTitle string, localizedTitle *string) string {
	if localizedTitle != nil {
		return *localizedTitle
	}
	return sourceTitle
}

func isValidUUID(value string) bool {
	_, err := uuid.Parse(value)
	return err == nil
}
