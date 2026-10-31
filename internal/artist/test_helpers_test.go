//go:build integration

package artist

import (
	"context"
	"errors"
	"testing"

	labeladapter "github.com/echovisionlab/geul-api/internal/adapters/label"
	programeventadapter "github.com/echovisionlab/geul-api/internal/adapters/programevent"
	releaseadapter "github.com/echovisionlab/geul-api/internal/adapters/release"
	sharelinkadapter "github.com/echovisionlab/geul-api/internal/adapters/sharelink"
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"
	"github.com/echovisionlab/geul-api/internal/testutil"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
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

// newTestService fills the collaborators deps leaves unset with the
// integration defaults: an audit recorder, a no-op signal publisher, the
// creative content store, the Artist test doubles and the owners' adapters.
func newTestService(t *testing.T, deps Deps) *Service {
	t.Helper()
	if deps.Audit == nil {
		deps.Audit = &audittest.Recorder{}
	}
	if deps.Publisher == nil {
		deps.Publisher = noopArtistAsyncPublisher{}
	}
	if deps.ContentBlocks == nil {
		deps.ContentBlocks = testutil.NewCreativeContentStore(t, deps.SpiceDB)
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
		deps.Runtime = newArtistIntegrationRuntime(deps.DB, "")
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
	service, err := NewService(deps)
	require.NoError(t, err)
	return service
}

type recordingArtistFileDeleter struct {
	deletedIDs []string
}

func (d *recordingArtistFileDeleter) DeleteFileByID(_ context.Context, id string) error {
	d.deletedIDs = append(d.deletedIDs, id)
	return nil
}

func (d *recordingArtistFileDeleter) CleanupTrackUploadSessions(context.Context, string, string) error {
	return nil
}

func (d *recordingArtistFileDeleter) RequireNoTrackUploadSessionsWithDB(context.Context, *gorm.DB, string) error {
	return nil
}

func (d *recordingArtistFileDeleter) ReconcilePublishedEntityAssets(
	context.Context,
	managev1.TranscodeEntityType,
	string,
) error {
	return nil
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
