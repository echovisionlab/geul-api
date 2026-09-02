package campaign

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
)

// CampaignDeliveryRuntime is Campaign's production implementation of the
// immutable run, recipient selection, and delivery-history port.
type CampaignDeliveryRuntime struct {
	spiceDB   *auth.SpiceDBClient
	publisher CampaignBulkPublisher
}

var _ CampaignDeliveryPort = (*CampaignDeliveryRuntime)(nil)

func NewCampaignDeliveryRuntime(
	spiceDB *auth.SpiceDBClient,
	publisher CampaignBulkPublisher,
) *CampaignDeliveryRuntime {
	return &CampaignDeliveryRuntime{spiceDB: spiceDB, publisher: publisher}
}

func (r *CampaignDeliveryRuntime) CountRecipients(
	ctx context.Context,
	tx *gorm.DB,
	target CampaignDeliveryTarget,
) (int64, error) {
	if r == nil || r.spiceDB == nil {
		return 0, errs.DependencyUnavailable("Campaign recipient authorization")
	}
	selection, err := campaignDeliveryTargetRecipientSelection(target)
	if err != nil {
		return 0, errs.InvalidArgumentMsg(err.Error())
	}
	return countBulkEmailRecipients(ctx, tx, r.spiceDB, selection)
}

func (*CampaignDeliveryRuntime) SealRun(
	ctx context.Context,
	tx *gorm.DB,
	definition CampaignDeliveryRunDefinition,
) (CampaignDeliveryRunRef, error) {
	campaignID := strings.TrimSpace(definition.CampaignID)
	if campaignID == "" {
		return CampaignDeliveryRunRef{}, errs.Required("campaign_id")
	}
	if definition.SnapshotSchemaVersion != CampaignDeliverySnapshotSchemaVersion {
		return CampaignDeliveryRunRef{}, errs.InvalidArgumentMsg("unsupported snapshot schema version")
	}
	if err := ValidateCampaignDeliverySnapshot(definition.Snapshot); err != nil {
		return CampaignDeliveryRunRef{}, errs.InvalidArgumentMsg(err.Error())
	}
	if err := ValidateCampaignDeliveryTarget(definition.Target); err != nil {
		return CampaignDeliveryRunRef{}, errs.InvalidArgumentMsg(err.Error())
	}
	renderSnapshot, err := campaignDeliverySnapshotJSONFields(definition.Snapshot)
	if err != nil {
		return CampaignDeliveryRunRef{}, errs.InvalidArgumentMsg(err.Error())
	}

	sourceCampaignUpdatedAt := definition.SourceCampaignUpdatedAt.UTC()
	run := model.CampaignDeliveryRun{
		RunKind:                 EmailDeliveryRunKindCampaign,
		CampaignID:              &campaignID,
		Status:                  CampaignDeliveryRunStatusScheduled,
		ScheduledAt:             definition.ScheduledAt.UTC(),
		TemplateData:            model.JSONFields{},
		RenderSnapshot:          renderSnapshot,
		SnapshotSchemaVersion:   definition.SnapshotSchemaVersion,
		SourceLayoutID:          definition.SourceLayoutID,
		AudienceSegmentID:       definition.AudienceSegmentID,
		SourceCampaignUpdatedAt: &sourceCampaignUpdatedAt,
		SourceLayoutUpdatedAt:   definition.SourceLayoutUpdatedAt,
		TargetQueryVersion:      definition.Target.QueryVersion,
		TargetMode:              definition.Target.Mode,
		TargetRecipientScope:    definition.Target.RecipientScope,
		TargetCreatedAfter:      definition.Target.CreatedAfter,
		TargetCreatedBefore:     definition.Target.CreatedBefore,
	}
	if err := sealCampaignOwnedEmailDeliveryRun(
		ctx,
		tx,
		&run,
		&definition.Target,
	); err != nil {
		return CampaignDeliveryRunRef{}, err
	}
	return CampaignDeliveryRunRef{ID: run.ID}, nil
}

func (r *CampaignDeliveryRuntime) StartRun(
	ctx context.Context,
	tx *gorm.DB,
	runID string,
	targetCount int64,
	startedAt time.Time,
) error {
	if r == nil || r.publisher == nil {
		return errs.DependencyUnavailable("Campaign delivery publisher")
	}
	if targetCount <= 0 {
		return errs.InvalidArgumentMsg("target count must be positive")
	}
	var run model.CampaignDeliveryRun
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		First(&run, "id = ?", strings.TrimSpace(runID)).Error; err != nil {
		return err
	}
	if run.RunKind != EmailDeliveryRunKindCampaign || run.CampaignID == nil || !run.DefinitionSealed {
		return errs.FailedPrecondition("Campaign delivery run definition is invalid")
	}
	run.TargetCount = int(targetCount)
	return enqueueStartedCampaignDeliveryRun(ctx, tx, r.publisher, &run, startedAt.UTC())
}

func (*CampaignDeliveryRuntime) CancelActiveRuns(
	ctx context.Context,
	tx *gorm.DB,
	campaignID string,
	now time.Time,
) error {
	campaignID = strings.TrimSpace(campaignID)
	var campaign model.Campaign
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id").
		First(&campaign, "id = ?", campaignID).Error; err != nil {
		return errs.Internal(err)
	}

	var activeRuns []model.CampaignDeliveryRun
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where(
			"campaign_id = ? AND status IN ?",
			campaignID,
			[]string{
				CampaignDeliveryRunStatusScheduled,
				CampaignDeliveryRunStatusSending,
			},
		).
		Order("id ASC").
		Find(&activeRuns).Error; err != nil {
		return errs.Internal(err)
	}
	for i := range activeRuns {
		if activeRuns[i].Status == CampaignDeliveryRunStatusSending {
			return errs.FailedPrecondition("campaign delivery has already started")
		}
	}

	if err := tx.WithContext(ctx).
		Model(&model.CampaignDeliveryRun{}).
		Where(
			"campaign_id = ? AND status = ?",
			campaignID,
			CampaignDeliveryRunStatusScheduled,
		).
		Updates(structured.Fields{
			"status":       CampaignDeliveryRunStatusCancelled,
			"completed_at": now.UTC(),
		}).Error; err != nil {
		return errs.Internal(err)
	}
	return nil
}

func (*CampaignDeliveryRuntime) HasHistory(
	ctx context.Context,
	db *gorm.DB,
	campaignID string,
) (bool, error) {
	var count int64
	if err := db.WithContext(ctx).
		Model(&model.CampaignDeliveryRun{}).
		Where("campaign_id = ?", strings.TrimSpace(campaignID)).
		Count(&count).Error; err != nil {
		return false, errs.Internal(err)
	}
	return count > 0, nil
}

func (*CampaignDeliveryRuntime) LatestStats(
	ctx context.Context,
	db *gorm.DB,
	campaignID string,
) (*CampaignDeliveryStats, error) {
	run, err := latestCampaignDeliveryRun(ctx, db, campaignID)
	if err != nil || run == nil {
		return nil, err
	}
	type statusCount struct {
		Status string `gorm:"column:status"`
		Count  int64  `gorm:"column:count"`
	}
	var counts []statusCount
	if err := db.WithContext(ctx).
		Model(&model.CampaignDeliveryRecipient{}).
		Select("status, COUNT(*) AS count").
		Where("run_id = ?", run.ID).
		Group("status").
		Find(&counts).Error; err != nil {
		return nil, errs.Internal(err)
	}
	stats := CampaignDeliveryStats{}
	for _, count := range counts {
		switch count.Status {
		case CampaignDeliveryRecipientStatusSent,
			CampaignDeliveryRecipientStatusDelivered:
			stats.TotalSent += int32(count.Count)
		case CampaignDeliveryRecipientStatusSkipped:
			stats.Skipped += int32(count.Count)
		case CampaignDeliveryRecipientStatusPermanentFailed,
			CampaignDeliveryRecipientStatusBounced,
			CampaignDeliveryRecipientStatusComplained:
			stats.Failed += int32(count.Count)
		case CampaignDeliveryRecipientStatusBlocked:
			stats.Blocked += int32(count.Count)
		case CampaignDeliveryRecipientStatusSuppressed:
			stats.Suppressed += int32(count.Count)
		}
	}
	return &stats, nil
}

func (*CampaignDeliveryRuntime) ListRecipients(
	ctx context.Context,
	db *gorm.DB,
	campaignID string,
	limit int,
	offset int,
) (CampaignDeliveryRecipientPage, error) {
	run, err := latestCampaignDeliveryRun(ctx, db, campaignID)
	if err != nil || run == nil {
		return CampaignDeliveryRecipientPage{}, err
	}
	var total int64
	if err := db.WithContext(ctx).
		Model(&model.CampaignDeliveryRecipient{}).
		Where("run_id = ?", run.ID).
		Count(&total).Error; err != nil {
		return CampaignDeliveryRecipientPage{}, errs.Internal(err)
	}
	var rows []model.CampaignDeliveryRecipient
	if err := db.WithContext(ctx).
		Where("run_id = ?", run.ID).
		Order("created_at DESC, id DESC").
		Limit(limit).
		Offset(offset).
		Find(&rows).Error; err != nil {
		return CampaignDeliveryRecipientPage{}, errs.Internal(err)
	}
	page := CampaignDeliveryRecipientPage{
		Recipients: make([]CampaignDeliveryRecipient, len(rows)),
		Total:      total,
	}
	for index, row := range rows {
		page.Recipients[index] = CampaignDeliveryRecipient{
			Email:      row.RecipientEmail,
			Status:     row.Status,
			MemberID:   strings.TrimSpace(ptrStringValue(row.MemberID)),
			ErrorType:  row.ErrorType,
			TerminalAt: row.TerminalAt,
		}
	}
	return page, nil
}

func campaignDeliverySnapshotJSONFields(
	snapshot CampaignDeliverySnapshot,
) (model.JSONFields, error) {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("encode Campaign delivery snapshot: %w", err)
	}
	var fields model.JSONFields
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return nil, fmt.Errorf("decode Campaign delivery snapshot: %w", err)
	}
	return fields, nil
}

func sealCampaignOwnedEmailDeliveryRun(
	ctx context.Context,
	tx *gorm.DB,
	run *model.CampaignDeliveryRun,
	target *CampaignDeliveryTarget,
) error {
	if err := tx.WithContext(ctx).Create(run).Error; err != nil {
		return errs.Internal(err)
	}
	if target != nil {
		if err := saveCampaignDeliveryTargetRelations(ctx, tx, run.ID, *target); err != nil {
			return errs.Internal(err)
		}
	}
	sealed := tx.WithContext(ctx).
		Model(&model.CampaignDeliveryRun{}).
		Where("id = ? AND definition_sealed = FALSE", run.ID).
		Update("definition_sealed", true)
	if sealed.Error != nil {
		return errs.Internal(sealed.Error)
	}
	if sealed.RowsAffected != 1 {
		return errs.InternalMsg("email delivery run definition could not be sealed")
	}
	run.DefinitionSealed = true
	return nil
}

func saveCampaignDeliveryTargetRelations(
	ctx context.Context,
	tx *gorm.DB,
	runID string,
	target CampaignDeliveryTarget,
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
		Joins(`JOIN kratos.identities AS identity
			ON identity.id = member.account_identity_id
			AND identity.external_id = member.id::text`).
		Where("member.id IN ?", memberIDs).
		Where("member.deleted_at IS NULL").
		Where("member.onboarded = TRUE").
		Order("member.id ASC").
		Clauses(clause.Locking{Strength: "SHARE"}).
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
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	return nil, errs.Internal(err)
}
