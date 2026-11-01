package filemedia

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mediaauth"

	filemediadomain "github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/google/uuid"

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
	ImportRemoteFile(context.Context, filemediadomain.RemoteFileImportInput) (*filemediadomain.ImportedFile, error)
	InitiateMultipartUpload(context.Context, filemediadomain.InitiateMultipartUploadCommand) (*filemediadomain.MultipartUpload, error)
	FindMultipartUploadCandidate(context.Context, filemediadomain.FindMultipartUploadCandidateQuery) (*filemediadomain.MultipartCandidate, error)
	CompleteMultipartUpload(context.Context, filemediadomain.CompleteMultipartUploadCommand) (*filemediadomain.CompletedUpload, error)
	DownloadFromURL(context.Context, filemediadomain.DownloadFromURLCommand) (*filemediadomain.ImportedFile, error)
	GetMediaDelivery(context.Context, filemediadomain.GetMediaDeliveryQuery) (*filemediadomain.MediaDeliveryResult, error)
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
	MCPFileKindTrackAudio MCPFileKind = "track_audio"
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
	Kind                  MCPFileKind
	Transport             MCPFileTransport
	FileName              string
	MIMEType              string
	FileSize              int64
	FileLastModified      *int64
	RemoteURL             string
	TrackID               string
	ExpectedCurrentFileID *string
	CorrelationID         string
}

// MCPFileUploadInput is the connector-resolved attachment descriptor. FileID
// is opaque ChatGPT metadata, never a DSUB File UUID or an ingest authority.
type MCPFileUploadInput struct {
	DownloadURL string `json:"download_url"`
	FileID      string `json:"file_id"`
	MIMEType    string `json:"mime_type,omitempty"`
	FileName    string `json:"file_name,omitempty"`
}

func (facade *MCPFileFacade) Upload(ctx context.Context, file MCPFileUploadInput, kind MCPFileKind, correlationID string) (MCPVerifiedFileHandle, error) {
	if strings.TrimSpace(file.FileID) == "" {
		return MCPVerifiedFileHandle{}, invalidMCPFileInput("file.file_id is required as an opaque ChatGPT attachment identifier")
	}
	if kind == MCPFileKindTrackAudio {
		return MCPVerifiedFileHandle{}, invalidMCPFileInput("standalone attachment upload cannot attach Track audio; use file_transfer")
	}
	uploadType, err := mcpUploadType(kind)
	if err != nil {
		return MCPVerifiedFileHandle{}, err
	}
	correlationID, err = normalizeMCPFileUUID(correlationID, "correlation_id")
	if err != nil {
		return MCPVerifiedFileHandle{}, err
	}
	remoteURL, err := normalizeRemoteHTTPSURL(file.DownloadURL)
	if err != nil {
		return MCPVerifiedFileHandle{}, err
	}
	// MIMEType is intentionally not forwarded: FileService sniffs verified bytes.
	response, err := facade.files.ImportRemoteFile(ctx, filemediadomain.RemoteFileImportInput{
		UploadType: uploadType, SourceURL: remoteURL, FileName: file.FileName, CorrelationID: correlationID,
	})
	if err != nil {
		return MCPVerifiedFileHandle{}, err
	}
	if response == nil || response.Delivery == nil || response.FileID == "" || response.Delivery.FileID != response.FileID {
		return MCPVerifiedFileHandle{}, invalidMCPFileRuntime("attachment import did not return matching verified File delivery")
	}
	return mcpVerifiedFileFromDelivery(response.Delivery)
}

type MCPFileSessionHandle struct {
	Transport             MCPFileTransport
	Kind                  MCPFileKind
	FileID                string
	UploadID              string
	TrackID               string
	ExpectedCurrentFileID *string
	// ClientMediaBundleID identifies browser-prepared derivatives at completion.
	// It is metadata alongside the independent File or scoped Track identity.
	ClientMediaBundleID string
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
	input.TrackID = strings.TrimSpace(input.TrackID)
	if err := validateMCPFileTrackTarget(input.Kind, input.TrackID, input.ExpectedCurrentFileID); err != nil {
		return MCPFileTransferResult{}, err
	}
	if input.ExpectedCurrentFileID != nil {
		input.ExpectedCurrentFileID = pointer(strings.TrimSpace(*input.ExpectedCurrentFileID))
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
	response, err := facade.files.FindMultipartUploadCandidate(ctx, filemediadomain.FindMultipartUploadCandidateQuery{
		UploadType:            uploadType,
		FileID:                pointer(handle.FileID),
		UploadID:              pointer(handle.UploadID),
		EntityID:              handle.TrackID,
		EntityType:            mcpFileEntityType(handle.Kind),
		ExpectedCurrentFileID: handle.ExpectedCurrentFileID,
	})
	if err != nil {
		return MCPFileTransferResult{}, err
	}
	if response == nil {
		return MCPFileTransferResult{}, invalidMCPFileRuntime("status response is missing")
	}
	if fileReferenceName(response.FileID) == "" && fileReferenceName(response.UploadID) == "" {
		file, err := facade.Read(ctx, handle.FileID)
		if err != nil {
			return MCPFileTransferResult{}, err
		}
		return MCPFileTransferResult{State: MCPFileTransferReady, File: &file}, nil
	}
	session, err := mcpFileSessionFromCandidate(handle, response)
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
	response, err := facade.files.CompleteMultipartUpload(ctx, filemediadomain.CompleteMultipartUploadCommand{
		FileID: handle.FileID, UploadID: handle.UploadID, ClientMediaBundleID: handle.ClientMediaBundleID,
	})
	if err != nil {
		return MCPFileTransferResult{}, err
	}
	if response == nil || response.Delivery == nil {
		return MCPFileTransferResult{}, invalidMCPFileRuntime("completion did not return verified File delivery")
	}
	if response.FileID != handle.FileID {
		return MCPFileTransferResult{}, invalidMCPFileRuntime("completion File ID does not match the session handle")
	}
	file, err := mcpVerifiedFileFromDelivery(response.Delivery)
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
	response, err := facade.files.GetMediaDelivery(ctx, filemediadomain.GetMediaDeliveryQuery{FileID: fileID})
	if err != nil {
		return MCPVerifiedFileHandle{}, err
	}
	if response == nil || response.Delivery == nil {
		return MCPVerifiedFileHandle{}, invalidMCPFileRuntime("File delivery response is missing")
	}
	if response.Delivery.FileID != fileID {
		return MCPVerifiedFileHandle{}, invalidMCPFileRuntime("File delivery ID does not match the requested handle")
	}
	return mcpVerifiedFileFromDelivery(response.Delivery)
}

func (facade *MCPFileFacade) beginMultipart(
	ctx context.Context,
	input MCPFileBeginInput,
	uploadType managev1.UploadType,
) (MCPFileTransferResult, error) {
	if strings.TrimSpace(input.RemoteURL) != "" {
		return MCPFileTransferResult{}, invalidMCPFileInput("multipart transfer must not include remote_url")
	}
	if input.CorrelationID != "" {
		return MCPFileTransferResult{}, invalidMCPFileInput("multipart transfer must not include correlation_id")
	}
	fileName := strings.TrimSpace(input.FileName)
	mimeType := strings.TrimSpace(input.MIMEType)
	if fileName == "" || mimeType == "" || input.FileSize <= 0 {
		return MCPFileTransferResult{}, invalidMCPFileInput("multipart transfer requires file_name, mime_type, and positive file_size")
	}
	response, err := facade.files.InitiateMultipartUpload(ctx, filemediadomain.InitiateMultipartUploadCommand{
		UploadType:            uploadType,
		FileSize:              input.FileSize,
		MimeType:              mimeType,
		FileName:              fileName,
		FileLastModified:      input.FileLastModified,
		EntityID:              input.TrackID,
		EntityType:            mcpFileEntityType(input.Kind),
		ExpectedCurrentFileID: input.ExpectedCurrentFileID,
	})
	if err != nil {
		return MCPFileTransferResult{}, err
	}
	if response == nil {
		return MCPFileTransferResult{}, invalidMCPFileRuntime("multipart initiation response is missing")
	}
	session, err := mcpFileSessionFromInitiation(input.Transport, input.Kind, response)
	if err != nil {
		return MCPFileTransferResult{}, err
	}
	session.Handle.TrackID = input.TrackID
	session.Handle.ExpectedCurrentFileID = input.ExpectedCurrentFileID
	return MCPFileTransferResult{State: session.State, Session: &session}, nil
}

func (facade *MCPFileFacade) beginRemoteHTTPS(
	ctx context.Context,
	input MCPFileBeginInput,
	uploadType managev1.UploadType,
) (MCPFileTransferResult, error) {
	if strings.TrimSpace(input.FileName) != "" || strings.TrimSpace(input.MIMEType) != "" ||
		input.FileSize != 0 || input.FileLastModified != nil {
		return MCPFileTransferResult{}, invalidMCPFileInput("remote HTTPS transfer must omit multipart File metadata")
	}
	var correlationID string
	if input.CorrelationID != "" {
		value, err := normalizeMCPFileUUID(input.CorrelationID, "correlation_id")
		if err != nil {
			return MCPFileTransferResult{}, err
		}
		correlationID = value
	} else if input.Kind != MCPFileKindGeneral {
		return MCPFileTransferResult{}, invalidMCPFileInput("correlation_id is required for durable remote File import; reuse it on retry")
	}
	remoteURL, err := normalizeRemoteHTTPSURL(input.RemoteURL)
	if err != nil {
		return MCPFileTransferResult{}, err
	}
	response, err := facade.files.DownloadFromURL(ctx, filemediadomain.DownloadFromURLCommand{
		UploadType: uploadType, URL: remoteURL, EntityID: input.TrackID,
		EntityType: mcpFileEntityType(input.Kind), ExpectedCurrentFileID: input.ExpectedCurrentFileID,
		CorrelationID: correlationID,
	})
	if err != nil {
		return MCPFileTransferResult{}, err
	}
	if response == nil || response.Delivery == nil {
		return MCPFileTransferResult{}, invalidMCPFileRuntime("remote import did not return verified File delivery")
	}
	if response.FileID == "" || response.Delivery.FileID != response.FileID {
		return MCPFileTransferResult{}, invalidMCPFileRuntime("remote import returned mismatched File IDs")
	}
	file, err := mcpVerifiedFileFromDelivery(response.Delivery)
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
	if err := validateMCPFileTrackTarget(handle.Kind, handle.TrackID, handle.ExpectedCurrentFileID); err != nil {
		return managev1.UploadType_UPLOAD_TYPE_UNSPECIFIED, err
	}
	return mcpUploadType(handle.Kind)
}

func validateMCPFileTrackTarget(kind MCPFileKind, trackID string, expectedCurrentFileID *string) error {
	if kind != MCPFileKindTrackAudio {
		if trackID != "" || expectedCurrentFileID != nil {
			return invalidMCPFileInput("track_id and expected_current_file_id are supported only for track_audio")
		}
		return nil
	}
	if _, err := normalizeMCPFileUUID(trackID, "track_id"); err != nil {
		return err
	}
	if expectedCurrentFileID != nil {
		if _, err := normalizeMCPFileUUID(*expectedCurrentFileID, "expected_current_file_id"); err != nil {
			return err
		}
	}
	return nil
}

func mcpFileEntityType(kind MCPFileKind) *managev1.TranscodeEntityType {
	if kind == MCPFileKindTrackAudio {
		return pointer(managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK)
	}
	return nil
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
	case MCPFileKindTrackAudio:
		return managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO, nil
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
	response *filemediadomain.MultipartUpload,
) (MCPFileTransferSession, error) {
	fileID, err := normalizeMCPFileUUID(response.FileID, "runtime file_id")
	if err != nil {
		return MCPFileTransferSession{}, invalidMCPFileRuntime(err.Error())
	}
	if strings.TrimSpace(response.UploadID) == "" || response.TotalParts <= 0 || response.ChunkSize <= 0 {
		return MCPFileTransferSession{}, invalidMCPFileRuntime("multipart initiation identity or part shape is invalid")
	}
	state, err := mcpTransferState(response.Status)
	if err != nil {
		return MCPFileTransferSession{}, err
	}
	return MCPFileTransferSession{
		Handle: MCPFileSessionHandle{
			Transport: transport,
			Kind:      kind,
			FileID:    fileID,
			UploadID:  response.UploadID,
		},
		State:               state,
		TotalParts:          response.TotalParts,
		ChunkSize:           response.ChunkSize,
		UploadedPartNumbers: mcpUploadedPartNumbers(response.UploadedParts),
	}, nil
}

func mcpFileSessionFromCandidate(
	handle MCPFileSessionHandle,
	response *filemediadomain.MultipartCandidate,
) (MCPFileTransferSession, error) {
	if fileReferenceName(response.FileID) != handle.FileID || fileReferenceName(response.UploadID) != handle.UploadID {
		return MCPFileTransferSession{}, invalidMCPFileRuntime("status response does not match the session handle")
	}
	handle.ClientMediaBundleID = fileReferenceName(response.ClientMediaBundleID)
	state, err := mcpTransferState(response.Status)
	if err != nil {
		return MCPFileTransferSession{}, err
	}
	if response.TotalParts <= 0 || response.ChunkSize <= 0 {
		return MCPFileTransferSession{}, invalidMCPFileRuntime("status part shape is invalid")
	}
	var lastActivityAt *time.Time
	if value := response.LastActivityAt; value != nil {
		if value.UTC().Year() < 1 || value.UTC().Year() > 9999 {
			return MCPFileTransferSession{}, invalidMCPFileRuntime("status last_activity_at is invalid")
		}
		converted := value.UTC()
		lastActivityAt = &converted
	}
	return MCPFileTransferSession{
		Handle:              handle,
		State:               state,
		FileName:            fileReferenceName(response.FileName),
		MIMEType:            fileReferenceName(response.MimeType),
		FileSize:            response.FileSize,
		TotalParts:          response.TotalParts,
		ChunkSize:           response.ChunkSize,
		UploadedPartNumbers: mcpUploadedPartNumbers(response.UploadedParts),
		LastActivityAt:      lastActivityAt,
	}, nil
}

func mcpTransferState(status filemediadomain.UploadSessionStatus) (MCPFileTransferState, error) {
	switch status {
	case filemediadomain.UploadSessionStatusInitiated:
		return MCPFileTransferInitiated, nil
	case filemediadomain.UploadSessionStatusUploading:
		return MCPFileTransferUploading, nil
	case filemediadomain.UploadSessionStatusFinalizing:
		return MCPFileTransferFinalizing, nil
	default:
		return "", invalidMCPFileRuntime("upload session status is not active")
	}
}

func mcpUploadedPartNumbers(parts []*filemediadomain.UploadedPart) []int32 {
	result := make([]int32, 0, len(parts))
	for _, part := range parts {
		if part != nil && part.PartNumber > 0 {
			result = append(result, part.PartNumber)
		}
	}
	return result
}

func mcpVerifiedFileFromDelivery(delivery *filemediadomain.Delivery) (MCPVerifiedFileHandle, error) {
	if delivery == nil {
		return MCPVerifiedFileHandle{}, invalidMCPFileRuntime("File delivery is missing")
	}
	fileID, err := normalizeMCPFileUUID(delivery.FileID, "runtime file_id")
	if err != nil {
		return MCPVerifiedFileHandle{}, invalidMCPFileRuntime(err.Error())
	}
	extension := strings.TrimSpace(delivery.Extension)
	mimeType := strings.TrimSpace(delivery.MimeType)
	if extension == "" || mimeType == "" || delivery.FileSize <= 0 {
		return MCPVerifiedFileHandle{}, invalidMCPFileRuntime("File delivery metadata is incomplete")
	}
	handle := MCPVerifiedFileHandle{
		ID:                   fileID,
		FileName:             fileReferenceName(delivery.FileName),
		Extension:            extension,
		MIMEType:             mimeType,
		FileSize:             delivery.FileSize,
		DurationSeconds:      delivery.DurationSeconds,
		DerivativeStatus:     mcpProcessingStatus(delivery.ProcessingStatus),
		DerivativePercentage: delivery.ProcessingPercentage,
		References:           make([]MCPFileReference, 0, 7),
	}
	for _, reference := range []struct {
		kind  MCPFileReferenceKind
		value *mediaauth.Reference
	}{
		{kind: MCPFileReferenceInline, value: delivery.Inline},
		{kind: MCPFileReferenceDownload, value: delivery.Download},
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
	if value := delivery.Asset; value != nil {
		converted, err := mcpAssetReference(MCPFileReferenceAsset, value)
		if err != nil {
			return MCPVerifiedFileHandle{}, err
		}
		handle.References = append(handle.References, converted)
	}
	if value := delivery.Playback; value != nil {
		converted, err := mcpPlaybackReference(fileID, value)
		if err != nil {
			return MCPVerifiedFileHandle{}, err
		}
		handle.References = append(handle.References, converted)
	}
	for _, reference := range []struct {
		kind  MCPFileReferenceKind
		value *mediaasset.Reference
	}{
		{kind: MCPFileReferenceThumbnail, value: delivery.Thumbnail},
		{kind: MCPFileReferenceSpectrogram, value: delivery.Spectrogram},
		{kind: MCPFileReferenceWaveform, value: delivery.Waveform},
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
	value *mediaauth.Reference,
) (MCPFileReference, error) {
	if value.FileID != fileID {
		return MCPFileReference{}, invalidMCPFileRuntime("expiring reference File ID is invalid")
	}
	if err := validateMCPFileReferenceURL(value.URL); err != nil {
		return MCPFileReference{}, err
	}
	expiresAt := value.ExpiresAt
	if expiresAt.IsZero() || expiresAt.UTC().Year() < 1 || expiresAt.UTC().Year() > 9999 {
		return MCPFileReference{}, invalidMCPFileRuntime("expiring reference expiry is invalid")
	}
	convertedExpiry := expiresAt.UTC()
	return MCPFileReference{
		Kind:      kind,
		ID:        fileID,
		URL:       value.URL,
		Extension: value.Extension,
		MIMEType:  value.MimeType,
		FileName:  fileReferenceName(value.FileName),
		ExpiresAt: &convertedExpiry,
	}, nil
}

func mcpAssetReference(kind MCPFileReferenceKind, value *mediaasset.Reference) (MCPFileReference, error) {
	if strings.TrimSpace(value.AssetID) == "" {
		return MCPFileReference{}, invalidMCPFileRuntime("asset reference ID is missing")
	}
	if err := validateMCPFileReferenceURL(value.URL); err != nil {
		return MCPFileReference{}, err
	}
	return MCPFileReference{
		Kind:      kind,
		ID:        value.AssetID,
		URL:       value.URL,
		Extension: value.Extension,
		MIMEType:  value.MimeType,
		FileSize:  value.FileSize,
		FileName:  fileReferenceName(value.DownloadFilename),
	}, nil
}

func mcpPlaybackReference(fileID string, value *filemediadomain.HLSReference) (MCPFileReference, error) {
	if value.FileID != fileID || strings.TrimSpace(value.GenerationID) == "" {
		return MCPFileReference{}, invalidMCPFileRuntime("playback reference identity is invalid")
	}
	if err := validateMCPFileReferenceURL(value.URL); err != nil {
		return MCPFileReference{}, err
	}
	return MCPFileReference{Kind: MCPFileReferencePlayback, ID: value.GenerationID, URL: value.URL}, nil
}

func validateMCPFileReferenceURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil {
		return invalidMCPFileRuntime("delivery reference URL is invalid")
	}
	return nil
}

func mcpProcessingStatus(status filemediadomain.ProcessingStatus) string {
	switch status {
	case filemediadomain.ProcessingActive:
		return "processing"
	case filemediadomain.ProcessingReady:
		return "ready"
	case filemediadomain.ProcessingFailed:
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

func fileReferenceName(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
