package filemediapostgres

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/favicon"
	faviconpostgres "github.com/echovisionlab/geul-api/internal/favicon/postgres"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *store) FaviconSource(ctx context.Context, id string, lock bool) (domain.File, error) {
	var row model.File
	query := s.tx.WithContext(ctx).Select("id", "mime_type", "file_size", "extension").Where("id = ?", id)
	if lock {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	err := query.Take(&row).Error
	return domain.File(row), fileError(err)
}
func (s *store) FaviconSet(ctx context.Context, cdn, file string) (*favicon.Set, error) {
	return faviconpostgres.LoadSet(ctx, s.tx, cdn, file)
}
func (s *store) AttachFaviconSource(ctx context.Context, asset, file string, now time.Time) (bool, error) {
	return faviconpostgres.Bind(s.tx, "").AttachSourceAsset(ctx, asset, file, now)
}
func (s *store) InsertFaviconDerivative(ctx context.Context, id, file, typ, asset string) error {
	return s.tx.WithContext(ctx).Table("file_derivative").Create(structured.Fields{"id": id, "file_id": file, "type": typ, "asset_id": asset}).Error
}
func (s *store) ScheduleStagedFaviconDeletion(ctx context.Context, ids []string, now time.Time) error {
	return faviconpostgres.Bind(s.tx, "").ScheduleStagedDeletion(ctx, ids, now)
}

type faviconCommits struct {
	db    *gorm.DB
	ports Ports
}

func NewFaviconCommits(db *gorm.DB, ports Ports) domain.FaviconCommits {
	if db == nil {
		panic("favicon commits: db is required")
	}
	return faviconCommits{db, ports}
}
func (c faviconCommits) Commit(ctx context.Context, change func(domain.Store) error) domain.FaviconCommitResult {
	tx := c.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return domain.FaviconCommitResult{Stage: domain.FaviconBeginFailed, Err: tx.Error}
	}
	if err := change(NewStore(tx, c.ports)); err != nil {
		return domain.FaviconCommitResult{Stage: domain.FaviconChangeFailed, Err: errors.Join(err, tx.Rollback().Error)}
	}
	if err := tx.Commit().Error; err != nil {
		return domain.FaviconCommitResult{Stage: domain.FaviconCommitFailed, Err: err}
	}
	return domain.FaviconCommitResult{}
}
