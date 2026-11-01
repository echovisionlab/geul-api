package formpostgres

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"time"

	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
)

// FormOf maps a form row to the domain Form.
func FormOf(row model.Form) form.Form {
	return form.Form{
		ID: row.ID, ContentDocumentID: row.ContentDocumentID, SourceLocale: row.SourceLocale, Slug: row.Slug,
		Status: form.Status(row.Status), IsPublic: row.IsPublic, RequireAuth: row.RequireAuth,
		AllowedRoles: slices.Clone([]string(row.AllowedRoles)), AllowDuplicateSubmission: row.AllowDuplicateSubmission,
		AccessPassword: row.AccessPassword, MaxSubmissions: row.MaxSubmissions, OpensAt: row.OpensAt, ClosesAt: row.ClosesAt,
		FeaturedImageFileID: row.FeaturedImageFileID, OgAssetID: row.OgAssetID, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func rowOf(value form.Form) model.Form {
	row := model.Form{
		ID: value.ID, ContentDocumentID: value.ContentDocumentID, SourceLocale: value.SourceLocale, Slug: value.Slug,
		Status: model.FormStatus(value.Status), IsPublic: value.IsPublic, RequireAuth: value.RequireAuth,
		AllowDuplicateSubmission: value.AllowDuplicateSubmission, AccessPassword: value.AccessPassword,
		MaxSubmissions: value.MaxSubmissions, OpensAt: value.OpensAt, ClosesAt: value.ClosesAt,
		FeaturedImageFileID: value.FeaturedImageFileID, OgAssetID: value.OgAssetID, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
	if value.AllowedRoles != nil {
		row.AllowedRoles = pq.StringArray(value.AllowedRoles)
	}
	return row
}

// FindForm reads the Form whose locator column equals value.
func (s *store) FindForm(ctx context.Context, locator form.Locator, value string) (form.Form, error) {
	return FindForm(ctx, s.tx, locator, value)
}

// FindForm reads the Form whose locator column equals value in tx; the
// public read Store shares it.
func FindForm(ctx context.Context, tx *gorm.DB, locator form.Locator, value string) (form.Form, error) {
	var row model.Form
	query := tx.WithContext(ctx)
	if locator == form.ByID {
		query = query.Where("id = ?", value)
	} else {
		query = query.Where("slug = ?", value)
	}
	if err := query.First(&row).Error; err != nil {
		return form.Form{}, notFound(err, form.ErrFormNotFound)
	}
	return FormOf(row), nil
}

func (s *store) FormExists(ctx context.Context, formID string) (bool, error) {
	var count int64
	if err := s.tx.WithContext(ctx).Model(&model.Form{}).Where("id = ?", formID).Count(&count).Error; err != nil {
		return false, err
	}
	return count != 0, nil
}

func (s *store) LockForm(ctx context.Context, formID string) (form.Form, error) {
	return LockForm(ctx, s.tx, formID)
}

// LockForm locks the Form row for update in tx and reads it.
func LockForm(ctx context.Context, tx *gorm.DB, formID string) (form.Form, error) {
	var row model.Form
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		First(&row, "id = ?", formID).Error; err != nil {
		return form.Form{}, notFound(err, form.ErrFormNotFound)
	}
	return FormOf(row), nil
}

func (s *store) LockFormRoot(ctx context.Context, formID string) (string, error) {
	var row model.Form
	if err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Select([]string{"id"}).
		First(&row, "id = ?", formID).Error; err != nil {
		return "", notFound(err, form.ErrFormNotFound)
	}
	return row.ID, nil
}

func (s *store) LockFeaturedImage(ctx context.Context, formID string) (*string, error) {
	var row model.Form
	if err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select([]string{"id", "featured_image_file_id"}).First(&row, "id = ?", formID).Error; err != nil {
		return nil, notFound(err, form.ErrFormNotFound)
	}
	return row.FeaturedImageFileID, nil
}

func (s *store) DocumentRoot(ctx context.Context, formID string, lock form.RowLock) (form.DocumentRoot, error) {
	query := s.tx.WithContext(ctx).Table("form").
		Select("status, source_locale, content_document_id").
		Where("id = ?::uuid", formID)
	switch lock {
	case form.KeyShareLock:
		query = query.Clauses(clause.Locking{Strength: "KEY SHARE"})
	case form.UpdateLock:
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	var root struct {
		Status       string `gorm:"column:status"`
		SourceLocale string `gorm:"column:source_locale"`
		DocumentID   string `gorm:"column:content_document_id"`
	}
	if err := query.Take(&root).Error; err != nil {
		return form.DocumentRoot{}, notFound(err, form.ErrFormNotFound)
	}
	return form.DocumentRoot{Status: form.Status(root.Status), SourceLocale: root.SourceLocale, DocumentID: root.DocumentID}, nil
}

func (s *store) SourceLocale(ctx context.Context, formID string) (string, error) {
	return SourceLocale(ctx, s.tx, formID)
}

// SourceLocale reads the Form's source locale in tx.
func SourceLocale(ctx context.Context, tx *gorm.DB, formID string) (string, error) {
	var sourceRow struct {
		Locale string `gorm:"column:locale"`
	}
	result := tx.WithContext(ctx).Raw(
		`SELECT source_locale AS locale
		 FROM form
		 WHERE id = ?
		 LIMIT 1`,
		formID,
	).Scan(&sourceRow)
	if result.Error != nil {
		return "", result.Error
	}
	if result.RowsAffected == 0 {
		return "", form.ErrFormNotFound
	}
	return sourceRow.Locale, nil
}

func (s *store) ListForms(ctx context.Context, list form.ListForms) ([]form.Form, int64, error) {
	listing, err := FormFilterConfig.ApplyFilters(s.tx.WithContext(ctx).Model(&model.Form{}), list.Filters)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err := listing.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if listing, err = formSortConfig.ApplySort(listing, list.Sorts); err != nil {
		return nil, 0, err
	}
	var rows []model.Form
	if err := listing.Limit(int(list.Limit)).Offset(int(list.Offset)).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	forms := make([]form.Form, len(rows))
	for i, row := range rows {
		forms[i] = FormOf(row)
	}
	return forms, total, nil
}

func (s *store) SourceTitles(ctx context.Context, formIDs []string) (map[string]string, error) {
	if len(formIDs) == 0 {
		return map[string]string{}, nil
	}

	var rows []struct {
		EntityID string         `gorm:"column:entity_id"`
		Title    sql.NullString `gorm:"column:title"`
	}

	result := s.tx.WithContext(ctx).Raw(
		`SELECT ft.entity_id, ft.title
		 FROM form_translation AS ft
		 JOIN form AS root ON root.id = ft.entity_id AND root.source_locale = ft.locale
		 WHERE ft.entity_id IN ?`,
		formIDs,
	).Scan(&rows)
	if result.Error != nil {
		return nil, result.Error
	}

	titles := make(map[string]string, len(rows))
	for _, formID := range formIDs {
		titles[formID] = form.DefaultTitle
	}
	for _, row := range rows {
		if row.Title.Valid {
			title := strings.TrimSpace(row.Title.String)
			if title != "" {
				titles[row.EntityID] = title
				continue
			}
		}
		titles[row.EntityID] = form.DefaultTitle
	}
	return titles, nil
}

func (s *store) SlugTaken(ctx context.Context, slug, excludeID string) (bool, error) {
	query := s.tx.WithContext(ctx).Model(&model.Form{}).Where("slug = ?", slug)
	if excludeID != "" {
		query = query.Where("id != ?", excludeID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count != 0, nil
}

func (s *store) RouteAvailable(ctx context.Context, slug string) (bool, error) {
	return s.ports.Routes.SlugAvailable(ctx, s.tx, slug)
}

func (s *store) LockRoute(ctx context.Context, slug string) error {
	return s.ports.Routes.EnsureAvailableLocked(ctx, s.tx, slug)
}

func (s *store) InsertForm(ctx context.Context, value *form.Form) error {
	row := rowOf(*value)
	if err := s.tx.WithContext(ctx).Omit("ID").Clauses(clause.Returning{}).Create(&row).Error; err != nil {
		return err
	}
	*value = FormOf(row)
	return nil
}

// settingColumns are the form columns of the settings an update writes.
var settingColumns = map[form.Setting]string{
	form.SettingSlug:                     "slug",
	form.SettingIsPublic:                 "is_public",
	form.SettingRequireAuth:              "require_auth",
	form.SettingAllowDuplicateSubmission: "allow_duplicate_submission",
	form.SettingAccessPassword:           "access_password",
	form.SettingMaxSubmissions:           "max_submissions",
	form.SettingOpensAt:                  "opens_at",
	form.SettingClosesAt:                 "closes_at",
	form.SettingStatus:                   "status",
	form.SettingAllowedRoles:             "allowed_roles",
}

func (s *store) UpdateSettings(ctx context.Context, next form.Form, settings []form.Setting, now time.Time) error {
	row := rowOf(next)
	values := structured.Fields{"updated_at": now}
	for _, setting := range settings {
		var value any
		switch setting {
		case form.SettingSlug:
			value = row.Slug
		case form.SettingIsPublic:
			value = row.IsPublic
		case form.SettingRequireAuth:
			value = row.RequireAuth
		case form.SettingAllowDuplicateSubmission:
			value = row.AllowDuplicateSubmission
		case form.SettingAccessPassword:
			value = row.AccessPassword
		case form.SettingMaxSubmissions:
			value = row.MaxSubmissions
		case form.SettingOpensAt:
			value = row.OpensAt
		case form.SettingClosesAt:
			value = row.ClosesAt
		case form.SettingStatus:
			value = string(row.Status)
		case form.SettingAllowedRoles:
			value = row.AllowedRoles
		}
		values[settingColumns[setting]] = value
	}
	return s.tx.WithContext(ctx).Model(&model.Form{ID: next.ID}).Updates(values).Error
}

func (s *store) SetFeaturedImage(ctx context.Context, formID string, fileID *string, now time.Time) error {
	var value any
	if fileID != nil {
		value = *fileID
	}
	return s.tx.WithContext(ctx).Model(&model.Form{}).Where("id = ?", formID).Updates(structured.Fields{
		"featured_image_file_id": value,
		"updated_at":             now,
	}).Error
}

func (s *store) TouchForm(ctx context.Context, formID string, now time.Time) error {
	return s.tx.WithContext(ctx).Table("form").Where("id = ?::uuid", formID).Update("updated_at", now).Error
}

func (s *store) DeleteForm(ctx context.Context, formID string) error {
	return s.tx.WithContext(ctx).Delete(&model.Form{ID: formID}).Error
}
