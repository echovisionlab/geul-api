package model

import "time"

type EmailSuppression struct {
	ID           string     `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	Email        string     `gorm:"column:email;type:varchar(255);not null"`
	Reason       string     `gorm:"column:reason;type:varchar(50);not null"`
	Source       string     `gorm:"column:source;type:varchar(50);not null"`
	ReferenceID  *string    `gorm:"column:reference_id;type:varchar(255)"`
	LastError    *string    `gorm:"column:last_error;type:text"`
	SuppressedAt time.Time  `gorm:"column:suppressed_at;type:timestamptz;not null;default:now()"`
	ReleasedAt   *time.Time `gorm:"column:released_at;type:timestamptz"`
	ReleasedBy   *string    `gorm:"column:released_by;type:varchar(255)"`
	CreatedAt    time.Time  `gorm:"column:created_at;type:timestamptz;not null;default:now()"`
	UpdatedAt    time.Time  `gorm:"column:updated_at;type:timestamptz;not null;default:now()"`
}

func (EmailSuppression) TableName() string {
	return "email_suppression"
}
