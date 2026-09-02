package worker

import (
	"context"
	"time"

	emaildeliveryadapter "github.com/echovisionlab/geul-api/internal/adapters/emaildelivery"
	"github.com/echovisionlab/geul-api/internal/model"
)

type mailMetrics struct {
	emaildeliveryadapter.Metrics
}

func newMailMetrics() mailMetrics {
	return mailMetrics{Metrics: emaildeliveryadapter.NewMetrics()}
}

func (m mailMetrics) RecordRunDuration(ctx context.Context, run model.CampaignDeliveryRun, completedAt time.Time) {
	m.Metrics.RecordRunDuration(ctx, run, completedAt)
}
