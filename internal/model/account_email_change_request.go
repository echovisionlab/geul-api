package model

import "time"

// AccountEmailChangeRequest is the active AUTH-12 product request. Kratos owns
// verification and canonical identity state; this row only keeps the immutable
// addresses needed to resume an already-started change.
type AccountEmailChangeRequest struct {
	ID                    string    `gorm:"column:id;type:uuid;primaryKey"`
	MemberID              string    `gorm:"column:member_id;type:uuid;not null;uniqueIndex"`
	IdentityID            string    `gorm:"column:identity_id;type:uuid;not null;uniqueIndex"`
	PreviousEmailAddress  string    `gorm:"column:previous_email_address;type:varchar(254);not null"`
	RequestedEmailAddress string    `gorm:"column:requested_email_address;type:varchar(254);not null;uniqueIndex"`
	CreatedAt             time.Time `gorm:"column:created_at;type:timestamptz;not null"`
}

func (AccountEmailChangeRequest) TableName() string {
	return "account_email_change_request"
}
