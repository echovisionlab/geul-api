// Package menupostgres is the PostgreSQL Store of the Menu domain: the menu,
// menu_translation and content_document rows of each Menu, the target rows
// its items select and the transaction-bound ports of other domains.
package menupostgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/menu"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SiteSettings clears the Site Settings slots that select a deleted Menu
// in the Menu's transaction (menuadapter.SiteSettingsReferences).
type SiteSettings interface {
	ClearMenuReferences(ctx context.Context, tx *gorm.DB, menuID string) error
}

// Checkpoints authorizes the contributors of a collaboration save in the
// Menu's transaction (collaboration.CheckpointFence).
type Checkpoints interface {
	RequireCurrentContributorsForPermission(
		ctx context.Context,
		tx *gorm.DB,
		resourceType intrav1.CollaborationResourceType,
		resourceID string,
		contributors []string,
		permission intrav1.CollaborationPermission,
	) error
}

// Ports are the other domains' adapters a Menu transaction calls with the
// transaction itself. A Store bound without one fails the call that needs
// it; the joined translation and target rewrite steps need neither.
type Ports struct {
	SiteSettings SiteSettings
	Checkpoints  Checkpoints
}

// NewUnits returns the production unit of work of the Menu use cases.
func NewUnits(db *gorm.DB, ports Ports) mutation.UnitOfWork[menu.Store] {
	return mutation.Postgres(db, func(tx *gorm.DB) menu.Store { return NewStore(tx, ports) })
}

// NewStore binds the Menu Store to tx, a transaction or a pool.
func NewStore(tx *gorm.DB, ports Ports) menu.Store {
	return store{tx: tx, ports: ports}
}

type store struct {
	tx    *gorm.DB
	ports Ports
}

// notFoundError keeps gorm's answer in the chain for the translation
// pipeline, which recognizes a missing entity by gorm.ErrRecordNotFound.
type notFoundError struct {
	error
	sentinel error
}

func (err notFoundError) Is(target error) bool { return target == err.sentinel }
func (err notFoundError) Unwrap() error        { return err.error }

func notFound(err, sentinel error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return notFoundError{error: err, sentinel: sentinel}
	}
	return err
}

// nameTakenError is a menu_name_key violation, readable as the driver error.
type nameTakenError struct{ error }

func (nameTakenError) Is(target error) bool { return target == menu.ErrMenuNameTaken }
func (err nameTakenError) Unwrap() error    { return err.error }

func (s store) Menu(ctx context.Context, id string) (menu.Root, error) {
	var row model.Menu
	if err := s.tx.WithContext(ctx).First(&row, "id = ?", id).Error; err != nil {
		return menu.Root{}, notFound(err, menu.ErrMenuNotFound)
	}
	return rootOf(row), nil
}

func (s store) LockMenu(ctx context.Context, id string) (menu.Root, error) {
	var row model.Menu
	if err := s.tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		First(&row, "id = ?", id).Error; err != nil {
		return menu.Root{}, notFound(err, menu.ErrMenuNotFound)
	}
	return rootOf(row), nil
}

func (s store) LockMenus(ctx context.Context) ([]menu.Root, error) {
	var rows []model.Menu
	if err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Order("id ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rootsOf(rows), nil
}

func (s store) MenuExists(ctx context.Context, id string) (bool, error) {
	var count int64
	if err := s.tx.WithContext(ctx).Table("menu").Where("id = ?", id).Count(&count).Error; err != nil {
		return false, err
	}
	return count != 0, nil
}

func (s store) ListMenus(ctx context.Context, limit, offset int) ([]menu.Root, int64, error) {
	stored := s.tx.WithContext(ctx).Model(&model.Menu{})
	var total int64
	if err := stored.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []model.Menu
	if err := stored.
		Order("name ASC").
		Limit(limit).
		Offset(offset).
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rootsOf(rows), total, nil
}

func (s store) MenuNameTaken(ctx context.Context, name, exceptID string) (bool, error) {
	query := s.tx.WithContext(ctx).Model(&model.Menu{})
	if exceptID == "" {
		query = query.Where("name = ?", name)
	} else {
		query = query.Where("name = ? AND id != ?", name, exceptID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s store) InsertMenu(ctx context.Context, root menu.Root) (menu.Root, error) {
	row := rowOf(root)
	if err := s.tx.WithContext(ctx).Omit("ID").Clauses(clause.Returning{}).Create(&row).Error; err != nil {
		if dberrors.IsUniqueViolation(err) {
			return menu.Root{}, nameTakenError{err}
		}
		return menu.Root{}, err
	}
	return rootOf(row), nil
}

func (s store) UpdateMenu(ctx context.Context, id string, change menu.RootChange) error {
	columns := structured.Fields{"updated_at": change.UpdatedAt}
	if change.Name != nil {
		columns["name"] = *change.Name
	}
	if change.ItemsJSON != nil {
		columns["items"] = change.ItemsJSON
	}
	result := s.tx.WithContext(ctx).Model(&model.Menu{}).Where("id = ?", id).Updates(columns)
	if result.Error != nil {
		wrapped := fmt.Errorf("failed to update menu: %w", result.Error)
		if dberrors.IsUniqueViolation(result.Error) {
			return nameTakenError{wrapped}
		}
		return wrapped
	}
	if result.RowsAffected == 0 {
		return notFoundError{error: gorm.ErrRecordNotFound, sentinel: menu.ErrMenuNotFound}
	}
	return nil
}

func (s store) DeleteMenu(ctx context.Context, id string) error {
	return s.tx.WithContext(ctx).Delete(&model.Menu{ID: id}).Error
}

func (s store) ContentDocument(ctx context.Context, id uuid.UUID) (menu.ContentDocument, error) {
	return s.contentDocument(ctx, id, false)
}

func (s store) LockContentDocument(ctx context.Context, id uuid.UUID) (menu.ContentDocument, error) {
	return s.contentDocument(ctx, id, true)
}

func (s store) contentDocument(ctx context.Context, id uuid.UUID, lock bool) (menu.ContentDocument, error) {
	query := s.tx.WithContext(ctx).Table("content_document").
		Select("profile, revision").Where("id = ?", id)
	if lock {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	var row struct {
		Profile  string `gorm:"column:profile"`
		Revision string `gorm:"column:revision"`
	}
	if err := query.Take(&row).Error; err != nil {
		return menu.ContentDocument{}, notFound(err, menu.ErrContentDocumentNotFound)
	}
	return menu.ContentDocument{ID: id, Profile: row.Profile, Revision: row.Revision}, nil
}

func (s store) InsertContentDocument(ctx context.Context, id uuid.UUID, profile string, revision uuid.UUID, at time.Time) error {
	return s.tx.WithContext(ctx).Exec(
		`INSERT INTO content_document (id, profile, revision, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)`,
		id, profile, revision, at, at,
	).Error
}

func (s store) AdvanceContentDocument(ctx context.Context, menuID string, id, expected, next uuid.UUID, at time.Time) error {
	updated := s.tx.WithContext(ctx).Exec(
		`UPDATE content_document
		 SET revision = ?, updated_at = ?
		 WHERE id = ? AND revision = ?
		   AND EXISTS (
			 SELECT 1 FROM menu
			 WHERE id = ? AND content_document_id = ?
		   )`,
		next, at, id, expected, menuID, id,
	)
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return menu.ErrRevisionChanged
	}
	return nil
}

func (s store) DeleteContentDocument(ctx context.Context, id uuid.UUID) error {
	result := s.tx.WithContext(ctx).Exec("DELETE FROM content_document WHERE id = ?", id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return menu.ErrContentDocumentNotFound
	}
	return nil
}

type localeRow struct {
	Locale    string    `gorm:"column:locale"`
	ItemsJSON []byte    `gorm:"column:items_json"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (s store) Locale(ctx context.Context, menuID, locale string) (menu.LocaleValues, error) {
	return s.locale(ctx, menuID, locale, false)
}

func (s store) LockLocale(ctx context.Context, menuID, locale string) (menu.LocaleValues, error) {
	return s.locale(ctx, menuID, locale, true)
}

func (s store) locale(ctx context.Context, menuID, locale string, lock bool) (menu.LocaleValues, error) {
	query := s.tx.WithContext(ctx).Table("menu_translation").
		Select("items_json, updated_at").Where("entity_id = ? AND locale = ?", menuID, locale)
	if lock {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	var row localeRow
	if err := query.Take(&row).Error; err != nil {
		return menu.LocaleValues{}, notFound(err, menu.ErrLocaleNotFound)
	}
	row.Locale = locale
	return localeValuesOf(menuID, row)
}

func (s store) Locales(ctx context.Context, menuID string) ([]menu.LocaleValues, error) {
	var rows []localeRow
	if err := s.tx.WithContext(ctx).
		Table("menu_translation").
		Select("locale, items_json, updated_at").
		Where("entity_id = ?", menuID).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	values := make([]menu.LocaleValues, 0, len(rows))
	for _, row := range rows {
		value, err := localeValuesOf(menuID, row)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func (s store) InsertLocale(ctx context.Context, values menu.LocaleValues) error {
	body, err := menu.EncodeTranslationLabelValues(values.Labels)
	if err != nil {
		return err
	}
	return s.tx.WithContext(ctx).Exec(
		`INSERT INTO menu_translation (entity_id, locale, items_json, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)`,
		values.MenuID, values.Locale, string(body), values.UpdatedAt, values.UpdatedAt,
	).Error
}

func (s store) UpdateLocale(ctx context.Context, values menu.LocaleValues) error {
	body, err := menu.EncodeTranslationLabelValues(values.Labels)
	if err != nil {
		return err
	}
	result := s.tx.WithContext(ctx).Exec(
		`UPDATE menu_translation
		 SET items_json = ?, updated_at = ?
		 WHERE entity_id = ? AND locale = ?`,
		string(body), values.UpdatedAt, values.MenuID, values.Locale,
	)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return menu.ErrLocaleNotFound
	}
	return nil
}

func (s store) UpsertLocale(ctx context.Context, values menu.LocaleValues) error {
	body, err := menu.EncodeTranslationLabelValues(values.Labels)
	if err != nil {
		return err
	}
	return s.tx.WithContext(ctx).Exec(
		`INSERT INTO menu_translation (entity_id, locale, items_json, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (entity_id, locale) DO UPDATE SET
		 items_json = EXCLUDED.items_json, updated_at = EXCLUDED.updated_at`,
		values.MenuID, values.Locale, string(body), values.UpdatedAt, values.UpdatedAt,
	).Error
}

func (s store) DeleteLocale(ctx context.Context, menuID, locale string) error {
	result := s.tx.WithContext(ctx).Exec(
		"DELETE FROM menu_translation WHERE entity_id = ? AND locale = ?",
		menuID, locale,
	)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return menu.ErrLocaleNotFound
	}
	return nil
}

func (s store) ClearSiteSettingsMenu(ctx context.Context, menuID string) error {
	if s.ports.SiteSettings == nil {
		return errors.New("menu store is not bound to Site Settings")
	}
	return s.ports.SiteSettings.ClearMenuReferences(ctx, s.tx, menuID)
}

func (s store) RequireContributors(ctx context.Context, menuID string, contributors []string, permission menu.ContributorPermission) error {
	if s.ports.Checkpoints == nil {
		return errors.New("menu store is not bound to collaboration checkpoints")
	}
	collaborationPermission := intrav1.CollaborationPermission_COLLABORATION_PERMISSION_EDIT
	if permission == menu.ContributorManage {
		collaborationPermission = intrav1.CollaborationPermission_COLLABORATION_PERMISSION_MANAGE
	}
	return s.ports.Checkpoints.RequireCurrentContributorsForPermission(
		ctx, s.tx, intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_MENU,
		menuID, contributors, collaborationPermission,
	)
}

func (s store) PublishAIDocumentChange(ctx context.Context, change menu.AIDocumentChange) error {
	return contentsignal.PublishInteractive(
		ctx, s.tx, managev1.ContentEntityType_CONTENT_ENTITY_TYPE_MENU,
		change.MenuID, change.Locale, change.DocumentRevision, change.TargetRevision,
		change.Changed, change.Deleted, change.MemberID,
	)
}

func rootOf(row model.Menu) menu.Root {
	return menu.Root{
		ID: row.ID, ContentDocumentID: row.ContentDocumentID, SourceLocale: row.SourceLocale, Name: row.Name,
		ItemsJSON: row.Items, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func rootsOf(rows []model.Menu) []menu.Root {
	roots := make([]menu.Root, len(rows))
	for i := range rows {
		roots[i] = rootOf(rows[i])
	}
	return roots
}

func rowOf(root menu.Root) model.Menu {
	return model.Menu{
		ID: root.ID, ContentDocumentID: root.ContentDocumentID, SourceLocale: root.SourceLocale, Name: root.Name,
		Items: root.ItemsJSON, CreatedAt: root.CreatedAt, UpdatedAt: root.UpdatedAt,
	}
}

func localeValuesOf(menuID string, row localeRow) (menu.LocaleValues, error) {
	labels, err := menu.DecodeTranslationLabelValues(row.ItemsJSON)
	if err != nil {
		return menu.LocaleValues{}, err
	}
	return menu.LocaleValues{MenuID: menuID, Locale: row.Locale, Labels: labels, UpdatedAt: row.UpdatedAt}, nil
}
