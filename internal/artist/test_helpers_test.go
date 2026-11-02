//go:build integration

package artist_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/testutil/contenttest"

	artist "github.com/echovisionlab/geul-api/internal/artist"

	artistadapter "github.com/echovisionlab/geul-api/internal/adapters/artist"
	labeladapter "github.com/echovisionlab/geul-api/internal/adapters/label"
	programeventadapter "github.com/echovisionlab/geul-api/internal/adapters/programevent"
	releaseadapter "github.com/echovisionlab/geul-api/internal/adapters/release"
	sharelinkadapter "github.com/echovisionlab/geul-api/internal/adapters/sharelink"
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	artistpostgres "github.com/echovisionlab/geul-api/internal/artist/postgres"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"

	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

// requestBackground carries the request context a request-attributed audit
// record needs; the caller's user becomes its actor.
func requestBackground() context.Context {
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.96")
	if err != nil {
		panic(err)
	}
	return sharedtelemetry.WithRequestContext(context.Background(), requestContext)
}

// integrationDeps are the collaborators an integration test may set; the
// rest take the integration defaults.
type integrationDeps struct {
	DB            *gorm.DB
	SpiceDB       *auth.SpiceDBClient
	Audit         domainaudit.Appender
	Publisher     contentsignal.Publisher
	ContentBlocks *contentblock.Store
	Checkpoints   persistencecheckpoint.ContributorFence
	Translation   artistpostgres.Translation
	Members       artist.MemberProjection
	Runtime       artistpostgres.Runtime
	Labels        artistpostgres.Labels
	Releases      artistpostgres.ReleaseRelations
	ProgramEvents artistpostgres.ProgramEventRelations
	Works         artistpostgres.WorkRelations
	ShareLinks    artistpostgres.ShareLinks
}

// newTestService fills the collaborators deps leaves unset with the
// integration defaults: an audit recorder, a no-op signal publisher, the
// creative content store, the Artist test doubles and the owners' adapters.
func newTestService(t *testing.T, deps integrationDeps) *artist.Service {
	t.Helper()
	if deps.Audit == nil {
		deps.Audit = &audittest.Recorder{}
	}
	if deps.Publisher == nil {
		deps.Publisher = noopArtistAsyncPublisher{}
	}
	if deps.ContentBlocks == nil {
		deps.ContentBlocks = contenttest.NewCreativeContentStore(t)
	}
	if deps.Checkpoints == nil {
		deps.Checkpoints = testcollaboration.NewCheckpoints(deps.DB, deps.SpiceDB)
	}
	if deps.Translation == nil {
		deps.Translation = artistIntegrationTranslation{}
	}
	if deps.Members == nil {
		deps.Members = artistIntegrationMemberProjection{db: deps.DB}
	}
	if deps.Runtime == nil {
		deps.Runtime = newArtistRuntimeForTest(deps.DB, "")
	}
	if deps.Labels == nil {
		deps.Labels = labeladapter.NewArtistLinks()
	}
	if deps.Releases == nil {
		deps.Releases = releaseadapter.NewArtistRelations()
	}
	if deps.ProgramEvents == nil {
		deps.ProgramEvents = programeventadapter.NewArtistRelations()
	}
	if deps.Works == nil {
		deps.Works = workadapter.NewArtistRelations()
	}
	if deps.ShareLinks == nil {
		deps.ShareLinks = sharelinkadapter.NewCatalogLinks()
	}
	units, err := artistpostgres.NewUnits(deps.DB, artistpostgres.Ports{
		ContentBlocks: deps.ContentBlocks, Publisher: deps.Publisher, Checkpoints: deps.Checkpoints,
		Translation: deps.Translation, Runtime: deps.Runtime, Labels: deps.Labels, Releases: deps.Releases,
		ProgramEvents: deps.ProgramEvents, Works: deps.Works, ShareLinks: deps.ShareLinks,
	})
	require.NoError(t, err)
	service, err := artist.NewService(artist.Deps{
		Units: units, SpiceDB: deps.SpiceDB, Fence: identitystate.PostgresFence{}, Audit: deps.Audit, Members: deps.Members,
	})
	require.NoError(t, err)
	return service
}

// newArtistRuntimeForTest is the Artist runtime adapter with an OG refresher
// over the integration render configuration.
func newArtistRuntimeForTest(db *gorm.DB, cdnDomain string) artistpostgres.Runtime {
	return artistadapter.NewRuntime(cdnDomain, newArtistOGRefresher(db, cdnDomain))
}

// seedArtistSourceTitle stores the Artist's title in locale through the
// Artist store.
func seedArtistSourceTitle(t *testing.T, db *gorm.DB, artistID, locale, title string, now time.Time) {
	t.Helper()
	require.NoError(t, artistpostgres.NewStore(db, artistpostgres.Ports{}).SaveLocaleTitle(t.Context(), artistID, locale, title, now))
}

type noopArtistAsyncPublisher struct{}

func (noopArtistAsyncPublisher) EnqueueProtobuf(context.Context, string, string, proto.Message) error {
	return nil
}

func (noopArtistAsyncPublisher) NotifyProtobuf(context.Context, string, proto.Message) error {
	return nil
}

func (noopArtistAsyncPublisher) EnqueueProtobufWithExecutor(
	_ context.Context,
	executor eventpkg.DBTX,
	_ string,
	_ string,
	_ proto.Message,
) error {
	if executor == nil {
		return errors.New("transactional executor is required")
	}
	return nil
}

func (publisher noopArtistAsyncPublisher) NotifyProtobufWithExecutor(
	ctx context.Context,
	_ eventpkg.DBTX,
	signal string,
	message proto.Message,
) error {
	return publisher.NotifyProtobuf(ctx, signal, message)
}
