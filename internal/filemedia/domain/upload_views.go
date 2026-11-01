package domain

import "time"

type UploadedPart struct {
	PartNumber int32
	ETag       string
}

type MultipartUpload struct {
	UploadID, FileID, Extension                    string
	TotalParts, ChunkSize                          int32
	UploadedParts                                  []*UploadedPart
	Status                                         UploadSessionStatus
	Resumed                                        bool
	SlotID, IngestAttemptID, ExpectedCurrentFileID *string
}

type MultipartCandidate struct {
	ClientMediaBundleID, UploadID, FileID, Extension *string
	TotalParts, ChunkSize                            int32
	Status                                           UploadSessionStatus
	FileName                                         *string
	FileSize                                         int64
	MimeType                                         *string
	FileLastModified                                 *int64
	LastActivityAt                                   *time.Time
	SlotID, IngestAttemptID, ExpectedCurrentFileID   *string
	UploadedParts                                    []*UploadedPart
}
