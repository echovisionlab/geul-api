//go:build integration

package public

import (
	"context"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	pagedomain "github.com/echovisionlab/geul-api/internal/page"
	"github.com/echovisionlab/geul-api/internal/pageaccess"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// recordingPageMedia records the owner authorization buildPageResponse hands
// to Block media hydration, which is the only observable trace of the access
// path (public, authenticated draft or share link) a visible read took.
type recordingPageMedia struct {
	publicPageMediaFixture
	mu             sync.Mutex
	authorizations []mediaasset.ContentDownloadOwnerAuthorization
}

func (r *recordingPageMedia) HydrateAuthorizedContentBlockMedia(
	ctx context.Context,
	items []*contentv1.ContentBlockMediaItem,
) ([]*contentv1.ContentBlockMediaItem, error) {
	authorization, ok := mediaasset.ContentDownloadOwnerAuthorizationFromContext(ctx)
	r.mu.Lock()
	if ok {
		r.authorizations = append(r.authorizations, authorization)
	}
	r.mu.Unlock()
	return r.publicPageMediaFixture.HydrateAuthorizedContentBlockMedia(ctx, items)
}

func (r *recordingPageMedia) recorded() []mediaasset.ContentDownloadOwnerAuthorization {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]mediaasset.ContentDownloadOwnerAuthorization(nil), r.authorizations...)
}

type pageAccessViewer int

const (
	pageViewerAnonymous pageAccessViewer = iota
	pageViewerReader
	pageViewerTaggedReader
	pageViewerAdmin
)

type pageAccessShare int

const (
	pageShareNone pageAccessShare = iota
	pageShareValid
	pageShareUnknownToken
	pageShareExpired
	pageShareOtherPage
	pageSharePasswordOmitted
	pageSharePasswordWrong
	pageSharePasswordCorrect
)

type pageAccessState int

const (
	pageStateDraft pageAccessState = iota
	pageStatePublished
	pageStateUnpublished
	pageStateDeleted
	pageStateMissing
)

type pageAccessMatrixFixture struct {
	db          *gorm.DB
	blocks      *contentblock.Store
	manage      *pagedomain.PageService
	adminCtx    context.Context
	adminID     string
	adminMember string
	readerCtx   context.Context
	taggedCtx   context.Context
	tagID       string
	files       *publicPageFileGateway
}

func newPageAccessMatrixFixture(t *testing.T) *pageAccessMatrixFixture {
	t.Helper()
	db := newPublicIntegrationDB(t)
	spiceDB := publicIntegrationSpiceDB(t)
	adminID := uuid.NewString()
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{ID: adminID, Name: "Access matrix admin"})
	adminMemberID := seedPublicAdminMemberIdentityLink(t, db, adminID, "Access matrix admin")
	blocks, err := contentblock.NewGeneratedStore(publicPageFileReuseAuthorizer{})
	require.NoError(t, err)
	files := newPublicReferenceManageFileService(db)
	tagID := uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO user_tag (id, name, created_at) VALUES (?::uuid, ?, NOW())`, tagID, "matrix-"+tagID).Error)
	taggedCtx := seedPageAccessMatrixReader(t, db, "Tagged matrix reader")
	require.NoError(t, db.Exec(
		`INSERT INTO user_tag_mapping (member_id, tag_id, created_at) VALUES (?::uuid, ?::uuid, NOW())`,
		auth.GetUser(taggedCtx).MemberID.String(), tagID,
	).Error)
	return &pageAccessMatrixFixture{
		db: db, blocks: blocks, files: files,
		manage: pagedomain.NewPageService(
			db,
			newPublicPageRuntimeForTest(db, "https://cdn.example.com"),
			files,
			publicReferenceAsyncPublisher{},
			&publicReferenceIdentityManager{identity: &auth.Identity{}},
			spiceDB,
			pagedomain.WithPageContentBlockStore(blocks),
			pagedomain.WithPageContentBlockMediaHydrator(files),
			pagedomain.WithPageMenuTargets(pageAccessMatrixMenuTargets{}),
		),
		adminCtx:    publicPrincipalContext(adminMemberID, adminID),
		adminID:     adminID,
		adminMember: adminMemberID,
		readerCtx:   seedPageAccessMatrixReader(t, db, "Matrix reader"),
		taggedCtx:   taggedCtx,
		tagID:       tagID,
	}
}

func seedPageAccessMatrixReader(t *testing.T, db *gorm.DB, name string) context.Context {
	t.Helper()
	identityID := uuid.NewString()
	memberID := testutil.SeedPostIntegrationIdentity(t, db, identityID, name+" "+identityID)
	return publicPrincipalContext(memberID, identityID)
}

// pageAccessMatrixMenuTargets lets DeletePage run; no Menu points at the
// matrix Pages.
type pageAccessMatrixMenuTargets struct{}

func (pageAccessMatrixMenuTargets) UpdateSlug(context.Context, *gorm.DB, string, string, string, string) error {
	return nil
}

func (pageAccessMatrixMenuTargets) Remove(context.Context, *gorm.DB, string, string, string) error {
	return nil
}

func (f *pageAccessMatrixFixture) service(t *testing.T) (*PageService, *recordingPageMedia) {
	t.Helper()
	media := &recordingPageMedia{publicPageMediaFixture: publicPageMediaFixture{files: f.files}}
	access := publicPageAccessFixture{spiceDB: publicIntegrationSpiceDB(t)}
	return NewPageService(
		f.db, access, access, media,
		WithPageContentBlockStore(f.blocks),
		WithPageAccessPermissionChecker(publicIntegrationSpiceDB(t)),
	), media
}

func (f *pageAccessMatrixFixture) viewerContext(viewer pageAccessViewer) context.Context {
	switch viewer {
	case pageViewerReader:
		return f.readerCtx
	case pageViewerTaggedReader:
		return f.taggedCtx
	case pageViewerAdmin:
		return f.adminCtx
	default:
		return context.Background()
	}
}

// createPage stores a Page with policy and drives it to state through the
// management RPCs, returning its ID and slug.
func (f *pageAccessMatrixFixture) createPage(
	t *testing.T,
	state pageAccessState,
	policy *commonv1.PageAccessPolicy,
) (string, string) {
	t.Helper()
	slug := "access-matrix-" + uuid.NewString()
	if state == pageStateMissing {
		return uuid.NewString(), slug
	}
	summary := "Access matrix summary"
	created, err := f.manage.CreatePage(f.adminCtx, connect.NewRequest(&managev1.CreatePageRequest{
		Title: "Access matrix page", Slug: &slug, Summary: &summary, SourceLocale: "en", AccessPolicy: policy,
	}))
	require.NoError(t, err)
	pageID := created.Msg.GetId()
	if state == pageStatePublished || state == pageStateUnpublished {
		_, err = f.manage.PublishPage(f.adminCtx, connect.NewRequest(&managev1.PublishPageRequest{Id: pageID}))
		require.NoError(t, err)
	}
	if state == pageStateUnpublished {
		_, err = f.manage.UnpublishPage(f.adminCtx, connect.NewRequest(&managev1.UnpublishPageRequest{Id: pageID}))
		require.NoError(t, err)
	}
	if state == pageStateDeleted {
		_, err = f.manage.DeletePage(f.adminCtx, connect.NewRequest(&managev1.DeletePageRequest{Id: pageID}))
		require.NoError(t, err)
	}
	return pageID, slug
}

// shareCredentials stores the share link a case needs and returns the token
// and password the reader presents.
func (f *pageAccessMatrixFixture) shareCredentials(
	t *testing.T,
	share pageAccessShare,
	pageID string,
) (*string, *string, string) {
	t.Helper()
	if share == pageShareNone {
		return nil, nil, ""
	}
	token := uuid.NewString()
	if share == pageShareUnknownToken {
		return &token, nil, ""
	}
	entityID := pageID
	if share == pageShareOtherPage {
		entityID, _ = f.createPage(t, pageStateDraft, nil)
	}
	createdAt, expires := time.Now(), time.Now().Add(time.Hour)
	if share == pageShareExpired {
		createdAt, expires = time.Now().Add(-2*time.Hour), time.Now().Add(-time.Minute)
	}
	link := &model.ShareLink{
		ID: uuid.NewString(), Token: token, EntityID: entityID, ExpiresAt: &expires, CreatedAt: createdAt,
		EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PAGE.String(),
	}
	var password *string
	switch share {
	case pageSharePasswordOmitted, pageSharePasswordWrong, pageSharePasswordCorrect:
		hash, err := crypto.NewPasswordHasher(nil).Hash("matrix-secret")
		require.NoError(t, err)
		link.PasswordHash = &hash
		if share == pageSharePasswordWrong {
			password = proto.String("wrong-secret")
		}
		if share == pageSharePasswordCorrect {
			password = proto.String("matrix-secret")
		}
	}
	require.NoError(t, f.db.Create(link).Error)
	return &token, password, link.ID
}

// TestPublicPageAccessMatrixIntegration pins every access decision of
// buildPageResponse for reads by ID and by slug.
func TestPublicPageAccessMatrixIntegration(t *testing.T) {
	t.Parallel()
	fixture := newPageAccessMatrixFixture(t)
	authenticated := &commonv1.PageAccessPolicy{Mode: commonv1.PageAccessMode_PAGE_ACCESS_MODE_AUTHENTICATED}
	tagged := &commonv1.PageAccessPolicy{
		Mode: commonv1.PageAccessMode_PAGE_ACCESS_MODE_CONDITIONS, UserTagIds: []string{fixture.tagID},
	}
	authorRole := &commonv1.PageAccessPolicy{
		Mode: commonv1.PageAccessMode_PAGE_ACCESS_MODE_CONDITIONS, AllowedRoles: []policyv1.AuthorizationRole{policyv1.AuthorizationRole_AUTHOR},
	}
	const (
		allowed  = commonv1.PageAccessReason_PAGE_ACCESS_REASON_ALLOWED
		needAuth = commonv1.PageAccessReason_PAGE_ACCESS_REASON_AUTHENTICATION_REQUIRED
		notMet   = commonv1.PageAccessReason_PAGE_ACCESS_REASON_CONDITIONS_NOT_MET
	)
	cases := []struct {
		name     string
		state    pageAccessState
		policy   *commonv1.PageAccessPolicy
		viewer   pageAccessViewer
		share    pageAccessShare
		wantCode connect.Code
		reason   commonv1.PageAccessReason
		mode     mediaasset.ContentDownloadOwnerAccessMode
	}{
		{name: "published public anonymous", state: pageStatePublished, reason: allowed, mode: mediaasset.ContentDownloadOwnerAccessPublic},
		{name: "published public signed in", state: pageStatePublished, viewer: pageViewerReader, reason: allowed, mode: mediaasset.ContentDownloadOwnerAccessPublic},
		{name: "published authenticated anonymous", state: pageStatePublished, policy: authenticated, reason: needAuth},
		{name: "published authenticated signed in", state: pageStatePublished, policy: authenticated, viewer: pageViewerReader, reason: allowed, mode: mediaasset.ContentDownloadOwnerAccessPublic},
		// A share link never opens a published Page's audience.
		{name: "published authenticated anonymous with share link", state: pageStatePublished, policy: authenticated, share: pageShareValid, reason: needAuth},
		{name: "published member tag anonymous", state: pageStatePublished, policy: tagged, reason: needAuth},
		{name: "published member tag without tag", state: pageStatePublished, policy: tagged, viewer: pageViewerReader, reason: notMet},
		{name: "published member tag with tag", state: pageStatePublished, policy: tagged, viewer: pageViewerTaggedReader, reason: allowed, mode: mediaasset.ContentDownloadOwnerAccessPublic},
		{name: "published member tag manager", state: pageStatePublished, policy: tagged, viewer: pageViewerAdmin, reason: allowed, mode: mediaasset.ContentDownloadOwnerAccessPublic},
		{name: "published author role reader", state: pageStatePublished, policy: authorRole, viewer: pageViewerReader, reason: notMet},
		{name: "draft anonymous", state: pageStateDraft, wantCode: connect.CodeNotFound},
		{name: "draft signed in without permission", state: pageStateDraft, viewer: pageViewerReader, wantCode: connect.CodeNotFound},
		{name: "draft manager", state: pageStateDraft, viewer: pageViewerAdmin, reason: allowed, mode: mediaasset.ContentDownloadOwnerAccessAuthenticatedDraft},
		// Draft reads skip the audience entirely: managers and share links see
		// a draft whose stored policy would otherwise deny them.
		{name: "draft manager ignores member tag policy", state: pageStateDraft, policy: tagged, viewer: pageViewerAdmin, reason: allowed, mode: mediaasset.ContentDownloadOwnerAccessAuthenticatedDraft},
		{name: "draft share link", state: pageStateDraft, share: pageShareValid, reason: allowed, mode: mediaasset.ContentDownloadOwnerAccessShare},
		{name: "draft share link ignores authenticated policy", state: pageStateDraft, policy: authenticated, share: pageShareValid, reason: allowed, mode: mediaasset.ContentDownloadOwnerAccessShare},
		{name: "draft share link for signed-in reader", state: pageStateDraft, viewer: pageViewerReader, share: pageShareValid, reason: allowed, mode: mediaasset.ContentDownloadOwnerAccessShare},
		{name: "draft unknown share token", state: pageStateDraft, share: pageShareUnknownToken, wantCode: connect.CodeNotFound},
		{name: "draft expired share link", state: pageStateDraft, share: pageShareExpired, wantCode: connect.CodeNotFound},
		{name: "draft share link for another page", state: pageStateDraft, share: pageShareOtherPage, wantCode: connect.CodeNotFound},
		{name: "draft password share link without password", state: pageStateDraft, share: pageSharePasswordOmitted, wantCode: connect.CodeNotFound},
		{name: "draft password share link wrong password", state: pageStateDraft, share: pageSharePasswordWrong, wantCode: connect.CodeNotFound},
		{name: "draft password share link correct password", state: pageStateDraft, share: pageSharePasswordCorrect, reason: allowed, mode: mediaasset.ContentDownloadOwnerAccessShare},
		{name: "unpublished anonymous", state: pageStateUnpublished, wantCode: connect.CodeNotFound},
		{name: "unpublished manager", state: pageStateUnpublished, viewer: pageViewerAdmin, reason: allowed, mode: mediaasset.ContentDownloadOwnerAccessAuthenticatedDraft},
		{name: "deleted manager", state: pageStateDeleted, viewer: pageViewerAdmin, wantCode: connect.CodeNotFound},
		{name: "missing anonymous", state: pageStateMissing, wantCode: connect.CodeNotFound},
		{name: "missing share token", state: pageStateMissing, share: pageShareUnknownToken, wantCode: connect.CodeNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pageID, slug := fixture.createPage(t, tc.state, tc.policy)
			token, password, shareLinkID := fixture.shareCredentials(t, tc.share, pageID)
			var revision, documentID string
			if tc.reason == allowed {
				managed, err := fixture.manage.GetPage(fixture.adminCtx, connect.NewRequest(&managev1.GetPageRequest{Id: pageID}))
				require.NoError(t, err)
				revision = managed.Msg.GetRevision()
				require.NoError(t, fixture.db.Raw("SELECT content_document_id::text FROM page WHERE id = ?::uuid", pageID).Scan(&documentID).Error)
			}
			for _, locator := range []struct{ name, value string }{{"by id", pageID}, {"by slug", slug}} {
				t.Run(locator.name, func(t *testing.T) {
					svc, media := fixture.service(t)
					ctx := fixture.viewerContext(tc.viewer)
					request := connect.NewRequest(&openv1.GetPageRequest{Slug: locator.value, ShareToken: token, SharePassword: password})
					response, err := svc.Get(ctx, request)
					if tc.wantCode != 0 {
						require.Nil(t, response)
						require.Equal(t, tc.wantCode, connect.CodeOf(err))
						var connectErr *connect.Error
						require.ErrorAs(t, err, &connectErr)
						require.Equal(t, "page not found", connectErr.Message())
						require.Empty(t, media.recorded())
						return
					}
					require.NoError(t, err)
					msg := response.Msg
					require.Equal(t, tc.reason, msg.GetAccessReason())
					if tc.reason != allowed {
						require.Nil(t, msg.GetPage())
						require.Empty(t, msg.GetBlockMedia())
						require.Empty(t, media.recorded())
						return
					}
					requireAllowedMatrixPage(t, msg, pageID, slug, tc.state, tc.policy, revision)
					wantAuthorization := mediaasset.ContentDownloadOwnerAuthorization{
						ResourceType: "page", ResourceID: pageID, DocumentID: documentID, Mode: tc.mode,
						Status: managev1.PageStatus_PAGE_STATUS_DRAFT.String(),
					}
					if tc.state == pageStatePublished {
						wantAuthorization.Status = managev1.PageStatus_PAGE_STATUS_PUBLISHED.String()
					}
					switch tc.mode {
					case mediaasset.ContentDownloadOwnerAccessAuthenticatedDraft:
						wantAuthorization.IdentityID = fixture.adminID
						wantAuthorization.MemberID = fixture.adminMember
					case mediaasset.ContentDownloadOwnerAccessShare:
						recorded := media.recorded()
						require.Len(t, recorded, 1)
						require.NotNil(t, recorded[0].ShareLink)
						require.Equal(t, shareLinkID, recorded[0].ShareLink.ID)
						require.Equal(t, managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PAGE.String(), recorded[0].ShareLink.EntityType)
						require.Equal(t, pageID, recorded[0].ShareLink.EntityID)
						require.NotNil(t, recorded[0].ShareLink.ExpiresAt)
						wantAuthorization.ShareLink = recorded[0].ShareLink
					}
					require.Equal(t, []mediaasset.ContentDownloadOwnerAuthorization{wantAuthorization}, media.recorded())
				})
			}
		})
	}
}

func requireAllowedMatrixPage(
	t *testing.T,
	msg *openv1.GetPageResponse,
	pageID string,
	slug string,
	state pageAccessState,
	policy *commonv1.PageAccessPolicy,
	revision string,
) {
	t.Helper()
	page := msg.GetPage()
	require.NotNil(t, page)
	require.Equal(t, pageID, page.GetId())
	require.Equal(t, slug, page.GetSlug())
	require.Equal(t, "Access matrix page", page.GetTitle())
	require.Equal(t, "Access matrix summary", page.GetSummary())
	wantPolicy, err := pageaccess.Normalize(policy)
	require.NoError(t, err)
	require.True(t, proto.Equal(wantPolicy, page.GetAccessPolicy()), "access policy %v", page.GetAccessPolicy())
	if state == pageStatePublished {
		require.Equal(t, openv1.PageStatus_PAGE_STATUS_PUBLISHED, page.GetStatus())
		require.NotNil(t, page.GetPublishedAt())
	} else {
		require.Equal(t, openv1.PageStatus_PAGE_STATUS_DRAFT, page.GetStatus())
	}
	require.Equal(t, revision, page.GetRevision())
	require.NotNil(t, page.GetDocument())
	require.Equal(t, "en", page.GetDocument().GetLocale())
	require.NotNil(t, page.GetDocumentLayout())
	require.Equal(t, "en", page.GetLocalizationInfo().GetDisplayedLocale())
	require.Equal(t, "en", page.GetLocalizationInfo().GetSourceLocale())
	require.True(t, page.GetLocalizationInfo().GetIsOriginal())
	require.False(t, page.GetLocalizationInfo().GetIsFallback())
	require.Nil(t, page.GetFeaturedImageDelivery())
	require.Nil(t, page.GetOgAsset())
	require.Empty(t, msg.GetBlockMedia())
}

// TestPublicPageHomepageAccessIntegration pins the homepage read, which only
// projects published Pages: an unpublished root Page is not found, and an
// unconfigured homepage answers an empty response.
func TestPublicPageHomepageAccessIntegration(t *testing.T) {
	t.Parallel()
	fixture := newPageAccessMatrixFixture(t)
	authenticated := &commonv1.PageAccessPolicy{Mode: commonv1.PageAccessMode_PAGE_ACCESS_MODE_AUTHENTICATED}
	cases := []struct {
		name   string
		state  pageAccessState
		policy *commonv1.PageAccessPolicy
		viewer pageAccessViewer
		reason commonv1.PageAccessReason
		page   bool
		// notFound marks a root Page the homepage read cannot show.
		notFound bool
	}{
		{name: "published", state: pageStatePublished, reason: commonv1.PageAccessReason_PAGE_ACCESS_REASON_ALLOWED, page: true},
		{name: "draft anonymous", state: pageStateDraft, notFound: true},
		{name: "draft for manager", state: pageStateDraft, viewer: pageViewerAdmin, notFound: true},
		{name: "unpublished anonymous", state: pageStateUnpublished, notFound: true},
		{name: "authenticated anonymous", state: pageStatePublished, policy: authenticated, reason: commonv1.PageAccessReason_PAGE_ACCESS_REASON_AUTHENTICATION_REQUIRED},
		{name: "authenticated signed in", state: pageStatePublished, policy: authenticated, viewer: pageViewerReader, reason: commonv1.PageAccessReason_PAGE_ACCESS_REASON_ALLOWED, page: true},
	}
	// Cases share the single site_settings row, so they run in order.
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pageID, _ := fixture.createPage(t, tc.state, tc.policy)
			setPublicHomepagePage(t, fixture.db, fixture.adminCtx, pageID)
			svc, media := fixture.service(t)
			response, err := svc.Get(fixture.viewerContext(tc.viewer), connect.NewRequest(&openv1.GetPageRequest{Slug: "/"}))
			if tc.notFound {
				require.Nil(t, response)
				var connectErr *connect.Error
				require.ErrorAs(t, err, &connectErr)
				require.Equal(t, connect.CodeNotFound, connectErr.Code())
				require.Equal(t, "page not found", connectErr.Message())
				require.Empty(t, media.recorded())
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.reason, response.Msg.GetAccessReason())
			if !tc.page {
				require.Nil(t, response.Msg.GetPage())
				require.Empty(t, media.recorded())
				return
			}
			require.Equal(t, pageID, response.Msg.GetPage().GetId())
			recorded := media.recorded()
			require.Len(t, recorded, 1)
			require.Equal(t, mediaasset.ContentDownloadOwnerAccessPublic, recorded[0].Mode)
		})
	}
	t.Run("not configured", func(t *testing.T) {
		require.NoError(t, fixture.db.Exec("UPDATE site_settings SET homepage_page_id = NULL WHERE id = 1").Error)
		svc, media := fixture.service(t)
		response, err := svc.Get(context.Background(), connect.NewRequest(&openv1.GetPageRequest{}))
		require.NoError(t, err)
		require.True(t, proto.Equal(&openv1.GetPageResponse{}, response.Msg))
		require.Empty(t, media.recorded())
	})
}
