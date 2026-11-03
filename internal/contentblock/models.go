package contentblock

import (
	"time"

	"github.com/google/uuid"
)

type documentRow struct {
	ID        uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`
	Profile   string    `gorm:"column:profile"`
	Revision  uuid.UUID `gorm:"column:revision;type:uuid"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (documentRow) TableName() string { return "content_document" }

type blockRow struct {
	ID            uuid.UUID  `gorm:"column:id;type:uuid;primaryKey"`
	DocumentID    uuid.UUID  `gorm:"column:document_id;type:uuid"`
	ParentBlockID *uuid.UUID `gorm:"column:parent_block_id;type:uuid"`
	ContainerSlot string     `gorm:"column:container_slot"`
	Position      int        `gorm:"column:position"`
	Kind          string     `gorm:"column:kind"`
	SharedData    []byte     `gorm:"column:shared_data;type:jsonb"`
	CreatedAt     time.Time  `gorm:"column:created_at"`
	UpdatedAt     time.Time  `gorm:"column:updated_at"`
}

func (blockRow) TableName() string { return "content_block" }

type blockLocaleRow struct {
	BlockID       uuid.UUID `gorm:"column:block_id;type:uuid;primaryKey"`
	Locale        string    `gorm:"column:locale;primaryKey"`
	LocalizedData []byte    `gorm:"column:localized_data;type:jsonb"`
}

func (blockLocaleRow) TableName() string { return "content_block_locale" }

type blockAttachmentRow struct {
	BlockID        uuid.UUID  `gorm:"column:block_id;type:uuid;primaryKey"`
	ReferencePath  string     `gorm:"column:reference_path;primaryKey"`
	SelectorKind   string     `gorm:"column:selector_kind"`
	FileID         *uuid.UUID `gorm:"column:file_id;type:uuid"`
	MissingKind    *string    `gorm:"column:missing_kind"`
	DownloadAccess string     `gorm:"column:download_access"`
}

func (blockAttachmentRow) TableName() string { return "content_block_attachment" }

type blockAttachmentDownloadMemberCriteriaRow struct {
	BlockID          uuid.UUID `gorm:"column:block_id;type:uuid;primaryKey"`
	ReferencePath    string    `gorm:"column:reference_path;primaryKey"`
	MemberCriteriaID uuid.UUID `gorm:"column:member_criteria_id;type:uuid;primaryKey"`
}

func (blockAttachmentDownloadMemberCriteriaRow) TableName() string {
	return "content_block_attachment_download_member_criteria"
}

type fileRow struct {
	ID                uuid.UUID  `gorm:"column:id;type:uuid;primaryKey"`
	MIMEType          string     `gorm:"column:mime_type"`
	DeleteRequestedAt *time.Time `gorm:"column:delete_requested_at"`
}

func (fileRow) TableName() string { return "file" }
