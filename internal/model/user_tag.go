package model

import (
	"time"
)

// UserTag represents the user_tag table
type UserTag struct {
	ID        string    `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	Name      string    `gorm:"column:name;type:varchar(100);not null;uniqueIndex"`
	CreatedAt time.Time `gorm:"column:created_at;not null;default:now()"`
}

// TableName returns the table name for GORM
func (UserTag) TableName() string {
	return "user_tag"
}

// UserTagMapping represents the user_tag_mapping junction table
type UserTagMapping struct {
	MemberID  string    `gorm:"column:member_id;type:uuid;primaryKey"`
	TagID     string    `gorm:"column:tag_id;type:uuid;primaryKey"`
	CreatedAt time.Time `gorm:"column:created_at;not null;default:now()"`
}

// TableName returns the table name for GORM
func (UserTagMapping) TableName() string {
	return "user_tag_mapping"
}
