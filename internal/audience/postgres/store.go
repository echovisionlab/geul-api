// Package postgres is the PostgreSQL Store of internal/audience: the
// audience_segment tables and the segment-matching reads other domains run
// in their own transactions (LoadSegmentConfigs,
// ValidateAuthenticatedAccessSegmentIDs).
package postgres

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/audience"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
)

// CampaignUsage reads and releases the campaigns and delivery runs that
// target a segment, in the store's transaction. Campaign owns those rows
// (campaign.SegmentUsage); it is passed in because Campaign's own tests
// build the Audience service, so this package cannot import it.
type CampaignUsage interface {
	// CountSegmentUsage counts the campaigns and delivery runs of each
	// segment; a segment nothing uses is absent from both maps.
	CountSegmentUsage(ctx context.Context, db *gorm.DB, segmentIDs []string) (campaigns, deliveryRuns map[string]int32, err error)
	// SegmentInActiveUse reports whether a scheduled or sending campaign,
	// and whether a scheduled or sending delivery run, targets the segment.
	SegmentInActiveUse(ctx context.Context, tx *gorm.DB, segmentID string) (campaign, deliveryRun bool, err error)
	// LockSegmentUsage locks the segment's campaigns, then its active
	// delivery runs, and returns what it locked.
	LockSegmentUsage(ctx context.Context, tx *gorm.DB, segmentID string) (campaignIDs, deliveryRunIDs []string, err error)
	// SegmentUsageChanged reports whether the segment's campaigns or active
	// delivery runs differ from the locked ones.
	SegmentUsageChanged(ctx context.Context, tx *gorm.DB, segmentID string, campaignIDs, deliveryRunIDs []string) (bool, error)
	// ReleaseSegmentForArchive cancels the segment's scheduled delivery runs
	// and returns its scheduled campaigns to draft, returning their IDs.
	ReleaseSegmentForArchive(ctx context.Context, tx *gorm.DB, segmentID string, now time.Time) (draftedCampaignIDs []string, err error)
}

// NewUnits returns the production unit of work of the Audience use cases:
// each transaction binds a Store over it that reaches campaigns through
// campaigns (campaign.SegmentUsage).
func NewUnits(db *gorm.DB, campaigns CampaignUsage) mutation.UnitOfWork[audience.Store] {
	return mutation.Postgres(db, func(tx *gorm.DB) audience.Store {
		return store{tx: tx, campaigns: campaigns}
	})
}

// store implements audience.Store over one gorm transaction.
type store struct {
	tx        *gorm.DB
	campaigns CampaignUsage
}

func (s store) Segment(ctx context.Context, id string) (audience.Segment, error) {
	var row model.AudienceSegment
	if err := s.tx.WithContext(ctx).First(&row, "id = ?", id).Error; err != nil {
		return audience.Segment{}, segmentNotFound(err)
	}
	if err := LoadSegmentConfig(ctx, s.tx, &row); err != nil {
		return audience.Segment{}, err
	}
	return SegmentOf(row), nil
}

func (s store) SegmentArchived(ctx context.Context, id string) (bool, error) {
	var observed model.AudienceSegment
	if err := s.tx.WithContext(ctx).Select("id", "archived_at").First(&observed, "id = ?", id).Error; err != nil {
		return false, segmentNotFound(err)
	}
	return observed.ArchivedAt != nil, nil
}

func (s store) LockSegment(ctx context.Context, id string) (audience.Segment, error) {
	var row model.AudienceSegment
	if err := s.tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		First(&row, "id = ?", id).Error; err != nil {
		return audience.Segment{}, segmentNotFound(err)
	}
	if err := LoadSegmentConfig(ctx, s.tx, &row); err != nil {
		return audience.Segment{}, err
	}
	return SegmentOf(row), nil
}

func segmentNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return audience.ErrSegmentNotFound
	}
	return err
}

func (s store) InsertSegment(_ context.Context, segment audience.Segment) (audience.Segment, error) {
	row := SegmentRow(segment)
	if err := s.tx.Omit("ID", "Config").Clauses(clause.Returning{}).Create(&row).Error; err != nil {
		return audience.Segment{}, err
	}
	created := SegmentOf(row)
	// The response carries the filter as requested, as Create always did.
	created.Config = segment.Config
	return created, nil
}

func (s store) UpdateSegment(_ context.Context, id string, change audience.SegmentChange) error {
	updates := structured.Fields{"updated_at": change.UpdatedAt}
	if change.Name != nil {
		updates["name"] = *change.Name
	}
	if change.Description != nil {
		updates["description"] = *change.Description
	}
	if change.Type != nil {
		updates["segment_type"] = model.AudienceSegmentType(*change.Type)
	}
	if change.SetCreatedAfter {
		updates["created_after"] = change.CreatedAfter
	}
	if change.SetCreatedBefore {
		updates["created_before"] = change.CreatedBefore
	}
	return s.tx.Model(&model.AudienceSegment{ID: id}).Updates(updates).Error
}

func (s store) ArchiveSegment(ctx context.Context, id string, at time.Time) error {
	archived := s.tx.WithContext(ctx).
		Model(&model.AudienceSegment{}).
		Where("id = ? AND archived_at IS NULL", id).
		Updates(structured.Fields{
			"archived_at": at,
			"updated_at":  at,
		})
	if archived.Error != nil {
		return archived.Error
	}
	if archived.RowsAffected != 1 {
		return audience.ErrArchiveStateChanged
	}
	return nil
}

func (s store) RestoreSegment(ctx context.Context, id string, at time.Time) error {
	restored := s.tx.WithContext(ctx).
		Model(&model.AudienceSegment{}).
		Where("id = ? AND archived_at IS NOT NULL", id).
		Updates(structured.Fields{
			"archived_at": nil,
			"updated_at":  at,
		})
	if restored.Error != nil {
		return restored.Error
	}
	if restored.RowsAffected != 1 {
		return audience.ErrArchiveStateChanged
	}
	return nil
}

func (s store) EligibleMemberIDs(ctx context.Context, memberIDs []string) ([]string, error) {
	return authorizationtarget.EligibleMemberIDs(ctx, s.tx, memberIDs)
}

func (s store) SegmentInActiveDelivery(ctx context.Context, id string) (bool, bool, error) {
	return s.campaigns.SegmentInActiveUse(ctx, s.tx, id)
}

func (s store) LockCampaignUsage(ctx context.Context, id string) (audience.CampaignUsage, error) {
	campaignIDs, runIDs, err := s.campaigns.LockSegmentUsage(ctx, s.tx, id)
	if err != nil {
		return audience.CampaignUsage{}, err
	}
	return audience.CampaignUsage{CampaignIDs: campaignIDs, DeliveryRunIDs: runIDs}, nil
}

func (s store) CampaignUsageChanged(ctx context.Context, id string, locked audience.CampaignUsage) (bool, error) {
	return s.campaigns.SegmentUsageChanged(ctx, s.tx, id, locked.CampaignIDs, locked.DeliveryRunIDs)
}

func (s store) ReleaseScheduledCampaigns(ctx context.Context, id string, at time.Time) ([]string, error) {
	return s.campaigns.ReleaseSegmentForArchive(ctx, s.tx, id, at)
}
