package filemedia

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/echovisionlab/geul-api/internal/model"

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

var ExportPrepareSourcePromotion = prepareSourceAssetPromotion
var ExportRecoverSourcePromotionAllocation = recoverSourceAssetPromotionAllocation
var ExportRecoverSourcePromotionCompletion = recoverSourceAssetPromotionCompletion
var ExportCompleteSourcePromotion = (*Service).completeSourceAssetPromotion
var ExportPromoteSourceFile = (*Service).promoteSourceFileToPublicAsset

func ExportPromotionService(units mutation.UnitOfWork[Store], client *s3.Client) *Service {
	return &Service{units: units, s3Client: client, s3Bucket: "media-bucket", cdnDomain: "https://cdn.example.com"}
}
func ExportPromotionStorage(t *testing.T, file File, body []byte) (*s3.Client, func(bool), func() []string) {
	f := newSourceAssetPromotionS3Fixture(t, model.File(file), body)
	return f.client, f.setFailTransfer, f.deletedKeys
}

func ExportSessionService(units mutation.UnitOfWork[Store]) *Service { return &Service{units: units} }

var ExportDeleteCompletedUploadSession = (*Service).deleteCompletedUploadSession
var ExportDeleteAbortedUploadSession = (*Service).deleteAbortedUploadSession
var ExportClaimUploadPartActivity = (*Service).claimUploadPartActivity
var ExportClaimUploadSessionAbort = (*Service).claimUploadSessionAbort

func ExportClaimMultipartCompletion(s *Service, ctx context.Context, upload, file string, now time.Time) (bool, []domain.UploadPart, error) {
	claimed, parts, err := s.claimMultipartCompletion(ctx, model.UploadSession{UploadID: upload, FileID: file}, now)
	var result []domain.UploadPart
	if parts != nil {
		result = make([]domain.UploadPart, len(parts))
		for i, p := range parts {
			result[i] = domain.UploadPart(p)
		}
	}
	return claimed, result, err
}
func ExportRetryableMultipartFailure(s *Service, ctx context.Context, upload, file string, now time.Time) error {
	return s.recordRetryableMultipartPartFailure(ctx, model.UploadSession{UploadID: upload, FileID: file}, now)
}

func ExportRecordUploadedPart(s *Service, ctx context.Context, row domain.UploadSession, part domain.UploadPart) (bool, error) {
	return s.recordUploadedPart(ctx, uploadSessionFromStore(row), model.UploadPart(part))
}

func ExportRecordUploadMIME(s *Service, ctx context.Context, row domain.UploadSession, mime string, now time.Time) error {
	return s.recordMultipartDetectedMIME(ctx, uploadSessionFromStore(row), mime, now)
}

func ExportReconcileUploadParts(s *Service, ctx context.Context, row domain.UploadSession, parts []domain.UploadPart) error {
	rows := make([]model.UploadPart, len(parts))
	for i, p := range parts {
		rows[i] = model.UploadPart(p)
	}
	return s.reconcileMultipartResumePartCache(ctx, uploadSessionFromStore(row), rows)
}

func ExportCreateUploadSession(s *Service, ctx context.Context, row domain.UploadSession) (domain.UploadSession, error) {
	stored := uploadSessionFromStore(row)
	err := s.createUploadSession(ctx, &stored)
	return uploadSessionToStore(stored), err
}

func ExportClaimTrackUploadCleanup(s *Service, ctx context.Context, id string) ([]domain.UploadSession, error) {
	rows, err := s.claimTrackUploadSessionsForCleanup(ctx, id)
	result := make([]domain.UploadSession, len(rows))
	for i, row := range rows {
		result[i] = uploadSessionToStore(row)
	}
	return result, err
}

func ExportAbortService(units mutation.UnitOfWork[Store], storage *s3.Client, checker *auth.SpiceDBClient) *Service {
	return &Service{units: units, s3Client: storage, s3Bucket: "media", spiceDB: checker, asyncPublisher: &hardCutAsyncPublisher{}}
}

func ExportRejectUploadPart(s *Service, w http.ResponseWriter, r *http.Request, row domain.UploadSession) bool {
	return s.rejectMultipartUploadPart(w, r, uploadSessionFromStore(row), "MIME type mismatch")
}

func ExportValidateCompletedFile(s *Service, ctx context.Context, row domain.UploadSession, uploadType, entityType int32) error {
	return s.validateCompletedFileRecord(ctx, uploadSessionFromStore(row), managev1.UploadType(uploadType), managev1.TranscodeEntityType(entityType))
}

func ExportLoadFinishedFile(s *Service, ctx context.Context, id string) (File, int32, error) {
	file, upload, err := s.loadFinishedMultipartFile(ctx, id)
	return File(file), int32(upload), err
}
