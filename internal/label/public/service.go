package public

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
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

// Deps are the collaborators of the public Label reads; all are required.
type Deps struct {
	DB      *gorm.DB
	Content ContentProjection
	Draft   DraftAccess
	Media   MediaHydrator
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"DB": d.DB == nil, "Content": d.Content == nil, "Draft": d.Draft == nil, "Media": d.Media == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("public label dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the public Label reads.
type Service struct {
	db      *gorm.DB
	content ContentProjection
	draft   DraftAccess
	media   MediaHydrator
}

// NewService validates deps and returns the public Label reads.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	return &Service{db: deps.DB, content: deps.Content, draft: deps.Draft, media: deps.Media}, nil
}

var errLabelNotFound = domainerr.New(domainerr.NotFound, "", "label not found")

// ListLabelsQuery pages through published Labels.
type ListLabelsQuery struct {
	Limit   int32
	Offset  int32
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
}

// LabelPage is one page of published Label summaries.
type LabelPage struct {
	Labels     []*openv1.LabelSummary
	Pagination *commonv1.PaginationResponse
}

// List returns published labels with filtering and sorting
func (s *Service) List(ctx context.Context, query ListLabelsQuery) (LabelPage, error) {
	labels := s.db.WithContext(ctx).Model(&model.Label{}).Where("status = ?", model.LabelStatusPublished)
	labels, err := LabelFilterConfig.ApplyFilters(labels, query.Filters)
	if err != nil {
		return LabelPage{}, err
	}
	var total int64
	if err := labels.Count(&total).Error; err != nil {
		return LabelPage{}, err
	}
	labels, err = LabelSortConfig.ApplySort(labels, query.Sorts)
	if err != nil {
		return LabelPage{}, err
	}
	pagination := queryutil.GetPaginationParams(query.Limit, query.Offset, 20)
	var rows []model.Label
	if err := queryutil.ApplyPagination(labels, pagination).Find(&rows).Error; err != nil {
		return LabelPage{}, err
	}
	summaries, err := s.labelSummaries(ctx, rows)
	if err != nil {
		return LabelPage{}, err
	}
	return LabelPage{Labels: summaries, Pagination: &commonv1.PaginationResponse{
		Total:   int32(total),
		Limit:   pagination.Limit,
		Offset:  pagination.Offset,
		HasMore: pagination.Offset+int32(len(rows)) < int32(total),
	}}, nil
}

// labelSummaries renders labels with their source titles, ready logos and
// OG images.
func (s *Service) labelSummaries(ctx context.Context, labels []model.Label) ([]*openv1.LabelSummary, error) {
	sourceTitles, err := s.content.LoadSourceTitles(ctx, s.db, collectLabelIDs(labels))
	if err != nil {
		return nil, err
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
		return nil, err
	}
	ogRows, err := s.media.ReadyForAssetIDs(ctx, s.db, ogAssetIDs)
	if err != nil {
		return nil, err
	}
	summaries := make([]*openv1.LabelSummary, 0, len(labels))
	for _, label := range labels {
		summaries = append(summaries, labelSummary(label, sourceTitles[label.ID], logoRows, ogRows))
	}
	return summaries, nil
}

func labelSummary(label model.Label, name string, logoRows, ogRows map[string]*commonv1.AssetRef) *openv1.LabelSummary {
	summary := &openv1.LabelSummary{
		Id: label.ID, Name: name, Slug: label.Slug, CountryCode: label.CountryCode, Website: label.Website,
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
	return summary
}

// GetLabelQuery reads one Label by ID or slug. A draft Label needs a share
// token (and its password) or the caller's exact view permission.
// AcceptLanguage picks the displayed locale.
type GetLabelQuery struct {
	SlugOrID       string
	ShareToken     string
	SharePassword  string
	AcceptLanguage string
}

// Get retrieves a label by slug or ID
// - UUID-first: if input is valid UUID, query by ID; otherwise query by slug
// - share_token allows access to draft labels
// - authenticated accounts with the exact SpiceDB view permission can access drafts
func (s *Service) Get(ctx context.Context, query GetLabelQuery) (*openv1.Label, error) {
	var label model.Label
	var err error
	if isValidUUID(query.SlugOrID) {
		err = s.db.WithContext(ctx).First(&label, "id = ?", query.SlugOrID).Error
	} else {
		err = s.db.WithContext(ctx).First(&label, "slug = ?", query.SlugOrID).Error
	}
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errLabelNotFound
		}
		return nil, err
	}
	if label.Status != model.LabelStatusPublished {
		if err := s.draft.Require(ctx, label.ID, query.ShareToken, query.SharePassword); err != nil {
			return nil, err
		}
	}
	return s.buildLabelResponse(ctx, query.AcceptLanguage, &label)
}

// buildLabelResponse projects the Label in one snapshot that waits out a
// concurrent writer of its row.
func (s *Service) buildLabelResponse(
	ctx context.Context,
	acceptLanguage string,
	label *model.Label,
) (*openv1.Label, error) {
	wasPublic := label.Status == model.LabelStatusPublished
	var response *openv1.Label
	err := mutation.LockingRead(ctx, s.db, func(tx *gorm.DB) error {
		projection := *s
		projection.db = tx
		var err error
		response, err = projection.buildLabelResponseInTransaction(
			ctx, acceptLanguage, label, wasPublic,
		)
		return err
	})
	return response, err
}

func (s *Service) buildLabelResponseInTransaction(
	ctx context.Context,
	acceptLanguage string,
	label *model.Label,
	wasPublic bool,
) (*openv1.Label, error) {
	labelID := label.ID
	var current model.Label
	if err := s.db.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
		Where("id = ?", labelID).
		Take(&current).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errLabelNotFound
		}
		return nil, err
	}
	if wasPublic && current.Status != model.LabelStatusPublished {
		return nil, errLabelNotFound
	}
	*label = current
	localization, err := publiccontent.Resolve(ctx, s.db, labelLocalizationSpec, labelID, acceptLanguage)
	if err != nil {
		slog.ErrorContext(ctx, "Label localization lookup failed; serving the unlocalized Label", "domain", "label", "operation", "resolve_label_localization", failurecause.Attr(err), "label_id", labelID)
	}
	content, err := s.content.LoadDocument(ctx, s.db, labelID, localization.DisplayedLocale)
	if err != nil {
		return nil, err
	}
	protoLabel := &openv1.Label{
		Id:          label.ID,
		Name:        localizedLabelName(content.SourceTitle, localization.Title),
		Document:    content.Document,
		SocialLinks: label.SocialLinks,
		Status:      publicLabelStatus(label.Status),
		CreatedAt:   timestamppb.New(label.CreatedAt),
		UpdatedAt:   timestamppb.New(label.UpdatedAt),
		Revision:    content.Revision,
		Slug:        label.Slug,
		CountryCode: label.CountryCode,
		Website:     label.Website,
	}
	if label.PublishedAt != nil {
		protoLabel.PublishedAt = timestamppb.New(*label.PublishedAt)
	}
	if labelHasEffectiveLogo(label.LogoLightFileID, label.LogoDarkFileID) {
		// Label OG is one locale-independent, logo-only derivative. Localized
		// description selection must never participate in image resolution.
		protoLabel.OgAsset, err = s.media.ResolveOg(ctx, s.db, label.OgAssetID)
		if err != nil {
			return nil, err
		}
	}
	s.applyLabelImageAssets(ctx, protoLabel, label.LogoLightFileID, label.LogoDarkFileID)
	if err := s.applyLabelRelations(ctx, protoLabel, label.ParentLabelID); err != nil {
		return nil, err
	}
	protoLabel.LocalizationInfo = publiccontent.ToProtoLocalizationInfo(localization)
	return protoLabel, nil
}

// applyLabelRelations adds the relation counts, the published parent, and
// the first 12 published Artists and Releases.
func (s *Service) applyLabelRelations(ctx context.Context, protoLabel *openv1.Label, parentLabelID *string) error {
	var err error
	protoLabel.ReleaseCount, protoLabel.ArtistCount, err = s.getLabelRelationCounts(ctx, protoLabel.Id)
	if err != nil {
		return err
	}
	if parentLabelID != nil {
		if parentLabel := s.getParentLabel(ctx, *parentLabelID); parentLabel != nil {
			protoLabel.ParentLabel = parentLabel
		}
	}
	if protoLabel.Artists, err = s.getLabelArtistsPreview(ctx, protoLabel.Id); err != nil {
		return err
	}
	protoLabel.Releases, err = s.getLabelReleasesPreview(ctx, protoLabel.Id)
	return err
}

// getLabelArtistsPreview gets first 12 published artists for a label
func (s *Service) getLabelArtistsPreview(ctx context.Context, labelID string) ([]*openv1.LabelArtist, error) {
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
		Where("artist_label.label_id = ? AND artist.status = ?", labelID, model.ArtistStatusPublished).
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
func (s *Service) getLabelReleasesPreview(ctx context.Context, labelID string) ([]*openv1.LabelRelease, error) {
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

func (s *Service) applyLabelImageAssets(ctx context.Context, label *openv1.Label, lightFileID, darkFileID *string) {
	if label == nil {
		return
	}
	label.ImageLightAsset = s.getOptionalLabelImageAsset(ctx, lightFileID)
	label.ImageDarkAsset = s.getOptionalLabelImageAsset(ctx, darkFileID)
}

func (s *Service) applyLabelSummaryImageAssets(ctx context.Context, label *openv1.LabelSummary, lightFileID, darkFileID *string) {
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

func (s *Service) getOptionalLabelImageAsset(ctx context.Context, fileID *string) *commonv1.AssetRef {
	if fileID == nil {
		return nil
	}
	assets, err := s.media.ReadyForSourceFiles(ctx, s.db, []string{*fileID}, "logo")
	if err != nil {
		slog.ErrorContext(ctx, "Label logo asset lookup failed; serving the Label without it", "domain", "label", "operation", "resolve_logo_asset", "outcome", "failed", failurecause.Attr(err), "file_id", *fileID)
		return nil
	}
	return assets[*fileID]
}

func (s *Service) getLabelRelationCounts(ctx context.Context, labelID string) (int32, int32, error) {
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
	`, labelID, managev1.ReleaseStatus_RELEASE_STATUS_PUBLISHED.String(), labelID, model.ArtistStatusPublished).Scan(&counts).Error; err != nil {
		return 0, 0, err
	}
	return counts.ReleaseCount, counts.ArtistCount, nil
}

// getParentLabel gets parent label summary
func (s *Service) getParentLabel(ctx context.Context, parentLabelID string) *openv1.LabelSummary {
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
		Where("id = ? AND status = ?", parentLabelID, model.LabelStatusPublished).
		Scan(&parent).Error

	if err != nil || parent.ID == "" {
		if err != nil {
			slog.ErrorContext(ctx, "Parent Label lookup failed; serving the Label without its parent", "domain", "label", "operation", "load_parent_label", "outcome", "failed", failurecause.Attr(err), "label_id", parentLabelID)
		}
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
		var err error
		summary.OgAsset, err = s.media.ResolveOg(ctx, s.db, parent.OgAssetID)
		if err != nil {
			slog.ErrorContext(ctx, "Parent Label OG asset lookup failed; serving the parent without it", "domain", "label", "operation", "resolve_parent_og_asset", "outcome", "failed", failurecause.Attr(err), "label_id", parentLabelID)
		}
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
