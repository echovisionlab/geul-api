package model

import (
	"time"
)

// TranslationSettings stores global translation runtime defaults in a singleton row.
type TranslationSettings struct {
	ID            int       `gorm:"column:id;primaryKey;default:1;check:id = 1"`
	DefaultLocale string    `gorm:"column:default_locale;type:text;not null"`
	CreatedAt     time.Time `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt     time.Time `gorm:"column:updated_at;not null;default:now()"`
}

func (TranslationSettings) TableName() string {
	return "translation_settings"
}

// PostTranslation stores the current locale row served for a post.
type PostTranslation struct {
	EntityID  string    `gorm:"column:entity_id;type:uuid;primaryKey"`
	Locale    string    `gorm:"column:locale;type:text;primaryKey"`
	Title     *string   `gorm:"column:title;type:text"`
	Summary   *string   `gorm:"column:summary;type:text"`
	OgAssetID *string   `gorm:"column:og_asset_id;type:uuid"`
	CreatedAt time.Time `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null;default:now()"`
}
