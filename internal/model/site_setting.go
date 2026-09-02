package model

import "time"

// SiteSettings stores all site-level settings in a single, typed row.
// The table enforces a singleton row via id=1 check.
type SiteSettings struct {
	ID int `gorm:"column:id;primaryKey;default:1;check:id = 1"`

	// General
	SiteTitle string `gorm:"column:site_title;type:varchar(255);not null;default:''"`

	// Company / Business Info
	CompanyName    string `gorm:"column:company_name;type:varchar(255);not null;default:''"`
	CompanyAddress string `gorm:"column:company_address;type:text;not null;default:''"`
	TaxID          string `gorm:"column:tax_id;type:varchar(255);not null;default:''"`
	LegalEmail     string `gorm:"column:legal_email;type:varchar(255);not null;default:''"`
	SupportEmail   string `gorm:"column:support_email;type:varchar(255);not null;default:''"`
	PrivacyEmail   string `gorm:"column:privacy_email;type:varchar(255);not null;default:''"`
	SocialLinks    []byte `gorm:"column:social_links;type:jsonb;not null;default:'{}'::jsonb"`

	// Branding
	LogoLightFileID           *string `gorm:"column:logo_light_file_id;type:uuid"`
	LogoDarkFileID            *string `gorm:"column:logo_dark_file_id;type:uuid"`
	LogoEmailFileID           *string `gorm:"column:logo_email_file_id;type:uuid"`
	FaviconFileID             *string `gorm:"column:favicon_file_id;type:uuid"`
	PrimaryColor              string  `gorm:"column:primary_color;type:varchar(7);not null;default:'#b02d23'"`
	SiteOgBackgroundFileID    *string `gorm:"column:site_og_background_file_id;type:uuid"`
	PrivacyOgBackgroundFileID *string `gorm:"column:privacy_og_background_file_id;type:uuid"`
	TermsOgBackgroundFileID   *string `gorm:"column:terms_og_background_file_id;type:uuid"`

	// Reading
	DefaultCommentsEnabled bool    `gorm:"column:default_comments_enabled;not null;default:true"`
	HomepagePageID         *string `gorm:"column:homepage_page_id;type:uuid"`
	DefaultMapThemeID      string  `gorm:"column:default_map_theme_id;type:uuid;not null"`

	// Navigation Menus
	MenuHeaderID         *string `gorm:"column:menu_header_id;type:uuid"`
	MenuSecondaryID      *string `gorm:"column:menu_secondary_id;type:uuid"`
	MenuFooterID         *string `gorm:"column:menu_footer_id;type:uuid"`
	MenuAvatarDropdownID *string `gorm:"column:menu_avatar_dropdown_id;type:uuid"`

	// SEO
	MetaDescription   string  `gorm:"column:meta_description;type:text;not null;default:''"`
	GoogleAnalyticsID *string `gorm:"column:google_analytics_id;type:varchar(255)"`
	SiteOgAssetID     *string `gorm:"column:site_og_asset_id;type:uuid"`

	// Structured JSON settings
	OGImageConfig []byte `gorm:"column:og_image_config;type:jsonb"`

	CreatedAt time.Time `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null;default:now()"`
}

func (SiteSettings) TableName() string {
	return "site_settings"
}

type SiteSettingLoaderFile struct {
	SiteSettingID int       `gorm:"column:site_setting_id;primaryKey;default:1"`
	FileID        string    `gorm:"column:file_id;primaryKey;type:uuid"`
	Position      int32     `gorm:"column:position;not null;default:0"`
	CreatedAt     time.Time `gorm:"column:created_at;not null;default:now()"`
}

func (SiteSettingLoaderFile) TableName() string {
	return "site_setting_loader_file"
}
