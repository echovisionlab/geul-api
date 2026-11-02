package filemediaqueue

import (
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

func CanonicalObjectTarget(file domain.File) (*commonv1.MediaObjectTarget, error) {
	target, err := domain.CanonicalObjectTarget(file)
	if err != nil {
		return nil, err
	}
	return &commonv1.MediaObjectTarget{FileId: target.FileID, ObjectKey: target.ObjectKey, Extension: target.Extension, MimeType: target.MimeType}, nil
}
