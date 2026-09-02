package filemedia

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

var (
	ErrInvalidMCPFileInput      = errors.New("MCP File input is invalid")
	ErrInvalidMCPFileRuntime    = errors.New("MCP File runtime response is invalid")
	ErrInvalidMCPFileDependency = errors.New("MCP File dependency is invalid")
)

// MCPFileRuntime is the existing File-owned ingest and delivery authority.
// The facade deliberately delegates to these public boundaries instead of
// creating MCP-specific upload state, storage, verification, or authorization.
type MCPFileRuntime interface {
	InitiateMultipartUpload(
		context.Context,
		*connect.Request[managev1.InitiateMultipartUploadRequest],
	) (*connect.Response[managev1.InitiateMultipartUploadResponse], error)
	FindMultipartUploadCandidate(
		context.Context,
		*connect.Request[managev1.FindMultipartUploadCandidateRequest],
	) (*connect.Response[managev1.FindMultipartUploadCandidateResponse], error)
	CompleteMultipartUpload(
		context.Context,
		*connect.Request[managev1.CompleteMultipartUploadRequest],
	) (*connect.Response[managev1.CompleteMultipartUploadResponse], error)
	DownloadFromUrl(
		context.Context,
		*connect.Request[managev1.DownloadFromUrlRequest],
	) (*connect.Response[managev1.DownloadFromUrlResponse], error)
	GetMediaDelivery(
		context.Context,
		*connect.Request[managev1.GetMediaDeliveryRequest],
	) (*connect.Response[managev1.GetMediaDeliveryResponse], error)
}

// MCPFileFacade is the compact typed file_transfer/file_read application
// boundary. Its input types have no byte or base64 payload field. Browser and
// presigned clients transfer bytes through the existing upload HTTP boundary;
// remote HTTPS import is streamed and verified by FileService.
type MCPFileFacade struct {
	files MCPFileRuntime
}

func NewMCPFileFacade(files MCPFileRuntime) (*MCPFileFacade, error) {
	if files == nil {
		return nil, ErrInvalidMCPFileDependency
	}
	return &MCPFileFacade{files: files}, nil
}

type MCPFileKind string

const (
	MCPFileKindGeneral    MCPFileKind = "general"
	MCPFileKindImage      MCPFileKind = "image"
	MCPFileKindVideo      MCPFileKind = "video"
	MCPFileKindAudio      MCPFileKind = "audio"
	MCPFileKindAttachment MCPFileKind = "attachment"
	MCPFileKindMesh       MCPFileKind = "mesh"
)

type MCPFileTransport string

const (
	MCPFileTransportBrowserUploadPage  MCPFileTransport = "browser_upload_page"
	MCPFileTransportPresignedMultipart MCPFileTransport = "presigned_multipart"
	MCPFileTransportRemoteHTTPS        MCPFileTransport = "remote_https"
)

type MCPFileTransferState string

const (
	MCPFileTransferInitiated  MCPFileTransferState = "initiated"
	MCPFileTransferUploading  MCPFileTransferState = "uploading"
	MCPFileTransferFinalizing MCPFileTransferState = "finalizing"
	MCPFileTransferReady      MCPFileTransferState = "ready"
)

// MCPFileBeginInput describes metadata or a remote HTTPS source only. File
// bytes, base64, data URIs, and chunked-base64 have no representable field.
type MCPFileBeginInput struct {
	Kind             MCPFileKind
	Transport        MCPFileTransport
	FileName         string
	MIMEType         string
	FileSize         int64
	FileLastModified *int64
	RemoteURL        string
}

type MCPFileSessionHandle struct {
	Transport MCPFileTransport
	Kind      MCPFileKind
	FileID    string
	UploadID  string
}

type MCPFileTransferSession struct {
	Handle              MCPFileSessionHandle
	State               MCPFileTransferState
	FileName            string
	MIMEType            string
	FileSize            int64
	TotalParts          int32
	ChunkSize           int32
	UploadedPartNumbers []int32
	LastActivityAt      *time.Time
}

type MCPFileTransferResult struct {
	State   MCPFileTransferState
	Session *MCPFileTransferSession
	File    *MCPVerifiedFileHandle
}

type MCPFileReferenceKind string

const (
	MCPFileReferenceInline      MCPFileReferenceKind = "inline"
	MCPFileReferenceDownload    MCPFileReferenceKind = "download"
	MCPFileReferenceAsset       MCPFileReferenceKind = "asset"
	MCPFileReferencePlayback    MCPFileReferenceKind = "playback"
	MCPFileReferenceThumbnail   MCPFileReferenceKind = "thumbnail"
	MCPFileReferenceSpectrogram MCPFileReferenceKind = "spectrogram"
	MCPFileReferenceWaveform    MCPFileReferenceKind = "waveform"
)

type MCPFileReference struct {
	Kind      MCPFileReferenceKind
	ID        string
	URL       string
	Extension string
	MIMEType  string
	FileSize  int64
	FileName  string
	ExpiresAt *time.Time
}

// MCPVerifiedFileHandle is returned only from FileService completion, remote
// import, or authorized delivery lookup. It contains bounded metadata and at
// most the seven existing delivery/derivative references; it never contains
// file bytes, base64, OCR, transcript, or a persisted extraction payload.
type MCPVerifiedFileHandle struct {
	ID                   string
	FileName             string
	Extension            string
	MIMEType             string
	FileSize             int64
	DurationSeconds      *int32
	DerivativeStatus     string
	DerivativePercentage *int32
	References           []MCPFileReference
}

func (facade *MCPFileFacade) Begin(
	ctx context.Context,
	input MCPFileBeginInput,
) (MCPFileTransferResult, error) {
	uploadType, err := mcpUploadType(input.Kind)
	if err != nil {
		return MCPFileTransferResult{}, err
	}
	switch input.Transport {
	case MCPFileTransportBrowserUploadPage, MCPFileTransportPresignedMultipart:
		return facade.beginMultipart(ctx, input, uploadType)
	case MCPFileTransportRemoteHTTPS:
		return facade.beginRemoteHTTPS(ctx, input, uploadType)
	default:
		return MCPFileTransferResult{}, invalidMCPFileInput("transport is unsupported")
	}
}

func (facade *MCPFileFacade) Status(
	ctx context.Context,
	handle MCPFileSessionHandle,
) (MCPFileTransferResult, error) {
	uploadType, err := validateMCPFileSessionHandle(handle)
	if err != nil {
		return MCPFileTransferResult{}, err
	}
	response, err := facade.files.FindMultipartUploadCandidate(ctx, connect.NewRequest(
		&managev1.FindMultipartUploadCandidateRequest{
			UploadType: uploadType,
			FileId:     pointer(handle.FileID),
			UploadId:   pointer(handle.UploadID),
		},
	))
	if err != nil {
		return MCPFileTransferResult{}, err
	}
	if response == nil || response.Msg == nil {
		return MCPFileTransferResult{}, invalidMCPFileRuntime("status response is missing")
	}
	if response.Msg.GetFileId() == "" && response.Msg.GetUploadId() == "" {
		file, err := facade.Read(ctx, handle.FileID)
		if err != nil {
			return MCPFileTransferResult{}, err
		}
		return MCPFileTransferResult{State: MCPFileTransferReady, File: &file}, nil
	}
	session, err := mcpFileSessionFromCandidate(handle, response.Msg)
	if err != nil {
		return MCPFileTransferResult{}, err
	}
	return MCPFileTransferResult{State: session.State, Session: &session}, nil
}

func (facade *MCPFileFacade) Complete(
	ctx context.Context,
	handle MCPFileSessionHandle,
) (MCPFileTransferResult, error) {
	if _, err := validateMCPFileSessionHandle(handle); err != nil {
		return MCPFileTransferResult{}, err
	}
	response, err := facade.files.CompleteMultipartUpload(ctx, connect.NewRequest(
		&managev1.CompleteMultipartUploadRequest{FileId: handle.FileID, UploadId: handle.UploadID},
	))
	if err != nil {
		return MCPFileTransferResult{}, err
	}
	if response == nil || response.Msg == nil || response.Msg.GetDelivery() == nil {
		return MCPFileTransferResult{}, invalidMCPFileRuntime("completion did not return verified File delivery")
	}
	if response.Msg.GetFileId() != handle.FileID {
		return MCPFileTransferResult{}, invalidMCPFileRuntime("completion File ID does not match the session handle")
	}
	file, err := mcpVerifiedFileFromDelivery(response.Msg.GetDelivery())
	if err != nil {
		return MCPFileTransferResult{}, err
	}
	return MCPFileTransferResult{State: MCPFileTransferReady, File: &file}, nil
}

func (facade *MCPFileFacade) Read(ctx context.Context, fileID string) (MCPVerifiedFileHandle, error) {
	fileID, err := normalizeMCPFileUUID(fileID, "file_id")
	if err != nil {
		return MCPVerifiedFileHandle{}, err
	}
	response, err := facade.files.GetMediaDelivery(ctx, connect.NewRequest(
		&managev1.GetMediaDeliveryRequest{FileId: fileID},
	))
	if err != nil {
		return MCPVerifiedFileHandle{}, err
	}
	if response == nil || response.Msg == nil || response.Msg.GetDelivery() == nil {
		return MCPVerifiedFileHandle{}, invalidMCPFileRuntime("File delivery response is missing")
	}
	if response.Msg.GetDelivery().GetFileId() != fileID {
		return MCPVerifiedFileHandle{}, invalidMCPFileRuntime("File delivery ID does not match the requested handle")
	}
	return mcpVerifiedFileFromDelivery(response.Msg.GetDelivery())
}

func (facade *MCPFileFacade) beginMultipart(
	ctx context.Context,
	input MCPFileBeginInput,
	uploadType managev1.UploadType,
) (MCPFileTransferResult, error) {
	if strings.TrimSpace(input.RemoteURL) != "" {
		return MCPFileTransferResult{}, invalidMCPFileInput("multipart transfer must not include remote_url")
	}
	fileName := strings.TrimSpace(input.FileName)
	mimeType := strings.TrimSpace(input.MIMEType)
	if fileName == "" || mimeType == "" || input.FileSize <= 0 {
		return MCPFileTransferResult{}, invalidMCPFileInput("multipart transfer requires file_name, mime_type, and positive file_size")
	}
	response, err := facade.files.InitiateMultipartUpload(ctx, connect.NewRequest(
		&managev1.InitiateMultipartUploadRequest{
			UploadType:       uploadType,
			FileSize:         input.FileSize,
			MimeType:         mimeType,
			FileName:         fileName,
			FileLastModified: input.FileLastModified,
		},
	))
	if err != nil {
		return MCPFileTransferResult{}, err
	}
	if response == nil || response.Msg == nil {
		return MCPFileTransferResult{}, invalidMCPFileRuntime("multipart initiation response is missing")
	}
	session, err := mcpFileSessionFromInitiation(input.Transport, input.Kind, response.Msg)
	if err != nil {
		return MCPFileTransferResult{}, err
	}
	return MCPFileTransferResult{State: session.State, Session: &session}, nil
}

func (facade *MCPFileFacade) beginRemoteHTTPS(
	ctx context.Context,
	input MCPFileBeginInput,
	uploadType managev1.UploadType,
) (MCPFileTransferResult, error) {
	if strings.TrimSpace(input.FileName) != "" || strings.TrimSpace(input.MIMEType) != "" ||
		input.FileSize != 0 || input.FileLastModified != nil {
		return MCPFileTransferResult{}, invalidMCPFileInput("remote HTTPS transfer accepts only kind and remote_url")
	}
	remoteURL, err := normalizeRemoteHTTPSURL(input.RemoteURL)
	if err != nil {
		return MCPFileTransferResult{}, err
	}
	response, err := facade.files.DownloadFromUrl(ctx, connect.NewRequest(
		&managev1.DownloadFromUrlRequest{UploadType: uploadType, Url: remoteURL},
	))
	if err != nil {
		return MCPFileTransferResult{}, err
	}
	if response == nil || response.Msg == nil || response.Msg.GetDelivery() == nil {
		return MCPFileTransferResult{}, invalidMCPFileRuntime("remote import did not return verified File delivery")
	}
	if response.Msg.GetFileId() == "" || response.Msg.GetDelivery().GetFileId() != response.Msg.GetFileId() {
		return MCPFileTransferResult{}, invalidMCPFileRuntime("remote import returned mismatched File IDs")
	}
	file, err := mcpVerifiedFileFromDelivery(response.Msg.GetDelivery())
	if err != nil {
		return MCPFileTransferResult{}, err
	}
	return MCPFileTransferResult{State: MCPFileTransferReady, File: &file}, nil
}

func validateMCPFileSessionHandle(handle MCPFileSessionHandle) (managev1.UploadType, error) {
	if handle.Transport != MCPFileTransportBrowserUploadPage &&
		handle.Transport != MCPFileTransportPresignedMultipart {
		return managev1.UploadType_UPLOAD_TYPE_UNSPECIFIED, invalidMCPFileInput("session transport must be browser_upload_page or presigned_multipart")
	}
	if _, err := normalizeMCPFileUUID(handle.FileID, "file_id"); err != nil {
		return managev1.UploadType_UPLOAD_TYPE_UNSPECIFIED, err
	}
	if strings.TrimSpace(handle.UploadID) == "" {
		return managev1.UploadType_UPLOAD_TYPE_UNSPECIFIED, invalidMCPFileInput("upload_id is required")
	}
	return mcpUploadType(handle.Kind)
}

func mcpUploadType(kind MCPFileKind) (managev1.UploadType, error) {
	switch kind {
	case MCPFileKindGeneral:
		return managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE, nil
	case MCPFileKindImage:
		return managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE, nil
	case MCPFileKindVideo:
		return managev1.UploadType_UPLOAD_TYPE_EDITOR_VIDEO, nil
	case MCPFileKindAudio:
		return managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO, nil
	case MCPFileKindAttachment:
		return managev1.UploadType_UPLOAD_TYPE_EDITOR_ATTACHMENT, nil
	case MCPFileKindMesh:
		return managev1.UploadType_UPLOAD_TYPE_EDITOR_MESH, nil
	default:
		return managev1.UploadType_UPLOAD_TYPE_UNSPECIFIED, invalidMCPFileInput("kind is unsupported")
	}
}

func mcpFileSessionFromInitiation(
	transport MCPFileTransport,
	kind MCPFileKind,
	response *managev1.InitiateMultipartUploadResponse,
) (MCPFileTransferSession, error) {
	fileID, err := normalizeMCPFileUUID(response.GetFileId(), "runtime file_id")
	if err != nil {
		return MCPFileTransferSession{}, invalidMCPFileRuntime(err.Error())
	}
	if strings.TrimSpace(response.GetUploadId()) == "" || response.GetTotalParts() <= 0 || response.GetChunkSize() <= 0 {
		return MCPFileTransferSession{}, invalidMCPFileRuntime("multipart initiation identity or part shape is invalid")
	}
	state, err := mcpTransferState(response.GetStatus())
	if err != nil {
		return MCPFileTransferSession{}, err
	}
	return MCPFileTransferSession{
		Handle: MCPFileSessionHandle{
			Transport: transport,
			Kind:      kind,
			FileID:    fileID,
			UploadID:  response.GetUploadId(),
		},
		State:               state,
		TotalParts:          response.GetTotalParts(),
		ChunkSize:           response.GetChunkSize(),
		UploadedPartNumbers: mcpUploadedPartNumbers(response.GetUploadedParts()),
	}, nil
}

func mcpFileSessionFromCandidate(
	handle MCPFileSessionHandle,
	response *managev1.FindMultipartUploadCandidateResponse,
) (MCPFileTransferSession, error) {
	if response.GetFileId() != handle.FileID || response.GetUploadId() != handle.UploadID {
		return MCPFileTransferSession{}, invalidMCPFileRuntime("status response does not match the session handle")
	}
	state, err := mcpTransferState(response.GetStatus())
	if err != nil {
		return MCPFileTransferSession{}, err
	}
	if response.GetTotalParts() <= 0 || response.GetChunkSize() <= 0 {
		return MCPFileTransferSession{}, invalidMCPFileRuntime("status part shape is invalid")
	}
	var lastActivityAt *time.Time
	if value := response.GetLastActivityAt(); value != nil {
		if err := value.CheckValid(); err != nil {
			return MCPFileTransferSession{}, invalidMCPFileRuntime("status last_activity_at is invalid")
		}
		converted := value.AsTime().UTC()
		lastActivityAt = &converted
	}
	return MCPFileTransferSession{
		Handle:              handle,
		State:               state,
		FileName:            response.GetFileName(),
		MIMEType:            response.GetMimeType(),
		FileSize:            response.GetFileSize(),
		TotalParts:          response.GetTotalParts(),
		ChunkSize:           response.GetChunkSize(),
		UploadedPartNumbers: mcpUploadedPartNumbers(response.GetUploadedParts()),
		LastActivityAt:      lastActivityAt,
	}, nil
}

func mcpTransferState(status managev1.UploadSessionStatus) (MCPFileTransferState, error) {
	switch status {
	case managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_INITIATED:
		return MCPFileTransferInitiated, nil
	case managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_UPLOADING:
		return MCPFileTransferUploading, nil
	case managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_FINALIZING:
		return MCPFileTransferFinalizing, nil
	default:
		return "", invalidMCPFileRuntime("upload session status is not active")
	}
}

func mcpUploadedPartNumbers(parts []*managev1.UploadPartInfo) []int32 {
	result := make([]int32, 0, len(parts))
	for _, part := range parts {
		if part != nil && part.GetPartNumber() > 0 {
			result = append(result, part.GetPartNumber())
		}
	}
	return result
}

func mcpVerifiedFileFromDelivery(delivery *commonv1.MediaDelivery) (MCPVerifiedFileHandle, error) {
	if delivery == nil {
		return MCPVerifiedFileHandle{}, invalidMCPFileRuntime("File delivery is missing")
	}
	fileID, err := normalizeMCPFileUUID(delivery.GetFileId(), "runtime file_id")
	if err != nil {
		return MCPVerifiedFileHandle{}, invalidMCPFileRuntime(err.Error())
	}
	extension := strings.TrimSpace(delivery.GetExtension())
	mimeType := strings.TrimSpace(delivery.GetMimeType())
	if extension == "" || mimeType == "" || delivery.GetFileSize() <= 0 {
		return MCPVerifiedFileHandle{}, invalidMCPFileRuntime("File delivery metadata is incomplete")
	}
	handle := MCPVerifiedFileHandle{
		ID:                   fileID,
		FileName:             delivery.GetFileName(),
		Extension:            extension,
		MIMEType:             mimeType,
		FileSize:             delivery.GetFileSize(),
		DurationSeconds:      delivery.DurationSeconds,
		DerivativeStatus:     mcpProcessingStatus(delivery.GetProcessingStatus()),
		DerivativePercentage: delivery.ProcessingPercentage,
		References:           make([]MCPFileReference, 0, 7),
	}
	for _, reference := range []struct {
		kind  MCPFileReferenceKind
		value *commonv1.ExpiringMediaRef
	}{
		{kind: MCPFileReferenceInline, value: delivery.GetInline()},
		{kind: MCPFileReferenceDownload, value: delivery.GetDownload()},
	} {
		if reference.value == nil {
			continue
		}
		converted, err := mcpExpiringFileReference(fileID, reference.kind, reference.value)
		if err != nil {
			return MCPVerifiedFileHandle{}, err
		}
		handle.References = append(handle.References, converted)
	}
	if value := delivery.GetAsset(); value != nil {
		converted, err := mcpAssetReference(MCPFileReferenceAsset, value)
		if err != nil {
			return MCPVerifiedFileHandle{}, err
		}
		handle.References = append(handle.References, converted)
	}
	if value := delivery.GetPlayback(); value != nil {
		converted, err := mcpPlaybackReference(fileID, value)
		if err != nil {
			return MCPVerifiedFileHandle{}, err
		}
		handle.References = append(handle.References, converted)
	}
	for _, reference := range []struct {
		kind  MCPFileReferenceKind
		value *commonv1.AssetRef
	}{
		{kind: MCPFileReferenceThumbnail, value: delivery.GetThumbnail()},
		{kind: MCPFileReferenceSpectrogram, value: delivery.GetSpectrogram()},
		{kind: MCPFileReferenceWaveform, value: delivery.GetWaveform()},
	} {
		if reference.value == nil {
			continue
		}
		converted, err := mcpAssetReference(reference.kind, reference.value)
		if err != nil {
			return MCPVerifiedFileHandle{}, err
		}
		handle.References = append(handle.References, converted)
	}
	return handle, nil
}

func mcpExpiringFileReference(
	fileID string,
	kind MCPFileReferenceKind,
	value *commonv1.ExpiringMediaRef,
) (MCPFileReference, error) {
	if value.GetFileId() != fileID {
		return MCPFileReference{}, invalidMCPFileRuntime("expiring reference File ID is invalid")
	}
	if err := validateMCPFileReferenceURL(value.GetUrl()); err != nil {
		return MCPFileReference{}, err
	}
	expiresAt := value.GetExpiresAt()
	if expiresAt == nil || expiresAt.CheckValid() != nil {
		return MCPFileReference{}, invalidMCPFileRuntime("expiring reference expiry is invalid")
	}
	convertedExpiry := expiresAt.AsTime().UTC()
	return MCPFileReference{
		Kind:      kind,
		ID:        fileID,
		URL:       value.GetUrl(),
		Extension: value.GetExtension(),
		MIMEType:  value.GetMimeType(),
		FileName:  value.GetFileName(),
		ExpiresAt: &convertedExpiry,
	}, nil
}

func mcpAssetReference(kind MCPFileReferenceKind, value *commonv1.AssetRef) (MCPFileReference, error) {
	if strings.TrimSpace(value.GetAssetId()) == "" {
		return MCPFileReference{}, invalidMCPFileRuntime("asset reference ID is missing")
	}
	if err := validateMCPFileReferenceURL(value.GetUrl()); err != nil {
		return MCPFileReference{}, err
	}
	return MCPFileReference{
		Kind:      kind,
		ID:        value.GetAssetId(),
		URL:       value.GetUrl(),
		Extension: value.GetExtension(),
		MIMEType:  value.GetMimeType(),
		FileSize:  value.GetFileSize(),
		FileName:  value.GetDownloadFilename(),
	}, nil
}

func mcpPlaybackReference(fileID string, value *commonv1.HlsMediaRef) (MCPFileReference, error) {
	if value.GetFileId() != fileID || strings.TrimSpace(value.GetGenerationId()) == "" {
		return MCPFileReference{}, invalidMCPFileRuntime("playback reference identity is invalid")
	}
	if err := validateMCPFileReferenceURL(value.GetUrl()); err != nil {
		return MCPFileReference{}, err
	}
	return MCPFileReference{Kind: MCPFileReferencePlayback, ID: value.GetGenerationId(), URL: value.GetUrl()}, nil
}

func validateMCPFileReferenceURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil {
		return invalidMCPFileRuntime("delivery reference URL is invalid")
	}
	return nil
}

func mcpProcessingStatus(status commonv1.MediaProcessingStatus) string {
	switch status {
	case commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_PROCESSING:
		return "processing"
	case commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_READY:
		return "ready"
	case commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_FAILED:
		return "failed"
	default:
		return ""
	}
}

func normalizeRemoteHTTPSURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return "", invalidMCPFileInput("remote_url must be an HTTPS URL without credentials or fragment")
	}
	return parsed.String(), nil
}

func normalizeMCPFileUUID(raw, field string) (string, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := uuid.Parse(raw)
	if err != nil || parsed.String() != raw {
		return "", invalidMCPFileInput(field + " must be a UUID")
	}
	return parsed.String(), nil
}

func invalidMCPFileInput(message string) error {
	return fmt.Errorf("%w: %s", ErrInvalidMCPFileInput, message)
}

func invalidMCPFileRuntime(message string) error {
	return fmt.Errorf("%w: %s", ErrInvalidMCPFileRuntime, message)
}

func pointer[T any](value T) *T { return &value }
