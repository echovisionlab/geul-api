//go:build integration

// Package postintegration composes Post-only integration fixtures without
// adding Post or its adapters to the parent testutil dependency graph.
package postintegration

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	fileref "github.com/echovisionlab/geul-api/internal/fileref"

	"github.com/echovisionlab/geul-api/internal/testutil/contenttest"

	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/stretchr/testify/require"

	postadapter "github.com/echovisionlab/geul-api/internal/adapters/post"

	sharelinkadapter "github.com/echovisionlab/geul-api/internal/adapters/sharelink"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"

	ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"

	postdomain "github.com/echovisionlab/geul-api/internal/post"

	postpostgres "github.com/echovisionlab/geul-api/internal/post/postgres"
	"github.com/echovisionlab/geul-api/internal/sharelink"

	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil/orytest"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

type fileService struct{}

func (fileService) DeleteFileByID(context.Context, string) error { return nil }
func (fileService) ResolveAuthorizedPostFeaturedImage(context.Context, string, string) (*fileref.Delivery, error) {
	return nil, nil
}
func (fileService) ResolveDisplayMedia(context.Context, []string) (map[string]*fileref.Delivery, error) {
	return map[string]*fileref.Delivery{}, nil
}

type asyncPublisher struct{}

func (asyncPublisher) NotifyProtobufWithExecutor(context.Context, eventpkg.DBTX, string, proto.Message) error {
	return nil
}

type mediaLoader struct{}

func (mediaLoader) LoadContentBlockMediaReferences(
	context.Context,
	*gorm.DB,
	uuid.UUID,
) ([]*fileref.BlockMediaItem, error) {
	return nil, nil
}

// MediaHydrator keeps attachment-only fixtures independent of delivery signing.
// Tests of real delivery use the FileService implementation.
type MediaHydrator struct{}

func (MediaHydrator) HydrateAuthorizedPostBlockMediaWithDB(
	_ context.Context, _ *gorm.DB, _ string, _ uuid.UUID, _ *auth.UserInfo,
	items []*fileref.BlockMediaItem,
) ([]*fileref.BlockMediaItem, error) {
	return items, nil
}

type versionRestore struct{}

func (versionRestore) LoadUnavailableVersionAttachmentKinds(
	context.Context,
	*gorm.DB,
	proto.Message,
) (map[uuid.UUID]contentv1.MissingAttachmentMediaKind, error) {
	return map[uuid.UUID]contentv1.MissingAttachmentMediaKind{}, nil
}

type renderConfig struct{}

func (renderConfig) Snapshot(context.Context, *gorm.DB, string) ([]byte, string, error) {
	payload := []byte(`{"site_title":"","primary_color":"#b02d23"}`)
	return payload, fmt.Sprintf("%x", sha256.Sum256(payload)), nil
}

// NewPostOGRefresher composes the Post OG adapters for integration tests.
func NewPostOGRefresher(db *gorm.DB, cdnDomain string) *ogpostgres.Refresher {
	planner := ogpostgres.NewPlanner(cdnDomain, renderConfig{}, postadapter.NewProjection())
	resolver := ogpostgres.NewResolver(postadapter.NewRequests())
	return ogpostgres.NewRefresher(planner, resolver)
}

// Posts is a Post service with its Connect adapters, so a test reaches the
// RPCs and the use cases only other adapters call (AI documents).
type Posts struct {
	*postdomain.ConnectHandler
	Service *postdomain.Service
}

// Deps are the collaborators of a Post integration service. NewService
// fills the ones a test leaves unset with integration-only stubs and the
// durable audit writer.
type Deps struct {
	DB             *gorm.DB
	CDNDomain      string
	SpiceDB        *auth.SpiceDBClient
	Files          postdomain.FileService
	Signals        contentsignal.Publisher
	MediaLoader    postpostgres.ContentBlockMediaLoader
	Media          postpostgres.ContentBlockMediaHydrator
	VersionRestore postpostgres.VersionRestoreSupport
	ContentBlocks  *contentblock.Store
	Audit          domainaudit.Appender
}

// NewUnits returns the Post unit of work over deps.DB with the runtimes
// deps names, filling the unset ones.
func NewUnits(t *testing.T, deps Deps) mutation.UnitOfWork[postdomain.Store] {
	t.Helper()
	deps = withDefaults(t, deps)
	units, err := postpostgres.NewUnits(deps.DB, postpostgres.Ports{
		ContentBlocks: deps.ContentBlocks, Signals: deps.Signals, OG: NewPostOGRefresher(deps.DB, deps.CDNDomain),
		ShareLinkCleanup: sharelinkadapter.EntityLinks{Type: sharelink.EntityPost},
		MediaLoader:      deps.MediaLoader, Media: deps.Media, VersionRestore: deps.VersionRestore, CDNDomain: deps.CDNDomain,
	})
	require.NoError(t, err)
	return units
}

func withDefaults(t *testing.T, deps Deps) Deps {
	t.Helper()
	if deps.SpiceDB == nil {
		deps.SpiceDB = orytest.PostIntegrationSpiceDB(t)
	}
	if deps.Files == nil {
		deps.Files = fileService{}
	}
	if deps.Signals == nil {
		deps.Signals = asyncPublisher{}
	}
	if deps.MediaLoader == nil {
		deps.MediaLoader = mediaLoader{}
	}
	if deps.Media == nil {
		deps.Media = MediaHydrator{}
	}
	if deps.VersionRestore == nil {
		deps.VersionRestore = versionRestore{}
	}
	if deps.ContentBlocks == nil {
		deps.ContentBlocks = contenttest.NewPostContentBlockStore(t)
	}
	if deps.Audit == nil {
		deps.Audit = apitelemetry.NewDurableWriter(deps.DB)
	}
	return deps
}

// NewService composes the Post use cases over deps.
func NewService(t *testing.T, deps Deps) *postdomain.Service {
	t.Helper()
	deps = withDefaults(t, deps)
	service, err := postdomain.NewService(postdomain.Deps{
		Units: NewUnits(t, deps), SpiceDB: deps.SpiceDB, Fence: identitystate.PostgresFence{}, Audit: deps.Audit,
		Files: deps.Files, Members: postadapter.NewMemberSummaries(deps.DB, deps.CDNDomain),
	})
	require.NoError(t, err)
	return service
}

// NewPostDomainService composes a Post service with integration-only stubs.
func NewPostDomainService(
	t *testing.T,
	db *gorm.DB,
	cdnDomain string,
	spiceDB *auth.SpiceDBClient,
	store *contentblock.Store,
) Posts {
	return NewPostDomainServiceWithFileService(t, db, cdnDomain, spiceDB, store, fileService{})
}

// NewPostDomainServiceWithFileService composes a Post service while allowing
// an integration test to observe the Post-owned File delivery boundary.
func NewPostDomainServiceWithFileService(
	t *testing.T,
	db *gorm.DB,
	cdnDomain string,
	spiceDB *auth.SpiceDBClient,
	store *contentblock.Store,
	files postdomain.FileService,
) Posts {
	t.Helper()
	service := NewService(t, Deps{DB: db, CDNDomain: cdnDomain, SpiceDB: spiceDB, ContentBlocks: store, Files: files})
	return Posts{ConnectHandler: postdomain.NewConnectHandler(service), Service: service}
}

// NewInternalPostDomainService composes the collaboration-facing Post RPCs.
func NewInternalPostDomainService(
	t *testing.T,
	db *gorm.DB,
	cdnDomain string,
	spiceDB *auth.SpiceDBClient,
	store *contentblock.Store,
) *postdomain.InternalConnectHandler {
	t.Helper()
	return postdomain.NewInternalConnectHandler(NewService(t, Deps{
		DB: db, CDNDomain: cdnDomain, SpiceDB: spiceDB, ContentBlocks: store,
	}))
}

// NewPostCommentService composes the Post comment RPCs with the member-summary
// adapter. A nil spiceDB uses the integration stack's client.
func NewPostCommentService(
	t *testing.T,
	db *gorm.DB,
	cdnDomain string,
	spiceDB *auth.SpiceDBClient,
) *postdomain.CommentConnectHandler {
	t.Helper()
	if spiceDB == nil {
		spiceDB = orytest.PostIntegrationSpiceDB(t)
	}
	comments, err := postdomain.NewCommentService(postdomain.CommentDeps{
		Units: NewUnits(t, Deps{DB: db, CDNDomain: cdnDomain, SpiceDB: spiceDB}), SpiceDB: spiceDB,
		Fence: identitystate.PostgresFence{}, Members: postadapter.NewMemberSummaries(db, cdnDomain),
		Audit: apitelemetry.NewDurableWriter(db),
	})
	require.NoError(t, err)
	return postdomain.NewCommentConnectHandler(comments)
}
