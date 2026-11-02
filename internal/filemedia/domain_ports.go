package filemedia

import (
	"context"
	"errors"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
)

type MemberSummary = domain.MemberSummary

// MemberSummaries projects uploader identities for File Manager responses.
type MemberSummaries interface {
	Load(context.Context, []string) (map[string]MemberSummary, error)
}

type TrackOriginalAudioInput = domain.TrackOriginalAudioInput

type TrackOriginalAudioAttachment = domain.TrackOriginalAudioAttachment

// ErrTrackUploadSessionsChanged is returned when the File-owned upload-session
// fence changes before a Track deletion transaction completes.
var ErrTrackUploadSessionsChanged = errors.New("track upload sessions changed during deletion")

func memberSummaryForID(summaries map[string]MemberSummary, id string) *MemberSummary {
	summary, ok := summaries[id]
	if !ok {
		return nil
	}
	return &summary
}
