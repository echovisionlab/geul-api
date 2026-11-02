//go:build integration

// Package releasetest wires the Release use cases for integration tests of
// the Release domain and of the domains that read Releases.
package releasetest

import (
	"context"
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil/contenttest"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	releaseadapter "github.com/echovisionlab/geul-api/internal/adapters/release"
	sharelinkadapter "github.com/echovisionlab/geul-api/internal/adapters/sharelink"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/release"
	releasepostgres "github.com/echovisionlab/geul-api/internal/release/postgres"
	"github.com/echovisionlab/geul-api/internal/sharelink"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"

	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

// Deps are what an integration test sets of the Release use cases; DB and
// SpiceDB are required.
type Deps struct {
	DB      *gorm.DB
	SpiceDB *auth.SpiceDBClient
	Audit   domainaudit.Appender
	// Ports are the Store's adapters; an unset port is the production
	// adapter with an empty CDN, the collaboration checkpoint fence, signals
	// that accept everything, and a content OG stand-in that requests
	// nothing.
	Ports releasepostgres.Ports
	// Files is the File domain's Track upload lifecycle; unset has no
	// uploads.
	Files        releaseadapter.TrackFileManager
	Members      release.MemberSummaryLoader
	Transcodes   release.TrackTranscodes
	WaveformJobs release.WaveformJobs
}

// NewService builds the Release use cases through release.NewService with
// durable audit unless deps names another.
func NewService(t *testing.T, deps Deps) *release.Service {
	t.Helper()
	service, _ := NewServiceAndUnits(t, deps)
	return service
}

// NewServiceAndUnits builds the Release use cases as NewService does and
// returns the unit of work they run in, for a test that seeds state through
// the Store.
func NewServiceAndUnits(t *testing.T, deps Deps) (*release.Service, mutation.UnitOfWork[release.Store]) {
	t.Helper()
	require.NotNil(t, deps.DB)
	require.NotNil(t, deps.SpiceDB)
	if deps.Audit == nil {
		deps.Audit = apitelemetry.NewDurableWriter(deps.DB)
	}
	if deps.Files == nil {
		deps.Files = TrackFiles{}
	}
	trackFiles := releaseadapter.NewTrackFiles(deps.Files)
	ports := deps.Ports
	if ports.ContentBlocks == nil {
		ports.ContentBlocks = contenttest.NewCreativeContentStore(t)
	}
	if ports.Checkpoints == nil {
		ports.Checkpoints = testcollaboration.NewCheckpoints(deps.DB, deps.SpiceDB)
	}
	if ports.Publisher == nil {
		ports.Publisher = Signals{}
	}
	if ports.TrackFiles == nil {
		ports.TrackFiles = trackFiles
	}
	if ports.Assets == nil {
		ports.Assets = releaseadapter.NewAssets("")
	}
	if ports.OG == nil {
		ports.OG = releaseadapter.NewOG("")
	}
	if ports.ContentOG == nil {
		ports.ContentOG = ContentOG{}
	}
	if ports.Artists == nil {
		ports.Artists = releaseadapter.NewArtistSummaries(deps.DB)
	}
	if ports.Labels == nil {
		ports.Labels = releaseadapter.NewLabelSummaries(deps.DB)
	}
	if ports.ShareLinks == nil {
		ports.ShareLinks = sharelinkadapter.EntityLinks{Type: sharelink.EntityRelease}
	}
	if deps.Members == nil {
		deps.Members = releaseadapter.NewMemberSummaries(deps.DB, "")
	}
	if deps.Transcodes == nil {
		deps.Transcodes = releaseadapter.NewTrackTranscodes(nil)
	}
	if deps.WaveformJobs == nil {
		deps.WaveformJobs = releaseadapter.NewWaveformJobs(deps.DB, nil)
	}
	units, err := releasepostgres.NewUnits(deps.DB, ports)
	require.NoError(t, err)
	service, err := release.NewService(release.Deps{
		Units: units, SpiceDB: deps.SpiceDB, Fence: identitystate.PostgresFence{}, Audit: deps.Audit,
		Members: deps.Members, TrackFiles: trackFiles, Transcodes: deps.Transcodes, WaveformJobs: deps.WaveformJobs,
	})
	require.NoError(t, err)
	return service, units
}

// Signals accepts every content.updated signal inside the caller's
// transaction.
type Signals struct{}

func (Signals) NotifyProtobufWithExecutor(context.Context, eventpkg.DBTX, string, proto.Message) error {
	return nil
}

// TrackFiles has no Track uploads.
type TrackFiles struct{}

func (TrackFiles) CleanupTrackUploadSessions(context.Context, string, string) error { return nil }

func (TrackFiles) RequireNoTrackUploadSessionsWithDB(context.Context, *gorm.DB, string) error {
	return nil
}

// ContentOG requests no Open Graph work.
type ContentOG struct{}

func (ContentOG) RequestCurrent(context.Context, *gorm.DB, string, string) error { return nil }
