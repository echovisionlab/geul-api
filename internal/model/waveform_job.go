package model

import "time"

// WaveformJob stores queryable waveform allocation, progress, cancellation and
// terminal failure facts. PGMQ owns delivery attempts, retry timing and
// heartbeat/redelivery state.
type WaveformJob struct {
	// EventID is the preallocated waveform asset ID, which keeps completion correlation ID-only.
	EventID         string     `gorm:"column:event_id;type:text;primaryKey"`
	EntityType      string     `gorm:"column:entity_type;type:varchar(100);not null"`
	EntityID        string     `gorm:"column:entity_id;type:text;not null"`
	FileID          string     `gorm:"column:file_id;type:text;not null"`
	Status          string     `gorm:"column:status;type:varchar(100);not null"`
	Progress        int        `gorm:"column:progress;not null;default:0"`
	LastSequence    *int64     `gorm:"column:last_sequence"`
	LastStage       *string    `gorm:"column:last_stage;type:text"`
	CancelRequested bool       `gorm:"column:cancel_requested;not null;default:false"`
	LastError       *string    `gorm:"column:last_error;type:text"`
	CompletedAt     *time.Time `gorm:"column:completed_at"`
	CreatedAt       time.Time  `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt       time.Time  `gorm:"column:updated_at;not null;default:now()"`
}

func (WaveformJob) TableName() string {
	return "waveform_job"
}
