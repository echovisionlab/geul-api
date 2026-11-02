package workpostgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	translationpostgres "github.com/echovisionlab/geul-api/internal/translation/postgres"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/work"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func workSourceLocaleColumnSQL(tableAlias string, column string) string {
	alias := strings.TrimSpace(tableAlias)
	if alias == "" {
		alias = "work"
	}
	return fmt.Sprintf(
		"(SELECT wt.%s FROM work_translation AS wt JOIN work AS source ON source.id = wt.entity_id AND source.source_locale = wt.locale WHERE wt.entity_id = %s.id LIMIT 1)",
		column,
		alias,
	)
}

// WorkSourceTitleSQL is the source-locale title of the Work aliased
// tableAlias.
func WorkSourceTitleSQL(tableAlias string) string {
	return fmt.Sprintf("COALESCE(%s, '')", workSourceLocaleColumnSQL(tableAlias, "title"))
}

// ArtistSourceTitleSQL is the read projection used by Work credit views.
func ArtistSourceTitleSQL(tableAlias string) string {
	alias := strings.TrimSpace(tableAlias)
	if alias == "" {
		alias = "artist"
	}
	return "COALESCE((SELECT at.title FROM artist_translation AS at WHERE at.entity_id = " + alias + ".id AND at.locale = " + alias + ".source_locale LIMIT 1), '')"
}

// WorkTypeValues are the Work types a type filter accepts.
func WorkTypeValues() []string {
	return []string{
		managev1.WorkType_WORK_TYPE_MUSIC_PROJECT.String(), managev1.WorkType_WORK_TYPE_PORTFOLIO.String(),
		managev1.WorkType_WORK_TYPE_ARTICLE.String(), managev1.WorkType_WORK_TYPE_CONTRIBUTION.String(),
	}
}

// adminFilterConfig defines the supported ListWorksAdmin filters.
var adminFilterConfig = &queryutil.FilterConfig{Fields: map[string]queryutil.FieldDef{
	"search":   {Type: queryutil.TypeText, AllowedOps: queryutil.SearchOps, SearchColumns: []string{WorkSourceTitleSQL("work")}},
	"type":     {Column: "type", Type: queryutil.TypeEnum, AllowedOps: queryutil.EnumOps, EnumValues: WorkTypeValues()},
	"status":   {Column: "status", Type: queryutil.TypeEnum, AllowedOps: queryutil.EnumOps, EnumValues: work.Statuses()},
	"featured": {Column: "featured", Type: queryutil.TypeBool, AllowedOps: queryutil.BoolOps},
	"map_place_id": {Column: "map_place_id", Type: queryutil.TypeID, AllowedOps: []commonv1.FilterOp{
		commonv1.FilterOp_FILTER_OP_EQ, commonv1.FilterOp_FILTER_OP_NEQ, commonv1.FilterOp_FILTER_OP_IN,
		commonv1.FilterOp_FILTER_OP_NOT_IN, commonv1.FilterOp_FILTER_OP_IS_NULL, commonv1.FilterOp_FILTER_OP_IS_NOT_NULL,
	}, IsFK: true},
	"published_at": {Column: "published_at", Type: queryutil.TypeDate, AllowedOps: queryutil.DateOps},
	"period_year":  {Column: "year", Type: queryutil.TypeNumber, AllowedOps: queryutil.NumberOps},
	"period_month": {Column: "month", Type: queryutil.TypeNumber, AllowedOps: queryutil.NumberOps},
	"year":         {Column: "year", Type: queryutil.TypeNumber, AllowedOps: queryutil.NumberOps},
	"month":        {Column: "month", Type: queryutil.TypeNumber, AllowedOps: queryutil.NumberOps},
	"until_year":   {Column: "until_year", Type: queryutil.TypeNumber, AllowedOps: queryutil.NumberOps},
	"until_month":  {Column: "until_month", Type: queryutil.TypeNumber, AllowedOps: queryutil.NumberOps},
	"is_present":   {Column: "is_present", Type: queryutil.TypeBool, AllowedOps: queryutil.BoolOps},
}}

// adminSortConfig defines allowed sort fields for works.
var adminSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"created_at":   "created_at",
		"updated_at":   "updated_at",
		"published_at": "published_at",
		"title":        WorkSourceTitleSQL("work"),
		"status":       "status",
		"slug":         "slug",
		"type":         "type",
		"featured":     "featured",
	},
	DefaultSort: "created_at DESC",
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

func workToRow(value work.Work) model.Work {
	return model.Work{
		ID: value.ID, ContentDocumentID: value.ContentDocumentID, SourceLocale: value.SourceLocale, Slug: value.Slug,
		Type: value.Type, Year: value.Year, Month: value.Month, UntilYear: value.UntilYear, UntilMonth: value.UntilMonth,
		IsPresent: value.IsPresent, MapPlaceID: value.MapPlaceID, FeaturedImageFileID: value.FeaturedImageFileID,
		Metadata: value.Metadata, Featured: value.Featured, Status: model.WorkStatus(value.Status),
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt, PublishedAt: value.PublishedAt,
	}
}

func (s *store) FindWork(ctx context.Context, workID string) (work.Work, error) {
	var row model.Work
	if err := s.tx.WithContext(ctx).Where("id = ?", workID).First(&row).Error; err != nil {
		return work.Work{}, notFound(err, work.ErrWorkNotFound)
	}
	return workFromRow(row), nil
}

func (s *store) LockWork(ctx context.Context, workID string, lock work.RowLock) (work.Work, error) {
	var row model.Work
	if err := withLock(s.tx.WithContext(ctx).Table("work").Where("id = ?", workID), lock).Take(&row).Error; err != nil {
		return work.Work{}, notFound(err, work.ErrWorkNotFound)
	}
	return workFromRow(row), nil
}

func (s *store) WorkExists(ctx context.Context, workID string) (bool, error) {
	var row model.Work
	err := s.tx.WithContext(ctx).Select("id").First(&row, "id = ?", workID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (s *store) LockRoot(ctx context.Context, workID string) (work.Root, error) {
	var row model.Work
	if err := s.tx.WithContext(ctx).Table("work").
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id", "status").Where("id = ?", workID).Take(&row).Error; err != nil {
		return work.Root{}, notFound(err, work.ErrWorkNotFound)
	}
	return work.Root{ID: row.ID, Status: work.Status(row.Status)}, nil
}

// contentRootRow is the Work row of a content document.
type contentRootRow struct {
	ID                string           `gorm:"column:id"`
	Status            model.WorkStatus `gorm:"column:status"`
	ContentDocumentID *string          `gorm:"column:content_document_id"`
	SourceLocale      string           `gorm:"column:source_locale"`
}

func (row contentRootRow) root() work.Root {
	return work.Root{ID: row.ID, Status: work.Status(row.Status), ContentDocumentID: row.ContentDocumentID, SourceLocale: row.SourceLocale}
}

func (s *store) ContentRoot(ctx context.Context, workID string, lock work.RowLock) (work.Root, error) {
	var row contentRootRow
	if err := withLock(s.tx.WithContext(ctx).Table("work"), lock).
		Select("id", "status", "content_document_id", "source_locale").
		Where("id = ?", workID).
		Take(&row).Error; err != nil {
		return work.Root{}, notFound(err, work.ErrWorkNotFound)
	}
	return row.root(), nil
}

func (s *store) LockDocumentRoot(ctx context.Context, documentID uuid.UUID) (work.Root, error) {
	var row contentRootRow
	if err := s.tx.WithContext(ctx).
		Table("work").
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id", "status", "content_document_id", "source_locale").
		Where("content_document_id = ?", documentID).
		Take(&row).Error; err != nil {
		return work.Root{}, notFound(err, work.ErrWorkNotFound)
	}
	return row.root(), nil
}

func (s *store) SourceLocale(ctx context.Context, workID string) (string, error) {
	var row struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	if err := s.tx.WithContext(ctx).
		Table("work").
		Select("source_locale").
		Where("id = ?", workID).
		Take(&row).Error; err != nil {
		return "", notFound(err, work.ErrWorkNotFound)
	}
	return row.SourceLocale, nil
}

func (s *store) LockSourceLocale(ctx context.Context, workID string) (string, error) {
	var row struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	if err := s.tx.WithContext(ctx).
		Table("work").
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("source_locale").
		Where("id = ?", workID).
		Take(&row).Error; err != nil {
		return "", notFound(err, work.ErrWorkNotFound)
	}
	return row.SourceLocale, nil
}

// applyWorkPresenceFilters applies has_slug and has_featured_image, which the
// filter configuration does not describe, and leaves the other filters.
func applyWorkPresenceFilters(stored *gorm.DB, filters *[]*commonv1.FilterSpec) *gorm.DB {
	presence := map[string][2]string{
		"has_slug":           {"slug IS NOT NULL AND slug != ''", "slug IS NULL OR slug = ''"},
		"has_featured_image": {"featured_image_file_id IS NOT NULL", "featured_image_file_id IS NULL"},
	}
	var standard []*commonv1.FilterSpec
	for _, filter := range *filters {
		if filter == nil {
			continue
		}
		conditions, ok := presence[filter.GetField()]
		switch {
		case !ok:
			standard = append(standard, filter)
		case filter.GetValue() == "true":
			stored = stored.Where(conditions[0])
		case filter.GetValue() == "false":
			stored = stored.Where(conditions[1])
		}
	}
	*filters = standard
	return stored
}

func (s *store) ListWorks(ctx context.Context, page work.AdminListPage) ([]work.Work, int64, error) {
	filters := page.Filters
	stored, err := adminFilterConfig.ApplyFilters(applyWorkPresenceFilters(s.tx.WithContext(ctx).Model(&model.Work{}), &filters), filters)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err := stored.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	stored, err = adminSortConfig.ApplySort(stored, page.Sorts)
	if err != nil {
		return nil, 0, err
	}
	var rows []model.Work
	if err := stored.Order("work.id ASC").Limit(int(page.Limit)).Offset(int(page.Offset)).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	works := make([]work.Work, len(rows))
	for i := range rows {
		works[i] = workFromRow(rows[i])
	}
	return works, total, nil
}

// countWorkRows counts the rows of table per Work.
func (s *store) countWorkRows(ctx context.Context, table string, workIDs []string) (map[string]int32, error) {
	counts := make(map[string]int32)
	if len(workIDs) == 0 {
		return counts, nil
	}
	var stats []struct {
		WorkID string
		Count  int32
	}
	if err := s.tx.WithContext(ctx).
		Table(table).
		Select("work_id, COUNT(*) as count").
		Where("work_id IN ?", workIDs).
		Group("work_id").
		Scan(&stats).Error; err != nil {
		return nil, err
	}
	for _, stat := range stats {
		counts[stat.WorkID] = stat.Count
	}
	return counts, nil
}

func (s *store) CountCredits(ctx context.Context, workIDs []string) (map[string]int32, error) {
	return s.countWorkRows(ctx, "work_credit", workIDs)
}

func (s *store) CountClients(ctx context.Context, workIDs []string) (map[string]int32, error) {
	return s.countWorkRows(ctx, "work_client", workIDs)
}

func (s *store) SlugTaken(ctx context.Context, slug string, excludeID *string) (bool, error) {
	var count int64
	stored := s.tx.WithContext(ctx).Model(&model.Work{}).Where("slug = ?", slug)
	if excludeID != nil {
		stored = stored.Where("id != ?", *excludeID)
	}
	if err := stored.Count(&count).Error; err != nil {
		return false, err
	}
	return count != 0, nil
}

func (s *store) InsertWork(ctx context.Context, value *work.Work) error {
	row := workToRow(*value)
	if err := s.tx.WithContext(ctx).Clauses(clause.Returning{Columns: []clause.Column{
		{Name: "id"}, {Name: "created_at"}, {Name: "updated_at"},
	}}).Create(&row).Error; err != nil {
		return slugTaken(err)
	}
	value.ID, value.CreatedAt, value.UpdatedAt = row.ID, row.CreatedAt, row.UpdatedAt
	return nil
}

// workChangeFields is the column update of change.
func workChangeFields(change work.WorkChange) structured.Fields {
	fields := structured.Fields{}
	if change.ClearSlug {
		fields["slug"] = nil
	} else if change.Slug != nil {
		fields["slug"] = *change.Slug
	}
	if change.Type != nil {
		fields["type"] = *change.Type
	}
	if change.Featured != nil {
		fields["featured"] = *change.Featured
	}
	if change.ClearPlace {
		fields["map_place_id"] = nil
	} else if change.MapPlaceID != nil {
		fields["map_place_id"] = *change.MapPlaceID
	}
	if change.Year != nil {
		fields["year"] = *change.Year
	}
	if change.Month != nil {
		fields["month"] = *change.Month
	}
	if change.UntilYear.Set {
		fields["until_year"] = change.UntilYear.Value
	}
	if change.UntilMonth.Set {
		fields["until_month"] = change.UntilMonth.Value
	}
	if change.IsPresent != nil {
		fields["is_present"] = *change.IsPresent
	}
	if change.Metadata != nil {
		fields["metadata"] = change.Metadata
	}
	fields["updated_at"] = change.UpdatedAt
	return fields
}

func (s *store) UpdateWork(ctx context.Context, workID string, change work.WorkChange) error {
	return slugTaken(s.tx.WithContext(ctx).Model(&model.Work{ID: workID}).Updates(workChangeFields(change)).Error)
}

func (s *store) SetStatus(ctx context.Context, workID string, status work.Status, publishedAt *time.Time, now time.Time) error {
	updates := structured.Fields{"status": model.WorkStatus(status), "updated_at": now}
	if publishedAt != nil {
		updates["published_at"] = *publishedAt
	}
	return s.tx.WithContext(ctx).Model(&model.Work{ID: workID}).Updates(updates).Error
}

func (s *store) SetFeaturedImage(ctx context.Context, workID string, fileID *string, now time.Time) error {
	var value any
	if fileID != nil {
		value = *fileID
	}
	result := s.tx.WithContext(ctx).Model(&model.Work{}).Where("id = ?", workID).Updates(structured.Fields{
		"featured_image_file_id": value,
		"updated_at":             now,
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return sentinelError{error: gorm.ErrRecordNotFound, sentinel: work.ErrWorkNotFound}
	}
	return nil
}

func (s *store) FeaturedImageFileID(ctx context.Context, workID string) (*string, error) {
	var current struct {
		FeaturedImageFileID *string `gorm:"column:featured_image_file_id"`
	}
	if err := s.tx.WithContext(ctx).Table("work").Select("featured_image_file_id").Where("id = ?", workID).Take(&current).Error; err != nil {
		return nil, notFound(err, work.ErrWorkNotFound)
	}
	return current.FeaturedImageFileID, nil
}

func (s *store) TouchWork(ctx context.Context, workID string, now time.Time) error {
	return s.tx.WithContext(ctx).Model(&model.Work{}).Where("id = ?", workID).UpdateColumn("updated_at", now).Error
}

func (s *store) StampWork(ctx context.Context, workID string, now time.Time) error {
	return s.tx.WithContext(ctx).Table("work").Where("id = ?::uuid", workID).UpdateColumn("updated_at", now).Error
}

func (s *store) SwitchSourceLocale(ctx context.Context, workID, from, to string, now time.Time) error {
	updated := s.tx.WithContext(ctx).Table("work").
		Where("id = ? AND source_locale = ?", workID, from).
		Updates(map[string]any{"source_locale": to, "updated_at": now})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return rowChanged
	}
	return nil
}

func (s *store) DeleteWork(ctx context.Context, workID string) error {
	return s.tx.WithContext(ctx).Delete(&model.Work{ID: workID}).Error
}

func (s *store) MapPlaceExists(ctx context.Context, mapPlaceID string) (bool, error) {
	var count int64
	if err := s.tx.WithContext(ctx).
		Table("map_place").
		Where("id = ?", mapPlaceID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count != 0, nil
}

func (s *store) CheckResourceRoute(ctx context.Context, slug string) error {
	return routeregistry.EnsureResourceRouteAvailable(ctx, s.tx, "work", "works", slug)
}

func (s *store) LockResourceRoute(ctx context.Context, slug string) error {
	return routeregistry.EnsureResourceRouteAvailableInTx(ctx, s.tx, "work", "works", slug)
}

func (s *store) ResourceRouteAvailable(ctx context.Context, slug string) (bool, error) {
	return routeregistry.IsResourceRouteAvailable(ctx, s.tx, "works", slug)
}

func (s *store) PreferredLocale(ctx context.Context, memberID string) (*string, error) {
	var preferred *string
	if err := s.tx.WithContext(ctx).Model(&model.Member{}).
		Select("preferred_locale").
		Where("id = ?::uuid AND deleted_at IS NULL", memberID).
		Scan(&preferred).Error; err != nil {
		return nil, err
	}
	return preferred, nil
}

func (s *store) DefaultLocale(ctx context.Context) (string, error) {
	settings, err := translationpostgres.NewRuntimeSettingsReader(s.tx).RuntimeSettings(ctx)
	if err != nil {
		return "", err
	}
	return settings.DefaultLocale, nil
}

func (s *store) DeleteShareLinks(ctx context.Context, workID string) error {
	return s.ports.ShareLinks.DeleteEntityShareLinks(ctx, s.tx, workID)
}
