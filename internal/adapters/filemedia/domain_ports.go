package filemedia

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	audiencedomain "github.com/echovisionlab/geul-api/internal/audience"
	audiencepostgres "github.com/echovisionlab/geul-api/internal/audience/postgres"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	filemediadomain "github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	memberdomain "github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/model"
	postpostgres "github.com/echovisionlab/geul-api/internal/post/postgres"
	"github.com/echovisionlab/geul-api/internal/programevent"
	programeventpostgres "github.com/echovisionlab/geul-api/internal/programevent/postgres"
	releasedomain "github.com/echovisionlab/geul-api/internal/release"
	releasepostgres "github.com/echovisionlab/geul-api/internal/release/postgres"
	workdomain "github.com/echovisionlab/geul-api/internal/work"
	workpostgres "github.com/echovisionlab/geul-api/internal/work/postgres"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type PostAccess struct {
	db      *gorm.DB
	spiceDB *auth.SpiceDBClient
}

func NewPostAccess(db *gorm.DB, spiceDB *auth.SpiceDBClient) *PostAccess {
	return &PostAccess{db: db, spiceDB: spiceDB}
}

func (a *PostAccess) RequireView(ctx context.Context, postID string) error {
	if a == nil || a.db == nil {
		return errs.DependencyUnavailable("Post access")
	}
	return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return a.RequireLockedView(ctx, tx, postID)
	})
}

func (a *PostAccess) RequireEdit(ctx context.Context, postID string) error {
	if a == nil || a.db == nil {
		return errs.DependencyUnavailable("Post access")
	}
	return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return a.RequireLockedEdit(ctx, tx, postID)
	})
}

func (a *PostAccess) RequireLockedView(ctx context.Context, tx *gorm.DB, postID string) error {
	if a == nil || tx == nil || a.spiceDB == nil {
		return errs.DependencyUnavailable("Post access")
	}
	return maskPrivatePostAccess(postpostgres.RequireLockedView(ctx, tx, a.spiceDB, postID), postID)
}

func (a *PostAccess) RequireLockedEdit(ctx context.Context, tx *gorm.DB, postID string) error {
	if a == nil || tx == nil || a.spiceDB == nil {
		return errs.DependencyUnavailable("Post access")
	}
	return maskPrivatePostAccess(postpostgres.RequireLockedSourceLocaleEdit(ctx, tx, a.spiceDB, postID), postID)
}

func maskPrivatePostAccess(err error, postID string) error {
	return maskPrivateDomainAccess(err, "post", postID)
}

func maskPrivateDomainAccess(err error, domain, resourceID string) error {
	if err != nil {
		switch errs.CodeOf(err) {
		case connect.CodeUnauthenticated, connect.CodePermissionDenied:
			return errs.WithCause(errs.NotFound(domain, resourceID), err)
		}
	}
	return err
}

type WorkAttachment struct{ db *gorm.DB }

func NewWorkAttachment(db *gorm.DB) *WorkAttachment { return &WorkAttachment{db: db} }

func (a *WorkAttachment) RequireExists(ctx context.Context, workID string) error {
	return workdomain.RequireExists(ctx, workpostgres.Bind(a.db, workpostgres.Ports{}, nil).Store, workID)
}

type ProgramEventAttachment struct{ db *gorm.DB }

func NewProgramEventAttachment(db *gorm.DB) *ProgramEventAttachment {
	return &ProgramEventAttachment{db: db}
}

func (a *ProgramEventAttachment) RequireView(
	ctx context.Context,
	spiceDB *auth.SpiceDBClient,
	eventID string,
) error {
	if a == nil || a.db == nil {
		return errs.DependencyUnavailable("Program Event access")
	}
	return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return a.RequireLockedView(ctx, tx, spiceDB, eventID)
	})
}

func (a *ProgramEventAttachment) RequireEdit(
	ctx context.Context,
	spiceDB *auth.SpiceDBClient,
	eventID string,
) error {
	if a == nil || a.db == nil {
		return errs.DependencyUnavailable("Program Event access")
	}
	return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return a.RequireLockedEdit(ctx, tx, spiceDB, eventID)
	})
}

func (a *ProgramEventAttachment) RequireLockedView(
	ctx context.Context,
	tx *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	eventID string,
) error {
	if a == nil || tx == nil || spiceDB == nil {
		return errs.DependencyUnavailable("Program Event access")
	}
	return maskPrivateDomainAccess(
		programEventAccessAnswer(programevent.RequireLockedView(ctx, programEventStore(tx), spiceDB, programEventLockActive(ctx, tx), eventID)),
		"program event",
		eventID,
	)
}

func (a *ProgramEventAttachment) RequireLockedEdit(
	ctx context.Context,
	tx *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	eventID string,
) error {
	if a == nil || tx == nil || spiceDB == nil {
		return errs.DependencyUnavailable("Program Event access")
	}
	return maskPrivateDomainAccess(
		programEventAccessAnswer(programevent.RequireLockedSourceLocaleEdit(ctx, programEventStore(tx), spiceDB, programEventLockActive(ctx, tx), eventID)),
		"program event",
		eventID,
	)
}

// programEventStore binds the Program Event rows to the attachment
// transaction.
func programEventStore(tx *gorm.DB) programevent.Store {
	return programeventpostgres.Bind(tx, programeventpostgres.Ports{}, nil).Store
}

// programEventLockActive is the identity fence of the attachment
// transaction.
func programEventLockActive(ctx context.Context, tx *gorm.DB) programevent.LockActive {
	return func(principal *auth.UserInfo) (bool, error) {
		return identitystate.LockActivePrincipal(ctx, tx, principal)
	}
}

// programEventAccessAnswer gives a Program Event access rejection the
// Connect form maskPrivateDomainAccess classifies by code; other errors pass
// unchanged.
func programEventAccessAnswer(err error) error {
	if _, rejected := errors.AsType[*domainerr.Error](err); rejected {
		return errs.Wrap(err)
	}
	return err
}

type AudienceAccess struct{}

func NewAudienceAccess() *AudienceAccess { return &AudienceAccess{} }

func (*AudienceAccess) ValidateAuthenticatedSegmentIDs(
	ctx context.Context,
	tx *gorm.DB,
	segmentIDs []string,
) error {
	// Audience answers with use-case errors; File keeps answering with
	// Connect errors until it migrates.
	if err := audiencepostgres.ValidateAuthenticatedAccessSegmentIDs(ctx, tx, segmentIDs); err != nil {
		return errs.Wrap(err)
	}
	return nil
}

func (*AudienceAccess) AuthenticatedSegmentSummary(
	segment *model.AudienceSegment,
) (*managev1.AudienceSegmentSummary, bool) {
	if segment == nil {
		return nil, false
	}
	return audiencedomain.AuthenticatedAccessSegmentSummary(audiencepostgres.SegmentOf(*segment))
}

type MemberSummaries struct {
	db        *gorm.DB
	cdnDomain string
}

func NewMemberSummaries(db *gorm.DB, cdnDomain string) *MemberSummaries {
	return &MemberSummaries{db: db, cdnDomain: cdnDomain}
}

func (a *MemberSummaries) Load(
	ctx context.Context,
	memberIDs []string,
) (map[string]*commonv1.MemberSummary, error) {
	return memberdomain.LoadSummaries(ctx, a.db, a.cdnDomain, memberIDs)
}

// TrackAttachment runs the Release-owned Track steps of a File ingest in
// the ingest's transaction.
type TrackAttachment struct{ audit domainaudit.Appender }

// NewTrackAttachment returns the Track steps of File ingest; audit is
// required.
func NewTrackAttachment(audit domainaudit.Appender) *TrackAttachment {
	if audit == nil {
		panic("release track authority audit appender is required")
	}
	return &TrackAttachment{audit: audit}
}

// LockExistsWithDB serializes File-owned Track upload-session work with
// Track mutation and deletion while preserving the caller's outer
// transaction.
func (a *TrackAttachment) LockExistsWithDB(ctx context.Context, tx *gorm.DB, trackID string) error {
	return releaseConnectError(releasedomain.LockTrackForUpload(ctx, releasepostgres.NewStore(tx, releasepostgres.Ports{}), trackID))
}

func (a *TrackAttachment) AttachOriginalWithDB(
	ctx context.Context,
	tx *gorm.DB,
	input filemediadomain.TrackOriginalAudioInput,
) (filemediadomain.TrackOriginalAudioAttachment, error) {
	attachment, err := releasedomain.AttachTrackOriginalAudio(ctx, releasepostgres.Bind(tx, releasepostgres.Ports{}, a.audit), releasedomain.TrackOriginalAudioInput{
		ClientMediaReady:      input.ClientMediaReady,
		DurationSeconds:       input.DurationSeconds,
		TrackID:               input.TrackID,
		VerifiedFileID:        input.VerifiedFileID,
		ExpectedCurrentFileID: input.ExpectedCurrentFileID,
	})
	return filemediadomain.TrackOriginalAudioAttachment{
		AlreadyApplied: attachment.AlreadyApplied,
		CurrentFileID:  attachment.CurrentFileID,
		ReleaseID:      attachment.ReleaseID,
	}, releaseConnectError(err)
}

// releaseConnectError gives a Release use-case answer the Connect form the
// File domain branches on; an unclassified error passes through.
func releaseConnectError(err error) error {
	if classified, ok := errs.Classified(err); ok {
		return classified
	}
	return err
}

// TrackFileManager adapts FileMedia's deletion fence error to the Release-owned
// retry sentinel while delegating the File-owned session lifecycle unchanged.
type TrackFileManager struct{ files *filemediadomain.Service }

func NewTrackFileManager(files *filemediadomain.Service) *TrackFileManager {
	return &TrackFileManager{files: files}
}

func (m *TrackFileManager) CleanupTrackUploadSessions(ctx context.Context, trackID, reason string) error {
	return m.files.CleanupTrackUploadSessions(ctx, trackID, reason)
}

func (m *TrackFileManager) DeleteFileByID(ctx context.Context, fileID string) error {
	return m.files.DeleteFileByID(ctx, fileID)
}

func (m *TrackFileManager) RequireNoTrackUploadSessionsWithDB(
	ctx context.Context,
	tx *gorm.DB,
	trackID string,
) error {
	err := m.files.RequireNoTrackUploadSessionsWithDB(ctx, tx, trackID)
	if errors.Is(err, filemediadomain.ErrTrackUploadSessionsChanged) {
		return releasedomain.ErrTrackUploadSessionsChanged
	}
	return err
}

var _ releasedomain.TrackUploads = (*TrackFileManager)(nil)
var _ releasepostgres.TrackFiles = (*TrackFileManager)(nil)
