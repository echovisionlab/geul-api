package mediaasset

import (
	"context"
	"fmt"
	"strings"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const fileAttachmentProtoFullName protoreflect.FullName = "api.content.v1.FileAttachment"

func LoadUnavailableVersionAttachmentKinds(ctx context.Context, store AttachmentStore, document proto.Message) (map[uuid.UUID]contentv1.MissingAttachmentMediaKind, error) {
	if store == nil {
		return nil, fmt.Errorf("version attachment transaction is required")
	}
	ids := make(map[uuid.UUID]struct{})
	if document != nil {
		if err := collectActiveVersionAttachmentIDs(document.ProtoReflect(), ids); err != nil {
			return nil, err
		}
	}
	kinds, err := UnavailableVersionAttachmentKinds(ctx, store, ids)
	if err != nil || kinds == nil {
		return nil, err
	}
	result := make(map[uuid.UUID]contentv1.MissingAttachmentMediaKind, len(kinds))
	for id, kind := range kinds {
		switch kind {
		case AttachmentImage:
			result[id] = contentv1.MissingAttachmentMediaKind_MISSING_ATTACHMENT_MEDIA_KIND_IMAGE
		case AttachmentAudio:
			result[id] = contentv1.MissingAttachmentMediaKind_MISSING_ATTACHMENT_MEDIA_KIND_AUDIO
		case AttachmentVideo:
			result[id] = contentv1.MissingAttachmentMediaKind_MISSING_ATTACHMENT_MEDIA_KIND_VIDEO
		default:
			result[id] = contentv1.MissingAttachmentMediaKind_MISSING_ATTACHMENT_MEDIA_KIND_FILE
		}
	}
	return result, nil
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
