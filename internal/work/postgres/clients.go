package workpostgres

import (
	"context"
	"errors"

	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/work"
)

func (s *store) ClientIDs(ctx context.Context, workID string) ([]string, error) {
	var current []string
	if err := s.tx.WithContext(ctx).Table("work_client").Where("work_id = ?", workID).Order("sort_order").Pluck("client_id", &current).Error; err != nil {
		return nil, err
	}
	return current, nil
}

func (s *store) Clients(ctx context.Context, workID string) ([]work.ClientRow, error) {
	var rows []struct {
		ID          string
		Name        string
		Website     *string
		LightFileID *string `gorm:"column:light_file_id"`
		DarkFileID  *string `gorm:"column:dark_file_id"`
	}
	if err := s.tx.WithContext(ctx).
		Table("work_client wc").
		Select("c.id, c.name, c.website, c.logo_light_file_id AS light_file_id, c.logo_dark_file_id AS dark_file_id").
		Joins("JOIN client c ON c.id = wc.client_id").
		Where("wc.work_id = ?", workID).
		Order("wc.sort_order ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	clients := make([]work.ClientRow, 0, len(rows))
	for _, row := range rows {
		clients = append(clients, work.ClientRow{ID: row.ID, Name: row.Name, Website: row.Website, LightFileID: row.LightFileID, DarkFileID: row.DarkFileID})
	}
	return clients, nil
}

func (s *store) CountExistingClients(ctx context.Context, clientIDs []string) (int64, error) {
	var validCount int64
	if err := s.tx.WithContext(ctx).Table("client").Where("id IN ?", clientIDs).Count(&validCount).Error; err != nil {
		return 0, err
	}
	return validCount, nil
}

func (s *store) ReplaceClients(ctx context.Context, workID string, clientIDs []string) error {
	if err := s.tx.WithContext(ctx).Exec("DELETE FROM work_client WHERE work_id = ?", workID).Error; err != nil {
		return err
	}
	for sortOrder, clientID := range clientIDs {
		if err := s.tx.WithContext(ctx).Exec(
			"INSERT INTO work_client (work_id, client_id, sort_order, created_at) VALUES (?, ?, ?, NOW())",
			workID, clientID, sortOrder,
		).Error; err != nil {
			return err
		}
	}
	return nil
}

func versionFromRow(row model.WorkVersion) work.Version {
	return work.Version{
		ID: row.ID, WorkID: row.WorkID, Version: row.Version, Title: row.Title, Summary: row.Summary,
		ContentSnapshot: row.ContentSnapshot, ContributorMemberIDs: []string(row.ContributorMemberIDs), CreatedAt: row.CreatedAt,
	}
}

func (s *store) CountVersions(ctx context.Context, workID string) (int64, error) {
	var total int64
	if err := s.tx.WithContext(ctx).
		Model(&model.WorkVersion{}).
		Where("work_id = ?", workID).
		Count(&total).Error; err != nil {
		return 0, err
	}
	return total, nil
}

func (s *store) ListVersions(ctx context.Context, workID string, limit, offset int32) ([]work.Version, error) {
	var rows []model.WorkVersion
	if err := s.tx.WithContext(ctx).
		Where("work_id = ?", workID).
		Order("version DESC").
		Limit(int(limit)).
		Offset(int(offset)).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	versions := make([]work.Version, len(rows))
	for i := range rows {
		versions[i] = versionFromRow(rows[i])
	}
	return versions, nil
}

func (s *store) FindVersion(ctx context.Context, versionID string) (work.Version, error) {
	var row model.WorkVersion
	if err := s.tx.WithContext(ctx).First(&row, "id = ?", versionID).Error; err != nil {
		return work.Version{}, notFound(err, work.ErrVersionNotFound)
	}
	return versionFromRow(row), nil
}

func (s *store) LockLatestVersion(ctx context.Context, workID string) (work.Version, bool, error) {
	var previous model.WorkVersion
	result := s.tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("work_id = ?", workID).
		Order("version DESC").
		First(&previous)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return work.Version{}, false, nil
	}
	if result.Error != nil {
		return work.Version{}, false, result.Error
	}
	return versionFromRow(previous), true, nil
}

func (s *store) InsertVersion(ctx context.Context, version *work.Version) error {
	row := model.WorkVersion{
		WorkID: version.WorkID, Version: version.Version, Title: version.Title, Summary: version.Summary,
		ContentSnapshot: version.ContentSnapshot, ContributorMemberIDs: pq.StringArray(version.ContributorMemberIDs),
		CreatedAt: version.CreatedAt,
	}
	if err := s.tx.WithContext(ctx).Create(&row).Error; err != nil {
		return err
	}
	version.ID = row.ID
	return nil
}

func (s *store) ContributorNicknames(ctx context.Context, memberIDs []string) (map[string]string, error) {
	var members []model.Member
	if err := s.tx.WithContext(ctx).Select("id", "nickname").Where("id IN ?", memberIDs).Find(&members).Error; err != nil {
		return nil, err
	}
	nicknames := make(map[string]string, len(members))
	for _, member := range members {
		nicknames[member.ID] = member.Nickname
	}
	return nicknames, nil
}
