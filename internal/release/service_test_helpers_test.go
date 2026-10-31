package release

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

var errUnusedTestDependency = errors.New("test did not configure this Release dependency")

// newTestService builds the Release use cases through NewService. Fields
// left unset in deps get stand-ins that fail if a test reaches them, and a
// recording audit appender.
func newTestService(t *testing.T, deps Deps) *Service {
	t.Helper()
	if deps.DB == nil {
		deps.DB = &gorm.DB{}
	}
	if deps.SpiceDB == nil {
		deps.SpiceDB = &auth.SpiceDBClient{}
	}
	if deps.Audit == nil {
		deps.Audit = &audittest.Recorder{}
	}
	if deps.ContentBlocks == nil {
		deps.ContentBlocks = &contentblock.Store{}
	}
	stand := unusedReleaseDependency{}
	if deps.Checkpoints == nil {
		deps.Checkpoints = stand
	}
	if deps.ContentSignals == nil {
		deps.ContentSignals = stand
	}
	if deps.TrackFiles == nil {
		deps.TrackFiles = stand
	}
	if deps.Transcodes == nil {
		deps.Transcodes = stand
	}
	if deps.WaveformJobs == nil {
		deps.WaveformJobs = stand
	}
	if deps.Assets == nil {
		deps.Assets = stand
	}
	if deps.OG == nil {
		deps.OG = stand
	}
	if deps.ContentOG == nil {
		deps.ContentOG = stand
	}
	if deps.Members == nil {
		deps.Members = stand
	}
	if deps.Artists == nil {
		deps.Artists = stand
	}
	if deps.Labels == nil {
		deps.Labels = stand
	}
	service, err := NewService(deps)
	require.NoError(t, err)
	return service
}

// unusedReleaseDependency stands in for every port a test leaves unset.
type unusedReleaseDependency struct{}

func (unusedReleaseDependency) RequireCurrentContributors(context.Context, *gorm.DB, intrav1.CollaborationResourceType, string, []string) error {
	return errUnusedTestDependency
}

func (unusedReleaseDependency) NotifyProtobufWithExecutor(context.Context, eventpkg.DBTX, string, proto.Message) error {
	return errUnusedTestDependency
}

func (unusedReleaseDependency) DeleteFileByID(context.Context, string) error {
	return errUnusedTestDependency
}

func (unusedReleaseDependency) CleanupTrackUploadSessions(context.Context, string, string) error {
	return errUnusedTestDependency
}

func (unusedReleaseDependency) RequireNoTrackUploadSessionsWithDB(context.Context, *gorm.DB, string) error {
	return errUnusedTestDependency
}

func (unusedReleaseDependency) CancelFiles(context.Context, string, managev1.TranscodeCancelReason, ...*string) {
}

func (unusedReleaseDependency) CancelTracks(context.Context, []string, managev1.TranscodeCancelReason) error {
	return errUnusedTestDependency
}

func (unusedReleaseDependency) LockAttachableFiles(context.Context, *gorm.DB, []string) error {
	return errUnusedTestDependency
}

func (unusedReleaseDependency) BindArtwork(context.Context, *gorm.DB, string, string) (*commonv1.AssetRef, error) {
	return nil, errUnusedTestDependency
}

func (unusedReleaseDependency) ReleaseArtwork(context.Context, *gorm.DB, string) error {
	return errUnusedTestDependency
}

func (unusedReleaseDependency) ReadyArtwork(context.Context, *gorm.DB, string) (*commonv1.AssetRef, error) {
	return nil, errUnusedTestDependency
}

func (unusedReleaseDependency) ReadyAssets(context.Context, *gorm.DB, ...*string) (map[string]*commonv1.AssetRef, error) {
	return nil, errUnusedTestDependency
}

func (unusedReleaseDependency) CancelAndRelease(context.Context, *gorm.DB, string) error {
	return errUnusedTestDependency
}

func (unusedReleaseDependency) LocaleAware() bool { return false }

func (unusedReleaseDependency) RequestCurrent(context.Context, *gorm.DB, string, string) error {
	return errUnusedTestDependency
}

func (unusedReleaseDependency) LoadMemberSummaries(context.Context, []string) (map[string]*commonv1.MemberSummary, error) {
	return nil, errUnusedTestDependency
}

func (unusedReleaseDependency) LoadArtistSummaries(context.Context, []string) (map[string]ArtistSummary, error) {
	return nil, errUnusedTestDependency
}

func (unusedReleaseDependency) LoadArtistSummariesWithDB(context.Context, *gorm.DB, []string) (map[string]ArtistSummary, error) {
	return nil, errUnusedTestDependency
}

func (unusedReleaseDependency) LoadLabelSummaries(context.Context, []string) (map[string]LabelSummary, error) {
	return nil, errUnusedTestDependency
}

func (unusedReleaseDependency) LoadLabelSummariesWithDB(context.Context, *gorm.DB, []string) (map[string]LabelSummary, error) {
	return nil, errUnusedTestDependency
}
