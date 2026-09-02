package og

import (
	"context"
	"time"

	"gorm.io/gorm"
)

const (
	defaultOgGenerationDeadline = 30 * time.Minute
	defaultOgGenerationLease    = 10 * time.Minute
	ogLifecycleNotifyChannel    = "og_lifecycle"
	postgresNotifyPayloadLimit  = 8_000
)

type Planner struct {
	db          *gorm.DB
	cdnDomain   string
	config      RenderConfig
	projections []Projection
	now         func() time.Time
}

func NewPlanner(db *gorm.DB, cdnDomain string, config RenderConfig, projections ...Projection) *Planner {
	if db == nil {
		panic("OG generation planner: db is required")
	}
	if config == nil || len(projections) == 0 {
		panic("OG generation planner dependencies are required")
	}
	return &Planner{
		db:          db,
		cdnDomain:   cdnDomain,
		config:      config,
		projections: projections,
		now:         time.Now,
	}
}

func (p *Planner) RequestBulk(
	ctx context.Context,
	triggerKind string,
	reason string,
	requests []Request,
	reload ReloadRequests,
) (*Plan, error) {
	var plan *Plan
	err := p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var requestErr error
		plan, requestErr = p.RequestBulkReloadedWithDB(ctx, tx, triggerKind, reason, requests, reload)
		return requestErr
	})
	return plan, err
}

type Lifecycle struct {
	db          *gorm.DB
	cdnDomain   string
	projections []Projection
	now         func() time.Time
	lease       time.Duration
}

func NewLifecycle(db *gorm.DB, cdnDomain string, projections ...Projection) *Lifecycle {
	if db == nil {
		panic("OG generation lifecycle: db is required")
	}
	return &Lifecycle{
		db:          db,
		cdnDomain:   cdnDomain,
		projections: projections,
		now:         time.Now,
		lease:       defaultOgGenerationLease,
	}
}
