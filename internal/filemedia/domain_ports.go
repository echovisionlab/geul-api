package filemedia

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// PostAccess is the FileMedia-owned authorization boundary for Post targets.
// The adapter owns the transaction for standalone read/preflight operations;
// callers that mutate File state must pass their authoritative transaction to
// the locked variants so Post lifecycle and authorization cannot drift before
// the File mutation commits.
type PostAccess interface {
	RequireView(context.Context, string) error
	RequireEdit(context.Context, string) error
	RequireLockedView(context.Context, *gorm.DB, string) error
	RequireLockedEdit(context.Context, *gorm.DB, string) error
}

// PagePolicyAccess owns Page root and active-principal authorization for File
// Block relation policies.
type PagePolicyAccess interface {
	RequireLockedView(context.Context, *gorm.DB, string) error
	RequireLockedEdit(context.Context, *gorm.DB, string) error
}

// WorkAttachment validates a Work target before FileMedia stores a reference.
type WorkAttachment interface {
	RequireExists(context.Context, string) error
}

// WorkPolicyAccess owns Work lifecycle-aware policy authorization.
type WorkPolicyAccess interface {
	RequireLockedView(context.Context, *gorm.DB, string) error
	RequireLockedEdit(context.Context, *gorm.DB, string) error
}

// ProgramEventAttachment owns the exact lifecycle-aware action check for a
// Program Event target. The action boundary also owns private existence
// masking, so FileMedia must not perform a separate existence preflight.
type ProgramEventAttachment interface {
	RequireView(context.Context, *auth.SpiceDBClient, string) error
	RequireEdit(context.Context, *auth.SpiceDBClient, string) error
	RequireLockedView(context.Context, *gorm.DB, *auth.SpiceDBClient, string) error
	RequireLockedEdit(context.Context, *gorm.DB, *auth.SpiceDBClient, string) error
}

// AudienceAccess validates and projects Audience-owned Segment references used
// by File download policy.
type AudienceAccess interface {
	ValidateAuthenticatedSegmentIDs(context.Context, *gorm.DB, []string) error
	AuthenticatedSegmentSummary(*model.AudienceSegment) (*managev1.AudienceSegmentSummary, bool)
}

// MemberSummaries projects uploader identities for File Manager responses.
type MemberSummaries interface {
	Load(context.Context, []string) (map[string]*commonv1.MemberSummary, error)
}

type TrackOriginalAudioInput struct {
	TrackID               string
	VerifiedFileID        string
	ExpectedCurrentFileID *string
}

type TrackOriginalAudioAttachment struct {
	AlreadyApplied bool
	CurrentFileID  string
	ReleaseID      string
}

// TrackAttachment is the FileMedia-owned Release boundary used to serialize
// Track upload sessions and attach a verified original audio File.
type TrackAttachment interface {
	LockExistsWithDB(context.Context, *gorm.DB, string) error
	AttachOriginalWithDB(context.Context, *gorm.DB, TrackOriginalAudioInput) (TrackOriginalAudioAttachment, error)
}

// ReleasePolicyAccess owns Release root and active-principal authorization for
// Track relation policies.
type ReleasePolicyAccess interface {
	RequireLockedView(context.Context, *gorm.DB, string) error
	RequireLockedEdit(context.Context, *gorm.DB, string) error
}

// ErrTrackUploadSessionsChanged is returned when the File-owned upload-session
// fence changes before a Track deletion transaction completes.
var ErrTrackUploadSessionsChanged = errors.New("track upload sessions changed during deletion")

func requirePostAccess(access PostAccess) (PostAccess, error) {
	if access == nil {
		return nil, errs.DependencyUnavailable("Post access")
	}
	return access, nil
}

func requirePagePolicyAccess(access PagePolicyAccess) (PagePolicyAccess, error) {
	if access == nil {
		return nil, errs.DependencyUnavailable("Page policy access")
	}
	return access, nil
}

func requireWorkAttachment(attachment WorkAttachment) (WorkAttachment, error) {
	if attachment == nil {
		return nil, errs.DependencyUnavailable("Work attachment")
	}
	return attachment, nil
}

func requireWorkPolicyAccess(access WorkPolicyAccess) (WorkPolicyAccess, error) {
	if access == nil {
		return nil, errs.DependencyUnavailable("Work policy access")
	}
	return access, nil
}

func requireProgramEventAttachment(attachment ProgramEventAttachment) (ProgramEventAttachment, error) {
	if attachment == nil {
		return nil, errs.DependencyUnavailable("Program Event attachment")
	}
	return attachment, nil
}

func requireTrackAttachment(attachment TrackAttachment) (TrackAttachment, error) {
	if attachment == nil {
		return nil, errs.DependencyUnavailable("Track attachment")
	}
	return attachment, nil
}

func requireReleasePolicyAccess(access ReleasePolicyAccess) (ReleasePolicyAccess, error) {
	if access == nil {
		return nil, errs.DependencyUnavailable("Release policy access")
	}
	return access, nil
}

func requireAudienceAccess(access AudienceAccess) (AudienceAccess, error) {
	if access == nil {
		return nil, errs.DependencyUnavailable("Audience access")
	}
	return access, nil
}

func requireMemberSummaries(summaries MemberSummaries) (MemberSummaries, error) {
	if summaries == nil {
		return nil, errs.DependencyUnavailable("Member summaries")
	}
	return summaries, nil
}
