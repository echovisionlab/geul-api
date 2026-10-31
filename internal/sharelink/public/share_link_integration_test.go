//go:build integration

package public_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/model"
	sharelinkpublic "github.com/echovisionlab/geul-api/internal/sharelink/public"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

type integrationTargetResolver struct {
	targets map[string]sharelinkpublic.Target
}

func (r *integrationTargetResolver) Resolve(
	_ context.Context,
	entityType managev1.ShareLinkEntityType,
	entityID string,
	_ time.Time,
) (sharelinkpublic.Target, error) {
	return r.targets[integrationTargetKey(entityType, entityID)], nil
}

func (*integrationTargetResolver) IsAutomaticPublicHistoryToken(
	context.Context,
	string,
	managev1.ShareLinkEntityType,
	string,
) (bool, error) {
	return false, nil
}

func (r *integrationTargetResolver) set(
	entityType managev1.ShareLinkEntityType,
	entityID string,
	target sharelinkpublic.Target,
) {
	r.targets[integrationTargetKey(entityType, entityID)] = target
}

func integrationTargetKey(entityType managev1.ShareLinkEntityType, entityID string) string {
	return entityType.String() + ":" + entityID
}

func TestPublicShareLinkValidateIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.NewIntegrationDB(t)
	resolver := &integrationTargetResolver{targets: map[string]sharelinkpublic.Target{}}
	validation, err := sharelinkpublic.NewService(sharelinkpublic.Deps{DB: db, Targets: resolver})
	require.NoError(t, err)
	publicService := sharelinkpublic.NewConnectHandler(validation)

	empty, err := publicService.Validate(
		context.Background(),
		connect.NewRequest(&openv1.ValidateShareLinkRequest{}),
	)
	require.NoError(t, err)
	require.False(t, empty.Msg.Valid)

	missing, err := publicService.Validate(
		context.Background(),
		connect.NewRequest(&openv1.ValidateShareLinkRequest{Token: "missing-" + uuid.NewString()}),
	)
	require.NoError(t, err)
	require.False(t, missing.Msg.Valid)

	for _, tc := range []struct {
		name       string
		entityType managev1.ShareLinkEntityType
		slug       *string
	}{
		{name: "post", entityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_POST, slug: shareLinkStringPointer("post-slug")},
		{name: "page", entityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PAGE, slug: shareLinkStringPointer("page-slug")},
		{name: "work", entityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_WORK, slug: shareLinkStringPointer("work-slug")},
		{name: "release", entityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_RELEASE, slug: shareLinkStringPointer("release-slug")},
		{name: "form", entityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_FORM, slug: shareLinkStringPointer("form-slug")},
		{name: "form dashboard", entityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_FORM_DASHBOARD, slug: shareLinkStringPointer("form-slug")},
		{name: "artist", entityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_ARTIST, slug: shareLinkStringPointer("artist-slug")},
		{name: "label", entityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_LABEL, slug: shareLinkStringPointer("label-slug")},
		{name: "privacy", entityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PRIVACY},
		{name: "terms", entityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_TERMS},
	} {
		t.Run(tc.name+" resolves the bounded target", func(t *testing.T) {
			entityID := uuid.NewString()
			resolver.set(tc.entityType, entityID, sharelinkpublic.Target{Exists: true, Slug: tc.slug})
			link := seedShareLink(t, db, tc.entityType, entityID, nil, nil)

			validated, err := publicService.Validate(
				context.Background(),
				connect.NewRequest(&openv1.ValidateShareLinkRequest{Token: link.Token}),
			)
			require.NoError(t, err)
			require.True(t, validated.Msg.Valid)
			require.Equal(t, tc.entityType, validated.Msg.GetEntityType())
			require.Equal(t, entityID, validated.Msg.GetEntityId())
			require.Equal(t, tc.slug, validated.Msg.Slug)
			require.False(t, validated.Msg.PasswordRequired)
		})
	}

	t.Run("password is required and verified", func(t *testing.T) {
		entityID := uuid.NewString()
		resolver.set(
			managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PAGE,
			entityID,
			sharelinkpublic.Target{Exists: true, Slug: shareLinkStringPointer("protected-page")},
		)
		password := "page-share-password"
		link := seedShareLink(t, db, managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PAGE, entityID, &password, nil)
		for _, supplied := range []*string{nil, shareLinkStringPointer("wrong-password")} {
			validated, err := publicService.Validate(
				context.Background(),
				connect.NewRequest(&openv1.ValidateShareLinkRequest{
					Token: link.Token, Password: supplied,
				}),
			)
			require.NoError(t, err)
			require.False(t, validated.Msg.Valid)
			require.True(t, validated.Msg.PasswordRequired)
		}
		validated, err := publicService.Validate(
			context.Background(),
			connect.NewRequest(&openv1.ValidateShareLinkRequest{
				Token: link.Token, Password: &password,
			}),
		)
		require.NoError(t, err)
		require.True(t, validated.Msg.Valid)
		require.True(t, validated.Msg.PasswordRequired)
	})

	t.Run("deleted link is invalid", func(t *testing.T) {
		entityType := managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PRIVACY
		entityID := uuid.NewString()
		resolver.set(entityType, entityID, sharelinkpublic.Target{Exists: true})
		link := seedShareLink(t, db, entityType, entityID, nil, nil)
		require.NoError(t, db.Delete(&model.ShareLink{}, "id = ?", link.ID).Error)
		validated, err := publicService.Validate(
			context.Background(),
			connect.NewRequest(&openv1.ValidateShareLinkRequest{Token: link.Token}),
		)
		require.NoError(t, err)
		require.False(t, validated.Msg.Valid)
	})

	t.Run("expired link is invalid", func(t *testing.T) {
		entityType := managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_POST
		entityID := uuid.NewString()
		resolver.set(entityType, entityID, sharelinkpublic.Target{Exists: true})
		past := time.Now().Add(-time.Hour)
		link := seedShareLink(t, db, entityType, entityID, nil, &past)
		validated, err := publicService.Validate(
			context.Background(),
			connect.NewRequest(&openv1.ValidateShareLinkRequest{Token: link.Token}),
		)
		require.NoError(t, err)
		require.False(t, validated.Msg.Valid)
	})
}

// seedShareLink stores a link as the management use case does; these tests
// cover what the public validation answers for it.
func seedShareLink(t *testing.T, db *gorm.DB, entityType managev1.ShareLinkEntityType, entityID string, password *string, expiresAt *time.Time) model.ShareLink {
	t.Helper()
	token, err := crypto.GenerateSecureToken()
	require.NoError(t, err)
	now := time.Now()
	expires := now.Add(time.Hour)
	if expiresAt != nil {
		expires = *expiresAt
		now = expires.Add(-time.Hour)
	}
	link := model.ShareLink{Token: token, EntityType: entityType.String(), EntityID: entityID, CreatedAt: now, ExpiresAt: &expires}
	if password != nil {
		hash, err := crypto.NewPasswordHasher(nil).Hash(*password)
		require.NoError(t, err)
		link.PasswordHash = &hash
	}
	require.NoError(t, db.Create(&link).Error)
	return link
}

func shareLinkStringPointer(value string) *string { return &value }
