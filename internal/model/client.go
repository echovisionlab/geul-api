package model

import (
	"time"
)

// Client represents a client (for work credits, e.g., commissioned by XYZ)
// Maps to: sql/001_schema.sql - client table
type Client struct {
	ID              string    `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	Name            string    `gorm:"column:name;type:varchar(255);not null"`
	Website         *string   `gorm:"column:website;type:varchar(255)"`
	LogoLightFileID *string   `gorm:"column:logo_light_file_id;type:uuid"`
	LogoDarkFileID  *string   `gorm:"column:logo_dark_file_id;type:uuid"`
	CreatedAt       time.Time `gorm:"column:created_at;not null;default:now()"`
}

// TableName returns the table name for GORM
func (Client) TableName() string {
	return "client"
}
