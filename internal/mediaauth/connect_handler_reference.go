package mediaauth

import (
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func ReferenceProto(ref *Reference) *commonv1.ExpiringMediaRef {
	if ref == nil {
		return nil
	}
	return &commonv1.ExpiringMediaRef{FileId: ref.FileID, Url: ref.URL, Purpose: DeliveryPurpose(ref.Purpose), ExpiresAt: timestamppb.New(ref.ExpiresAt), Extension: ref.Extension, MimeType: ref.MimeType, FileName: ref.FileName}
}
