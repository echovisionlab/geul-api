package campaignpostgres

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	campaigndomain "github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
	emailcontentpostgres "github.com/echovisionlab/geul-api/internal/emailcontent/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"gorm.io/gorm/clause"
)

func (s *store) FindCampaign(ctx context.Context, id string) (campaigndomain.Campaign, error) {
	var row model.Campaign
	err := s.tx.WithContext(ctx).First(&row, "id = ?", id).Error
	return CampaignFromRow(row), notFound(err, campaigndomain.ErrCampaignNotFound)
}
func (s *store) LockCampaign(ctx context.Context, id string) (campaigndomain.Campaign, error) {
	var row model.Campaign
	err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&row, "id = ?", id).Error
	return CampaignFromRow(row), notFound(err, campaigndomain.ErrCampaignNotFound)
}
func (s *store) InsertCampaign(ctx context.Context, value *campaigndomain.Campaign) error {
	row := CampaignToRow(*value)
	if err := s.tx.WithContext(ctx).Create(&row).Error; err != nil {
		return err
	}
	*value = CampaignFromRow(row)
	return nil
}
func (s *store) SaveCampaign(ctx context.Context, value *campaigndomain.Campaign) error {
	row := CampaignToRow(*value)
	if err := s.tx.WithContext(ctx).Save(&row).Error; err != nil {
		return err
	}
	*value = CampaignFromRow(row)
	return nil
}
func (s *store) DeleteCampaign(ctx context.Context, value campaigndomain.Campaign) error {
	row := CampaignToRow(value)
	err := s.tx.WithContext(ctx).Delete(&row).Error
	if dberrors.IsForeignKeyViolation(err) {
		return sentinelError{error: err, sentinel: campaigndomain.ErrDurableReference}
	}
	return err
}
func (s *store) UpdateCampaign(ctx context.Context, value campaigndomain.Campaign, change campaigndomain.CampaignChange) error {
	row := CampaignToRow(value)
	updates := structured.Fields{}
	if change.Name != nil {
		updates["name"] = *change.Name
	}
	if next := change.Configuration; next != nil {
		if value.TargetMode != next.TargetMode {
			updates["target_mode"] = next.TargetMode
		}
		if ptrStringValue(value.CriteriaID) != ptrStringValue(next.CriteriaID) {
			updates["member_criteria_id"] = next.CriteriaID
		}
		if ptrStringValue(value.LayoutID) != ptrStringValue(next.LayoutID) {
			updates["layout_id"] = next.LayoutID
		}
		if value.RecipientScope != next.RecipientScope {
			updates["recipient_scope"] = next.RecipientScope
		}
	}
	updates["updated_at"] = change.UpdatedAt
	return s.tx.WithContext(ctx).Model(&row).Updates(updates).Error
}
func (s *store) UpdateCampaignStatus(ctx context.Context, value campaigndomain.Campaign, status campaigndomain.CampaignStatus) error {
	row := CampaignToRow(value)
	return s.tx.WithContext(ctx).Model(&row).Update("status", status).Error
}
func (s *store) CampaignLayoutID(ctx context.Context, id string) (*string, error) {
	var row struct{ LayoutID *string }
	err := s.tx.WithContext(ctx).Model(&model.Campaign{}).Select("layout_id").First(&row, "id = ?", id).Error
	return row.LayoutID, notFound(err, campaigndomain.ErrCampaignNotFound)
}
func (s *store) CampaignDocumentID(ctx context.Context, id string, lock bool) (*string, error) {
	query := s.tx.WithContext(ctx).Table("campaign").Select("id", "content_document_id").Where("id = ?", id)
	if lock {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	var row struct {
		ID                string
		ContentDocumentID *string
	}
	err := query.Take(&row).Error
	return row.ContentDocumentID, notFound(err, campaigndomain.ErrCampaignNotFound)
}
func (s *store) CampaignSourceLocale(ctx context.Context, id string, lock bool) (string, error) {
	var row struct{ SourceLocale string }
	query := s.tx.WithContext(ctx).Table("campaign").Select("source_locale").Where("id = ?", id)
	if lock {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	err := query.Take(&row).Error
	return row.SourceLocale, notFound(err, campaigndomain.ErrCampaignNotFound)
}
func (s *store) CampaignStatus(ctx context.Context, id string) (campaigndomain.CampaignStatus, error) {
	var row struct{ Status string }
	err := s.tx.WithContext(ctx).Table("campaign").Select("status").Where("id = ?", id).Take(&row).Error
	return campaigndomain.CampaignStatus(row.Status), notFound(err, campaigndomain.ErrCampaignNotFound)
}
func (s *store) CampaignAIDocumentRoot(ctx context.Context, id string, lock emailcontent.RowLock) (campaigndomain.Campaign, error) {
	var row model.Campaign
	query := s.tx.WithContext(ctx).Table("campaign").Where("id = ?", id)
	if strength := emailcontentpostgres.Lock(lock); strength != "" {
		query = query.Clauses(clause.Locking{Strength: strength})
	}
	err := query.Take(&row).Error
	return CampaignFromRow(row), notFound(err, campaigndomain.ErrCampaignNotFound)
}
func (s *store) CampaignSourceLocales(ctx context.Context, ids []string) (map[string]string, error) {
	var rows []struct {
		EntityID     string
		SourceLocale string
	}
	if err := s.tx.WithContext(ctx).Table("campaign").Select("id AS entity_id, source_locale").Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make(map[string]string, len(rows))
	for _, row := range rows {
		result[row.EntityID] = row.SourceLocale
	}
	return result, nil
}
func (s *store) ListCampaigns(ctx context.Context, q campaigndomain.CampaignList) ([]campaigndomain.Campaign, int64, error) {
	list, err := campaignFilterConfig.ApplyFilters(s.tx.WithContext(ctx).Model(&model.Campaign{}), q.Filters)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err := list.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	list, err = campaignSortConfig.ApplySort(list, q.Sorts)
	if err != nil {
		return nil, 0, err
	}
	var rows []model.Campaign
	if err := list.Limit(q.Limit).Offset(q.Offset).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	result := make([]campaigndomain.Campaign, len(rows))
	for i, row := range rows {
		result[i] = CampaignFromRow(row)
	}
	return result, total, nil
}
func (s *store) ActiveCampaignRunCount(ctx context.Context, id string) (int64, error) {
	var count int64
	err := s.tx.WithContext(ctx).Model(&model.CampaignDeliveryRun{}).Where("campaign_id = ? AND status IN ?", id, []string{campaigndomain.CampaignDeliveryRunStatusScheduled, campaigndomain.CampaignDeliveryRunStatusSending}).Count(&count).Error
	return count, err
}
func (s *store) RenderableCampaignContentCount(ctx context.Context, id string) (int64, error) {
	var count int64
	err := s.tx.WithContext(ctx).Table("campaign_translation AS translation").Joins("JOIN campaign AS source ON source.id = translation.entity_id AND source.source_locale = translation.locale").Where("translation.entity_id = ?", id).Where("translation.content_html IS NOT NULL AND btrim(translation.content_html) <> ''").Count(&count).Error
	return count, err
}
func (s *store) SetSourceSubject(ctx context.Context, id, subject string, now time.Time) error {
	return s.tx.WithContext(ctx).Table("campaign").Where("id = ?", id).Updates(map[string]any{"subject": subject, "updated_at": now}).Error
}

func (s *store) CampaignLocaleMetadata(ctx context.Context, id string) ([]campaigndomain.CampaignLocaleMetadata, error) {
	var rows []struct {
		Locale  string
		Subject *string
	}
	if err := s.tx.WithContext(ctx).Table("campaign_translation").Select("locale", "subject").Where("entity_id = ?", id).Order("locale ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]campaigndomain.CampaignLocaleMetadata, len(rows))
	for i, row := range rows {
		result[i] = campaigndomain.CampaignLocaleMetadata{Locale: row.Locale, Subject: row.Subject}
	}
	return result, nil
}
func (s *store) CampaignRenderLocales(ctx context.Context, id string) ([]campaigndomain.CampaignRenderLocale, error) {
	var rows []struct {
		Locale         string
		Subject        *string
		IsSourceLocale bool
	}
	if err := s.tx.WithContext(ctx).Table("campaign_translation AS translation").Select("translation.locale, translation.subject, translation.locale = source.source_locale AS is_source_locale").Joins("JOIN campaign AS source ON source.id = translation.entity_id").Where("translation.entity_id = ?", id).Order("is_source_locale DESC, locale ASC").Scan(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]campaigndomain.CampaignRenderLocale, len(rows))
	for i, row := range rows {
		result[i] = campaigndomain.CampaignRenderLocale{Locale: row.Locale, Subject: row.Subject, IsSourceLocale: row.IsSourceLocale}
	}
	return result, nil
}

func (s *store) CampaignLocaleSubject(ctx context.Context, id, locale string, lock bool) (*string, bool, error) {
	var row struct{ Subject *string }
	query := s.tx.WithContext(ctx).Table("campaign_translation").Select("subject").Where("entity_id = ? AND locale = ?", id, locale)
	if lock {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	err := query.Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	return row.Subject, err == nil, err
}
func (s *store) InsertSourceSubject(ctx context.Context, id, locale, subject string, now time.Time) error {
	return s.tx.WithContext(ctx).Exec(`INSERT INTO campaign_translation (
                entity_id, locale, subject, created_at, updated_at
            ) VALUES (?, ?, ?, ?, ?)`, id, locale, subject, now, now).Error
}
func (s *store) UpdateSourceSubject(ctx context.Context, id, locale, subject string, now time.Time) error {
	return s.tx.WithContext(ctx).Table("campaign_translation").Where("entity_id = ? AND locale = ?", id, locale).Updates(map[string]any{"subject": subject, "updated_at": now}).Error
}
