package model

import (
	"time"

	"github.com/lib/pq"
)

// Member is Geul's durable domain actor. Authentication and account security
// remain owned by the linked account identity; a deleted Member is retained as
// a tombstone so product attribution never points at a replacement account.
type Member struct {
	ID                string            `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	AccountIdentityID *string           `gorm:"column:account_identity_id;type:uuid;uniqueIndex:uq_member_account_identity_id"`
	Nickname          string            `gorm:"column:nickname;type:text;not null"`
	Onboarded         bool              `gorm:"column:onboarded;not null;default:false"`
	PrimaryEmail      *string           `gorm:"column:primary_email;type:varchar(254)"`
	AvailableEmails   pq.StringArray    `gorm:"column:available_emails;type:text[];not null;default:'{}'"`
	Bio               *string           `gorm:"column:bio;type:text"`
	Website           *string           `gorm:"column:website;type:text"`
	SocialLinks       map[string]string `gorm:"column:social_links;type:jsonb;serializer:json;not null;default:'{}'"`
	PreferredLocale   *string           `gorm:"column:preferred_locale;type:text"`
	DeletedAt         *time.Time        `gorm:"column:deleted_at;type:timestamptz"`
	CreatedAt         time.Time         `gorm:"column:created_at;type:timestamptz;not null;default:now()"`
	UpdatedAt         time.Time         `gorm:"column:updated_at;type:timestamptz;not null;default:now()"`
}

func (Member) TableName() string { return "member" }
