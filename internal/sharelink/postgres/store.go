// Package sharelinkpostgres is the PostgreSQL Share Link Store: the
// share_link table, and the rows of link targets other domains own.
package sharelinkpostgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/sharelink"
)

// NewUnits returns the Share Link unit of work over db.
func NewUnits(db *gorm.DB) mutation.UnitOfWork[sharelink.Store] {
	return mutation.Postgres(db, NewStore)
}

// NewStore returns the Store bound to db, a transaction or the pool, for an
// adapter that serves another domain inside that domain's own transaction.
func NewStore(db *gorm.DB) sharelink.Store {
	return store{db: db}
}

type store struct {
	db *gorm.DB
}

func (s store) ListLinks(ctx context.Context, entityType sharelink.EntityType, entityID string) ([]sharelink.Link, error) {
	var rows []model.ShareLink
	if err := s.db.WithContext(ctx).
		Where("entity_type = ? AND entity_id = ?", string(entityType), entityID).
		Order("created_at DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	links := make([]sharelink.Link, len(rows))
	for i := range rows {
		links[i] = linkOf(rows[i])
	}
	return links, nil
}

func (s store) Link(ctx context.Context, id string) (sharelink.Link, error) {
	var row model.ShareLink
	if err := s.db.WithContext(ctx).First(&row, "id = ?", id).Error; err != nil {
		return sharelink.Link{}, notFound(err, sharelink.ErrLinkNotFound)
	}
	return linkOf(row), nil
}

func (s store) LinkByToken(ctx context.Context, token string) (sharelink.Link, error) {
	var row model.ShareLink
	if err := s.db.WithContext(ctx).First(&row, "token = ?", token).Error; err != nil {
		return sharelink.Link{}, notFound(err, sharelink.ErrLinkNotFound)
	}
	return linkOf(row), nil
}

func (s store) LiveLink(ctx context.Context, token string, entityType sharelink.EntityType, entityID string, now time.Time) (sharelink.Link, error) {
	var row model.ShareLink
	if err := s.db.WithContext(ctx).
		Where("token = ? AND entity_type = ? AND entity_id = ? AND expires_at > ?", token, string(entityType), entityID, now).
		Take(&row).Error; err != nil {
		return sharelink.Link{}, notFound(err, sharelink.ErrLinkNotFound)
	}
	return linkOf(row), nil
}

func (s store) InsertLink(ctx context.Context, link sharelink.Link) (sharelink.Link, error) {
	row := RowOf(link)
	if err := s.db.WithContext(ctx).Clauses(clause.Returning{}).
		Select("Token", "EntityType", "EntityID", "Label", "PasswordHash", "ExpiresAt", "CreatedAt").Create(&row).Error; err != nil {
		return sharelink.Link{}, err
	}
	return linkOf(row), nil
}

func (s store) InsertLinkUnlessTokenExists(ctx context.Context, link sharelink.Link) error {
	row := RowOf(link)
	return s.db.WithContext(ctx).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "token"}}, DoNothing: true}).
		Create(&row).Error
}

func (s store) DeleteLink(ctx context.Context, link sharelink.Link) error {
	result := s.db.WithContext(ctx).Where("id = ? AND entity_type = ? AND entity_id = ?", link.ID, string(link.EntityType), link.EntityID).Delete(&model.ShareLink{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return sharelink.ErrLinkNotFound
	}
	return nil
}

func (s store) DeleteTargetLinks(ctx context.Context, entityID string, entityTypes ...sharelink.EntityType) error {
	stored := make([]string, len(entityTypes))
	for i, entityType := range entityTypes {
		stored[i] = string(entityType)
	}
	return s.db.WithContext(ctx).Where("entity_id = ? AND entity_type IN ?", entityID, stored).Delete(&model.ShareLink{}).Error
}

func (s store) DeletePasswordFreeLink(ctx context.Context, token string, entityType sharelink.EntityType, entityID string) error {
	return s.db.WithContext(ctx).
		Where("token = ? AND entity_type = ? AND entity_id = ? AND password_hash IS NULL", token, string(entityType), entityID).
		Delete(&model.ShareLink{}).Error
}

func (s store) DeleteExpiredLinks(ctx context.Context, now time.Time) (int64, error) {
	result := s.db.WithContext(ctx).Where("expires_at < ?", now).Delete(&model.ShareLink{})
	return result.RowsAffected, result.Error
}

func (s store) Target(ctx context.Context, entityType sharelink.EntityType, entityID string) (sharelink.Target, error) {
	return s.target(ctx, entityType, entityID, false)
}

func (s store) LockTarget(ctx context.Context, entityType sharelink.EntityType, entityID string) (sharelink.Target, error) {
	return s.target(ctx, entityType, entityID, true)
}

// targetTable is where a link target's lifecycle is stored.
type targetTable struct {
	table string
	// archived is the status of an archived target; empty for a table
	// without an archived lifecycle.
	archived string
	// scheduled is the status of a legal version scheduled to take effect;
	// empty for a target that is not a legal version.
	scheduled string
}

func targetTableOf(entityType sharelink.EntityType) (targetTable, bool) {
	switch entityType {
	case sharelink.EntityPost:
		return targetTable{table: "post", archived: string(model.PostStatusArchived)}, true
	case sharelink.EntityPage:
		return targetTable{table: "page"}, true
	case sharelink.EntityWork:
		return targetTable{table: "work", archived: string(model.WorkStatusArchived)}, true
	case sharelink.EntityRelease:
		return targetTable{table: "release"}, true
	case sharelink.EntityForm, sharelink.EntityFormDashboard:
		return targetTable{table: "form"}, true
	case sharelink.EntityLabel:
		return targetTable{table: "label"}, true
	case sharelink.EntityArtist:
		return targetTable{table: "artist"}, true
	case sharelink.EntityPrivacy:
		return targetTable{table: "privacy_history", archived: string(model.PrivacyStatusArchived), scheduled: string(model.PrivacyStatusScheduled)}, true
	case sharelink.EntityTerms:
		return targetTable{table: "terms_history", archived: string(model.TermsStatusArchived), scheduled: string(model.TermsStatusScheduled)}, true
	default:
		return targetTable{}, false
	}
}

type targetRow struct {
	ID            string     `gorm:"column:id"`
	Version       int64      `gorm:"column:version"`
	Status        string     `gorm:"column:status"`
	EffectiveFrom *time.Time `gorm:"column:effective_from"`
}

func (s store) target(ctx context.Context, entityType sharelink.EntityType, entityID string, lock bool) (sharelink.Target, error) {
	table, ok := targetTableOf(entityType)
	if !ok {
		return sharelink.Target{}, fmt.Errorf("share link target type %q has no table", entityType)
	}
	columns := []string{"id"}
	if table.archived != "" {
		columns = append(columns, "status")
	}
	if table.scheduled != "" {
		columns = append(columns, "effective_from", "version")
	}
	var row targetRow
	query := s.db.WithContext(ctx).Table(table.table).Select(columns).Where("id = ?", entityID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	if err := query.Take(&row).Error; err != nil {
		return sharelink.Target{}, notFound(err, sharelink.ErrTargetNotFound)
	}
	return targetOf(table, row), nil
}

func targetOf(table targetTable, row targetRow) sharelink.Target {
	return sharelink.Target{
		Archived:      table.archived != "" && row.Status == table.archived,
		Scheduled:     table.scheduled != "" && row.Status == table.scheduled,
		EffectiveFrom: row.EffectiveFrom,
		Version:       row.Version,
	}
}

// notFound answers sentinel for a missing row and keeps every other failure.
func notFound(err, sentinel error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return sentinel
	}
	return err
}

func linkOf(row model.ShareLink) sharelink.Link {
	link := sharelink.Link{
		ID: row.ID, Token: row.Token, EntityType: sharelink.EntityType(row.EntityType), EntityID: row.EntityID,
		Label: row.Label, PasswordHash: row.PasswordHash, CreatedAt: row.CreatedAt,
	}
	if row.ExpiresAt != nil {
		link.ExpiresAt = *row.ExpiresAt
	}
	return link
}

// RowOf is the share_link row of link, for an adapter whose port still
// answers with the row.
func RowOf(link sharelink.Link) model.ShareLink {
	expiresAt := link.ExpiresAt
	return model.ShareLink{
		ID: link.ID, Token: link.Token, EntityType: string(link.EntityType), EntityID: link.EntityID,
		Label: link.Label, PasswordHash: link.PasswordHash, ExpiresAt: &expiresAt, CreatedAt: link.CreatedAt,
	}
}
