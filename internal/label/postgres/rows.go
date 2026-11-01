package labelpostgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"github.com/echovisionlab/geul-api/internal/label"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"github.com/echovisionlab/geul-api/internal/structured"
)

func labelSourceLocaleColumnSQL(tableAlias string, column string) string {
	alias := strings.TrimSpace(tableAlias)
	if alias == "" {
		alias = "label"
	}
	return fmt.Sprintf(
		"(SELECT lt.%s FROM label_translation AS lt JOIN label AS source ON source.id = lt.entity_id AND source.source_locale = lt.locale WHERE lt.entity_id = %s.id LIMIT 1)",
		column,
		alias,
	)
}

// labelSourceTitleSQL is the source-locale title of the Label aliased
// tableAlias.
func labelSourceTitleSQL(tableAlias string) string {
	return fmt.Sprintf("COALESCE(NULLIF(%s, ''), '')", labelSourceLocaleColumnSQL(tableAlias, "title"))
}

var labelFilterConfig = &queryutil.FilterConfig{Fields: map[string]queryutil.FieldDef{
	"search":          {Type: queryutil.TypeText, AllowedOps: queryutil.SearchOps, SearchColumns: []string{labelSourceTitleSQL("label")}},
	"country_code":    {Column: "country_code", Type: queryutil.TypeText, AllowedOps: queryutil.TextOps},
	"parent_label_id": {Column: "parent_label_id", Type: queryutil.TypeID, AllowedOps: queryutil.IDOps, IsFK: true},
	"status":          {Column: "status", Type: queryutil.TypeEnum, AllowedOps: queryutil.EnumOps, EnumValues: []string{string(model.LabelStatusDraft), string(model.LabelStatusPublished)}},
}}

var labelSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"name":         labelSourceTitleSQL("label"),
		"slug":         "slug",
		"status":       "status",
		"country_code": "country_code",
		"created_at":   "created_at",
		"updated_at":   "updated_at",
		"published_at": "published_at",
	},
	DefaultSort: labelSourceTitleSQL("label") + " ASC",
}

func lockStrength(lock label.RowLock) string {
	switch lock {
	case label.KeyShareLock:
		return "KEY SHARE"
	case label.ShareLock:
		return clause.LockingStrengthShare
	case label.UpdateLock:
		return clause.LockingStrengthUpdate
	default:
		return ""
	}
}

func withLock(query *gorm.DB, lock label.RowLock) *gorm.DB {
	if strength := lockStrength(lock); strength != "" {
		return query.Clauses(clause.Locking{Strength: strength})
	}
	return query
}

func labelFromRow(row model.Label) label.Label {
	return label.Label{
		ID: row.ID, ContentDocumentID: row.ContentDocumentID, SourceLocale: row.SourceLocale,
		Slug: row.Slug, CountryCode: row.CountryCode, Website: row.Website,
		LogoLightFileID: row.LogoLightFileID, LogoDarkFileID: row.LogoDarkFileID,
		SocialLinks: row.SocialLinks, ParentLabelID: row.ParentLabelID, OgAssetID: row.OgAssetID,
		Status: label.Status(row.Status), PublishedAt: row.PublishedAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func labelToRow(value label.Label) model.Label {
	return model.Label{
		ID: value.ID, ContentDocumentID: value.ContentDocumentID, SourceLocale: value.SourceLocale,
		Slug: value.Slug, CountryCode: value.CountryCode, Website: value.Website,
		LogoLightFileID: value.LogoLightFileID, LogoDarkFileID: value.LogoDarkFileID,
		SocialLinks: value.SocialLinks, ParentLabelID: value.ParentLabelID, OgAssetID: value.OgAssetID,
		Status: model.LabelStatus(value.Status), PublishedAt: value.PublishedAt,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
}

func (s *store) FindLabel(ctx context.Context, labelID string) (label.Label, error) {
	var row model.Label
	if err := s.tx.WithContext(ctx).Where("id = ?", labelID).First(&row).Error; err != nil {
		return label.Label{}, labelNotFound(err)
	}
	return labelFromRow(row), nil
}

func (s *store) FindLabelID(ctx context.Context, labelID string) (string, error) {
	var row model.Label
	if err := s.tx.WithContext(ctx).Select("id").First(&row, "id = ?", labelID).Error; err != nil {
		return "", labelNotFound(err)
	}
	return row.ID, nil
}

func (s *store) FindLabelLogos(ctx context.Context, labelID string) (label.Label, error) {
	var row model.Label
	if err := s.tx.WithContext(ctx).Select("id", "logo_light_file_id", "logo_dark_file_id").First(&row, "id = ?", labelID).Error; err != nil {
		return label.Label{}, labelNotFound(err)
	}
	return labelFromRow(row), nil
}

func (s *store) LockLabel(_ context.Context, labelID string) (label.Label, error) {
	var row model.Label
	if err := s.tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&row, "id = ?", labelID).Error; err != nil {
		return label.Label{}, labelNotFound(err)
	}
	return labelFromRow(row), nil
}

func (s *store) LockLabelID(_ context.Context, labelID string) (string, error) {
	var row model.Label
	if err := s.tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id").
		First(&row, "id = ?", labelID).Error; err != nil {
		return "", labelNotFound(err)
	}
	return row.ID, nil
}

func (s *store) ShareLockLabel(ctx context.Context, labelID string) (label.Label, error) {
	var row model.Label
	if err := s.tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
		Where("id = ?", labelID).
		Take(&row).Error; err != nil {
		return label.Label{}, labelNotFound(err)
	}
	return labelFromRow(row), nil
}

func (s *store) LockLabelRelations(_ context.Context) error {
	return s.tx.Exec(`
		LOCK TABLE label, artist_label, program_event_label, release_label
		IN SHARE ROW EXCLUSIVE MODE
	`).Error
}

func (s *store) ListLabels(ctx context.Context, page label.ListPage) ([]label.Label, int64, error) {
	base := s.tx.WithContext(ctx).Model(&model.Label{})
	if page.PublishedOnly {
		base = base.Where("status = ?", model.LabelStatusPublished)
	}
	filtered, err := labelFilterConfig.ApplyFilters(base, page.Filters)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err := filtered.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	sorted, err := labelSortConfig.ApplySort(filtered, page.Sorts)
	if err != nil {
		return nil, 0, err
	}
	var rows []model.Label
	if err := sorted.Limit(int(page.Limit)).Offset(int(page.Offset)).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	labels := make([]label.Label, len(rows))
	for i, row := range rows {
		labels[i] = labelFromRow(row)
	}
	return labels, total, nil
}

func (s *store) SlugTaken(ctx context.Context, slug, excludeID string) (bool, error) {
	query := s.tx.WithContext(ctx).Model(&model.Label{}).Where("slug = ?", slug)
	if excludeID != "" {
		query = query.Where("id != ?", excludeID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count != 0, nil
}

// slugTakenError keeps the unique violation readable and in the chain while
// it answers label.ErrSlugTaken.
type slugTakenError struct{ error }

func (err slugTakenError) Is(target error) bool { return target == label.ErrSlugTaken }
func (err slugTakenError) Unwrap() error        { return err.error }

func (s *store) InsertLabel(_ context.Context, value *label.Label) error {
	row := labelToRow(*value)
	if err := s.tx.Clauses(clause.Returning{Columns: []clause.Column{
		{Name: "id"}, {Name: "created_at"}, {Name: "updated_at"},
	}}).Create(&row).Error; err != nil {
		if dberrors.IsUniqueViolation(err) {
			return slugTakenError{err}
		}
		return err
	}
	value.ID, value.CreatedAt, value.UpdatedAt = row.ID, row.CreatedAt, row.UpdatedAt
	return nil
}

func (s *store) InsertCreatorOwner(_ context.Context, labelID, memberID string) error {
	return s.tx.Exec(`
		INSERT INTO label_owner (label_id, member_id, created_at)
		VALUES (?::uuid, ?::uuid, now())
	`, labelID, memberID).Error
}

func (s *store) SetLabelStatus(_ context.Context, labelID string, status label.Status, publishedAt *time.Time, now time.Time) error {
	fields := structured.Fields{"status": model.LabelStatus(status), "updated_at": now}
	if publishedAt != nil {
		fields["published_at"] = *publishedAt
	}
	return s.tx.Model(&model.Label{ID: labelID}).Updates(fields).Error
}

func logoColumn(variant label.LogoVariant) string {
	if variant == label.LogoVariantDark {
		return "logo_dark_file_id"
	}
	return "logo_light_file_id"
}

func (s *store) SetLabelLogo(_ context.Context, labelID string, variant label.LogoVariant, fileID *string, now time.Time) error {
	var value any
	if fileID != nil {
		value = *fileID
	}
	result := s.tx.Model(&model.Label{ID: labelID}).Updates(structured.Fields{logoColumn(variant): value, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return labelNotFound(gorm.ErrRecordNotFound)
	}
	return nil
}

func (s *store) EffectiveLogoFileID(ctx context.Context, labelID string) (*string, error) {
	var row struct {
		FileID *string `gorm:"column:file_id"`
	}
	if err := s.tx.WithContext(ctx).
		Raw(`SELECT COALESCE(logo_light_file_id, logo_dark_file_id) AS file_id FROM label WHERE id = ? LIMIT 1`, labelID).
		Scan(&row).Error; err != nil {
		return nil, err
	}
	return row.FileID, nil
}

func (s *store) ClearOgAsset(ctx context.Context, labelID string) error {
	return s.tx.WithContext(ctx).Table("label").Where("id = ?", labelID).Update("og_asset_id", nil).Error
}

func (s *store) TouchLabel(ctx context.Context, labelID string, now time.Time) error {
	result := s.tx.WithContext(ctx).
		Model(&model.Label{}).
		Where("id = ?", labelID).
		Updates(structured.Fields{"updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return labelNotFound(gorm.ErrRecordNotFound)
	}
	return nil
}

func (s *store) DeleteLabel(_ context.Context, labelID string) error {
	result := s.tx.Delete(&model.Label{}, "id = ?", labelID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return labelNotFound(gorm.ErrRecordNotFound)
	}
	return nil
}

func (s *store) ContentDocumentID(ctx context.Context, labelID string, lock label.RowLock) (*uuid.UUID, error) {
	var row struct {
		DocumentID *uuid.UUID `gorm:"column:content_document_id"`
	}
	if err := withLock(s.tx.WithContext(ctx).Table("label"), lock).Select("content_document_id").Where("id = ?", labelID).Take(&row).Error; err != nil {
		return nil, labelNotFound(err)
	}
	return row.DocumentID, nil
}

func (s *store) SourceLocale(ctx context.Context, labelID string, lock label.RowLock) (string, error) {
	var row struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	if err := withLock(s.tx.WithContext(ctx).Table("label").Select("source_locale").Where("id = ?", labelID), lock).Take(&row).Error; err != nil {
		return "", labelNotFound(err)
	}
	return row.SourceLocale, nil
}

func (s *store) DocumentRoot(ctx context.Context, labelID string, lock label.RowLock) (label.DocumentRoot, error) {
	var row struct {
		ID         string            `gorm:"column:id"`
		Status     model.LabelStatus `gorm:"column:status"`
		DocumentID *uuid.UUID        `gorm:"column:content_document_id"`
	}
	query := withLock(s.tx.WithContext(ctx).
		Table("label").
		Select("id::text AS id", "status", "content_document_id").
		Where("id = ?::uuid", labelID), lock)
	if err := query.Take(&row).Error; err != nil {
		return label.DocumentRoot{}, labelNotFound(err)
	}
	return label.DocumentRoot{ID: row.ID, Status: label.Status(row.Status), DocumentID: row.DocumentID}, nil
}

func (s *store) DocumentMetadata(ctx context.Context, labelID string) (label.DocumentMetadata, error) {
	var row model.Label
	if err := s.tx.WithContext(ctx).
		Select("id", "slug", "country_code", "website", "social_links", "parent_label_id").
		Where("id = ?", labelID).
		Take(&row).Error; err != nil {
		return label.DocumentMetadata{}, labelNotFound(err)
	}
	return label.DocumentMetadata{
		Slug: row.Slug, CountryCode: row.CountryCode, Website: row.Website,
		SocialLinks: row.SocialLinks, ParentLabelID: row.ParentLabelID,
	}, nil
}

// assign adds a changed nullable column to fields.
func assign(fields structured.Fields, column string, assignment entityparticipant.Assignment) {
	if !assignment.Changed {
		return
	}
	if assignment.Value == nil {
		fields[column] = nil
	} else {
		fields[column] = *assignment.Value
	}
}

func (s *store) UpdateDocumentMetadata(ctx context.Context, labelID string, change entityparticipant.MetadataChange, now time.Time) error {
	fields := structured.Fields{}
	assign(fields, "slug", change.Slug)
	assign(fields, "country_code", change.CountryCode)
	assign(fields, "website", change.Website)
	assign(fields, labelTables.ParentColumn, change.Parent)
	if change.SetSocialLinks {
		fields["social_links"] = change.SocialLinks
	}
	fields["updated_at"] = now
	return s.tx.WithContext(ctx).Model(&model.Label{}).Where("id = ?", labelID).Updates(fields).Error
}

func (s *store) CheckResourceRoute(ctx context.Context, slug string) error {
	return routeregistry.EnsureResourceRouteAvailable(ctx, s.tx, "label", "labels", slug)
}

func (s *store) LockResourceRoute(ctx context.Context, slug string) error {
	return routeregistry.EnsureResourceRouteAvailableInTx(ctx, s.tx, "label", "labels", slug)
}

func (s *store) ResourceRouteAvailable(ctx context.Context, slug string) (bool, error) {
	return routeregistry.IsResourceRouteAvailable(ctx, s.tx, "labels", slug)
}

func (s *store) LocaleTitles(ctx context.Context, labelID string) ([]label.LocaleTitle, error) {
	var rows []struct {
		Locale    string    `gorm:"column:locale"`
		Title     *string   `gorm:"column:title"`
		UpdatedAt time.Time `gorm:"column:updated_at"`
	}
	if err := s.tx.WithContext(ctx).Table("label_translation").Select("locale", "title", "updated_at").Where("entity_id = ?", labelID).Order("locale ASC").Scan(&rows).Error; err != nil {
		return nil, err
	}
	titles := make([]label.LocaleTitle, 0, len(rows))
	for _, row := range rows {
		titles = append(titles, label.LocaleTitle{Locale: row.Locale, Title: row.Title, UpdatedAt: row.UpdatedAt})
	}
	return titles, nil
}

func (s *store) LocaleTitle(ctx context.Context, labelID, locale string, forUpdate bool) (label.LocaleTitle, bool, error) {
	query := s.tx.WithContext(ctx).Table("label_translation").
		Select("locale", "title", "created_at", "updated_at").
		Where("entity_id = ?::uuid AND locale = ?", labelID, locale)
	if forUpdate {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	var row struct {
		Locale    string    `gorm:"column:locale"`
		Title     *string   `gorm:"column:title"`
		CreatedAt time.Time `gorm:"column:created_at"`
		UpdatedAt time.Time `gorm:"column:updated_at"`
	}
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return label.LocaleTitle{}, false, nil
		}
		return label.LocaleTitle{}, false, err
	}
	return label.LocaleTitle{Locale: row.Locale, Title: row.Title, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}, true, nil
}

func (s *store) LocaleTitleText(ctx context.Context, labelID, locale string) (string, error) {
	var row struct {
		Title string `gorm:"column:title"`
	}
	if err := s.tx.WithContext(ctx).Table("label_translation").Select("title").
		Where("entity_id = ? AND locale = ?", labelID, locale).Take(&row).Error; err != nil {
		return "", localeNotFound(err)
	}
	return row.Title, nil
}

func (s *store) SaveLocaleTitle(ctx context.Context, labelID, locale, title string, now time.Time) error {
	return s.tx.WithContext(ctx).Exec(`
		INSERT INTO label_translation (
			entity_id, locale, title, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (entity_id, locale) DO UPDATE SET
			title = EXCLUDED.title,
			updated_at = EXCLUDED.updated_at`,
		labelID, locale, title, now, now,
	).Error
}

func (s *store) CreateTargetLocale(ctx context.Context, labelID, locale string, now time.Time) (bool, error) {
	created := s.tx.WithContext(ctx).Exec(`
		INSERT INTO label_translation (entity_id, locale, title, created_at, updated_at)
		VALUES (?::uuid, ?, NULL, ?, ?)`, labelID, locale, now, now)
	return created.RowsAffected == 1, created.Error
}

func (s *store) TouchTargetLocale(ctx context.Context, labelID, locale string, updatedAt time.Time) (bool, error) {
	updated := s.tx.WithContext(ctx).Table("label_translation").
		Where("entity_id = ?::uuid AND locale = ?", labelID, locale).
		Update("updated_at", updatedAt)
	return updated.RowsAffected == 1, updated.Error
}

func (s *store) DeleteTargetLocale(ctx context.Context, labelID, locale string) (bool, error) {
	deleted := s.tx.WithContext(ctx).Exec("DELETE FROM label_translation WHERE entity_id = ?::uuid AND locale = ?", labelID, locale)
	return deleted.RowsAffected == 1, deleted.Error
}
