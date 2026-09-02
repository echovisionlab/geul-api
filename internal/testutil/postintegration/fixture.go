//go:build integration

// Package postintegration composes Post-only integration fixtures without
// adding Post or its adapters to the parent testutil dependency graph.
package postintegration

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	postadapter "github.com/echovisionlab/geul-api/internal/adapters/post"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
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

func (asyncPublisher) EnqueueProtobuf(context.Context, string, string, proto.Message) error {
	return nil
}
func (asyncPublisher) NotifyProtobuf(context.Context, string, proto.Message) error { return nil }

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

// NewPostDomainService composes a Post service with integration-only stubs.
func NewPostDomainService(
	t *testing.T,
	db *gorm.DB,
	cdnDomain string,
	spiceDB *auth.SpiceDBClient,
	identities auth.IdentityManager,
	store *contentblock.Store,
) *postdomain.PostService {
	return NewPostDomainServiceWithFileService(
		t, db, cdnDomain, spiceDB, identities, store, fileService{},
	)
}

// NewPostDomainServiceWithFileService composes a Post service while allowing
// an integration test to observe the Post-owned File delivery boundary.
func NewPostDomainServiceWithFileService(
	t *testing.T,
	db *gorm.DB,
	cdnDomain string,
	spiceDB *auth.SpiceDBClient,
	identities auth.IdentityManager,
	store *contentblock.Store,
	files postdomain.FileService,
) *postdomain.PostService {
	t.Helper()
	if store == nil {
		store = testutil.NewPostContentBlockStore(t)
	}
	return postdomain.NewPostService(
		db,
		cdnDomain,
		NewPostOGRefresher(db, cdnDomain),
		spiceDB,
		identities,
		files,
		asyncPublisher{},
		shareLinks{},
		mediaLoader{},
		postadapter.NewMemberSummaries(db, cdnDomain),
		versionRestore{},
		postdomain.WithPostContentBlockStore(store),
	)
}

// NewInternalPostDomainService composes the collaboration-facing Post service.
func NewInternalPostDomainService(
	t *testing.T,
	db *gorm.DB,
	cdnDomain string,
	spiceDB *auth.SpiceDBClient,
	store *contentblock.Store,
) *postdomain.InternalPostService {
	t.Helper()
	if store == nil {
		store = testutil.NewPostContentBlockStore(t)
	}
	return postdomain.NewInternalPostService(
		db,
		spiceDB,
		asyncPublisher{},
		cdnDomain,
		NewPostOGRefresher(db, cdnDomain),
		mediaLoader{},
		postdomain.WithInternalPostDomainAuditWriter(apitelemetry.NewDurableWriter(db)),
		postdomain.WithInternalPostContentBlockStore(store),
	)
}

// NewPostCommentService composes the Post comment member-summary adapter.
func NewPostCommentService(
	db *gorm.DB,
	cdnDomain string,
	spiceDB *auth.SpiceDBClient,
) *postdomain.CommentService {
	return postdomain.NewCommentService(db, spiceDB, postadapter.NewMemberSummaries(db, cdnDomain))
}
