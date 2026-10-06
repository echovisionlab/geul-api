//go:build integration

package public

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/pageaccess"
	"github.com/echovisionlab/geul-api/internal/sharelink"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"gorm.io/gorm"
)

func TestPublicPageAudienceGatesAllProjectionAndReopensIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	adminID := uuid.NewString()
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{ID: adminID, Name: "Audience admin"})
	adminMemberID := seedPublicAdminMemberIdentityLink(t, db, adminID, "Audience admin")
	adminCtx := publicPrincipalContext(adminMemberID, adminID)
	viewerCtx := seedAudienceReader(t, db)
	blocks, err := contentblock.NewGeneratedStore(publicPageFileReuseAuthorizer{})
	require.NoError(t, err)
	managed := newPublicPageManageService(db, blocks, newPublicReferenceManageFileService(db))
	slug, summary := "audience-"+uuid.NewString(), "private summary marker"
	created, err := managed.CreatePage(adminCtx, connect.NewRequest(&managev1.CreatePageRequest{Title: "private title marker", Summary: &summary, Slug: &slug, SourceLocale: "en"}))
	require.NoError(t, err)
	pageID := created.Msg.Id
	var documentID string
	require.NoError(t, db.Raw("SELECT content_document_id FROM page WHERE id=?::uuid", pageID).Scan(&documentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO content_block (id,document_id,container_slot,position,kind,shared_data) VALUES (?::uuid,?::uuid,'sections',0,'external-video','{"externalVideo":{"props":{"uri":"https://private.example.test/body-marker"}}}'::jsonb)`, uuid.NewString(), documentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO content_block_locale(block_id,locale,localized_data) SELECT id,'en','{"externalVideo":{"props":{"caption":"private body caption"}}}'::jsonb FROM content_block WHERE document_id=?::uuid`, documentID).Error)
	featuredID, ogID := seedCanonicalPublicFileFixture(t, db, "private-featured.webp", "image/webp", "image")
	require.NoError(t, db.Exec("UPDATE page SET status='PAGE_STATUS_PUBLISHED', published_at=now(), featured_image_file_id=?::uuid, og_asset_id=?::uuid WHERE id=?::uuid", featuredID, ogID, pageID).Error)
	files := newPublicReferenceManageFileService(db)
	access := publicPageAccessFixture{}
	svc := NewPageService(db, access, access, publicPageMediaFixture{files: files}, WithPageContentBlockStore(blocks), WithPageAccessPermissionChecker(publicIntegrationSpiceDB))
	savePolicy := func(policy *commonv1.PageAccessPolicy) {
		raw, encodeErr := pageaccess.Encode(policy)
		require.NoError(t, encodeErr)
		require.NoError(t, db.Exec("UPDATE page SET access_policy=?::jsonb WHERE id=?::uuid", string(raw), pageID).Error)
	}
	read := func(ctx context.Context, token *string) *openv1.GetPageResponse {
		response, readErr := svc.Get(ctx, connect.NewRequest(&openv1.GetPageRequest{Slug: slug, ShareToken: token}))
		require.NoError(t, readErr)
		return response.Msg
	}
	assertDenied := func(response *openv1.GetPageResponse, reason commonv1.PageAccessReason) {
		require.Equal(t, reason, response.GetAccessReason())
		require.Nil(t, response.GetPage())
		require.Empty(t, response.GetBlockMedia())
		encoded, marshalErr := protojson.Marshal(response)
		require.NoError(t, marshalErr)
		for _, hidden := range []string{"private title marker", summary, "body-marker", featuredID, ogID} {
			require.NotContains(t, string(encoded), hidden)
		}
	}
	savePolicy(&commonv1.PageAccessPolicy{Mode: commonv1.PageAccessMode_PAGE_ACCESS_MODE_AUTHENTICATED})
	assertDenied(read(t.Context(), nil), commonv1.PageAccessReason_PAGE_ACCESS_REASON_AUTHENTICATION_REQUIRED)
	allowed := read(viewerCtx, nil)
	require.Equal(t, commonv1.PageAccessReason_PAGE_ACCESS_REASON_ALLOWED, allowed.GetAccessReason())
	require.Equal(t, "private title marker", allowed.GetPage().GetTitle())
	encoded, err := protojson.Marshal(allowed)
	require.NoError(t, err)
	require.Contains(t, string(encoded), "body-marker")
	require.NotNil(t, allowed.GetPage().GetFeaturedImageDelivery())
	require.NotNil(t, allowed.GetPage().GetOgAsset())
	savePolicy(&commonv1.PageAccessPolicy{Mode: commonv1.PageAccessMode_PAGE_ACCESS_MODE_CONDITIONS, AllowedRoles: []policyv1.AuthorizationRole{policyv1.AuthorizationRole_AUTHOR}})
	assertDenied(read(viewerCtx, nil), commonv1.PageAccessReason_PAGE_ACCESS_REASON_CONDITIONS_NOT_MET)
	require.NotNil(t, read(adminCtx, nil).GetPage(), "active administrator retains manage access")
	token := uuid.NewString()
	expires := time.Now().Add(time.Hour)
	require.NoError(t, db.Create(&model.ShareLink{ID: uuid.NewString(), Token: token, EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PAGE.String(), EntityID: pageID, ExpiresAt: &expires, CreatedAt: time.Now()}).Error)
	validLink, err := sharelink.ValidateForEntity(t.Context(), db, token, "", managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PAGE, pageID)
	require.NoError(t, err)
	require.NotNil(t, validLink)
	assertDenied(read(t.Context(), &token), commonv1.PageAccessReason_PAGE_ACCESS_REASON_AUTHENTICATION_REQUIRED)
	savePolicy(nil)
	reopened := read(t.Context(), nil)
	require.Equal(t, commonv1.PageAccessReason_PAGE_ACCESS_REASON_ALLOWED, reopened.GetAccessReason())
	require.Equal(t, "private title marker", reopened.GetPage().GetTitle())
}

func seedAudienceReader(t *testing.T, db *gorm.DB) context.Context {
	t.Helper()
	identityID, memberID := uuid.NewString(), uuid.NewString()
	email := identityID + "@example.test"
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{ID: identityID, Email: email, Name: "Audience reader"})
	require.NoError(t, db.Exec("INSERT INTO account_identity(id) VALUES (?::uuid)", identityID).Error)
	now := time.Now().UTC()
	require.NoError(t, db.Create(&model.Member{ID: memberID, AccountIdentityID: &identityID, Nickname: "Audience reader", Onboarded: true, PrimaryEmail: &email, AvailableEmails: []string{email}, SocialLinks: map[string]string{}, CreatedAt: now, UpdatedAt: now}).Error)
	require.NoError(t, db.Exec("UPDATE kratos.identities SET external_id=? WHERE id=?::uuid", memberID, identityID).Error)
	return publicPrincipalContext(memberID, identityID)
}
