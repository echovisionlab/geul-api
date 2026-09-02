package model

import "time"

const (
	PublicAssetStatusAllocated     = "allocated"
	PublicAssetStatusReady         = "ready"
	PublicAssetStatusDeletePending = "delete_pending"
	PublicAssetStatusDeleted       = "deleted"
	PublicAssetStatusFailed        = "failed"

	MediaGenerationStatusAllocated = "allocated"
	MediaGenerationStatusReady     = "ready"
	MediaGenerationStatusRetired   = "retired"
)

type PublicAsset struct {
	ID                string     `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	SourceFileID      *string    `gorm:"column:source_file_id;type:uuid"`
	Kind              string     `gorm:"column:kind;type:text;not null"`
	ObjectKey         string     `gorm:"column:object_key;type:text;not null;uniqueIndex"`
	Extension         string     `gorm:"column:extension;type:text;not null"`
	MimeType          string     `gorm:"column:mime_type;type:text;not null"`
	FileSize          *int64     `gorm:"column:file_size;type:bigint"`
	SHA256            []byte     `gorm:"column:sha256;type:bytea"`
	Disposition       string     `gorm:"column:disposition;type:text;not null"`
	DownloadFilename  *string    `gorm:"column:download_filename;type:text"`
	Status            string     `gorm:"column:status;type:text;not null"`
	ReadyAt           *time.Time `gorm:"column:ready_at"`
	DeleteRequestedAt *time.Time `gorm:"column:delete_requested_at"`
	DeletedAt         *time.Time `gorm:"column:deleted_at"`
	FailedAt          *time.Time `gorm:"column:failed_at"`
	FailureReason     *string    `gorm:"column:failure_reason;type:text"`
	CreatedAt         time.Time  `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt         time.Time  `gorm:"column:updated_at;not null;default:now()"`
}

func (PublicAsset) TableName() string { return "public_asset" }

type PublicAssetBinding struct {
	AssetID      string    `gorm:"column:asset_id;type:uuid;not null"`
	OwnerType    string    `gorm:"column:owner_type;type:text;primaryKey"`
	OwnerID      string    `gorm:"column:owner_id;type:text;primaryKey"`
	BindingKey   string    `gorm:"column:binding_key;type:text;primaryKey"`
	SourceFileID *string   `gorm:"column:source_file_id;type:uuid"`
	CreatedAt    time.Time `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt    time.Time `gorm:"column:updated_at;not null;default:now()"`
}

func (PublicAssetBinding) TableName() string { return "public_asset_binding" }

type MediaGeneration struct {
	ID             string     `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	FileID         string     `gorm:"column:file_id;type:uuid;not null"`
	Kind           string     `gorm:"column:kind;type:text;not null"`
	ObjectPrefix   string     `gorm:"column:object_prefix;type:text;not null;uniqueIndex"`
	ManifestName   string     `gorm:"column:manifest_name;type:text;not null"`
	ManifestSHA256 []byte     `gorm:"column:manifest_sha256;type:bytea"`
	ObjectCount    *int32     `gorm:"column:object_count"`
	TotalSize      *int64     `gorm:"column:total_size;type:bigint"`
	Status         string     `gorm:"column:status;type:text;not null"`
	ReadyAt        *time.Time `gorm:"column:ready_at"`
	RetiredAt      *time.Time `gorm:"column:retired_at"`
	DeleteAfter    *time.Time `gorm:"column:delete_after"`
	CreatedAt      time.Time  `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt      time.Time  `gorm:"column:updated_at;not null;default:now()"`
}

func (MediaGeneration) TableName() string { return "media_generation" }
