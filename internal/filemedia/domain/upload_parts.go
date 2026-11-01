package domain

import (
	"context"
	"time"
)

type UploadMIMEState struct {
	Status       UploadSessionStatus
	DetectedMime *string
}
type UploadPartStore interface {
	WritableUploadSession(context.Context, string, string) (UploadSession, error)
	MarkUploadPartUploading(context.Context, string, string, time.Time) (bool, error)
	UpsertUploadPart(context.Context, UploadPart) error
	DeleteUploadPart(context.Context, string, int32) error
	ReplaceUploadParts(context.Context, string, []UploadPart) error
	DetectUploadMIME(context.Context, string, string, string, time.Time) (bool, error)
	UploadMIMEState(context.Context, string, string) (UploadMIMEState, error)
}
