package model

import "time"

// TranscodeJob stores queryable transcode allocation and progress facts.
// PGMQ owns delivery attempts, retry timing, and dead-letter state.
type TranscodeJob struct {
	EventID             string     `gorm:"column:event_id;type:text;primaryKey"`
	QueueName           string     `gorm:"column:queue_name;type:varchar(100);not null"`
	EntityType          string     `gorm:"column:entity_type;type:varchar(100);not null"`
	EntityID            string     `gorm:"column:entity_id;type:text;not null"`
	FileID              string     `gorm:"column:file_id;type:text;not null"`
	Payload             []byte     `gorm:"column:payload;type:bytea;not null"`
	Status              string     `gorm:"column:status;type:varchar(100);not null"`
	Progress            int        `gorm:"column:progress;not null;default:0"`
	HLSProgress         int        `gorm:"column:hls_progress;not null;default:0"`
	SpectrogramProgress int        `gorm:"column:spectrogram_progress;not null;default:0"`
	LastSequence        *int64     `gorm:"column:last_sequence"`
	LastStage           *string    `gorm:"column:last_stage;type:text"`
	LastError           *string    `gorm:"column:last_error;type:text"`
	CompletedAt         *time.Time `gorm:"column:completed_at"`
	CreatedAt           time.Time  `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt           time.Time  `gorm:"column:updated_at;not null;default:now()"`
}

func (TranscodeJob) TableName() string {
	return "transcode_job"
}
