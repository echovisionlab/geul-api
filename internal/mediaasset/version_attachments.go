package mediaasset

import (
	"context"
	"fmt"
	"strings"
	"time"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const fileAttachmentProtoFullName protoreflect.FullName = "api.content.v1.FileAttachment"

// LoadUnavailableVersionAttachmentKinds resolves every active File ID in an
// immutable Version snapshot against the restore transaction. Missing Files
// degrade to the generic File placeholder; retained Files pending deletion
// preserve their MIME category.
func LoadUnavailableVersionAttachmentKinds(
	ctx context.Context,
	tx *gorm.DB,
	document proto.Message,
) (map[uuid.UUID]contentv1.MissingAttachmentMediaKind, error) {
	if tx == nil {
		return nil, fmt.Errorf("version attachment transaction is required")
	}
	fileIDs := make(map[uuid.UUID]struct{})
	if document != nil {
		if err := collectActiveVersionAttachmentIDs(document.ProtoReflect(), fileIDs); err != nil {
			return nil, err
		}
	}
	if len(fileIDs) == 0 {
		return nil, nil
	}

	ids := make([]string, 0, len(fileIDs))
	for fileID := range fileIDs {
		ids = append(ids, fileID.String())
	}
	var rows []struct {
		ID                string     `gorm:"column:id"`
		MIMEType          string     `gorm:"column:mime_type"`
		DeleteRequestedAt *time.Time `gorm:"column:delete_requested_at"`
	}
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "SHARE"}).
		Table("file").
		Select("id, mime_type, delete_requested_at").
		Where("id IN ?", ids).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load Version attachment Files: %w", err)
	}

	available := make(map[uuid.UUID]struct{}, len(rows))
	unavailable := make(map[uuid.UUID]contentv1.MissingAttachmentMediaKind)
	for _, row := range rows {
		fileID, err := uuid.Parse(row.ID)
		if err != nil {
			return nil, fmt.Errorf("invalid Version attachment File ID %q: %w", row.ID, err)
		}
		available[fileID] = struct{}{}
		if row.DeleteRequestedAt != nil {
			unavailable[fileID] = missingAttachmentKindForMIME(row.MIMEType)
		}
	}
	for fileID := range fileIDs {
		if _, exists := available[fileID]; !exists {
			unavailable[fileID] = contentv1.MissingAttachmentMediaKind_MISSING_ATTACHMENT_MEDIA_KIND_FILE
		}
	}
	return unavailable, nil
}

func collectActiveVersionAttachmentIDs(message protoreflect.Message, fileIDs map[uuid.UUID]struct{}) error {
	if !message.IsValid() {
		return nil
	}
	if message.Descriptor().FullName() == fileAttachmentProtoFullName {
		attachment, ok := message.Interface().(*contentv1.FileAttachment)
		if !ok {
			return fmt.Errorf("unexpected FileAttachment protobuf implementation")
		}
		rawID := strings.TrimSpace(attachment.GetActiveFileId())
		if rawID == "" {
			return nil
		}
		fileID, err := uuid.Parse(rawID)
		if err != nil {
			return fmt.Errorf("invalid Version attachment File ID %q: %w", rawID, err)
		}
		fileIDs[fileID] = struct{}{}
		return nil
	}

	var visitErr error
	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		if field.IsMap() {
			if field.MapValue().Kind() != protoreflect.MessageKind {
				return true
			}
			value.Map().Range(func(_ protoreflect.MapKey, item protoreflect.Value) bool {
				visitErr = collectActiveVersionAttachmentIDs(item.Message(), fileIDs)
				return visitErr == nil
			})
			return visitErr == nil
		}
		if field.IsList() {
			if field.Kind() != protoreflect.MessageKind {
				return true
			}
			list := value.List()
			for index := 0; index < list.Len(); index++ {
				if visitErr = collectActiveVersionAttachmentIDs(list.Get(index).Message(), fileIDs); visitErr != nil {
					return false
				}
			}
			return true
		}
		if field.Kind() == protoreflect.MessageKind {
			visitErr = collectActiveVersionAttachmentIDs(value.Message(), fileIDs)
			return visitErr == nil
		}
		return true
	})
	return visitErr
}

func missingAttachmentKindForMIME(mimeType string) contentv1.MissingAttachmentMediaKind {
	normalized := strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0]))
	switch {
	case strings.HasPrefix(normalized, "image/"):
		return contentv1.MissingAttachmentMediaKind_MISSING_ATTACHMENT_MEDIA_KIND_IMAGE
	case strings.HasPrefix(normalized, "audio/"):
		return contentv1.MissingAttachmentMediaKind_MISSING_ATTACHMENT_MEDIA_KIND_AUDIO
	case strings.HasPrefix(normalized, "video/"):
		return contentv1.MissingAttachmentMediaKind_MISSING_ATTACHMENT_MEDIA_KIND_VIDEO
	default:
		return contentv1.MissingAttachmentMediaKind_MISSING_ATTACHMENT_MEDIA_KIND_FILE
	}
}
