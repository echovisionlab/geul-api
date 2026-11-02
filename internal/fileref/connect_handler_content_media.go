package fileref

import (
	"slices"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

func BlockMediaSelectorProto(selector *BlockMediaSelector) *contentv1.ContentBlockMediaSelector {
	if selector == nil {
		return nil
	}
	result := &contentv1.ContentBlockMediaSelector{BlockId: selector.BlockID, ReferencePath: selector.ReferencePath}
	result.ProtoReflect().SetUnknown(slices.Clone(selector.Extensions))
	return result
}
func MissingAttachmentProto(missing *MissingAttachment) *contentv1.MissingAttachment {
	if missing == nil {
		return nil
	}
	result := &contentv1.MissingAttachment{FormerFileId: missing.FormerFileID, MediaKind: contentv1.MissingAttachmentMediaKind(missing.MediaKind)}
	result.ProtoReflect().SetUnknown(slices.Clone(missing.Extensions))
	return result
}
func AttachmentProto(attachment *Attachment) *contentv1.FileAttachment {
	if attachment == nil {
		return nil
	}
	result := &contentv1.FileAttachment{}
	switch attachment.Kind {
	case AttachmentActive:
		result.State = &contentv1.FileAttachment_ActiveFileId{ActiveFileId: attachment.FileID}
	case AttachmentMissing:
		result.State = &contentv1.FileAttachment_MissingAttachment{MissingAttachment: MissingAttachmentProto(attachment.Missing)}
	}
	result.ProtoReflect().SetUnknown(slices.Clone(attachment.Extensions))
	return result
}
func BlockMediaProto(item *BlockMediaItem) *contentv1.ContentBlockMediaItem {
	if item == nil {
		return nil
	}
	result := &contentv1.ContentBlockMediaItem{Selector: BlockMediaSelectorProto(item.Selector), Attachment: AttachmentProto(item.Attachment), Delivery: DeliveryProto(item.Delivery), DownloadAvailability: contentv1.ContentBlockDownloadAvailability(item.DownloadAvailability), DownloadAction: contentv1.ContentBlockDownloadAction(item.DownloadAction)}
	result.ProtoReflect().SetUnknown(slices.Clone(item.Extensions))
	return result
}
func BlockMediaItemsProto(items []*BlockMediaItem) []*contentv1.ContentBlockMediaItem {
	if items == nil {
		return nil
	}
	result := make([]*contentv1.ContentBlockMediaItem, len(items))
	for i, item := range items {
		result[i] = BlockMediaProto(item)
	}
	return result
}
