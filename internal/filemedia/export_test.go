package filemedia

import (
	"context"
	"image/color"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/echovisionlab/geul-api/internal/model"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/favicon"
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
	claimed, parts, err := s.claimMultipartCompletion(ctx, domain.UploadSession{UploadID: upload, FileID: file}, now)
	var result []domain.UploadPart
	if parts != nil {
		result = make([]domain.UploadPart, len(parts))
		copy(result, parts)
	}
	return claimed, result, err
}
func ExportRetryableMultipartFailure(s *Service, ctx context.Context, upload, file string, now time.Time) error {
	return s.recordRetryableMultipartPartFailure(ctx, domain.UploadSession{UploadID: upload, FileID: file}, now)
}

func ExportRecordUploadedPart(s *Service, ctx context.Context, row domain.UploadSession, part domain.UploadPart) (bool, error) {
	return s.recordUploadedPart(ctx, row, part)
}

func ExportRecordUploadMIME(s *Service, ctx context.Context, row domain.UploadSession, mime string, now time.Time) error {
	return s.recordMultipartDetectedMIME(ctx, row, mime, now)
}

func ExportReconcileUploadParts(s *Service, ctx context.Context, row domain.UploadSession, parts []domain.UploadPart) error {
	rows := make([]domain.UploadPart, len(parts))
	copy(rows, parts)
	return s.reconcileMultipartResumePartCache(ctx, row, rows)
}

func ExportCreateUploadSession(s *Service, ctx context.Context, row domain.UploadSession) (domain.UploadSession, error) {
	stored := row
	err := s.createUploadSession(ctx, &stored)
	return stored, err
}

func ExportClaimTrackUploadCleanup(s *Service, ctx context.Context, id string) ([]domain.UploadSession, error) {
	rows, err := s.claimTrackUploadSessionsForCleanup(ctx, id)
	result := make([]domain.UploadSession, len(rows))
	copy(result, rows)
	return result, err
}

func ExportAbortService(units mutation.UnitOfWork[Store], storage *s3.Client, checker mutation.Authorizer) *Service {
	return &Service{units: units, s3Client: storage, s3Bucket: "media", spiceDB: checker, asyncPublisher: &hardCutAsyncPublisher{}}
}

func ExportRejectUploadPart(s *Service, w http.ResponseWriter, r *http.Request, row domain.UploadSession) bool {
	return s.rejectMultipartUploadPart(w, r, row, "MIME type mismatch")
}

func ExportValidateCompletedFile(s *Service, ctx context.Context, row domain.UploadSession, uploadType, entityType int32) error {
	return s.validateCompletedFileRecord(ctx, row, managev1.UploadType(uploadType), managev1.TranscodeEntityType(entityType))
}

func ExportLoadFinishedFile(s *Service, ctx context.Context, id string) (File, int32, error) {
	file, upload, err := s.loadFinishedMultipartFile(ctx, id)
	return File(file), int32(upload), err
}

func ExportPrepareBundle(s *Service, ctx context.Context, row domain.UploadSession, command PrepareClientMediaUploadCommand) (string, error) {
	return s.prepareMediaBundle(ctx, row, command)
}

func ExportClientBundleFixture(t *testing.T, kind string) (domain.UploadSession, PrepareClientMediaUploadCommand, string) {
	row, _ := clientMediaTestBundle(t, kind)
	plan, err := decodeClientMediaPlan(row)
	if err != nil {
		t.Fatal(err)
	}
	command := PrepareClientMediaUploadCommand{FileID: row.FileID, UploadID: row.UploadID, Kind: managev1.ClientMediaKind_CLIENT_MEDIA_KIND_AUDIO, DurationSeconds: plan.DurationSeconds}
	if kind == "video" {
		command.Kind = managev1.ClientMediaKind_CLIENT_MEDIA_KIND_VIDEO
	}
	for _, artifact := range plan.Artifacts {
		command.Artifacts = append(command.Artifacts, ClientMediaUploadArtifact{Path: artifact.Path, MimeType: artifact.MimeType, Size: artifact.Size, SHA256: artifact.SHA256, DerivativeType: managev1.FileDerivativeType(artifact.DerivativeType)})
	}
	return row, command, plan.MemberID
}

func ExportIngestChecker(s *Service, checker mutation.Authorizer) { s.spiceDB = checker }

func ExportCreateVerifiedFile(s *Service, ctx context.Context, file File, uploadType, entityType int32, entityID string, session *domain.UploadSession) error {
	var authority verifiedFileIngestAuthority = requestFileIngestAuthority{}
	if session != nil {
		var err error
		authority, err = newMultipartFileIngestAuthority(*session)
		if err != nil {
			return err
		}
	}
	return s.createVerifiedFileIngestRecord(ctx, fileIngestRecord{id: file.ID, fileName: file.FileName, mimeType: file.MimeType, fileSize: file.FileSize, extension: file.Extension, sha256: file.SHA256, ingestSlotID: derefString(file.IngestSlotID), ingestAttemptID: derefString(file.IngestAttemptID)}, managev1.UploadType(uploadType), managev1.TranscodeEntityType(entityType), entityID, authority)
}

func ExportArtifactWritable(s *Service, ctx context.Context, row domain.UploadSession) (bool, error) {
	return s.clientMediaArtifactWritable(ctx, row)
}

func ExportRestoreRemoteReceipt(s *Service, ctx context.Context, id, slot, attempt, entityID string, uploadType, entityType int32) (File, bool, error) {
	file, found, err := s.loadCompletedRemoteImportFile(ctx, id)
	if err != nil || !found {
		return File(file), found, err
	}
	if err := validateCompletedRemoteImportFile(file, slot, remoteImportOperationIdentity{fileID: id, attemptID: attempt}); err != nil {
		return File{}, true, err
	}
	err = s.validateCompletedRemoteImportBinding(ctx, id, entityID, remoteFileImportOptions{uploadType: managev1.UploadType(uploadType), transcodeEntityType: managev1.TranscodeEntityType(entityType)})
	return File(file), true, err
}

var ExportTriggerFileProcessing = (*Service).triggerFileScopedProcessingIfNeeded

func ExportClientMediaStorage(t *testing.T, row domain.UploadSession) *s3.Client {
	plan, err := decodeClientMediaPlan(row)
	if err != nil {
		t.Fatal(err)
	}
	_, objects := clientMediaTestBundle(t, plan.Kind)
	return newClientMediaStorageService(t, row, objects).s3Client
}

func ExportConfigureClientMediaStorage(s *Service, client *s3.Client) {
	s.s3Client, s.s3Bucket, s.cdnDomain = client, "media-bucket", "https://cdn.example.com"
}

func ExportRegisterClientMedia(s *Service, ctx context.Context, row domain.UploadSession, authorize func(context.Context, Store, func(*auth.UserInfo) (bool, error)) error) error {
	return s.registerVerifiedClientMediaBundle(ctx, &multipartCompletion{session: row}, authorize)
}

func ExportFaviconService(units mutation.UnitOfWork[Store], commits domain.FaviconCommits, client *s3.Client, processor favicon.Processor) *Service {
	return &Service{units: units, faviconCommits: commits, s3Client: client, s3Bucket: "media-bucket", cdnDomain: "https://cdn.example.com", faviconProcessor: processor}
}

func ExportFaviconFixture(t *testing.T, id, kind string, failPutAt int) (domain.CatalogRow, *s3.Client, favicon.Processor, func() []string, func() []string) {
	source := faviconTestPNG(t, 151, 152, color.NRGBA{R: 200, A: 255})
	mime, extension := "image/png", "png"
	if kind == "svg" {
		source = []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="151" height="152"><rect width="151" height="152" fill="red"/></svg>`)
		mime, extension = "image/svg+xml", "svg"
	}
	if kind == "unsafe svg" {
		source = []byte(`<svg xmlns="http://www.w3.org/2000/svg"><style>.x{fill:red}</style><rect class="x"/></svg>`)
		mime, extension = "image/svg+xml", "svg"
	}
	storage := newFaviconS3Fixture(t, id, extension, source, failPutAt)
	return domain.CatalogRow{ID: id, Name: "favicon", Extension: &extension, MimeType: &mime, FileSize: new(int64(len(source)))}, storage.client, staticFaviconProcessor{outputs: faviconTestGeneratedOutputs(t)}, storage.writtenKeys, storage.deletedKeys
}

var ExportGenerateFavicon = (*Service).generateFaviconBundle
var ExportFaviconCommitUncertain = errFaviconBundleCommitUncertain

func ExportFaviconReconcileError(service *Service, err error) {
	service.testFaviconReconcileError = err
}

func ExportFaviconTimeout(service *Service, timeout time.Duration) {
	service.testFaviconBundleTimeout = timeout
}

var ExportDeleteMeshCandidateAndOutput = (*MeshOptimizationService).deleteCandidateAndOutput

func ExportMeshFileService(units mutation.UnitOfWork[Store], authorizer mutation.Authorizer) *Service {
	return &Service{units: units, spiceDB: authorizer}
}
