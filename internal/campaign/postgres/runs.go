package campaignpostgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	campaigndomain "github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/identityread"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *store) CancelActiveRuns(
	ctx context.Context,
	campaignID string,
	now time.Time,
) error {
	campaignID = strings.TrimSpace(campaignID)
	var campaign model.Campaign
	if err := s.tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id").
		First(&campaign, "id = ?", campaignID).Error; err != nil {
		return err
	}

	var activeRuns []model.CampaignDeliveryRun
	if err := s.tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where(
			"campaign_id = ? AND status IN ?",
			campaignID,
			[]string{
				campaigndomain.CampaignDeliveryRunStatusScheduled,
				campaigndomain.CampaignDeliveryRunStatusSending,
			},
		).
		Order("id ASC").
		Find(&activeRuns).Error; err != nil {
		return err
	}
	for i := range activeRuns {
		if activeRuns[i].Status == campaigndomain.CampaignDeliveryRunStatusSending {
			return campaigndomain.ErrDeliveryStarted
		}
	}

	if err := s.tx.WithContext(ctx).
		Model(&model.CampaignDeliveryRun{}).
		Where(
			"campaign_id = ? AND status = ?",
			campaignID,
			campaigndomain.CampaignDeliveryRunStatusScheduled,
		).
		Updates(structured.Fields{
			"status":       campaigndomain.CampaignDeliveryRunStatusCancelled,
			"completed_at": now.UTC(),
		}).Error; err != nil {
		return err
	}
	return nil
}
func (s *store) HasHistory(
	ctx context.Context,
	campaignID string,
) (bool, error) {
	var count int64
	if err := s.tx.WithContext(ctx).
		Model(&model.CampaignDeliveryRun{}).
		Where("campaign_id = ?", strings.TrimSpace(campaignID)).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}
func (s *store) LatestStats(
	ctx context.Context,
	campaignID string,
) (*campaigndomain.CampaignDeliveryStats, error) {
	run, err := latestCampaignDeliveryRun(ctx, s.tx, campaignID)
	if err != nil || run == nil {
		return nil, err
	}
	type statusCount struct {
		Status string `gorm:"column:status"`
		Count  int64  `gorm:"column:count"`
	}
	var counts []statusCount
	if err := s.tx.WithContext(ctx).
		Model(&model.CampaignDeliveryRecipient{}).
		Select("status, COUNT(*) AS count").
		Where("run_id = ?", run.ID).
		Group("status").
		Find(&counts).Error; err != nil {
		return nil, err
	}
	stats := campaigndomain.CampaignDeliveryStats{}
	for _, count := range counts {
		switch count.Status {
		case campaigndomain.CampaignDeliveryRecipientStatusSent,
			campaigndomain.CampaignDeliveryRecipientStatusDelivered:
			stats.TotalSent += int32(count.Count)
		case campaigndomain.CampaignDeliveryRecipientStatusSkipped:
			stats.Skipped += int32(count.Count)
		case campaigndomain.CampaignDeliveryRecipientStatusPermanentFailed,
			campaigndomain.CampaignDeliveryRecipientStatusBounced,
			campaigndomain.CampaignDeliveryRecipientStatusComplained:
			stats.Failed += int32(count.Count)
		case campaigndomain.CampaignDeliveryRecipientStatusBlocked:
			stats.Blocked += int32(count.Count)
		case campaigndomain.CampaignDeliveryRecipientStatusSuppressed:
			stats.Suppressed += int32(count.Count)
		}
	}
	return &stats, nil
}
func (s *store) ListRecipients(
	ctx context.Context,
	campaignID string,
	limit int,
	offset int,
) (campaigndomain.CampaignDeliveryRecipientPage, error) {
	run, err := latestCampaignDeliveryRun(ctx, s.tx, campaignID)
	if err != nil || run == nil {
		return campaigndomain.CampaignDeliveryRecipientPage{}, err
	}
	var total int64
	if err := s.tx.WithContext(ctx).
		Model(&model.CampaignDeliveryRecipient{}).
		Where("run_id = ?", run.ID).
		Count(&total).Error; err != nil {
		return campaigndomain.CampaignDeliveryRecipientPage{}, err
	}
	var rows []model.CampaignDeliveryRecipient
	if err := s.tx.WithContext(ctx).
		Where("run_id = ?", run.ID).
		Order("created_at DESC, id DESC").
		Limit(limit).
		Offset(offset).
		Find(&rows).Error; err != nil {
		return campaigndomain.CampaignDeliveryRecipientPage{}, err
	}
	page := campaigndomain.CampaignDeliveryRecipientPage{
		Recipients: make([]campaigndomain.CampaignDeliveryRecipient, len(rows)),
		Total:      total,
	}
	for index, row := range rows {
		page.Recipients[index] = campaigndomain.CampaignDeliveryRecipient{
			Email:      row.RecipientEmail,
			Status:     row.Status,
			MemberID:   strings.TrimSpace(ptrStringValue(row.MemberID)),
			ErrorType:  row.ErrorType,
			TerminalAt: row.TerminalAt,
		}
	}
	return page, nil
}
func saveCampaignDeliveryTargetRelations(
	ctx context.Context,
	tx *gorm.DB,
	runID string,
	target campaigndomain.CampaignDeliveryTarget,
) error {
	if len(target.MemberTagIDs) > 0 {
		rows := make([]model.EmailDeliveryRunTargetUserTag, 0, len(target.MemberTagIDs))
		for _, tagID := range target.MemberTagIDs {
			rows = append(rows, model.EmailDeliveryRunTargetUserTag{RunID: runID, UserTagID: tagID})
		}
		if err := tx.WithContext(ctx).Create(&rows).Error; err != nil {
			return err
		}
	}
	if len(target.AccountRoles) > 0 {
		rows := make([]model.EmailDeliveryRunTargetUserRole, 0, len(target.AccountRoles))
		for _, role := range target.AccountRoles {
			rows = append(rows, model.EmailDeliveryRunTargetUserRole{RunID: runID, Role: role})
		}
		if err := tx.WithContext(ctx).Create(&rows).Error; err != nil {
			return err
		}
	}
	if len(target.ExcludedMemberIDs) == 0 {
		return nil
	}
	rows, err := resolveCampaignDeliveryExcludedMemberPairs(
		ctx,
		tx,
		runID,
		target.ExcludedMemberIDs,
	)
	if err != nil {
		return err
	}
	return tx.WithContext(ctx).Create(&rows).Error
}
func resolveCampaignDeliveryExcludedMemberPairs(
	ctx context.Context,
	tx *gorm.DB,
	runID string,
	memberIDs []string,
) ([]model.EmailDeliveryRunTargetExcludedMember, error) {
	type memberIdentityPair struct {
		MemberID   string `gorm:"column:member_id"`
		IdentityID string `gorm:"column:identity_id"`
	}
	var pairs []memberIdentityPair
	if err := tx.WithContext(ctx).
		Table("member AS member").
		Select("member.id AS member_id, identity.id AS identity_id").
		Joins(identityread.JoinLinked("member", "identity")).
		Where("member.id IN ?", memberIDs).
		Where("member.deleted_at IS NULL").
		Where("member.onboarded = TRUE").
		Order("member.id ASC").
		Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
		Scan(&pairs).Error; err != nil {
		return nil, err
	}
	if len(pairs) != len(memberIDs) {
		return nil, fmt.Errorf(
			"every excluded member must be onboarded with an exact bilateral Kratos identity link",
		)
	}
	rows := make([]model.EmailDeliveryRunTargetExcludedMember, 0, len(pairs))
	for _, pair := range pairs {
		rows = append(rows, model.EmailDeliveryRunTargetExcludedMember{
			RunID:      runID,
			MemberID:   pair.MemberID,
			IdentityID: pair.IdentityID,
		})
	}
	return rows, nil
}
func latestCampaignDeliveryRun(
	ctx context.Context,
	db *gorm.DB,
	campaignID string,
) (*model.CampaignDeliveryRun, error) {
	var run model.CampaignDeliveryRun
	err := db.WithContext(ctx).
		Where("campaign_id = ?", strings.TrimSpace(campaignID)).
		Order("created_at DESC, id DESC").
		First(&run).Error
	if err == nil {
		return &run, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return nil, err
}
func (s *store) InsertSealedRun(
	ctx context.Context,
	value *campaigndomain.DeliveryRun,
	target *campaigndomain.CampaignDeliveryTarget,
) error {
	row := DeliveryRunToRow(*value)
	run := &row
	tx := s.tx.WithContext(ctx)
	if err := tx.WithContext(ctx).Create(run).Error; err != nil {
		return err
	}
	if target != nil {
		if err := saveCampaignDeliveryTargetRelations(ctx, tx, run.ID, *target); err != nil {
			return err
		}
	}
	sealed := tx.WithContext(ctx).
		Model(&model.CampaignDeliveryRun{}).
		Where("id = ? AND definition_sealed = FALSE", run.ID).
		Update("definition_sealed", true)
	if sealed.Error != nil {
		return sealed.Error
	}
	if sealed.RowsAffected != 1 {
		return errors.New("email delivery run definition could not be sealed")
	}
	run.DefinitionSealed = true
	*value = DeliveryRunFromRow(*run)
	return nil
}
func (s *store) LoadDeliveryTarget(
	ctx context.Context,
	run campaigndomain.DeliveryRun,
) (campaigndomain.CampaignDeliveryTarget, error) {
	target := campaigndomain.CampaignDeliveryTarget{
		QueryVersion:   run.TargetQueryVersion,
		Mode:           run.TargetMode,
		RecipientScope: run.TargetRecipientScope,
		CreatedAfter:   run.TargetCreatedAfter,
		CreatedBefore:  run.TargetCreatedBefore,
	}
	if err := s.tx.WithContext(ctx).
		Model(&model.EmailDeliveryRunTargetUserTag{}).
		Where("run_id = ?", run.ID).
		Order("user_tag_id ASC").
		Pluck("user_tag_id", &target.MemberTagIDs).Error; err != nil {
		return campaigndomain.CampaignDeliveryTarget{}, err
	}
	if err := s.tx.WithContext(ctx).
		Model(&model.EmailDeliveryRunTargetUserRole{}).
		Where("run_id = ?", run.ID).
		Order("role ASC").
		Pluck("role", &target.AccountRoles).Error; err != nil {
		return campaigndomain.CampaignDeliveryTarget{}, err
	}
	if err := s.tx.WithContext(ctx).
		Model(&model.EmailDeliveryRunTargetExcludedMember{}).
		Where("run_id = ?", run.ID).
		Order("member_id ASC").
		Pluck("member_id", &target.ExcludedMemberIDs).Error; err != nil {
		return campaigndomain.CampaignDeliveryTarget{}, err
	}
	return target, nil
}
func campaignDeliveryTargetRecipientSelection(
	target campaigndomain.CampaignDeliveryTarget,
) (*bulkEmailRecipientSelection, error) {
	if err := campaigndomain.ValidateCampaignDeliveryTarget(target); err != nil {
		return nil, err
	}
	selection := &bulkEmailRecipientSelection{
		RequireNewsletterSubscription: target.RecipientScope == "SUBSCRIBED_USERS",
	}
	switch target.Mode {
	case campaigndomain.CampaignDeliveryTargetModeAllUsers:
		selection.Mode = campaigndomain.CampaignDeliveryTargetModeAllUsers
	case campaigndomain.CampaignDeliveryTargetModeUserTags:
		if len(target.MemberTagIDs) == 0 {
			return nil, fmt.Errorf("user-tags target has no user tags")
		}
		selection.Mode = campaigndomain.CampaignDeliveryTargetModeUserTags
		selection.MemberTagIDs = target.MemberTagIDs
	case campaigndomain.CampaignDeliveryTargetModeUsersByFilter:
		selection.Mode = campaigndomain.CampaignDeliveryTargetModeUsersByFilter
		selection.Filters = &bulkEmailRecipientFilters{
			AccountRoles:      target.AccountRoles,
			CreatedAfter:      target.CreatedAfter,
			CreatedBefore:     target.CreatedBefore,
			ExcludedMemberIDs: target.ExcludedMemberIDs,
		}
	default:
		return nil, fmt.Errorf("unsupported email delivery target mode: %s", target.Mode)
	}
	return selection, nil
}
func (s *store) lockDeliveryRun(
	ctx context.Context,
	runID string,
	skipLocked bool,
) (campaigndomain.LockedDeliveryRun, bool, error) {
	tx := s.tx.WithContext(ctx)
	lockOptions := ""
	if skipLocked {
		lockOptions = clause.LockingOptionsSkipLocked
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return campaigndomain.LockedDeliveryRun{}, false, gorm.ErrRecordNotFound
	}

	var identity struct {
		RunKind    string  `gorm:"column:run_kind"`
		CampaignID *string `gorm:"column:campaign_id"`
	}
	if err := tx.WithContext(ctx).
		Model(&model.CampaignDeliveryRun{}).
		Select("run_kind", "campaign_id").
		First(&identity, "id = ?", runID).Error; err != nil {
		return campaigndomain.LockedDeliveryRun{}, false, err
	}

	lock := clause.Locking{Strength: clause.LockingStrengthUpdate, Options: lockOptions}
	locked := campaigndomain.LockedDeliveryRun{}
	campaignID := strings.TrimSpace(ptrStringValue(identity.CampaignID))
	campaign, skipped, err := lockEmailDeliveryCampaign(
		ctx,
		tx,
		lock,
		identity.RunKind,
		campaignID,
		lockOptions,
	)
	if err != nil || skipped {
		return campaigndomain.LockedDeliveryRun{}, skipped, err
	}
	if campaign != nil {
		row := CampaignFromRow(*campaign)
		locked.Campaign = &row
	}
	var run model.CampaignDeliveryRun

	if err := tx.WithContext(ctx).
		Clauses(lock).
		First(&run, "id = ?", runID).Error; err != nil {
		if lockOptions != "" && errors.Is(err, gorm.ErrRecordNotFound) {
			return campaigndomain.LockedDeliveryRun{}, true, nil
		}
		return campaigndomain.LockedDeliveryRun{}, false, err
	}
	locked.Run = DeliveryRunFromRow(run)
	if locked.Run.RunKind != identity.RunKind ||
		strings.TrimSpace(ptrStringValue(locked.Run.CampaignID)) != campaignID {
		return campaigndomain.LockedDeliveryRun{}, false, fmt.Errorf(
			"email delivery run relationship changed while acquiring locks",
		)
	}
	return locked, false, nil
}
func lockEmailDeliveryCampaign(
	ctx context.Context,
	tx *gorm.DB,
	lock clause.Locking,
	runKind string,
	campaignID string,
	lockOptions string,
) (*model.Campaign, bool, error) {
	if runKind != campaigndomain.EmailDeliveryRunKindCampaign {
		return nil, false, nil
	}
	if campaignID == "" {
		return nil, false, fmt.Errorf("campaign delivery run missing campaign id")
	}

	var campaign model.Campaign
	err := tx.WithContext(ctx).Clauses(lock).First(&campaign, "id = ?", campaignID).Error
	if err == nil {
		return &campaign, false, nil
	}
	if lockOptions != "" && errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, true, nil
	}
	return nil, false, err
}
func (s *store) FindRun(ctx context.Context, id string) (campaigndomain.DeliveryRun, error) {
	var row model.CampaignDeliveryRun
	err := s.tx.WithContext(ctx).First(&row, "id = ?", id).Error
	return DeliveryRunFromRow(row), notFound(err, campaigndomain.ErrNotFound)
}
func (s *store) LockRun(ctx context.Context, id string) (campaigndomain.DeliveryRun, error) {
	var row model.CampaignDeliveryRun
	err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&row, "id = ?", id).Error
	return DeliveryRunFromRow(row), notFound(err, campaigndomain.ErrNotFound)
}
func (s *store) SaveRun(ctx context.Context, value *campaigndomain.DeliveryRun) error {
	row := DeliveryRunToRow(*value)
	if err := s.tx.WithContext(ctx).Save(&row).Error; err != nil {
		return err
	}
	*value = DeliveryRunFromRow(row)
	return nil
}
func (s *store) StartRun(ctx context.Context, run campaigndomain.DeliveryRun, now time.Time) error {
	return s.tx.WithContext(ctx).Model(&model.CampaignDeliveryRun{}).Where("id = ? AND status = ?", run.ID, campaigndomain.CampaignDeliveryRunStatusScheduled).Updates(structured.Fields{"status": run.Status, "started_at": now, "target_count": run.TargetCount}).Error
}
func (s *store) FailRun(ctx context.Context, id string, now time.Time) error {
	return s.tx.WithContext(ctx).Model(&model.CampaignDeliveryRun{}).Where("id = ?", id).Updates(structured.Fields{"status": campaigndomain.CampaignDeliveryRunStatusFailed, "last_error": "campaign has no recipients", "completed_at": now}).Error
}
func (s *store) DispatchRun(ctx context.Context, run campaigndomain.DeliveryRun, now time.Time) error {
	updates := structured.Fields{"status": run.Status, "target_count": run.TargetCount}
	if run.CompletedAt != nil {
		updates["completed_at"] = now
	} else {
		updates["started_at"] = now
	}
	return s.tx.WithContext(ctx).Model(&model.CampaignDeliveryRun{}).Where("id = ?", run.ID).Updates(updates).Error
}
func (s *store) DispatchCampaign(ctx context.Context, id, scope string) (bool, error) {
	result := s.tx.WithContext(ctx).Model(&model.Campaign{}).Where("id = ? AND status IN ?", id, []string{string(model.CampaignStatusScheduled), string(model.CampaignStatusSending)}).Updates(structured.Fields{"status": string(model.CampaignStatusSending), "scheduled_at": nil, "sent_at": nil, "sent_count": 0, "recipient_scope": scope})
	return result.RowsAffected != 0, result.Error
}
func (s *store) DueRuns(ctx context.Context, now time.Time, limit int) ([]campaigndomain.DeliveryRun, error) {
	var rows []model.CampaignDeliveryRun
	err := s.tx.WithContext(ctx).Where("status = ? AND scheduled_at <= ?", campaigndomain.CampaignDeliveryRunStatusScheduled, now).Order("scheduled_at ASC, id ASC").Limit(limit).Find(&rows).Error
	return deliveryRuns(rows), err
}
func (s *store) ActiveRuns(ctx context.Context, limit int) ([]campaigndomain.DeliveryRun, error) {
	var rows []model.CampaignDeliveryRun
	err := s.tx.WithContext(ctx).Where("status = ?", campaigndomain.CampaignDeliveryRunStatusSending).Where("completed_at IS NULL").Order("updated_at ASC, id ASC").Limit(limit).Find(&rows).Error
	return deliveryRuns(rows), err
}
func deliveryRuns(rows []model.CampaignDeliveryRun) []campaigndomain.DeliveryRun {
	if rows == nil {
		return nil
	}
	result := make([]campaigndomain.DeliveryRun, len(rows))
	for i, row := range rows {
		result[i] = DeliveryRunFromRow(row)
	}
	return result
}
func (s *store) CountRecipients(ctx context.Context, target campaigndomain.CampaignDeliveryTarget) (int64, error) {
	selection, err := campaignDeliveryTargetRecipientSelection(target)
	if err != nil {
		return 0, domainerr.New(domainerr.Invalid, "", err.Error())
	}
	return countBulkEmailRecipients(ctx, s.tx, s.ports.SpiceDB, selection)
}

func (s *store) LockDeliveryRun(ctx context.Context, id string, skip bool) (campaigndomain.LockedDeliveryRun, bool, error) {
	locked, skipped, err := s.lockDeliveryRun(ctx, id, skip)
	return locked, skipped, notFound(err, campaigndomain.ErrNotFound)
}
