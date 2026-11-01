package applicationpostgres

import (
	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/model"
)

func file(row model.File) filemedia.File {
	return filemedia.File{
		ClientMediaBundleID: row.ClientMediaBundleID,
		ID:                  row.ID,
		FileName:            row.FileName,
		MimeType:            row.MimeType,
		FileSize:            row.FileSize,
		Extension:           row.Extension,
		SHA256:              row.SHA256,
		DurationSeconds:     row.DurationSeconds,
		IngestSlotID:        row.IngestSlotID,
		IngestAttemptID:     row.IngestAttemptID,
		FolderID:            row.FolderID,
		UploadedByMemberID:  row.UploadedByMemberID,
		DeleteRequestedAt:   row.DeleteRequestedAt,
		CreatedAt:           row.CreatedAt,
		UpdatedAt:           row.UpdatedAt,
	}
}
