package filemedia

import (
	"context"
	"errors"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// MemberSummaries projects uploader identities for File Manager responses.
type MemberSummaries interface {
	Load(context.Context, []string) (map[string]*commonv1.MemberSummary, error)
}

type TrackOriginalAudioInput = domain.TrackOriginalAudioInput

type TrackOriginalAudioAttachment = domain.TrackOriginalAudioAttachment

// ErrTrackUploadSessionsChanged is returned when the File-owned upload-session
// fence changes before a Track deletion transaction completes.
var ErrTrackUploadSessionsChanged = errors.New("track upload sessions changed during deletion")
