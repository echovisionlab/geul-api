package model

import "time"

// UserCookieConsent is an append-only consent ledger for authenticated users.
// Each update inserts a new row so consent changes remain auditable.
type UserCookieConsent struct {
	ID             string    `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	MemberID       string    `gorm:"column:member_id;type:uuid;not null"`
	Essential      bool      `gorm:"column:essential;not null;default:true"`
	Analytics      bool      `gorm:"column:analytics;not null;default:false"`
	ConsentVersion int32     `gorm:"column:consent_version;not null;default:1"`
	Source         string    `gorm:"column:source;type:varchar(50);not null;default:'banner'"`
	IPAddress      *string   `gorm:"column:ip_address;type:varchar(255)"`
	UserAgent      *string   `gorm:"column:user_agent;type:text"`
	RecordedAt     time.Time `gorm:"column:recorded_at;type:timestamptz;not null;default:now()"`
}

// TableName returns the table name for GORM.
func (UserCookieConsent) TableName() string {
	return "user_cookie_consent"
}
