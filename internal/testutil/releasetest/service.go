//go:build integration

// Package releasetest wires the Release use cases for integration tests of
// the Release domain and of the domains that read Releases.
package releasetest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	releaseadapter "github.com/echovisionlab/geul-api/internal/adapters/release"
	"github.com/echovisionlab/geul-api/internal/release"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"
	"github.com/echovisionlab/geul-api/internal/testutil"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

// NewService builds the Release use cases through release.NewService. DB
// and SpiceDB are required; every other unset dependency is the production
// adapter with an empty CDN, durable audit, the collaboration checkpoint
// fence, Track files and signals that accept everything, and a content OG
// stand-in that requests nothing.
func NewService(t *testing.T, deps release.Deps) *release.Service {
	t.Helper()
	require.NotNil(t, deps.DB)
	require.NotNil(t, deps.SpiceDB)
	if deps.Audit == nil {
		deps.Audit = apitelemetry.NewDurableWriter(deps.DB)
	}
	if deps.ContentBlocks == nil {
		deps.ContentBlocks = testutil.NewCreativeContentStore(t, deps.SpiceDB)
	}
	if deps.Checkpoints == nil {
		deps.Checkpoints = testcollaboration.NewCheckpoints(deps.DB, deps.SpiceDB)
	}
	if deps.ContentSignals == nil {
		deps.ContentSignals = Signals{}
	}
	if deps.TrackFiles == nil {
		deps.TrackFiles = releaseadapter.NewTrackFiles(TrackFiles{})
	}
	if deps.Transcodes == nil {
		deps.Transcodes = releaseadapter.NewTrackTranscodes(nil)
	}
	if deps.WaveformJobs == nil {
		deps.WaveformJobs = releaseadapter.NewWaveformJobs(deps.DB, nil)
	}
	if deps.Assets == nil {
		deps.Assets = releaseadapter.NewAssets("")
	}
	if deps.OG == nil {
		deps.OG = releaseadapter.NewOG("")
	}
	if deps.ContentOG == nil {
		deps.ContentOG = ContentOG{}
	}
	if deps.Members == nil {
		deps.Members = releaseadapter.NewMemberSummaries(deps.DB, "")
	}
	if deps.Artists == nil {
		deps.Artists = releaseadapter.NewArtistSummaries(deps.DB)
	}
	if deps.Labels == nil {
		deps.Labels = releaseadapter.NewLabelSummaries(deps.DB)
	}
	service, err := release.NewService(deps)
	require.NoError(t, err)
	return service
}

// Signals accepts every content.updated signal inside the caller's
// transaction.
type Signals struct{}

func (Signals) NotifyProtobufWithExecutor(context.Context, eventpkg.DBTX, string, proto.Message) error {
	return nil
}

// TrackFiles has no Track uploads and deletes nothing.
type TrackFiles struct{}

func (TrackFiles) DeleteFileByID(context.Context, string) error { return nil }

func (TrackFiles) CleanupTrackUploadSessions(context.Context, string, string) error { return nil }

func (TrackFiles) RequireNoTrackUploadSessionsWithDB(context.Context, *gorm.DB, string) error {
	return nil
}

// ContentOG requests no Open Graph work.
type ContentOG struct{}

func (ContentOG) RequestCurrent(context.Context, *gorm.DB, string, string) error { return nil }
