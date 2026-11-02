package public

import (
	"context"
	"errors"

	translationpostgres "github.com/echovisionlab/geul-api/internal/translation/postgres"

	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"

	"gorm.io/gorm"

	seriesadapter "github.com/echovisionlab/geul-api/internal/adapters/series"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	seriespublic "github.com/echovisionlab/geul-api/internal/series/public"
	"github.com/google/uuid"
)

type PublicReader struct {
	db        *gorm.DB
	cdnDomain string
}

var _ seriespublic.Reader = (*PublicReader)(nil)

func NewPublicReader(db *gorm.DB, cdnDomain string) *PublicReader {
	if db == nil {
		panic("series public reader: database is required")
	}
	return &PublicReader{db: db, cdnDomain: cdnDomain}
}

type publicSeriesRow struct {
	ID                  string
	Slug                string
	PostCount           int32
	FeaturedImageFileID *string `gorm:"column:featured_image_file_id"`
}

func (r *PublicReader) List(ctx context.Context, request seriespublic.ListSeriesQuery) (seriespublic.Page, error) {
	var rows []publicSeriesRow
	var total int64
	query := r.db.WithContext(ctx).Table("series AS s").
		Select(`s.id, s.slug, s.featured_image_file_id,
			(SELECT COUNT(DISTINCT p.id) FROM post p WHERE p.series_id = s.id AND p.status IN ?) AS post_count`, publicPostStatusValues()).
		Where("s.status = ?", model.SeriesStatusPublished)

	var err error
	query, err = seriesFilterConfig.ApplyFilters(query, request.Filters)
	if err != nil {
		return seriespublic.Page{}, err
	}
	if err := query.Count(&total).Error; err != nil {
		return seriespublic.Page{}, errs.Internal(err)
	}
	query, err = seriesSortConfig.ApplySort(query, request.Sorts)
	if err != nil {
		return seriespublic.Page{}, err
	}
	query = query.Order("s.id ASC")
	pagination := queryutil.GetPaginationParams(request.Limit, request.Offset, 100)
	if err := queryutil.ApplyPagination(query, pagination).Find(&rows).Error; err != nil {
		return seriespublic.Page{}, errs.Internal(err)
	}

	items, err := r.projectSeriesRows(ctx, rows, request.AcceptLanguage)
	if err != nil {
		return seriespublic.Page{}, err
	}
	return seriespublic.Page{Items: items, Total: int32(total), Limit: pagination.Limit, Offset: pagination.Offset}, nil
}

func (r *PublicReader) Get(ctx context.Context, idOrSlug, acceptLanguage string) (seriespublic.Series, error) {
	var row publicSeriesRow
	query := r.db.WithContext(ctx).Table("series AS s").
		Select(`s.id, s.slug, s.featured_image_file_id,
			(SELECT COUNT(DISTINCT p.id) FROM post p WHERE p.series_id = s.id AND p.status IN ?) AS post_count`, publicPostStatusValues()).
		Where("s.status = ?", model.SeriesStatusPublished)
	if _, err := uuid.Parse(idOrSlug); err == nil {
		query = query.Where("s.id = ?", idOrSlug)
	} else {
		query = query.Where("s.slug = ?", idOrSlug)
	}
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return seriespublic.Series{}, errs.NotFound("series", idOrSlug)
		}
		return seriespublic.Series{}, errs.Internal(err)
	}
	items, err := r.projectSeriesRows(ctx, []publicSeriesRow{row}, acceptLanguage)
	if err != nil {
		return seriespublic.Series{}, err
	}
	if len(items) != 1 {
		return seriespublic.Series{}, errs.NotFound("series", idOrSlug)
	}
	return items[0], nil
}

func (r *PublicReader) projectSeriesRows(
	ctx context.Context,
	rows []publicSeriesRow,
	acceptLanguage string,
) ([]seriespublic.Series, error) {
	ids := make([]string, 0, len(rows))
	fileIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
		if row.FeaturedImageFileID != nil {
			fileIDs = append(fileIDs, *row.FeaturedImageFileID)
		}
	}
	selections, err := publiccontent.ResolveBatch(ctx, r.db, seriesLocalizationSpec, ids, acceptLanguage, translationpostgres.NewRuntimeSettingsReader(r.db))
	if err != nil {
		return nil, errs.Internal(err)
	}
	for _, row := range rows {
		selection, ok := selections[row.ID]
		if !ok {
			continue
		}
		selection, err = publiccontent.ResolveOGConsistency(ctx, r.db, seriesLocalizationSpec, row.ID, selection,
			func(ctx context.Context, assetID string) (bool, error) {
				var count int64
				err := r.db.WithContext(ctx).Model(&model.PublicAsset{}).
					Where("id = ? AND status = ? AND file_size IS NOT NULL AND octet_length(sha256) = 32", assetID, model.PublicAssetStatusReady).
					Count(&count).Error
				return count != 0, err
			},
		)
		if err != nil {
			return nil, errs.Internal(err)
		}
		selections[row.ID] = selection
	}
	ogIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		if selection, ok := selections[row.ID]; ok && selection.OgAssetID != nil {
			ogIDs = append(ogIDs, *selection.OgAssetID)
		}
	}
	featuredAssets, ogAssets, err := r.loadReadySeriesAssetRefs(ctx, fileIDs, ogIDs)
	if err != nil {
		return nil, errs.Internal(err)
	}

	items := make([]seriespublic.Series, 0, len(rows))
	for _, row := range rows {
		selection := selections[row.ID]
		if selection.Title == nil {
			return nil, errs.InternalMsg("published series is missing localized title")
		}
		item := seriespublic.Series{
			ID:          row.ID,
			Title:       *selection.Title,
			Slug:        row.Slug,
			Description: selection.Summary,
			PostCount:   row.PostCount,
			Localization: seriespublic.Localization{
				RequestedLocale: selection.RequestedLocale, DisplayedLocale: selection.DisplayedLocale,
				SourceLocale: selection.SourceLocale, IsFallback: selection.IsFallback, IsOriginal: selection.IsOriginal,
				FallbackReason: int32(selection.FallbackReason), AvailableLocales: selection.AvailableLocales,
			},
		}
		if row.FeaturedImageFileID != nil {
			item.FeaturedImageAsset = seriesadapter.AssetFromRef(featuredAssets[*row.FeaturedImageFileID])
		}
		if selection.OgAssetID != nil {
			item.OgAsset = seriesadapter.AssetFromRef(ogAssets[*selection.OgAssetID])
		}
		items = append(items, item)
	}
	return items, nil
}

func (r *PublicReader) loadReadySeriesAssetRefs(
	ctx context.Context,
	featuredImageFileIDs []string,
	ogAssetIDs []string,
) (map[string]*mediaasset.Reference, map[string]*mediaasset.Reference, error) {
	featuredRefs := make(map[string]*mediaasset.Reference, len(featuredImageFileIDs))
	ogRefs := make(map[string]*mediaasset.Reference, len(ogAssetIDs))
	if len(featuredImageFileIDs) == 0 && len(ogAssetIDs) == 0 {
		return featuredRefs, ogRefs, nil
	}

	featuredWanted := make(map[string]struct{}, len(featuredImageFileIDs))
	for _, fileID := range featuredImageFileIDs {
		featuredWanted[fileID] = struct{}{}
	}
	ogWanted := make(map[string]struct{}, len(ogAssetIDs))
	for _, assetID := range ogAssetIDs {
		ogWanted[assetID] = struct{}{}
	}

	query := r.db.WithContext(ctx).Model(&model.PublicAsset{}).
		Where("status = ?", model.PublicAssetStatusReady)
	switch {
	case len(featuredImageFileIDs) != 0 && len(ogAssetIDs) != 0:
		query = query.Where(
			"((source_file_id IN ? AND kind = ?) OR id IN ?)",
			featuredImageFileIDs,
			"image",
			ogAssetIDs,
		)
	case len(featuredImageFileIDs) != 0:
		query = query.Where("source_file_id IN ? AND kind = ?", featuredImageFileIDs, "image")
	default:
		query = query.Where("id IN ?", ogAssetIDs)
	}

	var assets []model.PublicAsset
	if err := query.Order("source_file_id ASC, created_at DESC, id DESC").Find(&assets).Error; err != nil {
		return nil, nil, err
	}
	lifecycle := mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(r.db), r.cdnDomain)
	for _, asset := range assets {
		if asset.FileSize == nil || len(asset.SHA256) != 32 {
			continue
		}
		_, exactOG := ogWanted[asset.ID]
		featured := false
		if asset.SourceFileID != nil && asset.Kind == "image" {
			_, featured = featuredWanted[*asset.SourceFileID]
		}
		if !exactOG && !featured {
			continue
		}
		ref, err := lifecycle.AssetRef(mediaassetpostgres.Asset(asset))
		if err != nil {
			return nil, nil, err
		}
		if exactOG {
			ogRefs[asset.ID] = ref
		}
		if featured {
			fileID := *asset.SourceFileID
			if featuredRefs[fileID] == nil {
				featuredRefs[fileID] = ref
			}
		}
	}
	return featuredRefs, ogRefs, nil
}

var seriesLocalizationSpec = publiccontent.Spec{
	EntityType:   "series",
	TableName:    "series_translation",
	SelectClause: "locale, title, summary, content_json, content_html, content_text, og_asset_id",
}

func publicPostStatusValues() []string {
	return []string{
		string(model.PostStatusPublished),
		string(model.PostStatusArchived),
	}
}
