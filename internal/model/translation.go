package model

import (
	"time"

	"github.com/lib/pq"
)

// TranslationSettings stores global translation runtime defaults in a singleton row.
type TranslationSettings struct {
	ID             int            `gorm:"column:id;primaryKey;default:1;check:id = 1"`
	DefaultLocale  string         `gorm:"column:default_locale;type:text;not null"`
	ProtectedTerms pq.StringArray `gorm:"column:protected_terms;type:text[];not null;default:'{}'"`
	CreatedAt      time.Time      `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt      time.Time      `gorm:"column:updated_at;not null;default:now()"`
}

func (TranslationSettings) TableName() string {
	return "translation_settings"
}

// TranslationJob tracks one explicitly requested in-flight target-locale generation.
type TranslationJob struct {
	ID                          string     `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	EntityType                  string     `gorm:"column:entity_type;type:text;not null"`
	EntityID                    string     `gorm:"column:entity_id;type:text;not null"`
	TargetLocale                string     `gorm:"column:target_locale;type:text;not null"`
	SourceLocale                string     `gorm:"column:source_locale;type:text;not null"`
	RequestArtifactDigest       string     `gorm:"column:request_artifact_digest;type:text;not null"`
	OperationID                 string     `gorm:"column:operation_id;type:text;not null"`
	Status                      string     `gorm:"column:status;type:varchar(32);not null"`
	RequestedByMemberID         string     `gorm:"column:requested_by_member_id;type:uuid;not null"`
	Provider                    *string    `gorm:"column:provider;type:varchar(100)"`
	Model                       *string    `gorm:"column:model;type:varchar(255)"`
	ProviderDocumentID          *string    `gorm:"column:provider_document_id;type:text" json:"-"`
	ProviderDocumentKey         *string    `gorm:"column:provider_document_key;type:text" json:"-"`
	ProviderDocumentSubmittedAt *time.Time `gorm:"column:provider_document_submitted_at" json:"-"`
	RequestXLIFF                []byte     `gorm:"column:request_xliff;type:bytea;not null" json:"-"`
	RequestManifest             []byte     `gorm:"column:request_manifest;type:jsonb;not null" json:"-"`
	FailureReason               *string    `gorm:"-"`
	RequestedAt                 time.Time  `gorm:"column:requested_at;not null;default:now()"`
	StartedAt                   *time.Time `gorm:"column:started_at"`
	CompletedAt                 *time.Time `gorm:"-"`
	CreatedAt                   time.Time  `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt                   time.Time  `gorm:"column:updated_at;not null;default:now()"`
}

func (TranslationJob) TableName() string {
	return "translation_job"
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
