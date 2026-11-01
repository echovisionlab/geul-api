package filemedia

import (
	"context"

	"connectrpc.com/connect"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1/intrav1connect"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

// ConnectHandler serves the administrator File RPCs. Each method translates
// the request into a use-case command or query, and the result into the
// response; errs.Wrap maps use-case errors to Connect codes.
type ConnectHandler struct {
	managev1connect.UnimplementedFileServiceHandler
	files *Service
}

// NewConnectHandler returns the administrator File Connect adapter.
func NewConnectHandler(files *Service) *ConnectHandler {
	return &ConnectHandler{files: files}
}

// InternalConnectHandler serves the collaboration service's File ingest
// boundary.
type InternalConnectHandler struct {
	intrav1connect.UnimplementedInternalFileIngestServiceHandler
	files *Service
}

// NewInternalConnectHandler returns the collaboration File ingest Connect
// adapter.
func NewInternalConnectHandler(files *Service) *InternalConnectHandler {
	return &InternalConnectHandler{files: files}
}

// LinkPreviewConnectHandler serves the editor's link preview RPC.
type LinkPreviewConnectHandler struct {
	managev1connect.UnimplementedLinkPreviewServiceHandler
	previews *LinkPreviewService
}

// NewLinkPreviewConnectHandler returns the link preview Connect adapter.
func NewLinkPreviewConnectHandler(previews *LinkPreviewService) *LinkPreviewConnectHandler {
	return &LinkPreviewConnectHandler{previews: previews}
}

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

func (h *ConnectHandler) ListFileManagerItems(ctx context.Context, req *connect.Request[managev1.ListFileManagerItemsRequest]) (*connect.Response[managev1.ListFileManagerItemsResponse], error) {
	page, err := h.files.ListFileManagerItems(ctx, ListFileManagerItemsQuery{
		FolderID: req.Msg.GetFolderId(), UploadedByMemberID: req.Msg.GetUploadedByMemberId(),
		Query: req.Msg.GetQuery(), MimeTypePrefix: req.Msg.GetMimeTypePrefix(),
		PageToken: req.Msg.GetPageToken(), PageSize: req.Msg.PageSize,
		SortField: req.Msg.SortField, SortOrder: req.Msg.SortOrder,
	})
	return respond(&managev1.ListFileManagerItemsResponse{Items: page.Items, NextPageToken: page.NextPageToken, Total: page.Total}, err)
}

func (h *ConnectHandler) GetFile(ctx context.Context, req *connect.Request[managev1.GetFileRequest]) (*connect.Response[managev1.GetFileResponse], error) {
	return respond(h.files.GetFile(ctx, GetFileQuery{FileID: req.Msg.FileId}))
}

func (h *ConnectHandler) ListFileUsages(ctx context.Context, req *connect.Request[managev1.ListFileUsagesRequest]) (*connect.Response[managev1.ListFileUsagesResponse], error) {
	page, err := h.files.ListFileUsages(ctx, ListFileUsagesQuery{FileID: req.Msg.FileId, PageToken: req.Msg.GetPageToken(), PageSize: req.Msg.PageSize})
	return respond(&managev1.ListFileUsagesResponse{Usages: fileUsageViewsProto(page.Usages), NextPageToken: page.NextPageToken, Total: page.Total}, err)
}

func (h *ConnectHandler) CreateFileFolder(ctx context.Context, req *connect.Request[managev1.CreateFileFolderRequest]) (*connect.Response[managev1.CreateFileFolderResponse], error) {
	folder, err := h.files.CreateFileFolder(ctx, CreateFileFolderCommand{ParentID: req.Msg.GetParentId(), Name: req.Msg.Name})
	return respond(&managev1.CreateFileFolderResponse{Folder: folderViewProto(folder)}, err)
}

func (h *ConnectHandler) RenameFileFolder(ctx context.Context, req *connect.Request[managev1.RenameFileFolderRequest]) (*connect.Response[managev1.RenameFileFolderResponse], error) {
	folder, err := h.files.RenameFileFolder(ctx, RenameFileFolderCommand{FolderID: req.Msg.FolderId, Name: req.Msg.Name})
	return respond(&managev1.RenameFileFolderResponse{Folder: folderViewProto(folder)}, err)
}

func (h *ConnectHandler) MoveFileFolder(ctx context.Context, req *connect.Request[managev1.MoveFileFolderRequest]) (*connect.Response[managev1.MoveFileFolderResponse], error) {
	folder, err := h.files.MoveFileFolder(ctx, MoveFileFolderCommand{FolderID: req.Msg.FolderId, ParentID: req.Msg.GetParentId()})
	return respond(&managev1.MoveFileFolderResponse{Folder: folderViewProto(folder)}, err)
}

func (h *ConnectHandler) DeleteFileFolder(ctx context.Context, req *connect.Request[managev1.DeleteFileFolderRequest]) (*connect.Response[managev1.DeleteFileFolderResponse], error) {
	accepted, err := h.files.DeleteFileFolder(ctx, DeleteFileFolderCommand{FolderID: req.Msg.FolderId})
	return respond(&managev1.DeleteFileFolderResponse{AcceptedFileIds: accepted}, err)
}

func (h *ConnectHandler) RenameFile(ctx context.Context, req *connect.Request[managev1.RenameFileRequest]) (*connect.Response[managev1.RenameFileResponse], error) {
	file, err := h.files.RenameFile(ctx, RenameFileCommand{FileID: req.Msg.FileId, FileName: req.Msg.FileName})
	return respond(&managev1.RenameFileResponse{File: file}, err)
}

func (h *ConnectHandler) MoveFiles(ctx context.Context, req *connect.Request[managev1.MoveFilesRequest]) (*connect.Response[managev1.MoveFilesResponse], error) {
	files, err := h.files.MoveFiles(ctx, MoveFilesCommand{FileIDs: req.Msg.FileIds, FolderID: req.Msg.GetFolderId()})
	return respond(&managev1.MoveFilesResponse{Files: files}, err)
}

func (h *ConnectHandler) GetFileDeletionImpact(ctx context.Context, req *connect.Request[managev1.GetFileDeletionImpactRequest]) (*connect.Response[managev1.GetFileDeletionImpactResponse], error) {
	impacts, err := h.files.GetFileDeletionImpact(ctx, GetFileDeletionImpactQuery{FileIDs: req.Msg.FileIds})
	return respond(&managev1.GetFileDeletionImpactResponse{Impacts: deletionImpactsProto(impacts)}, err)
}

func (h *ConnectHandler) DeleteFiles(ctx context.Context, req *connect.Request[managev1.DeleteFilesRequest]) (*connect.Response[managev1.DeleteFilesResponse], error) {
	outcome, err := h.files.DeleteFiles(ctx, DeleteFilesCommand{FileIDs: req.Msg.FileIds})
	return respond(&managev1.DeleteFilesResponse{AcceptedFileIds: outcome.AcceptedFileIDs, RejectedFiles: deletionImpactsProto(outcome.RejectedFiles)}, err)
}

func (h *ConnectHandler) DeleteFile(ctx context.Context, req *connect.Request[managev1.DeleteFileRequest]) (*connect.Response[managev1.DeleteFileResponse], error) {
	err := h.files.DeleteFile(ctx, DeleteFileCommand{FileID: req.Msg.FileId})
	return respond(&managev1.DeleteFileResponse{Success: true}, err)
}

func (h *ConnectHandler) InitiateMultipartUpload(ctx context.Context, req *connect.Request[managev1.InitiateMultipartUploadRequest]) (*connect.Response[managev1.InitiateMultipartUploadResponse], error) {
	return respond(h.files.InitiateMultipartUpload(ctx, InitiateMultipartUploadCommand{
		UploadType: req.Msg.UploadType, EntityID: req.Msg.EntityId, EntityType: req.Msg.EntityType,
		FileName: req.Msg.FileName, FileSize: req.Msg.FileSize, MimeType: req.Msg.MimeType,
		FileLastModified: req.Msg.FileLastModified, SlotID: req.Msg.SlotId,
		ExpectedCurrentFileID: req.Msg.ExpectedCurrentFileId,
	}))
}

func (h *ConnectHandler) PrepareClientMediaUpload(ctx context.Context, req *connect.Request[managev1.PrepareClientMediaUploadRequest]) (*connect.Response[managev1.PrepareClientMediaUploadResponse], error) {
	bundleID, err := h.files.PrepareClientMediaUpload(ctx, prepareClientMediaUploadCommand(req.Msg))
	return respond(&managev1.PrepareClientMediaUploadResponse{BundleId: bundleID}, err)
}

func prepareClientMediaUploadCommand(request *managev1.PrepareClientMediaUploadRequest) PrepareClientMediaUploadCommand {
	artifacts := make([]ClientMediaUploadArtifact, 0, len(request.Artifacts))
	for _, artifact := range request.Artifacts {
		artifacts = append(artifacts, ClientMediaUploadArtifact{
			Path: artifact.GetPath(), MimeType: artifact.GetMimeType(), Size: artifact.GetSize(),
			SHA256: artifact.GetSha256(), DerivativeType: artifact.GetDerivativeType(),
		})
	}
	return PrepareClientMediaUploadCommand{
		FileID: request.FileId, UploadID: request.UploadId, Kind: request.Kind,
		DurationSeconds: request.DurationSeconds, Artifacts: artifacts,
	}
}

func (h *ConnectHandler) CompleteMultipartUpload(ctx context.Context, req *connect.Request[managev1.CompleteMultipartUploadRequest]) (*connect.Response[managev1.CompleteMultipartUploadResponse], error) {
	return respond(h.files.CompleteMultipartUpload(ctx, CompleteMultipartUploadCommand{
		FileID: req.Msg.FileId, UploadID: req.Msg.UploadId,
		CorrelationID: req.Msg.GetCorrelationId(), ClientMediaBundleID: req.Msg.GetClientMediaBundleId(),
	}))
}

func (h *ConnectHandler) AbortMultipartUpload(ctx context.Context, req *connect.Request[managev1.AbortMultipartUploadRequest]) (*connect.Response[managev1.AbortMultipartUploadResponse], error) {
	err := h.files.AbortMultipartUpload(ctx, AbortMultipartUploadCommand{
		FileID: req.Msg.FileId, UploadID: req.Msg.UploadId, CorrelationID: req.Msg.GetCorrelationId(),
	})
	return respond(&managev1.AbortMultipartUploadResponse{Success: true}, err)
}

func (h *ConnectHandler) FindMultipartUploadCandidate(ctx context.Context, req *connect.Request[managev1.FindMultipartUploadCandidateRequest]) (*connect.Response[managev1.FindMultipartUploadCandidateResponse], error) {
	return respond(h.files.FindMultipartUploadCandidate(ctx, FindMultipartUploadCandidateQuery{
		UploadType: req.Msg.UploadType, EntityID: req.Msg.EntityId, EntityType: req.Msg.EntityType,
		FileID: req.Msg.FileId, UploadID: req.Msg.UploadId, FileName: req.Msg.FileName,
		FileSize: req.Msg.FileSize, MimeType: req.Msg.MimeType, FileLastModified: req.Msg.FileLastModified,
		SlotID: req.Msg.SlotId, ExpectedCurrentFileID: req.Msg.ExpectedCurrentFileId,
	}))
}

func (h *ConnectHandler) DownloadFromUrl(ctx context.Context, req *connect.Request[managev1.DownloadFromUrlRequest]) (*connect.Response[managev1.DownloadFromUrlResponse], error) {
	return respond(h.files.DownloadFromURL(ctx, DownloadFromURLCommand{
		UploadType: req.Msg.UploadType, EntityID: req.Msg.EntityId, EntityType: req.Msg.EntityType,
		URL: req.Msg.Url, CorrelationID: req.Msg.GetCorrelationId(), SlotID: req.Msg.GetSlotId(),
		ExpectedCurrentFileID: req.Msg.ExpectedCurrentFileId,
	}))
}

func (h *ConnectHandler) GetMediaDelivery(ctx context.Context, req *connect.Request[managev1.GetMediaDeliveryRequest]) (*connect.Response[managev1.GetMediaDeliveryResponse], error) {
	return respond(h.files.GetMediaDelivery(ctx, GetMediaDeliveryQuery{FileID: req.Msg.FileId}))
}

func (h *ConnectHandler) GetBulkMediaDeliveries(ctx context.Context, req *connect.Request[managev1.GetBulkMediaDeliveriesRequest]) (*connect.Response[managev1.GetBulkMediaDeliveriesResponse], error) {
	files, err := h.files.GetBulkMediaDeliveries(ctx, GetBulkMediaDeliveriesQuery{FileIDs: req.Msg.FileIds})
	return respond(&managev1.GetBulkMediaDeliveriesResponse{Files: files}, err)
}

func fileDownloadPolicyTarget(entityType managev1.TranscodeEntityType, entityID string, blockID, referencePath *string) FileDownloadPolicyTarget {
	return FileDownloadPolicyTarget{EntityType: entityType, EntityID: entityID, BlockID: blockID, ReferencePath: referencePath}
}

func (h *ConnectHandler) GetFileDownloadPolicy(ctx context.Context, req *connect.Request[managev1.GetFileDownloadPolicyRequest]) (*connect.Response[managev1.GetFileDownloadPolicyResponse], error) {
	policy, err := h.files.GetFileDownloadPolicy(ctx, GetFileDownloadPolicyQuery{
		Target: fileDownloadPolicyTarget(req.Msg.EntityType, req.Msg.EntityId, req.Msg.BlockId, req.Msg.ReferencePath),
	})
	return respond(&managev1.GetFileDownloadPolicyResponse{Policy: policy}, err)
}

func (h *ConnectHandler) UpdateFileDownloadPolicy(ctx context.Context, req *connect.Request[managev1.UpdateFileDownloadPolicyRequest]) (*connect.Response[managev1.UpdateFileDownloadPolicyResponse], error) {
	command := UpdateFileDownloadPolicyCommand{
		Target:         fileDownloadPolicyTarget(req.Msg.EntityType, req.Msg.EntityId, req.Msg.BlockId, req.Msg.ReferencePath),
		ExpectedFileID: req.Msg.ExpectedFileId,
		Policy:         FileDownloadPolicyState{Audience: req.Msg.Audience, AudienceSegmentIDs: req.Msg.AudienceSegmentIds},
	}
	if observed := req.Msg.ObservedPolicy; observed != nil {
		command.Observed = &FileDownloadPolicyState{Audience: observed.Audience, AudienceSegmentIDs: observed.AudienceSegmentIds}
	}
	policy, err := h.files.UpdateFileDownloadPolicy(ctx, command)
	return respond(&managev1.UpdateFileDownloadPolicyResponse{Policy: policy}, err)
}

func meshOptimizationCandidatesQuery(sourceFileID string, entityType managev1.TranscodeEntityType, entityID string, profile managev1.MeshOptimizationProfile) ListMeshOptimizationCandidatesQuery {
	return ListMeshOptimizationCandidatesQuery{SourceFileID: sourceFileID, EntityType: entityType, EntityID: entityID, Profile: profile}
}

func (h *ConnectHandler) ListMeshOptimizationCandidates(ctx context.Context, req *connect.Request[managev1.ListMeshOptimizationCandidatesRequest]) (*connect.Response[managev1.ListMeshOptimizationCandidatesResponse], error) {
	candidates, err := h.files.ListMeshOptimizationCandidates(ctx, meshOptimizationCandidatesQuery(req.Msg.SourceFileId, req.Msg.EntityType, req.Msg.EntityId, req.Msg.Profile))
	return respond(&managev1.ListMeshOptimizationCandidatesResponse{Candidates: candidates}, err)
}

func (h *ConnectHandler) GenerateMeshOptimizationCandidate(ctx context.Context, req *connect.Request[managev1.GenerateMeshOptimizationCandidateRequest]) (*connect.Response[managev1.GenerateMeshOptimizationCandidateResponse], error) {
	return respond(h.files.GenerateMeshOptimizationCandidate(ctx, GenerateMeshOptimizationCandidateCommand{
		SourceFileID: req.Msg.SourceFileId, EntityType: req.Msg.EntityType, EntityID: req.Msg.EntityId,
		TargetRatioPercent: req.Msg.TargetRatioPercent, Method: req.Msg.Method, Profile: req.Msg.Profile,
	}))
}

func (h *ConnectHandler) UseMeshOptimizationCandidate(ctx context.Context, req *connect.Request[managev1.UseMeshOptimizationCandidateRequest]) (*connect.Response[managev1.UseMeshOptimizationCandidateResponse], error) {
	candidate, err := h.files.UseMeshOptimizationCandidate(ctx, UseMeshOptimizationCandidateCommand{CandidateID: req.Msg.CandidateId})
	return respond(&managev1.UseMeshOptimizationCandidateResponse{Candidate: candidate}, err)
}

func (h *ConnectHandler) ClearMeshOptimizationCandidates(ctx context.Context, req *connect.Request[managev1.ClearMeshOptimizationCandidatesRequest]) (*connect.Response[managev1.ClearMeshOptimizationCandidatesResponse], error) {
	err := h.files.ClearMeshOptimizationCandidates(ctx, ClearMeshOptimizationCandidatesCommand{
		CandidateID: req.Msg.GetCandidateId(),
		Candidates:  meshOptimizationCandidatesQuery(req.Msg.SourceFileId, req.Msg.EntityType, req.Msg.EntityId, req.Msg.Profile),
	})
	return respond(&managev1.ClearMeshOptimizationCandidatesResponse{Success: true}, err)
}

func (h *InternalConnectHandler) AttachTrackOriginalAudio(ctx context.Context, req *connect.Request[intrav1.AttachTrackOriginalAudioRequest]) (*connect.Response[intrav1.AttachTrackOriginalAudioResponse], error) {
	outcome, err := h.files.AttachTrackOriginalAudio(ctx, AttachTrackOriginalAudioCommand{
		TrackID: req.Msg.TrackId, VerifiedFileID: req.Msg.VerifiedFileId,
		IngestAttemptID: req.Msg.IngestAttemptId, ExpectedCurrentFileID: req.Msg.ExpectedCurrentFileId,
	})
	result := intrav1.AttachTrackOriginalAudioResult_ATTACH_TRACK_ORIGINAL_AUDIO_RESULT_APPLIED
	if outcome.AlreadyApplied {
		result = intrav1.AttachTrackOriginalAudioResult_ATTACH_TRACK_ORIGINAL_AUDIO_RESULT_ALREADY_APPLIED
	}
	return respond(&intrav1.AttachTrackOriginalAudioResponse{
		Result: result, CurrentFileId: outcome.CurrentFileID, ReleaseId: outcome.ReleaseID,
	}, err)
}

func (h *LinkPreviewConnectHandler) GetLinkPreview(ctx context.Context, req *connect.Request[managev1.GetLinkPreviewRequest]) (*connect.Response[managev1.GetLinkPreviewResponse], error) {
	result, err := h.previews.GetLinkPreview(ctx, GetLinkPreviewQuery{URL: req.Msg.GetUrl()})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&managev1.GetLinkPreviewResponse{ResolvedUrl: result.ResolvedURL, Title: result.Title, Description: result.Description, SiteName: result.SiteName, ImageSource: result.ImageSource, IconSource: result.IconSource}), nil
}
