package model

import "time"

const (
	OgGenerationRunStatusQueued        = "queued"
	OgGenerationRunStatusRunning       = "running"
	OgGenerationRunStatusReady         = "ready"
	OgGenerationRunStatusPartialFailed = "partial_failed"
	OgGenerationRunStatusFailed        = "failed"
	OgGenerationRunStatusCancelled     = "cancelled"

	OgGenerationStatusQueued     = "queued"
	OgGenerationStatusProcessing = "processing"
	OgGenerationStatusReady      = "ready"
	OgGenerationStatusFailed     = "failed"
	OgGenerationStatusSuperseded = "superseded"
	OgGenerationStatusCancelled  = "cancelled"
)

// OgGenerationRun groups the targets created by one manual or automatic request.
// RenderConfigSnapshot is immutable so every target in a run renders the same revision.
type OgGenerationRun struct {
	ID                   string     `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	TriggerKind          string     `gorm:"column:trigger_kind;type:text;not null"`
	Reason               string     `gorm:"column:reason;type:text;not null"`
	RenderConfigSnapshot []byte     `gorm:"column:render_config_snapshot;type:jsonb;not null"`
	ConfigRevision       string     `gorm:"column:config_revision;type:text;not null"`
	Status               string     `gorm:"column:status;type:text;not null"`
	StartedAt            *time.Time `gorm:"column:started_at"`
	CompletedAt          *time.Time `gorm:"column:completed_at"`
	CreatedAt            time.Time  `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt            time.Time  `gorm:"column:updated_at;not null;default:now()"`
}

func (OgGenerationRun) TableName() string { return "og_generation_run" }

// OgGenerationTarget serializes generation selection for an entity/locale.
// LatestGenerationID is the compare-and-swap token used by completion.
type OgGenerationTarget struct {
	ID                 string    `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	EntityType         string    `gorm:"column:entity_type;type:text;not null"`
	EntityID           string    `gorm:"column:entity_id;type:text;not null"`
	TargetKind         string    `gorm:"column:target_kind;type:text;not null"`
	Locale             *string   `gorm:"column:locale;type:text"`
	LatestGenerationID *string   `gorm:"column:latest_generation_id;type:uuid"`
	CreatedAt          time.Time `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt          time.Time `gorm:"column:updated_at;not null;default:now()"`
}

func (OgGenerationTarget) TableName() string { return "og_generation_target" }

// OgGeneration stores the queryable generation result and immutable render
// snapshot. ID is also the public_asset ID and therefore the immutable output
// key token. A processing lease permits same-ID crash recovery; it is not a
// product retry or attempt ledger.
type OgGeneration struct {
	ID              string     `gorm:"column:id;type:uuid;primaryKey"`
	RunID           string     `gorm:"column:run_id;type:uuid;not null"`
	TargetID        string     `gorm:"column:target_id;type:uuid;not null"`
	RequestSequence int64      `gorm:"column:request_sequence;autoIncrement"`
	Status          string     `gorm:"column:status;type:text;not null"`
	EntitySnapshot  []byte     `gorm:"column:entity_snapshot;type:jsonb;not null"`
	ProcessingAt    *time.Time `gorm:"column:processing_at"`
	LeaseToken      *string    `gorm:"column:lease_token;type:uuid"`
	LeaseExpiresAt  *time.Time `gorm:"column:lease_expires_at"`
	DeadlineAt      time.Time  `gorm:"column:deadline_at;not null"`
	LastErrorCode   *string    `gorm:"column:last_error_code;type:text"`
	ReadyAt         *time.Time `gorm:"column:ready_at"`
	FailedAt        *time.Time `gorm:"column:failed_at"`
	SupersededAt    *time.Time `gorm:"column:superseded_at"`
	SupersededByID  *string    `gorm:"column:superseded_by_id;type:uuid"`
	CancelledAt     *time.Time `gorm:"column:cancelled_at"`
	CompletedAt     *time.Time `gorm:"column:completed_at"`
	CreatedAt       time.Time  `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt       time.Time  `gorm:"column:updated_at;not null;default:now()"`
}

func (OgGeneration) TableName() string { return "og_generation" }
