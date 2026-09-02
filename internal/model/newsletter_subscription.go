package model

import "time"

// NewsletterSubscription is the current Kratos Identity opt-in product fact.
// Row presence means subscribed; deleting the row means unsubscribed.
type NewsletterSubscription struct {
	IdentityID   string    `gorm:"column:identity_id;type:uuid;primaryKey"`
	SubscribedAt time.Time `gorm:"column:subscribed_at;type:timestamptz;not null;default:now()"`
}

func (NewsletterSubscription) TableName() string {
	return "newsletter_subscription"
}
