// Package workpublicpostgres is the PostgreSQL store of the public Work
// reads.
package workpublicpostgres

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	translationpostgres "github.com/echovisionlab/geul-api/internal/translation/postgres"

	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/work"
	workpostgres "github.com/echovisionlab/geul-api/internal/work/postgres"
	workpublic "github.com/echovisionlab/geul-api/internal/work/public"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// MapPlaces projects the Map Place a public Work shows.
type MapPlaces interface {
	Basic(*model.MapPlace) *openv1.MapPlaceBasic
}

// ReadyAssets reports whether a public asset is ready, in the read's
// transaction.
type ReadyAssets interface {
	IsReadyAsset(context.Context, *gorm.DB, string) (bool, error)
}

// Ports are the shared runtimes the public reads drive in their snapshot.
type Ports struct {
	ContentBlocks *contentblock.Store
	MapPlaces     MapPlaces
	Assets        ReadyAssets
}

func (p Ports) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"ContentBlocks": p.ContentBlocks == nil, "MapPlaces": p.MapPlaces == nil, "Assets": p.Assets == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("public work store ports are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// NewUnits returns the read snapshots of the public Work reads. The content
// document reads through the Work store over the same transaction.
func NewUnits(db *gorm.DB, ports Ports) (mutation.UnitOfWork[workpublic.Store], error) {
	if db == nil {
		return nil, errors.New("public work store database is required")
	}
	if err := ports.validate(); err != nil {
		return nil, err
	}
	return mutation.Postgres(db, func(tx *gorm.DB) workpublic.Store {
		return store{tx: tx, ports: ports, works: workpostgres.Bind(tx, workpostgres.Ports{ContentBlocks: ports.ContentBlocks}, nil).Store}
	}), nil
}

type store struct {
	tx    *gorm.DB
	ports Ports
	works work.Store
}

var _ workpublic.Store = store{}

// notFoundError keeps the driver's not-found error readable and in the
// chain while it answers work.ErrWorkNotFound.
type notFoundError struct{ error }

func (err notFoundError) Is(target error) bool { return target == work.ErrWorkNotFound }
func (err notFoundError) Unwrap() error        { return err.error }

var workLocalizationSpec = publiccontent.Spec{
	EntityType:   "work",
	TableName:    "work_translation",
	SelectClause: "locale, title, summary, NULL::jsonb AS content_json, NULL::text AS content_html, NULL::text AS content_text, og_asset_id",
}

var workFilterConfig = &queryutil.FilterConfig{
	Fields: map[string]queryutil.FieldDef{
		"search":   {Type: queryutil.TypeText, AllowedOps: queryutil.SearchOps, SearchColumns: []string{workpostgres.WorkSourceTitleSQL("work")}},
		"status":   {Column: "work.status", Type: queryutil.TypeEnum, AllowedOps: queryutil.EnumOps, EnumValues: workpublic.PublicStatuses()},
		"type":     {Column: "type", Type: queryutil.TypeEnum, AllowedOps: queryutil.EnumOps, EnumValues: workpostgres.WorkTypeValues()},
		"featured": {Column: "featured", Type: queryutil.TypeBool, AllowedOps: queryutil.BoolOps},
		"map_place_id": {Column: "map_place_id", Type: queryutil.TypeID, AllowedOps: []commonv1.FilterOp{
			commonv1.FilterOp_FILTER_OP_EQ, commonv1.FilterOp_FILTER_OP_IN,
			commonv1.FilterOp_FILTER_OP_IS_NULL, commonv1.FilterOp_FILTER_OP_IS_NOT_NULL,
		}, IsFK: true},
		"published_at": {Column: "published_at", Type: queryutil.TypeDate, AllowedOps: queryutil.DateOps},
		"period_year":  {Column: "year", Type: queryutil.TypeNumber, AllowedOps: queryutil.NumberOps},
		"period_month": {Column: "month", Type: queryutil.TypeNumber, AllowedOps: queryutil.NumberOps},
		"year":         {Column: "year", Type: queryutil.TypeNumber, AllowedOps: queryutil.NumberOps},
		"month":        {Column: "month", Type: queryutil.TypeNumber, AllowedOps: queryutil.NumberOps},
		"until_year":   {Column: "until_year", Type: queryutil.TypeNumber, AllowedOps: queryutil.NumberOps},
		"until_month":  {Column: "until_month", Type: queryutil.TypeNumber, AllowedOps: queryutil.NumberOps},
		"is_present":   {Column: "is_present", Type: queryutil.TypeBool, AllowedOps: queryutil.BoolOps},
	},
}

var workSortConfig = &queryutil.SortConfig{
	AllowedFields: map[string]string{
		"title": workpostgres.WorkSourceTitleSQL("work"), "published_at": "published_at", "updated_at": "updated_at",
	},
	DefaultSort: "published_at DESC",
}

func applyPublicWorkFilters(query *gorm.DB, filters []*commonv1.FilterSpec) (*gorm.DB, error) {
	query = query.Where("work.status IN ?", workpublic.PublicStatuses())
	return workFilterConfig.ApplyFilters(query, filters)
}

func workFromRow(row model.Work) work.Work {
	return work.Work{
		ID: row.ID, ContentDocumentID: row.ContentDocumentID, SourceLocale: row.SourceLocale, Slug: row.Slug,
		Type: row.Type, Year: row.Year, Month: row.Month, UntilYear: row.UntilYear, UntilMonth: row.UntilMonth,
		IsPresent: row.IsPresent, MapPlaceID: row.MapPlaceID, FeaturedImageFileID: row.FeaturedImageFileID,
		Metadata: row.Metadata, Featured: row.Featured, Status: work.Status(row.Status),
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, PublishedAt: row.PublishedAt,
	}
}

func (s store) ListWorks(ctx context.Context, page workpublic.ListPage) ([]work.Work, int64, error) {
	var rows []model.Work
	var total int64

	query, err := applyPublicWorkFilters(s.tx.WithContext(ctx).Model(&model.Work{}), page.Filters)
	if err != nil {
		return nil, 0, err
	}
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	query, err = workSortConfig.ApplySort(query, page.Sorts)
	if err != nil {
		return nil, 0, err
	}
	query = queryutil.ApplyPagination(query, queryutil.PaginationParams{Limit: page.Limit, Offset: page.Offset})
	if err := query.Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	works := make([]work.Work, len(rows))
	for i := range rows {
		works[i] = workFromRow(rows[i])
	}
	return works, total, nil
}

func (s store) SourceDocuments(ctx context.Context, workIDs []string) (map[string]work.SourceDocument, error) {
	return s.works.SourceDocuments(ctx, workIDs)
}

func (s store) ResolveLocalizations(ctx context.Context, workIDs []string, acceptLanguage string) (map[string]publiccontent.Selection, error) {
	return publiccontent.ResolveBatch(ctx, s.tx, workLocalizationSpec, workIDs, acceptLanguage, translationpostgres.NewRuntimeSettingsReader(s.tx))
}

func (s store) MapFeatures(ctx context.Context, bounds workpublic.MapBounds, filters []*commonv1.FilterSpec, sorts []*commonv1.SortSpec) ([]workpublic.MapFeatureRow, error) {
	var rows []struct {
		WorkID       string  `gorm:"column:work_id"`
		WorkTitle    string  `gorm:"column:work_title"`
		WorkSlug     *string `gorm:"column:work_slug"`
		PlaceID      string  `gorm:"column:place_id"`
		PlaceName    string  `gorm:"column:place_name"`
		PlaceAddress string  `gorm:"column:place_address"`
		PlaceLat     float64 `gorm:"column:place_lat"`
		PlaceLng     float64 `gorm:"column:place_lng"`
	}
	query := s.tx.WithContext(ctx).
		Model(&model.Work{}).
		Select(`
			work.id AS work_id,
			`+workpostgres.WorkSourceTitleSQL("work")+` AS work_title,
			work.slug AS work_slug,
			map_place.id AS place_id,
			map_place.name AS place_name,
			map_place.address AS place_address,
			map_place.lat AS place_lat,
			map_place.lng AS place_lng
		`).
		Joins("JOIN map_place ON map_place.id = work.map_place_id").
		Where("map_place.lat BETWEEN ? AND ?", bounds.South, bounds.North)

	if bounds.FullLongitude {
		// World-wrapping viewport: longitude filter would incorrectly exclude visible places.
	} else if bounds.West <= bounds.East {
		query = query.Where("map_place.lng BETWEEN ? AND ?", bounds.West, bounds.East)
	} else {
		query = query.Where("(map_place.lng >= ? OR map_place.lng <= ?)", bounds.West, bounds.East)
	}

	query, err := applyPublicWorkFilters(query, filters)
	if err != nil {
		return nil, err
	}
	query, err = workSortConfig.ApplySort(query, sorts)
	if err != nil {
		return nil, err
	}
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	features := make([]workpublic.MapFeatureRow, 0, len(rows))
	for _, row := range rows {
		features = append(features, workpublic.MapFeatureRow{
			WorkID: row.WorkID, WorkTitle: row.WorkTitle, WorkSlug: row.WorkSlug, PlaceID: row.PlaceID,
			PlaceName: row.PlaceName, PlaceAddress: row.PlaceAddress, PlaceLat: row.PlaceLat, PlaceLng: row.PlaceLng,
		})
	}
	return features, nil
}

func (s store) FindWork(ctx context.Context, slugOrID string, byID bool) (workpublic.Work, error) {
	var row model.Work
	stored := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).Preload("MapPlace")
	var err error
	if byID {
		err = stored.First(&row, "id = ?", slugOrID).Error
	} else {
		err = stored.First(&row, "slug = ?", slugOrID).Error
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return workpublic.Work{}, notFoundError{err}
	}
	if err != nil {
		return workpublic.Work{}, err
	}
	found := workpublic.Work{Work: workFromRow(row)}
	if row.MapPlaceID != nil {
		found.MapPlace = s.ports.MapPlaces.Basic(row.MapPlace)
	}
	return found, nil
}

func (s store) ContentDocumentID(ctx context.Context, workID string) (uuid.UUID, error) {
	return work.LoadWorkContentDocumentIDForPublicRead(ctx, s.works, workID)
}

func (s store) ResolveLocalization(ctx context.Context, workID, acceptLanguage string) (publiccontent.Selection, error) {
	return publiccontent.Resolve(ctx, s.tx, workLocalizationSpec, workID, acceptLanguage, translationpostgres.NewRuntimeSettingsReader(s.tx))
}

func (s store) ResolveOGConsistency(ctx context.Context, workID string, selection publiccontent.Selection) (publiccontent.Selection, error) {
	return publiccontent.ResolveOGConsistency(
		ctx, s.tx, workLocalizationSpec, workID, selection,
		func(ctx context.Context, assetID string) (bool, error) {
			return s.ports.Assets.IsReadyAsset(ctx, s.tx, assetID)
		},
	)
}

func (s store) Document(ctx context.Context, workID string, documentID uuid.UUID, locale string) (workpublic.Document, error) {
	document, revision, media, err := work.LoadLocalizedWorkContentProjectionForPublic(ctx, s.works, workID, documentID, locale)
	return workpublic.Document{Document: document, Revision: revision, BlockMedia: media}, err
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

func (s store) CreditGroups(ctx context.Context, workID string) ([]work.CreditGroup, error) {
	var rows []model.WorkCreditGroup
	err := optionalRead(s.tx, "work_credit_groups", func(savepoint *gorm.DB) error {
		return savepoint.WithContext(ctx).
			Where("work_id = ?", workID).
			Order("sort_order ASC").
			Order("id ASC").
			Find(&rows).Error
	})
	if err != nil {
		return nil, err
	}
	groups := make([]work.CreditGroup, len(rows))
	for i, row := range rows {
		groups[i] = work.CreditGroup{ID: row.ID, WorkID: row.WorkID, Name: row.Name, SortOrder: row.SortOrder}
	}
	return groups, nil
}

func (s store) Credits(ctx context.Context, workID string) ([]work.Credit, error) {
	var rows []model.WorkCredit
	if err := s.tx.WithContext(ctx).
		Where("work_id = ?", workID).
		Order("sort_order ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	credits := make([]work.Credit, len(rows))
	for i, row := range rows {
		credits[i] = work.Credit{
			ID: row.ID, WorkID: row.WorkID, GroupID: row.GroupID, ArtistID: row.ArtistID, MemberID: row.MemberID,
			Name: row.Name, CreditRole: row.CreditRole, SortOrder: row.SortOrder,
		}
	}
	return credits, nil
}

func (s store) Clients(ctx context.Context, workID string) ([]work.ClientRow, error) {
	var rows []struct {
		ID          string
		Name        string
		Website     *string
		LightFileID *string `gorm:"column:light_file_id"`
		DarkFileID  *string `gorm:"column:dark_file_id"`
	}
	err := optionalRead(s.tx, "work_clients", func(savepoint *gorm.DB) error {
		return savepoint.WithContext(ctx).
			Table("work_client wc").
			Select("c.id, c.name, c.website, c.logo_light_file_id AS light_file_id, c.logo_dark_file_id AS dark_file_id").
			Joins("JOIN client c ON c.id = wc.client_id").
			Where("wc.work_id = ?", workID).
			Order("wc.sort_order ASC").
			Scan(&rows).Error
	})
	if err != nil {
		return nil, err
	}
	clients := make([]work.ClientRow, 0, len(rows))
	for _, row := range rows {
		clients = append(clients, work.ClientRow{ID: row.ID, Name: row.Name, Website: row.Website, LightFileID: row.LightFileID, DarkFileID: row.DarkFileID})
	}
	return clients, nil
}

func (s store) ShareLink(ctx context.Context, token, workID string, now time.Time) (workpublic.ShareLink, bool, error) {
	var link model.ShareLink
	err := s.tx.WithContext(ctx).
		Where("token = ? AND entity_type = ? AND entity_id = ? AND expires_at > ?", token, managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_WORK.String(), workID, now).
		Take(&link).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return workpublic.ShareLink{}, false, nil
	}
	if err != nil {
		return workpublic.ShareLink{}, false, err
	}
	return workpublic.ShareLink{PasswordHash: link.PasswordHash, Witness: mediaassetpostgres.ContentDownloadShareLinkWitnessFromModel(&link)}, true, nil
}

func (s store) CreditArtist(ctx context.Context, artistID string) (workpublic.CreditArtistRow, bool, error) {
	var artist struct {
		ID   string  `gorm:"column:id"`
		Name string  `gorm:"column:name"`
		Slug *string `gorm:"column:slug"`
	}
	if err := s.tx.WithContext(ctx).
		Table("artist").
		Select("artist.id, "+workpostgres.ArtistSourceTitleSQL("artist")+" AS name, artist.slug").
		Where("artist.id = ?", artistID).
		Scan(&artist).Error; err != nil {
		return workpublic.CreditArtistRow{}, false, err
	}
	if artist.ID == "" {
		return workpublic.CreditArtistRow{}, false, nil
	}
	return workpublic.CreditArtistRow{ID: artist.ID, Name: artist.Name, Slug: artist.Slug}, true, nil
}
