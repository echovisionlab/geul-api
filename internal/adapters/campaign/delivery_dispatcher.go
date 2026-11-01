package campaign

import (
	"context"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	campaignpostgres "github.com/echovisionlab/geul-api/internal/campaign/postgres"
	"github.com/echovisionlab/geul-api/internal/domainaudit"

	"gorm.io/gorm"

	campaigndomain "github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/failurecause"
)

// DeliveryDispatcher drives Campaign's delivery dispatcher for the worker and
// the Legal notice runtime. The dispatcher has no request principal and opens
// no transaction, so each run's dispatch and recipient materialization
// commits here in a transaction of its own.
type DeliveryDispatcher struct {
	db    *gorm.DB
	runs  *campaigndomain.CampaignDeliveryDispatcher
	ports campaignpostgres.Ports
	audit domainaudit.Appender
}

type BulkPublisher interface {
	campaigndomain.CampaignBulkPublisher
	campaignpostgres.Publisher
}

// NewDeliveryDispatcher returns the dispatcher of runs over db.
func NewDeliveryDispatcher(db *gorm.DB, spiceDB *auth.SpiceDBClient, publisher BulkPublisher, audit domainaudit.Appender, legalNotice campaignpostgres.LegalNoticeDeliveryPort) *DeliveryDispatcher {
	if db == nil || spiceDB == nil || audit == nil {
		panic("campaign delivery dispatcher dependencies are required")
	}
	return &DeliveryDispatcher{db: db, runs: campaigndomain.NewCampaignDeliveryDispatcher(publisher, time.Now), ports: campaignpostgres.Ports{SpiceDB: spiceDB, Publisher: publisher, LegalNotice: legalNotice}, audit: audit}
}
func (d *DeliveryDispatcher) joined(tx *gorm.DB) campaigndomain.Joined {
	return campaignpostgres.Bind(tx, d.ports, d.audit)
}

// DispatchEmailDeliveryRun dispatches the run if it is due, waiting for a
// concurrent writer of it.
func (d *DeliveryDispatcher) DispatchEmailDeliveryRun(ctx context.Context, runID string) error {
	_, err := d.dispatch(ctx, runID, false)
	return err
}

func (d *DeliveryDispatcher) dispatch(ctx context.Context, runID string, skipLocked bool) (bool, error) {
	published := false
	err := d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		published, err = d.runs.DispatchEmailDeliveryRun(ctx, d.joined(tx), runID, skipLocked)
		return err
	})
	if err != nil {
		return false, err
	}
	return published, nil
}

// DispatchDueEmailDeliveryRuns dispatches up to limit due runs, skipping runs
// another dispatcher holds, and returns how many it handed to delivery. A
// run that fails is logged and left for the next tick.
func (d *DeliveryDispatcher) DispatchDueEmailDeliveryRuns(ctx context.Context, limit int) (int, error) {
	runs, err := d.runs.DueEmailDeliveryRuns(ctx, d.joined(d.db), limit)
	if err != nil {
		return 0, err
	}
	dispatched := 0
	for _, run := range runs {
		published, err := d.dispatch(ctx, run.ID, true)
		if err != nil {
			slog.ErrorContext(ctx, "failed to dispatch email delivery run", "run_id", run.ID, "run_kind", run.RunKind, failurecause.Attr(err))
			continue
		}
		if published {
			dispatched++
		}
	}
	return dispatched, nil
}

// ResumeActiveEmailDeliveryRuns re-materializes up to limit active runs and
// hands those with pending recipients back to bulk delivery, returning how
// many it resumed. A run that fails is logged and left for the next tick.
func (d *DeliveryDispatcher) ResumeActiveEmailDeliveryRuns(ctx context.Context, limit int) (int, error) {
	runs, err := d.runs.ActiveEmailDeliveryRuns(ctx, d.joined(d.db), limit)
	if err != nil {
		return 0, err
	}
	resumed := 0
	for _, run := range runs {
		if !d.runs.ResumableEmailDeliveryRun(ctx, d.joined(d.db), run) {
			continue
		}
		if err := d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return d.runs.MaterializeEmailDeliveryRun(ctx, d.joined(tx), run.ID)
		}); err != nil {
			slog.ErrorContext(ctx, "failed to recover campaign delivery materialization", "run_id", run.ID, failurecause.Attr(err))
			continue
		}
		if d.runs.PublishResumedEmailDeliveryRun(ctx, d.joined(d.db), run.ID) {
			resumed++
		}
	}
	return resumed, nil
}
