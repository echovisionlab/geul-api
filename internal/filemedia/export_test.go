package filemedia

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func ExportLibraryService(units mutation.UnitOfWork[Store], authorizer mutation.Authorizer, fence identitystate.Fence, audit domainaudit.Appender, members MemberSummaries) *Service {
	runner, err := mutation.NewStoreRunner(units, authorizer, fence, audit)
	if err != nil {
		panic(err)
	}
	return &Service{units: units, storeMutations: runner, memberSummaries: members}
}

var ExportRenameLockedFile = renameLockedFile
var ExportMoveLockedFiles = moveLockedFiles

type ExportDeliveryGrant struct {
	Uploader, AvatarOwner, OwnerFingerprint string
	Usage                                   *domain.DeliveryUsageWitness
}

func ExportFinalizeManageDelivery(units mutation.UnitOfWork[Store], ctx context.Context, user *auth.UserInfo, response *managev1.GetMediaDeliveryResponse, grant ExportDeliveryGrant) (map[string]*managev1.GetMediaDeliveryResponse, bool, error) {
	s := &Service{units: units, mediaDomain: "https://media.example.com", mediaSecret: "delivery-test-secret"}
	g := manageFileDeliveryGrant{lockFile: grant.Usage == nil, expectedUploader: grant.Uploader, expectedAvatarOwner: grant.AvatarOwner}
	if w := grant.Usage; w != nil {
		g.usageWitnesses = []manageFileDeliveryUsageWitness{{kind: w.Kind, fileID: w.FileID, resourceType: w.ResourceType, resourceID: w.ResourceID, relationID: w.RelationID, referencePath: w.ReferencePath, ownerFingerprint: grant.OwnerFingerprint}}
	}
	id := response.GetDelivery().GetFileId()
	return s.finalizeManageFileURLResponses(ctx, map[string]*managev1.GetMediaDeliveryResponse{id: response}, &manageFileDeliveryAuthorization{principal: user, files: map[string]manageFileDeliveryGrant{id: g}})
}

func ExportDeliveryService(units mutation.UnitOfWork[Store]) *Service {
	return &Service{units: units, cdnDomain: "cdn.example.com", mediaDomain: "media.example.com", mediaSecret: "test-secret"}
}

var ExportGetFileURLs = (*Service).getFileUrlsForID
var ExportLoadFileURLs = (*Service).loadFileURLResponses
