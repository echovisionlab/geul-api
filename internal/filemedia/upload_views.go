package filemedia

import "github.com/echovisionlab/geul-api/internal/filemedia/domain"

type UploadedPart = domain.UploadedPart
type MultipartUpload = domain.MultipartUpload
type MultipartCandidate = domain.MultipartCandidate

type UploadSessionStatus = domain.UploadSessionStatus

const (
	UploadSessionStatusInitiated  = domain.UploadSessionStatusInitiated
	UploadSessionStatusUploading  = domain.UploadSessionStatusUploading
	UploadSessionStatusFinalizing = domain.UploadSessionStatusFinalizing
	UploadSessionStatusFailed     = domain.UploadSessionStatusFailed
	UploadSessionStatusAborted    = domain.UploadSessionStatusAborted
)
