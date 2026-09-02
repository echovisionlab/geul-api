//go:build integration

package public_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	sharelinkdomain "github.com/echovisionlab/geul-api/internal/sharelink"
	sharelinkpublic "github.com/echovisionlab/geul-api/internal/sharelink/public"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

type integrationTargetAuthority struct{ db *gorm.DB }

func (integrationTargetAuthority) AuthorizeList(
	context.Context,
	managev1.ShareLinkEntityType,
	string,
) error {
	return nil
}

func (a integrationTargetAuthority) Create(
	ctx context.Context,
	_ managev1.ShareLinkEntityType,
	_ string,
	link *model.ShareLink,
	create sharelinkdomain.CreateRecord,
) error {
	return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return create(ctx, tx, link)
	})
}

func (a integrationTargetAuthority) Delete(
	ctx context.Context,
	_ managev1.ShareLinkEntityType,
	link model.ShareLink,
	deleteRecord sharelinkdomain.DeleteRecord,
) error {
	return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return deleteRecord(ctx, tx, link)
	})
}

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
	db := testutil.NewIntegrationDB(t)
	resolver := &integrationTargetResolver{targets: map[string]sharelinkpublic.Target{}}
	manageService := sharelinkdomain.NewService(db, integrationTargetAuthority{db: db})
	publicService := sharelinkpublic.NewService(db, resolver)

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
		{name: "form", entityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_FORM, slug: shareLinkStringPointer("form-slug")},
		{name: "form dashboard", entityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_FORM_DASHBOARD, slug: shareLinkStringPointer("form-slug")},
		{name: "privacy", entityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PRIVACY},
		{name: "terms", entityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_TERMS},
	} {
		t.Run(tc.name+" resolves the bounded target", func(t *testing.T) {
			entityID := uuid.NewString()
			resolver.set(tc.entityType, entityID, sharelinkpublic.Target{Exists: true, Slug: tc.slug})
			link, err := manageService.CreateShareLink(
				context.Background(),
				connect.NewRequest(&managev1.CreateShareLinkRequest{
					EntityType: tc.entityType,
					EntityId:   entityID,
				}),
			)
			require.NoError(t, err)

			validated, err := publicService.Validate(
				context.Background(),
				connect.NewRequest(&openv1.ValidateShareLinkRequest{Token: link.Msg.ShareLink.Token}),
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
		link, err := manageService.CreateShareLink(
			context.Background(),
			connect.NewRequest(&managev1.CreateShareLinkRequest{
				EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PAGE,
				EntityId:   entityID,
				Password:   &password,
			}),
		)
		require.NoError(t, err)
		for _, supplied := range []*string{nil, shareLinkStringPointer("wrong-password")} {
			validated, err := publicService.Validate(
				context.Background(),
				connect.NewRequest(&openv1.ValidateShareLinkRequest{
					Token: link.Msg.ShareLink.Token, Password: supplied,
				}),
			)
			require.NoError(t, err)
			require.False(t, validated.Msg.Valid)
			require.True(t, validated.Msg.PasswordRequired)
		}
		validated, err := publicService.Validate(
			context.Background(),
			connect.NewRequest(&openv1.ValidateShareLinkRequest{
				Token: link.Msg.ShareLink.Token, Password: &password,
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
		link, err := manageService.CreateShareLink(
			context.Background(),
			connect.NewRequest(&managev1.CreateShareLinkRequest{EntityType: entityType, EntityId: entityID}),
		)
		require.NoError(t, err)
		_, err = manageService.DeleteShareLink(
			context.Background(),
			connect.NewRequest(&managev1.DeleteShareLinkRequest{Id: link.Msg.ShareLink.Id}),
		)
		require.NoError(t, err)
		validated, err := publicService.Validate(
			context.Background(),
			connect.NewRequest(&openv1.ValidateShareLinkRequest{Token: link.Msg.ShareLink.Token}),
		)
		require.NoError(t, err)
		require.False(t, validated.Msg.Valid)
	})

	t.Run("expired link is invalid", func(t *testing.T) {
		entityType := managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_POST
		entityID := uuid.NewString()
		resolver.set(entityType, entityID, sharelinkpublic.Target{Exists: true})
		link, err := manageService.CreateShareLink(
			context.Background(),
			connect.NewRequest(&managev1.CreateShareLinkRequest{
				EntityType: entityType,
				EntityId:   entityID,
				ExpiresAt:  timestamppb.New(time.Now().Add(time.Hour)),
			}),
		)
		require.NoError(t, err)
		past := time.Now().Add(-time.Hour)
		require.NoError(t, db.Model(&model.ShareLink{}).
			Where("id = ?", link.Msg.ShareLink.Id).
			Updates(map[string]any{"created_at": past.Add(-time.Hour), "expires_at": past}).Error)
		validated, err := publicService.Validate(
			context.Background(),
			connect.NewRequest(&openv1.ValidateShareLinkRequest{Token: link.Msg.ShareLink.Token}),
		)
		require.NoError(t, err)
		require.False(t, validated.Msg.Valid)
	})
}

func shareLinkStringPointer(value string) *string { return &value }
