package model

import (
	"encoding/json"
	"time"

	"github.com/lib/pq"
)

// PageStatus represents the status of a page
type PageStatus string

// Page represents a CMS page (Domain Object)
// Maps to: schema migrations - page table
type Page struct {
	ID                    string         `gorm:"column:id;primaryKey"`
	ContentDocumentID     *string        `gorm:"column:content_document_id;type:uuid"`
	SourceLocale          string         `gorm:"column:source_locale;type:text;not null;default:en"`
	Title                 string         `gorm:"-"`
	Summary               *string        `gorm:"-"`
	Slug                  *string        `gorm:"column:slug"`
	DocumentLayout        DocumentLayout `gorm:"column:document_layout;type:jsonb;not null"`
	Status                PageStatus     `gorm:"column:status"`
	ShowTitle             bool           `gorm:"column:show_title"`
	FeaturedImageFileID   *string        `gorm:"column:featured_image_file_id;type:uuid"`
	OgAssetID             *string        `gorm:"column:og_asset_id;type:uuid"`
	SourceLocaleOgAssetID *string        `gorm:"-"`
	PublishedAt           *time.Time     `gorm:"column:published_at"`
	CreatedAt             time.Time      `gorm:"column:created_at"`
	UpdatedAt             time.Time      `gorm:"column:updated_at"`
}

func (Page) TableName() string {
	return "page"
}

// PageVersion represents a version snapshot of a page
type PageVersion struct {
	ID                   string          `gorm:"column:id;primaryKey;default:gen_random_uuid()"`
	PageID               string          `gorm:"column:page_id"`
	Version              int32           `gorm:"column:version"`
	Title                *string         `gorm:"column:title"`
	Summary              *string         `gorm:"column:summary"`
	ContentSnapshot      json.RawMessage `gorm:"column:content_snapshot;type:jsonb"`
	ContributorMemberIDs pq.StringArray  `gorm:"column:contributor_member_ids;type:uuid[];not null;default:'{}'"`
	CreatedAt            time.Time       `gorm:"column:created_at"`
}

func (PageVersion) TableName() string {
	return "page_version"
}

// Menu represents a navigation menu
// Maps to: schema migrations - menu table
// Items are stored as JSONB array
type Menu struct {
	ID                string          `gorm:"column:id;primaryKey"`
	ContentDocumentID string          `gorm:"column:content_document_id;type:uuid"`
	SourceLocale      string          `gorm:"column:source_locale;type:text;not null;default:en"`
	Name              string          `gorm:"column:name"`
	Items             json.RawMessage `gorm:"column:items;type:jsonb"`
	CreatedAt         time.Time       `gorm:"column:created_at"`
	UpdatedAt         time.Time       `gorm:"column:updated_at"`
}

func (Menu) TableName() string {
	return "menu"
}

// MenuItem represents a menu item structure (stored in Menu.Items JSONB)
// This is NOT a database table, just a struct for JSON marshaling
const (
	MenuItemLocalizationModeTranslated  = "translated"
	MenuItemLocalizationModeFixedLocale = "fixed_locale"
)

type MenuItem struct {
	ID               string          `json:"id"`
	Label            string          `json:"label"`
	LinkType         string          `json:"linkType"` // custom, page, category, tag, series
	URL              *string         `json:"url,omitempty"`
	TargetID         *string         `json:"targetId,omitempty"`
	TargetSlug       *string         `json:"targetSlug,omitempty"`
	OpenInNewTab     *bool           `json:"openInNewTab,omitempty"`
	Visibility       *MenuVisibility `json:"visibility,omitempty"`
	LocalizationMode *string         `json:"localizationMode,omitempty"`
	FixedLocale      *string         `json:"fixedLocale,omitempty"`
	Children         []MenuItem      `json:"children,omitempty"`
}

// MenuVisibility controls who can see the menu item
type MenuVisibility struct {
	Mode  string   `json:"mode"` // all, authenticated, guest, roles
	Roles []string `json:"roles,omitempty"`
}

// FormStatus represents the status of a form
type FormStatus string

// Form represents a form (Domain Object)
// Maps to: schema migrations - form table
type Form struct {
	ID                       string         `gorm:"column:id;primaryKey"`
	ContentDocumentID        string         `gorm:"column:content_document_id;type:uuid"`
	SourceLocale             string         `gorm:"column:source_locale;type:text"`
	Slug                     *string        `gorm:"column:slug"`
	Status                   FormStatus     `gorm:"column:status"`
	IsPublic                 bool           `gorm:"column:is_public"`
	RequireAuth              *bool          `gorm:"column:require_auth"`
	AllowedRoles             pq.StringArray `gorm:"column:allowed_roles;type:text[]"`
	AllowDuplicateSubmission *bool          `gorm:"column:allow_duplicate_submission"`
	AccessPassword           *string        `gorm:"column:access_password"`
	MaxSubmissions           *int32         `gorm:"column:max_submissions"`
	OpensAt                  *time.Time     `gorm:"column:opens_at"`
	ClosesAt                 *time.Time     `gorm:"column:closes_at"`
	FeaturedImageFileID      *string        `gorm:"column:featured_image_file_id;type:uuid"`
	OgAssetID                *string        `gorm:"column:og_asset_id;type:uuid"`
	CreatedAt                time.Time      `gorm:"column:created_at"`
	UpdatedAt                *time.Time     `gorm:"column:updated_at"`

	Submissions []FormSubmission `gorm:"foreignKey:FormID"`
}

func (Form) TableName() string {
	return "form"
}

// FormSubmission represents a form submission
// Maps to: schema migrations - form_submission table
type FormSubmission struct {
	ID          string    `gorm:"column:id;primaryKey"`
	FormID      string    `gorm:"column:form_id"`
	MemberID    *string   `gorm:"column:member_id"`
	Data        []byte    `gorm:"column:data;type:jsonb"`
	IPAddress   *string   `gorm:"column:ip_address"`
	CountryCode *string   `gorm:"column:country_code"`
	UserAgent   *string   `gorm:"column:user_agent"`
	CreatedAt   time.Time `gorm:"column:created_at"`
}

func (FormSubmission) TableName() string {
	return "form_submission"
}
