//go:build integration

// Package postintegration composes Post-only integration fixtures without
// adding Post or its adapters to the parent testutil dependency graph.
package postintegration

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/stretchr/testify/require"

	postadapter "github.com/echovisionlab/geul-api/internal/adapters/post"
	sharelinkadapter "github.com/echovisionlab/geul-api/internal/adapters/sharelink"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/sharelink"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

type fileService struct{}

func (fileService) DeleteFileByID(context.Context, string) error { return nil }
func (fileService) ResolveAuthorizedPostFeaturedImage(context.Context, string, string) (*commonv1.MediaDelivery, error) {
	return nil, nil
}
func (fileService) ResolvePublicDisplayMedia(context.Context, []string) (map[string]*commonv1.MediaDelivery, error) {
	return map[string]*commonv1.MediaDelivery{}, nil
}

type asyncPublisher struct{}

func (asyncPublisher) NotifyProtobufWithExecutor(context.Context, eventpkg.DBTX, string, proto.Message) error {
	return nil
}

type shareLinks struct{}

func (shareLinks) ValidateShareLinkForEntity(
	context.Context,
	*gorm.DB,
	string,
	string,
	managev1.ShareLinkEntityType,
	string,
) (*model.ShareLink, error) {
	return nil, nil
}

type mediaLoader struct{}

func (mediaLoader) LoadContentBlockMediaReferences(
	context.Context,
	*gorm.DB,
	uuid.UUID,
) ([]*contentv1.ContentBlockMediaItem, error) {
	return nil, nil
}

// MediaHydrator keeps attachment-only fixtures independent of delivery signing.
// Tests of real delivery use the FileService implementation.
type MediaHydrator struct{}

func (MediaHydrator) HydrateAuthorizedPostBlockMediaWithDB(
	_ context.Context, _ *gorm.DB, _ string, _ uuid.UUID, _ *auth.UserInfo,
	items []*contentv1.ContentBlockMediaItem,
) ([]*contentv1.ContentBlockMediaItem, error) {
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
func NewPostOGRefresher(db *gorm.DB, cdnDomain string) *og.Refresher {
	planner := og.NewPlanner(db, cdnDomain, renderConfig{}, postadapter.NewProjection())
	resolver := og.NewResolver(postadapter.NewRequests())
	return og.NewRefresher(planner, resolver)
}

// Posts is a Post service with its Connect adapters, so a test reaches the
// RPCs and the use cases only other adapters call (AI documents).
type Posts struct {
	*postdomain.ConnectHandler
	Service *postdomain.Service
}

// NewService fills the collaborators deps leaves unset with integration-only
// stubs and the durable audit writer.
func NewService(t *testing.T, deps postdomain.Deps) *postdomain.Service {
	t.Helper()
	if deps.SpiceDB == nil {
		deps.SpiceDB = testutil.PostIntegrationSpiceDB(t)
	}
	if deps.OG == nil {
		deps.OG = NewPostOGRefresher(deps.DB, deps.CDNDomain)
	}
	if deps.Files == nil {
		deps.Files = fileService{}
	}
	if deps.Signals == nil {
		deps.Signals = asyncPublisher{}
	}
	if deps.ShareLinks == nil {
		deps.ShareLinks = shareLinks{}
	}
	if deps.ShareLinkCleanup == nil {
		deps.ShareLinkCleanup = sharelinkadapter.EntityLinks{Type: sharelink.EntityPost}
	}
	if deps.MediaLoader == nil {
		deps.MediaLoader = mediaLoader{}
	}
	if deps.Media == nil {
		deps.Media = MediaHydrator{}
	}
	if deps.Members == nil {
		deps.Members = postadapter.NewMemberSummaries(deps.DB, deps.CDNDomain)
	}
	if deps.VersionRestore == nil {
		deps.VersionRestore = versionRestore{}
	}
	if deps.ContentBlocks == nil {
		deps.ContentBlocks = testutil.NewPostContentBlockStore(t)
	}
	if deps.Audit == nil {
		deps.Audit = apitelemetry.NewDurableWriter(deps.DB)
	}
	service, err := postdomain.NewService(deps)
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
	service := NewService(t, postdomain.Deps{DB: db, CDNDomain: cdnDomain, SpiceDB: spiceDB, ContentBlocks: store, Files: files})
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
	return postdomain.NewInternalConnectHandler(NewService(t, postdomain.Deps{
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
		spiceDB = testutil.PostIntegrationSpiceDB(t)
	}
	comments, err := postdomain.NewCommentService(postdomain.CommentDeps{
		DB: db, SpiceDB: spiceDB, Members: postadapter.NewMemberSummaries(db, cdnDomain),
		Audit: apitelemetry.NewDurableWriter(db),
	})
	require.NoError(t, err)
	return postdomain.NewCommentConnectHandler(comments)
}
