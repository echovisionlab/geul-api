// Package postgres is the PostgreSQL Store of internal/criteria: the
// member_criteria tables and the criteria-matching reads other domains run
// in their own transactions (LoadCriteriaConfigs,
// ValidateAuthenticatedAccessCriteriaIDs).
package postgres

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	membercriteria "github.com/echovisionlab/geul-api/internal/member/criteria"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
)

// CampaignUsage reads and releases the campaigns and delivery runs that
// target a criteria, in the store's transaction. Campaign owns those rows
// (campaign.CriteriaUsage); it is passed in because Campaign's own tests
// build the Criteria service, so this package cannot import it.
type CampaignUsage interface {
	// CountCriteriaUsage counts the campaigns and delivery runs of each
	// criteria; a criteria nothing uses is absent from both maps.
	CountCriteriaUsage(ctx context.Context, db *gorm.DB, criteriaIDs []string) (campaigns, deliveryRuns map[string]int32, err error)
	// CriteriaInActiveUse reports whether a scheduled or sending campaign,
	// and whether a scheduled or sending delivery run, targets the criteria.
	CriteriaInActiveUse(ctx context.Context, tx *gorm.DB, criteriaID string) (campaign, deliveryRun bool, err error)
	// LockCriteriaUsage locks the criteria's campaigns, then its active
	// delivery runs, and returns what it locked.
	LockCriteriaUsage(ctx context.Context, tx *gorm.DB, criteriaID string) (campaignIDs, deliveryRunIDs []string, err error)
	// CriteriaUsageChanged reports whether the criteria's campaigns or active
	// delivery runs differ from the locked ones.
	CriteriaUsageChanged(ctx context.Context, tx *gorm.DB, criteriaID string, campaignIDs, deliveryRunIDs []string) (bool, error)
	// ReleaseCriteriaForArchive cancels the criteria's scheduled delivery runs
	// and returns its scheduled campaigns to draft, returning their IDs.
	ReleaseCriteriaForArchive(ctx context.Context, tx *gorm.DB, criteriaID string, now time.Time) (draftedCampaignIDs []string, err error)
}

// SignInAppUsage counts the sign-in apps that admit Members through each
// criteria. OAuth App owns those rows (internal/adapters/oauthapp).
type SignInAppUsage interface {
	// CountSignInApps counts the sign-in apps of each criteria; a criteria
	// no app uses is absent.
	CountSignInApps(ctx context.Context, db *gorm.DB, criteriaIDs []string) (map[string]int32, error)
}

// NewUnits returns the production unit of work of the Criteria use cases:
// each transaction binds a Store over it that reaches campaigns through
// campaigns (campaign.CriteriaUsage) and sign-in apps through signInApps.
func NewUnits(db *gorm.DB, campaigns CampaignUsage, signInApps SignInAppUsage) mutation.UnitOfWork[membercriteria.Store] {
	return mutation.Postgres(db, func(tx *gorm.DB) membercriteria.Store {
		return store{tx: tx, campaigns: campaigns, signInApps: signInApps}
	})
}

// store implements membercriteria.Store over one gorm transaction.
type store struct {
	tx         *gorm.DB
	campaigns  CampaignUsage
	signInApps SignInAppUsage
}

func (s store) Criteria(ctx context.Context, id string) (membercriteria.Criteria, error) {
	var row model.MemberCriteria
	if err := s.tx.WithContext(ctx).First(&row, "id = ?", id).Error; err != nil {
		return membercriteria.Criteria{}, criteriaNotFound(err)
	}
	if err := LoadCriteriaConfig(ctx, s.tx, &row); err != nil {
		return membercriteria.Criteria{}, err
	}
	return CriteriaOf(row), nil
}

func (s store) CriteriaArchived(ctx context.Context, id string) (bool, error) {
	var observed model.MemberCriteria
	if err := s.tx.WithContext(ctx).Select("id", "archived_at").First(&observed, "id = ?", id).Error; err != nil {
		return false, criteriaNotFound(err)
	}
	return observed.ArchivedAt != nil, nil
}

func (s store) LockCriteria(ctx context.Context, id string) (membercriteria.Criteria, error) {
	var row model.MemberCriteria
	if err := s.tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		First(&row, "id = ?", id).Error; err != nil {
		return membercriteria.Criteria{}, criteriaNotFound(err)
	}
	if err := LoadCriteriaConfig(ctx, s.tx, &row); err != nil {
		return membercriteria.Criteria{}, err
	}
	return CriteriaOf(row), nil
}

func criteriaNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return membercriteria.ErrCriteriaNotFound
	}
	return err
}

func (s store) InsertCriteria(_ context.Context, criteria membercriteria.Criteria) (membercriteria.Criteria, error) {
	row := CriteriaRow(criteria)
	if err := s.tx.Omit("ID", "Config").Clauses(clause.Returning{}).Create(&row).Error; err != nil {
		return membercriteria.Criteria{}, err
	}
	created := CriteriaOf(row)
	// The response carries the filter as requested, as Create always did.
	created.Config = criteria.Config
	return created, nil
}

func (s store) UpdateCriteria(_ context.Context, id string, change membercriteria.CriteriaChange) error {
	updates := structured.Fields{"updated_at": change.UpdatedAt}
	if change.Name != nil {
		updates["name"] = *change.Name
	}
	if change.Description != nil {
		updates["description"] = *change.Description
	}
	if change.Type != nil {
		updates["kind"] = model.MemberCriteriaKind(*change.Type)
	}
	if change.SetCreatedAfter {
		updates["created_after"] = change.CreatedAfter
	}
	if change.SetCreatedBefore {
		updates["created_before"] = change.CreatedBefore
	}
	return s.tx.Model(&model.MemberCriteria{ID: id}).Updates(updates).Error
}

func (s store) ArchiveCriteria(ctx context.Context, id string, at time.Time) error {
	archived := s.tx.WithContext(ctx).
		Model(&model.MemberCriteria{}).
		Where("id = ? AND archived_at IS NULL", id).
		Updates(structured.Fields{
			"archived_at": at,
			"updated_at":  at,
		})
	if archived.Error != nil {
		return archived.Error
	}
	if archived.RowsAffected != 1 {
		return membercriteria.ErrArchiveStateChanged
	}
	return nil
}

func (s store) RestoreCriteria(ctx context.Context, id string, at time.Time) error {
	restored := s.tx.WithContext(ctx).
		Model(&model.MemberCriteria{}).
		Where("id = ? AND archived_at IS NOT NULL", id).
		Updates(structured.Fields{
			"archived_at": nil,
			"updated_at":  at,
		})
	if restored.Error != nil {
		return restored.Error
	}
	if restored.RowsAffected != 1 {
		return membercriteria.ErrArchiveStateChanged
	}
	return nil
}

func (s store) EligibleMemberIDs(ctx context.Context, memberIDs []string) ([]string, error) {
	return authorizationtarget.EligibleMemberIDs(ctx, s.tx, memberIDs)
}

func (s store) CriteriaInActiveDelivery(ctx context.Context, id string) (bool, bool, error) {
	return s.campaigns.CriteriaInActiveUse(ctx, s.tx, id)
}

func (s store) LockCampaignUsage(ctx context.Context, id string) (membercriteria.CampaignUsage, error) {
	campaignIDs, runIDs, err := s.campaigns.LockCriteriaUsage(ctx, s.tx, id)
	if err != nil {
		return membercriteria.CampaignUsage{}, err
	}
	return membercriteria.CampaignUsage{CampaignIDs: campaignIDs, DeliveryRunIDs: runIDs}, nil
}

func (s store) CampaignUsageChanged(ctx context.Context, id string, locked membercriteria.CampaignUsage) (bool, error) {
	return s.campaigns.CriteriaUsageChanged(ctx, s.tx, id, locked.CampaignIDs, locked.DeliveryRunIDs)
}

func (s store) ReleaseScheduledCampaigns(ctx context.Context, id string, at time.Time) ([]string, error) {
	return s.campaigns.ReleaseCriteriaForArchive(ctx, s.tx, id, at)
}
