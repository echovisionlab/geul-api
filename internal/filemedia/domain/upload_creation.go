package domain

import (
	"context"
	"time"
)

type UploadCreationStore interface {
	RequireUploadTrack(context.Context, string) error
	InsertUploadSession(context.Context, UploadSession, UploadTargetMode) (UploadSession, error)
	TrackUploadSessions(context.Context, string) ([]UploadSession, error)
	AbortUploadSessions(context.Context, []string, time.Time) (int64, error)
	HasTrackUploadSessions(context.Context, string) (bool, error)
}
