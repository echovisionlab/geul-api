// Package model provides database model types.
// This file contains common embedded types for models.
package model

import (
	"time"
)

// Timestamps provides common timestamp fields for models.
// Embed this in models that need created_at and updated_at.
type Timestamps struct {
	CreatedAt time.Time  `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt *time.Time `gorm:"column:updated_at;default:now()"`
}

// TimestampsRequired provides timestamp fields where both are required.
type TimestampsRequired struct {
	CreatedAt time.Time `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null;default:now()"`
}

// SoftDelete provides soft delete functionality.
type SoftDelete struct {
	DeletedAt *time.Time `gorm:"column:deleted_at;index"`
}

// Publishable provides common fields for publishable content.
type Publishable struct {
	Status      string     `gorm:"column:status;not null;default:POST_STATUS_DRAFT"`
	PublishedAt *time.Time `gorm:"column:published_at"`
}

// Sluggable provides common fields for entities with slugs.
type Sluggable struct {
	Slug *string `gorm:"column:slug;uniqueIndex"`
}
