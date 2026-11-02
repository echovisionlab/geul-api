package public

import (
	"github.com/echovisionlab/geul-api/internal/mediaauth"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

func downloadResultProto(result *DownloadResult) *openv1.AuthorizeDownloadResponse {
	if result == nil {
		return nil
	}
	value := &openv1.AuthorizeDownloadResponse{Download: mediaauth.ReferenceProto(result.Download)}
	if result.Access != nil {
		value.Access = &openv1.FileDownloadAccess{Availability: openv1.FileDownloadAvailability(result.Access.Availability), Action: openv1.FileDownloadAction(result.Access.Action)}
	}
	return value
}
